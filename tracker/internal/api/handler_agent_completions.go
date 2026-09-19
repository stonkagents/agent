// Package api: agent completions — validate key, deduct credits, proxy to LLM.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// AgentCompletionsPaths are every path (relative to the /api subrouter) that serves the
// credit-metered LLM proxy. The /v1/agents/* paths are the user-facing StonkAgents surface;
// them baked into their config. All paths share one handler, one auth path, one rate limit
// bucket and one metering path — adding a path here is the only way to add an alias.
var AgentCompletionsPaths = []string{
	"/v1/agents/completions",
	"/v1/agents/chat/completions",
}

const (
	agentCompletionCostDefault  = 10 // fallback if no per-model cost configured
	agentCompletionRefundReason = "agent_completion_refund"
	// AutopilotMarkerHeader marks a request the daemon's autopilot made on its own (value "1").
	AutopilotMarkerHeader = "X-StonkAgents-Auto"
	// 5 minutes — reasoning models (gpt-5.x, o-series) can take 90–180s for
	// complex queries, plus network/proxy latency. 120s was too tight and was
	// canceling completed-but-slow detailed calls. Keeping a single timeout for
	// both mini and detailed (mini still completes in seconds; the upper bound
	// just gives detailed enough headroom).
	agentProxyTimeout = 5 * time.Minute
	// detailedMaxOutputTokens caps how much the upstream LLM can emit on a
	// detailed call so a single response can't exceed our 75-credit budget.
	// 1500 tokens ≈ 1100 words, plenty for a thorough answer.
	detailedMaxOutputTokens = 1500
)

// AgentCompletionsHandler handles POST /api/v1/agents/completions.
type AgentCompletionsHandler struct {
	creditSvc    *services.CreditService
	apiKeyRepo   repository.PeerAPIKeyRepository
	accountRepo  repository.AccountRepository
	guestKeyRepo repository.GuestKeyMappingRepository
	settingsRepo *repository.PlatformSettingsRepository
	usageRepo    *repository.LLMUsageRepository
	llmURL       string
	llmAPIKey    string
	llmProvider  string // "openai" (default) or "anthropic"
	llmModel     string // fallback model when client doesn't specify one (e.g. "gpt-5.4-mini")
}

// AgentCompletionsConfig is the config for the Agent completions proxy (env or config file).
type AgentCompletionsConfig struct {
	LLMURL      string
	LLMAPIKey   string
	LLMProvider string // "openai" or "anthropic"; defaults to "openai"
	LLMModel    string // default model to inject (e.g. "gpt-4o-mini")
}

// NewAgentCompletionsHandler creates a new Agent completions handler.
func NewAgentCompletionsHandler(
	creditSvc *services.CreditService,
	apiKeyRepo repository.PeerAPIKeyRepository,
	accountRepo repository.AccountRepository,
	guestKeyRepo repository.GuestKeyMappingRepository,
	settingsRepo *repository.PlatformSettingsRepository,
	usageRepo *repository.LLMUsageRepository,
	cfg AgentCompletionsConfig,
) *AgentCompletionsHandler {
	provider := strings.ToLower(strings.TrimSpace(cfg.LLMProvider))
	if provider == "" {
		provider = "openai"
	}
	return &AgentCompletionsHandler{
		creditSvc:    creditSvc,
		apiKeyRepo:   apiKeyRepo,
		accountRepo:  accountRepo,
		guestKeyRepo: guestKeyRepo,
		settingsRepo: settingsRepo,
		usageRepo:    usageRepo,
		llmURL:       strings.TrimSuffix(strings.TrimSpace(cfg.LLMURL), "/"),
		llmAPIKey:    strings.TrimSpace(cfg.LLMAPIKey),
		llmProvider:  provider,
		llmModel:     strings.TrimSpace(cfg.LLMModel),
	}
}

// HandleCompletions handles POST /api/v1/agents/completions.
func (h *AgentCompletionsHandler) HandleCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}

	accountID, err := ResolveAgentAccountID(r.Context(), r, h.apiKeyRepo, h.accountRepo, h.guestKeyRepo)
	if err != nil || accountID == "" {
		if err == models.ErrNotFound {
			SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to resolve API key")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024)) // 2MB max
	if err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to read body")
		return
	}

	if h.llmURL == "" {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Agent LLM not configured")
		return
	}

	// Parse body to read client-requested model, validate against allowed list, and look up cost.
	var parsed map[string]interface{}
	_ = json.Unmarshal(bodyBytes, &parsed)
	requestedModel, _ := parsed["model"].(string)
	requestedModel = strings.TrimSpace(requestedModel)

	// Validate requested model against allowed list (if settings configured).
	if h.settingsRepo != nil && requestedModel != "" {
		allowed, _ := h.settingsRepo.GetAllowedModels(r.Context())
		if len(allowed) > 0 {
			ok := false
			for _, m := range allowed {
				if m == requestedModel {
					ok = true
					break
				}
			}
			if !ok {
				SendError(w, http.StatusBadRequest, "INVALID_MODEL", "Requested model is not allowed")
				return
			}
		}
	}

	// Resolve effective model: client request > tracker env fallback > leave as-is.
	effectiveModel := requestedModel
	if effectiveModel == "" {
		effectiveModel = h.llmModel
	}

	// Look up per-model credit cost from settings (falls back to default if unset).
	cost := agentCompletionCostDefault
	if h.settingsRepo != nil && effectiveModel != "" {
		if c, err := h.settingsRepo.GetModelCost(r.Context(), effectiveModel, agentCompletionCostDefault); err == nil {
			cost = c
		}
	}

	requestID := idempotencyKey(accountID, bodyBytes)

	// Branch spend logic by model. Mini spends free→paid; detailed is paid-only
	// with trial fallback. Returns the actual deduction (0 for trial calls) and
	// the source bucket so refunds can target the right place.
	// An autopilot draft (X-StonkAgents-Auto: 1, the marker the daemon puts on its auto
	// board writes) is recorded under the autopilot spend reasons for the outcomes summary.
	autopilot := strings.TrimSpace(r.Header.Get(AutopilotMarkerHeader)) == "1"
	spendResult, spendErr := h.creditSvc.SpendForModelAs(r.Context(), accountID, effectiveModel, cost, requestID, autopilot)
	if spendErr != nil {
		switch {
		case errors.Is(spendErr, models.ErrInsufficientCredits):
			SendError(w, http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", "Not enough credits")
		case errors.Is(spendErr, models.ErrPaidCreditsRequired):
			SendError(w, http.StatusPaymentRequired, "PAID_CREDITS_REQUIRED", "Detailed mode requires paid credits. Top up to continue.")
		case errors.Is(spendErr, models.ErrInsufficientPaidCredits):
			SendError(w, http.StatusPaymentRequired, "INSUFFICIENT_PAID_CREDITS", "Not enough paid credits for detailed mode")
		default:
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to deduct credits")
		}
		return
	}

	// Inject effective model + output cap into the upstream request body.
	// Output cap guards against runaway output blowing the 75-credit budget.
	//
	// Reasoning-class OpenAI models (o-series, gpt-5.x family) replaced
	// max_tokens with max_completion_tokens; sending max_tokens to those
	// models returns 400 "Unsupported parameter". We use max_completion_tokens
	// for the detailed tier on that assumption — switch to a per-model setting
	// (PR 3 of the audit) if we add detailed models that prefer max_tokens.
	if parsed == nil {
		parsed = map[string]interface{}{}
	}
	if effectiveModel != "" {
		parsed["model"] = effectiveModel
	}
	if isDetailedModel(effectiveModel) {
		// Strip a stale max_tokens if the client sent one; reasoning models reject it.
		delete(parsed, "max_tokens")
		// Only inject if client didn't already specify a smaller cap.
		if existing, _ := parsed["max_completion_tokens"].(float64); existing == 0 || existing > detailedMaxOutputTokens {
			parsed["max_completion_tokens"] = detailedMaxOutputTokens
		}
	}
	if rewritten, err := json.Marshal(parsed); err == nil {
		bodyBytes = rewritten
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.llmURL, bytes.NewReader(bodyBytes))
	if err != nil {
		// Refund since we already deducted.
		h.refundOnError(accountID, spendResult, requestID)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create upstream request")
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	if h.llmAPIKey != "" {
		switch h.llmProvider {
		case "anthropic":
			proxyReq.Header.Set("x-api-key", h.llmAPIKey)
			proxyReq.Header.Set("anthropic-version", "2023-06-01")
		default: // openai
			proxyReq.Header.Set("Authorization", "Bearer "+h.llmAPIKey)
		}
	}

	client := &http.Client{Timeout: agentProxyTimeout}
	resp, err := client.Do(proxyReq)
	if err != nil {
		// Client cancelled the request before the upstream completed (e.g. user clicked Stop).
		// Refund the credits to the bucket they came from.
		if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
			h.refundOnError(accountID, spendResult, requestID)
			// Status 499 (client closed request) — non-standard but informative.
			SendError(w, 499, "CLIENT_CANCELLED", "Request cancelled by client")
			return
		}
		// Real upstream failure — refund as well, since user got nothing.
		h.refundOnError(accountID, spendResult, requestID)
		SendError(w, http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Upstream LLM unreachable")
		return
	}
	defer resp.Body.Close()

	// Read full body so we can both forward to client and parse the usage block.
	respBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		h.refundOnError(accountID, spendResult, requestID)
		SendError(w, http.StatusBadGateway, "GATEWAY_READ_FAILED", "Failed to read upstream response")
		return
	}

	// Non-2xx from upstream — user got no useful response, so refund credits/trial
	// before forwarding the upstream error body. Without this, every model-name or
	// parameter mismatch silently drains user balance.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.refundOnError(accountID, spendResult, requestID)
		for k, v := range resp.Header {
			if strings.ToLower(k) == "content-type" || strings.ToLower(k) == "content-length" {
				for _, vv := range v {
					w.Header().Add(k, vv)
				}
			}
		}
		// Signal to the daemon that nothing was charged after the refund.
		w.Header().Set("X-Credits-Deducted", "0")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBytes)
		return
	}

	// Log token usage for margin validation. Best-effort; never fails the request.
	if h.usageRepo != nil {
		if in, out, ok := parseUsage(respBytes); ok {
			cost := estimateCostUSD(effectiveModel, in, out)
			_ = h.usageRepo.Insert(r.Context(), repository.LLMUsageEntry{
				AccountID:        accountID,
				Model:            effectiveModel,
				InputTokens:      in,
				OutputTokens:     out,
				EstimatedCostUSD: cost,
				CreditsCharged:   spendResult.CreditsDeducted,
				RequestID:        requestID,
			})
		}
	}

	// Forward selected headers + body.
	for k, v := range resp.Header {
		if strings.ToLower(k) == "content-type" || strings.ToLower(k) == "content-length" {
			for _, vv := range v {
				w.Header().Add(k, vv)
			}
		}
	}
	// Tell the daemon how many credits were actually deducted (0 for trial calls).
	w.Header().Set("X-Credits-Deducted", strconv.Itoa(spendResult.CreditsDeducted))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)
}

// refundOnError reverses a prior SpendForModel using a detached context so the
// refund completes even when the request context was cancelled.
func (h *AgentCompletionsHandler) refundOnError(accountID string, spendResult services.SpendResult, requestID string) {
	refundCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.creditSvc.Refund(refundCtx, accountID, spendResult.CreditsDeducted, agentCompletionRefundReason, requestID, spendResult.Source)
}

// isDetailedModel mirrors the heuristic in CreditService — anything not -mini
// is detailed.
func isDetailedModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	return !strings.Contains(m, "-mini")
}

// parseUsage extracts input/output token counts from an OpenAI chat completion
// response body. Returns false if the usage block is missing.
func parseUsage(body []byte) (input, output int, ok bool) {
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, 0, false
	}
	if resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
		return 0, 0, false
	}
	return resp.Usage.PromptTokens, resp.Usage.CompletionTokens, true
}

// estimateCostUSD returns the OpenAI USD cost for a call. Hard-coded for the
// two models we currently support; falls back to mini pricing for unknowns.
func estimateCostUSD(model string, inputTokens, outputTokens int) float64 {
	const (
		miniInputPerM  = 0.75
		miniOutputPerM = 4.50
		fullInputPerM  = 2.50
		fullOutputPerM = 15.00
	)
	in, out := miniInputPerM, miniOutputPerM
	if isDetailedModel(model) {
		in, out = fullInputPerM, fullOutputPerM
	}
	return (float64(inputTokens)/1e6)*in + (float64(outputTokens)/1e6)*out
}

func idempotencyKey(accountID string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(accountID))
	h.Write(body)
	return "agent:" + hex.EncodeToString(h.Sum(nil))[:32]
}
