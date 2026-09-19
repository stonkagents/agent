// Package: tracker/internal/api
// Feature: StonkAgents roadmap interest
// Purpose: POST /api/v1/interest (public, rate limited) stores a capability vote, answers how
//          many agents want each selected capability and forwards it to a chat webhook
//          best-effort; GET /api/v1/admin/interest (X-Admin-Key) pages it back and
//          GET /api/v1/admin/interest/summary rolls it up for product development.

package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

const (
	interestBodyLimit         = 16 * 1024
	interestAdminDefaultLimit = 50
	interestAdminMaxLimit     = 200
)

// InterestForwarder posts a stored submission to a chat webhook (implemented by
// *services.FeedbackForwarder). Errors are logged by the handler, never returned to the user.
type InterestForwarder interface {
	ForwardInterest(ctx context.Context, it *models.AgentInterest) error
}

// submitInterestDTO is the body of POST /api/v1/interest (camelCase, sent by the portal).
type submitInterestDTO struct {
	Capabilities  []string `json:"capabilities"`
	Description   string   `json:"description"`
	Priority      string   `json:"priority"`
	Contact       string   `json:"contact"`
	ContactVia    string   `json:"contactVia"`
	WalletAddress string   `json:"walletAddress"`
	Path          string   `json:"path"`
}

// submitInterestResponse is the body of a successful POST /api/v1/interest.
type submitInterestResponse struct {
	OK bool `json:"ok"`
	// Counts is, per selected capability, how many submissions (this one included) contain it.
	Counts map[string]int64 `json:"counts"`
}

// interestListResponse is the body of GET /api/v1/admin/interest.
type interestListResponse struct {
	Data []*models.AgentInterest `json:"data"`
	// NextCursor is the cursor for the next page; absent on the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}

// InterestHandlerDeps holds dependencies for InterestHandler.
type InterestHandlerDeps struct {
	Repo repository.InterestRepository
	// Forwarder is optional (nil = store only).
	Forwarder InterestForwarder
	// AdminKey guards the admin routes; empty makes them answer 503 ADMIN_DISABLED.
	AdminKey string
	Clock    clock.Clock
	Logger   *slog.Logger
}

// InterestHandler serves the roadmap interest endpoints.
type InterestHandler struct {
	repo      repository.InterestRepository
	forwarder InterestForwarder
	adminKey  string
	clock     clock.Clock
	logger    *slog.Logger
	forwards  sync.WaitGroup
}

// NewInterestHandler creates an InterestHandler.
func NewInterestHandler(d InterestHandlerDeps) *InterestHandler {
	h := &InterestHandler{repo: d.Repo, forwarder: d.Forwarder, adminKey: strings.TrimSpace(d.AdminKey), clock: d.Clock, logger: d.Logger}
	if h.clock == nil {
		h.clock = clock.RealClock{}
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	return h
}

// AdminEnabled reports whether an admin key is configured (the admin routes answer, not 503).
func (h *InterestHandler) AdminEnabled() bool { return h != nil && h.adminKey != "" }

// WaitForwards blocks until every in-flight webhook forward has finished (tests, shutdown).
func (h *InterestHandler) WaitForwards() { h.forwards.Wait() }

// authorizedAdmin compares the request header to the admin key in constant time.
func (h *InterestHandler) authorizedAdmin(r *http.Request) bool {
	got := r.Header.Get(AdminKeyHeader)
	return h.adminKey != "" && subtle.ConstantTimeCompare([]byte(got), []byte(h.adminKey)) == 1
}

// normalizeCapabilities lowercases, trims and de-duplicates keys, preserving first-seen order.
// The second result is the first unknown key ("" when every key is allowed).
func normalizeCapabilities(raw []string) (keys []string, unknown string) {
	seen := map[string]bool{}
	for _, k := range raw {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || seen[k] {
			continue
		}
		if !models.IsValidInterestCapability(k) {
			return nil, k
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys, ""
}

// validateInterestDTO trims and validates the body; returns a message ("" = valid).
func validateInterestDTO(d *submitInterestDTO) string {
	keys, unknown := normalizeCapabilities(d.Capabilities)
	d.Capabilities = keys
	d.Description = strings.TrimSpace(d.Description)
	d.Priority = strings.ToLower(strings.TrimSpace(d.Priority))
	d.Path = strings.TrimSpace(d.Path)
	d.Contact = strings.TrimSpace(d.Contact)
	d.ContactVia = strings.ToLower(strings.TrimSpace(d.ContactVia))
	d.WalletAddress = strings.TrimSpace(d.WalletAddress)

	hasOther := false
	for _, k := range keys {
		if k == models.InterestCapabilityOther {
			hasOther = true
		}
	}
	switch {
	case unknown != "":
		return "capabilities must be among: " + strings.Join(models.InterestCapabilities, ", ")
	case len(keys) == 0:
		return "capabilities must name at least one capability"
	case len(keys) > models.InterestMaxCapabilities:
		return "capabilities must name at most " + strconv.Itoa(models.InterestMaxCapabilities)
	case utf8.RuneCountInString(d.Description) > models.InterestMaxDescriptionChars || !isSafeMultilineText(d.Description):
		return "description must be at most 600 characters"
	case hasOther && d.Description == "":
		return "description is required with \"other\""
	case !models.IsValidInterestPriority(d.Priority):
		return "priority must be one of: " + strings.Join(models.InterestPriorities, ", ")
	case utf8.RuneCountInString(d.Path) > models.InterestMaxPathChars || !isSafeText(d.Path):
		return "path must be at most 200 characters"
	case utf8.RuneCountInString(d.Contact) > models.InterestMaxContactChars || !isSafeText(d.Contact):
		return "contact must be at most 200 characters"
	case utf8.RuneCountInString(d.ContactVia) > models.InterestMaxContactViaChars || !isSafeText(d.ContactVia):
		return "contactVia must be at most 40 characters"
	case d.WalletAddress != "" && !isPubkey(d.WalletAddress):
		return "walletAddress must be a base58 32-byte public key"
	}
	return ""
}

// HandleSubmit handles POST /api/v1/interest (public). 200 {"ok":true,"counts":{...}} once
// stored; the webhook forward runs in the background and can never fail the request.
func (h *InterestHandler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	var dto submitInterestDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, interestBodyLimit)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	if msg := validateInterestDTO(&dto); msg != "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
		return
	}
	it := &models.AgentInterest{
		Capabilities: dto.Capabilities, Description: dto.Description, Priority: dto.Priority,
		Contact: dto.Contact, ContactVia: dto.ContactVia, WalletAddress: dto.WalletAddress,
		UserAgent: truncateRunes(r.UserAgent(), models.InterestMaxUserAgentChars),
		IPHash:    hashIP(ratelimit.IPKey(r)), Path: dto.Path, CreatedAt: h.clock.Now().UTC(),
	}
	if err := h.repo.Create(r.Context(), it); err != nil {
		h.logger.Error("[InterestHandler.HandleSubmit] insert failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to store interest")
		return
	}
	h.forwardAsync(it)
	counts, err := h.repo.CountByCapability(r.Context(), it.Capabilities)
	if err != nil {
		// The row is stored: answer ok without the tally rather than fail a landed submission.
		h.logger.Error("[InterestHandler.HandleSubmit] count failed", "error", err)
		counts = map[string]int64{}
	}
	SendJSON(w, http.StatusOK, submitInterestResponse{OK: true, Counts: counts})
}

// forwardAsync posts the stored submission to the webhook without blocking the response.
func (h *InterestHandler) forwardAsync(it *models.AgentInterest) {
	if h.forwarder == nil {
		return
	}
	copied := *it
	copied.Capabilities = append([]string(nil), it.Capabilities...)
	h.forwards.Add(1)
	go func() {
		defer h.forwards.Done()
		ctx, cancel := context.WithTimeout(context.Background(), feedbackForwardTimeout)
		defer cancel()
		if err := h.forwarder.ForwardInterest(ctx, &copied); err != nil {
			h.logger.Error("[InterestHandler] webhook forward failed", "interest_id", copied.ID, "error", err)
		}
	}()
}

// requireAdmin answers 503 (ADMIN_API_KEY unset) or 401 and reports whether the request may proceed.
func (h *InterestHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !h.AdminEnabled() {
		SendError(w, http.StatusServiceUnavailable, AdminDisabledCode, adminDisabledMsg)
		return false
	}
	if !h.authorizedAdmin(r) {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing admin key")
		return false
	}
	return true
}

// HandleAdminList handles GET /api/v1/admin/interest?limit=&cursor= (X-Admin-Key).
// Newest first; cursor is the last id of the previous page (opaque to clients).
func (h *InterestHandler) HandleAdminList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	limit, _ := parsePagination(r, interestAdminDefaultLimit, interestAdminMaxLimit)
	var before int64
	if c := strings.TrimSpace(r.URL.Query().Get("cursor")); c != "" {
		parsed, err := strconv.ParseInt(c, 10, 64)
		if err != nil || parsed <= 0 {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "cursor must be a positive integer id")
			return
		}
		before = parsed
	}
	items, err := h.repo.List(r.Context(), repository.ListInterestOptions{Limit: limit + 1, BeforeID: before})
	if err != nil {
		h.logger.Error("[InterestHandler.HandleAdminList] list failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list interest")
		return
	}
	resp := interestListResponse{Data: items}
	if len(items) > limit {
		resp.Data = items[:limit]
		resp.NextCursor = strconv.FormatInt(items[limit-1].ID, 10)
	}
	if resp.Data == nil {
		resp.Data = []*models.AgentInterest{}
	}
	SendJSON(w, http.StatusOK, resp)
}

// HandleAdminSummary handles GET /api/v1/admin/interest/summary (X-Admin-Key): the total and
// the per-capability and per-priority counts, every allowed key present.
func (h *InterestHandler) HandleAdminSummary(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	s, err := h.repo.Summary(r.Context())
	if err != nil {
		h.logger.Error("[InterestHandler.HandleAdminSummary] summary failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to summarize interest")
		return
	}
	SendJSON(w, http.StatusOK, s)
}
