// Package: tracker/internal/api
// Feature: F-031 (Token Data Persistence)
// Story: US-031-01, US-031-02 (Backend Token Persistence + Metrics Aggregation)
// Purpose: HTTP handlers for token identity persistence (POST), retrieval (GET), and metrics (GET)

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/mr-tron/base58"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// tokenBodyLimit is the max request body size for POST /api/token (4 KB).
const tokenBodyLimit = 4 * 1024

// tokenHandlerTimeout leaves 3s margin for JSON + response write below server's 15s WriteTimeout.
const tokenHandlerTimeout = 12 * time.Second

// persistTokenDTO is the request body for POST /api/token.
type persistTokenDTO struct {
	TokenContractAddress string `json:"token_contract_address"`
	TokenTicker          string `json:"token_ticker"`
	TokenName            string `json:"token_name"`
	TokenImageURL        string `json:"token_image_url"`
}

// TokenHandler handles token identity HTTP requests.
type TokenHandler struct {
	tokenRepo repository.TokenRepository
	peerRepo  repository.PeerRepository // nil OK — display_name is then omitted
	metrics   *services.MetricsService  // nil OK — metrics endpoint returns 503
	// reputation resolves reputation_tier for the bound peers (nil OK: omitted).
	reputation *services.ReputationService
	logger     *slog.Logger
}

// peerTokenDTO is a PeerToken plus the owner-set display name of the bound peer.
type peerTokenDTO struct {
	*models.PeerToken
	// DisplayName is the peer's owner-set agent name; omitted when unset.
	DisplayName string `json:"display_name,omitempty"`
	// ReputationTier is the peer's community board tier; omitted without the reputation service.
	ReputationTier string `json:"reputation_tier,omitempty"`
}

// NewTokenHandler creates a new TokenHandler.
func NewTokenHandler(tokenRepo repository.TokenRepository) *TokenHandler {
	return &TokenHandler{
		tokenRepo: tokenRepo,
		logger:    slog.Default(),
	}
}

// SetMetricsService wires the optional MetricsService (called from main.go after construction).
func (h *TokenHandler) SetMetricsService(ms *services.MetricsService) {
	h.metrics = ms
}

// SetPeerRepo wires the peer repository used to resolve display names (nil-safe).
func (h *TokenHandler) SetPeerRepo(repo repository.PeerRepository) {
	h.peerRepo = repo
}

// SetReputationService wires the board reputation used for reputation_tier (nil-safe).
func (h *TokenHandler) SetReputationService(r *services.ReputationService) {
	h.reputation = r
}

// reputationTiers resolves peer id -> board tier in one lookup (empty without the service).
func (h *TokenHandler) reputationTiers(ctx context.Context, peerIDs []string) map[string]string {
	if h.reputation == nil {
		return map[string]string{}
	}
	return h.reputation.TiersByIDs(ctx, peerIDs)
}

// isValidBase58 checks if a string is valid base58 (Solana address format).
func isValidBase58(s string) bool {
	if s == "" {
		return false
	}
	_, err := base58.Decode(s)
	return err == nil
}

// HandlePersistToken handles POST /api/token (authenticated via RequireAPIKey middleware).
// Persists token identity for the authenticated peer. Idempotent on conflict (409 ALREADY_EXISTS).
func (h *TokenHandler) HandlePersistToken(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	limitedBody := io.LimitReader(r.Body, tokenBodyLimit)
	var dto persistTokenDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	// Validate required fields
	dto.TokenContractAddress = strings.TrimSpace(dto.TokenContractAddress)
	dto.TokenTicker = strings.TrimSpace(dto.TokenTicker)
	dto.TokenName = strings.TrimSpace(dto.TokenName)

	if dto.TokenContractAddress == "" || dto.TokenTicker == "" || dto.TokenName == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "token_contract_address, token_ticker, and token_name are required")
		return
	}

	// Validate base58 (Solana address format)
	if !isValidBase58(dto.TokenContractAddress) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "token_contract_address must be a valid base58 Solana address")
		return
	}

	// Gate 0 Finding #3 fix: Length validation before DB to return 400, not 500
	if len(dto.TokenTicker) > 20 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "token_ticker must be 20 characters or less")
		return
	}
	if len(dto.TokenName) > 255 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "token_name must be 255 characters or less")
		return
	}

	token := &models.PeerToken{
		PeerID:               peerID,
		TokenContractAddress: dto.TokenContractAddress,
		TokenTicker:          dto.TokenTicker,
		TokenName:            dto.TokenName,
		TokenImageURL:        dto.TokenImageURL,
		LaunchedAt:           time.Now().UTC(),
	}

	if err := h.tokenRepo.Create(r.Context(), token); err != nil {
		if err == models.ErrAlreadyExists {
			SendError(w, http.StatusConflict, "ALREADY_EXISTS", "Token identity already persisted for this peer")
			return
		}
		h.logger.Error("failed to persist token", "peer_id", peerID, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to persist token")
		return
	}

	SendJSON(w, http.StatusCreated, DataEnvelope{Data: token})
}

// HandleGetPeerToken handles GET /api/peers/{id}/token (public read, no auth).
// Returns the token identity for a peer, or 404 if no token exists.
func (h *TokenHandler) HandleGetPeerToken(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}

	token, err := h.tokenRepo.GetByPeerID(r.Context(), peerID)
	if err != nil {
		if err == models.ErrNotFound {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "No token found for this peer")
			return
		}
		h.logger.Error("failed to get peer token", "peer_id", peerID, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get token")
		return
	}

	names := displayNamesFor(r.Context(), h.peerRepo, []string{token.PeerID})
	tiers := h.reputationTiers(r.Context(), []string{token.PeerID})
	SendData(w, peerTokenDTO{PeerToken: token, DisplayName: names[token.PeerID], ReputationTier: tiers[token.PeerID]})
}

// HandleGetTokenMetrics handles GET /api/peers/{id}/token/metrics (public read, rate limited).
// Returns aggregated on-chain metrics from pump.fun and Moralis.
func (h *TokenHandler) HandleGetTokenMetrics(w http.ResponseWriter, r *http.Request) {
	if h.metrics == nil {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Token metrics service not configured")
		return
	}

	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}

	// AC-2: 12s context timeout (below server's 15s WriteTimeout)
	ctx, cancel := context.WithTimeout(r.Context(), tokenHandlerTimeout)
	defer cancel()

	metrics, err := h.metrics.GetTokenMetrics(ctx, peerID)
	if err != nil {
		if err == models.ErrNotFound {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "No token found for this peer")
			return
		}
		h.logger.Error("[TokenHandler.HandleGetTokenMetrics] failed",
			"peer_id", peerID,
			"error", err,
		)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get token metrics")
		return
	}

	SendData(w, metrics)
}

// tokenListItem is a single item in the token listing response.
type tokenListItem struct {
	PeerID string `json:"peer_id"`
	// DisplayName is the peer's owner-set agent name; omitted when unset.
	DisplayName string `json:"display_name,omitempty"`
	// ReputationTier is the peer's community board tier; omitted without the reputation service.
	ReputationTier       string               `json:"reputation_tier,omitempty"`
	TokenContractAddress string               `json:"token_contract_address"`
	TokenTicker          string               `json:"token_ticker"`
	TokenName            string               `json:"token_name"`
	TokenImageURL        string               `json:"token_image_url,omitempty"`
	LaunchedAt           time.Time            `json:"launched_at"`
	Metrics              *models.TokenMetrics `json:"metrics"`
}

// paginatedMeta holds pagination metadata for list responses.
type paginatedMeta struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// paginatedTokenResponse is the response envelope for GET /api/tokens.
type paginatedTokenResponse struct {
	Data []tokenListItem `json:"data"`
	Meta paginatedMeta   `json:"meta"`
}

// HandleListTokens handles GET /api/tokens (public read, paginated).
// Returns all tokens with cached metrics merged in.
func (h *TokenHandler) HandleListTokens(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, 20, 100)

	tokens, total, err := h.tokenRepo.List(r.Context(), limit, offset)
	if err != nil {
		h.logger.Error("[TokenHandler.HandleListTokens] list failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list tokens")
		return
	}

	// Batch-get cached metrics for all contract addresses
	var metricsMap map[string]*models.TokenMetrics
	if h.metrics != nil {
		addrs := make([]string, len(tokens))
		for i, t := range tokens {
			addrs[i] = t.TokenContractAddress
		}
		metricsMap = h.metrics.BatchGetCachedMetrics(r.Context(), addrs)
	}

	// One lookup for every peer on the page
	peerIDs := make([]string, len(tokens))
	for i, t := range tokens {
		peerIDs[i] = t.PeerID
	}
	names := displayNamesFor(r.Context(), h.peerRepo, peerIDs)
	tiers := h.reputationTiers(r.Context(), peerIDs)

	// Build response items
	items := make([]tokenListItem, len(tokens))
	for i, t := range tokens {
		items[i] = tokenListItem{
			PeerID:               t.PeerID,
			DisplayName:          names[t.PeerID],
			ReputationTier:       tiers[t.PeerID],
			TokenContractAddress: t.TokenContractAddress,
			TokenTicker:          t.TokenTicker,
			TokenName:            t.TokenName,
			TokenImageURL:        t.TokenImageURL,
			LaunchedAt:           t.LaunchedAt,
			Metrics:              metricsMap[t.TokenContractAddress],
		}
	}

	SendJSON(w, http.StatusOK, paginatedTokenResponse{
		Data: items,
		Meta: paginatedMeta{Total: total, Limit: limit, Offset: offset},
	})
}
