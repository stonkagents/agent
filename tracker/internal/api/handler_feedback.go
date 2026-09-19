// Package: tracker/internal/api
// Feature: StonkAgents portal feedback
// Purpose: POST /api/v1/feedback (public, rate limited) stores a submission and forwards it to
//          a chat webhook best-effort; GET /api/v1/admin/feedback (X-Admin-Key) pages it back.

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	// AdminKeyHeader carries the shared secret for admin read endpoints (env ADMIN_API_KEY).
	AdminKeyHeader = "X-Admin-Key"
	// TurnstileTokenHeader carries the Cloudflare Turnstile widget response the portal sends
	// with a feedback submission; verified server-side when TURNSTILE_SECRET_KEY is set.
	TurnstileTokenHeader = "X-Turnstile-Token"
	// AdminDisabledCode is the 503 code of the admin read endpoints when ADMIN_API_KEY is unset.
	AdminDisabledCode = "ADMIN_DISABLED"
	adminDisabledMsg  = "Admin reads are disabled: ADMIN_API_KEY is not configured on this tracker"

	feedbackBodyLimit         = 16 * 1024
	feedbackAdminDefaultLimit = 50
	feedbackAdminMaxLimit     = 200
	// feedbackForwardTimeout bounds the background webhook call (independent of the request).
	feedbackForwardTimeout = 8 * time.Second
)

// FeedbackForwarder posts a stored submission to a chat webhook (implemented by
// *services.FeedbackForwarder). Errors are logged by the handler, never returned to the user.
type FeedbackForwarder interface {
	Forward(ctx context.Context, fb *models.Feedback) error
}

// submitFeedbackDTO is the body of POST /api/v1/feedback (camelCase, sent by the portal).
type submitFeedbackDTO struct {
	Kind          string `json:"kind"`
	Message       string `json:"message"`
	Path          string `json:"path"`
	Contact       string `json:"contact"`
	ContactVia    string `json:"contactVia"`
	WalletAddress string `json:"walletAddress"`
}

// feedbackListResponse is the body of GET /api/v1/admin/feedback.
type feedbackListResponse struct {
	Data []*models.Feedback `json:"data"`
	// NextCursor is the cursor for the next page; absent on the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}

// TurnstileChecker verifies a Turnstile widget response (implemented by *services.TurnstileVerifier).
type TurnstileChecker interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// FeedbackHandlerDeps holds dependencies for FeedbackHandler.
type FeedbackHandlerDeps struct {
	Repo repository.FeedbackRepository
	// Forwarder is optional (nil = store only).
	Forwarder FeedbackForwarder
	// Turnstile is optional (nil = the X-Turnstile-Token header is not verified).
	Turnstile TurnstileChecker
	// AdminKey guards GET /api/v1/admin/feedback; empty makes the route answer 503 ADMIN_DISABLED.
	AdminKey string
	Clock    clock.Clock
	Logger   *slog.Logger
}

// FeedbackHandler serves the feedback endpoints.
type FeedbackHandler struct {
	repo      repository.FeedbackRepository
	forwarder FeedbackForwarder
	turnstile TurnstileChecker
	adminKey  string
	clock     clock.Clock
	logger    *slog.Logger
	forwards  sync.WaitGroup
}

// NewFeedbackHandler creates a FeedbackHandler.
func NewFeedbackHandler(d FeedbackHandlerDeps) *FeedbackHandler {
	h := &FeedbackHandler{repo: d.Repo, forwarder: d.Forwarder, turnstile: d.Turnstile, adminKey: strings.TrimSpace(d.AdminKey), clock: d.Clock, logger: d.Logger}
	if h.clock == nil {
		h.clock = clock.RealClock{}
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	return h
}

// AdminEnabled reports whether an admin key is configured (the admin route answers, not 503).
func (h *FeedbackHandler) AdminEnabled() bool { return h != nil && h.adminKey != "" }

// TurnstileEnabled reports whether submissions must carry a valid X-Turnstile-Token.
func (h *FeedbackHandler) TurnstileEnabled() bool { return h != nil && h.turnstile != nil }

// checkTurnstile verifies the X-Turnstile-Token header when a verifier is wired. It writes
// the error response and reports whether the request may proceed.
//
//	403 TURNSTILE_FAILED       token missing or rejected by Cloudflare
//	503 TURNSTILE_UNAVAILABLE  siteverify unreachable or the server secret is wrong (fail closed)
func checkTurnstile(w http.ResponseWriter, r *http.Request, t TurnstileChecker, logger *slog.Logger) bool {
	if t == nil {
		return true
	}
	err := t.Verify(r.Context(), r.Header.Get(TurnstileTokenHeader), ratelimit.IPKey(r))
	switch {
	case err == nil:
		return true
	case errors.Is(err, services.ErrTurnstileMissing), errors.Is(err, services.ErrTurnstileRejected):
		SendError(w, http.StatusForbidden, "TURNSTILE_FAILED", "Challenge verification failed. Reload the page and try again.")
	default:
		logger.Error("[turnstile] verification unavailable", "error", err)
		SendError(w, http.StatusServiceUnavailable, "TURNSTILE_UNAVAILABLE", "Could not verify the challenge right now. Try again in a minute.")
	}
	return false
}

// WaitForwards blocks until every in-flight webhook forward has finished (tests, shutdown).
func (h *FeedbackHandler) WaitForwards() { h.forwards.Wait() }

// authorizedAdmin compares the request header to the admin key in constant time.
func (h *FeedbackHandler) authorizedAdmin(r *http.Request) bool {
	got := r.Header.Get(AdminKeyHeader)
	return h.adminKey != "" && subtle.ConstantTimeCompare([]byte(got), []byte(h.adminKey)) == 1
}

// isSafeMultilineText rejects control characters other than tab, newline and carriage return.
func isSafeMultilineText(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}

// validateFeedbackDTO trims and validates the body; returns a message ("" = valid).
func validateFeedbackDTO(d *submitFeedbackDTO) string {
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	d.Message = strings.TrimSpace(d.Message)
	d.Path = strings.TrimSpace(d.Path)
	d.Contact = strings.TrimSpace(d.Contact)
	d.ContactVia = strings.ToLower(strings.TrimSpace(d.ContactVia))
	d.WalletAddress = strings.TrimSpace(d.WalletAddress)

	switch {
	case !models.IsValidFeedbackKind(d.Kind):
		return "kind must be one of: " + strings.Join(models.FeedbackKinds, ", ")
	case d.Message == "":
		return "message is required"
	case utf8.RuneCountInString(d.Message) > models.FeedbackMaxMessageChars || !isSafeMultilineText(d.Message):
		return "message must be at most 2000 characters"
	case utf8.RuneCountInString(d.Path) > models.FeedbackMaxPathChars || !isSafeText(d.Path):
		return "path must be at most 200 characters"
	case utf8.RuneCountInString(d.Contact) > models.FeedbackMaxContactChars || !isSafeText(d.Contact):
		return "contact must be at most 200 characters"
	case utf8.RuneCountInString(d.ContactVia) > models.FeedbackMaxContactViaChars || !isSafeText(d.ContactVia):
		return "contactVia must be at most 40 characters"
	case d.WalletAddress != "" && !isPubkey(d.WalletAddress):
		return "walletAddress must be a base58 32-byte public key"
	}
	return ""
}

// hashIP returns the SHA-256 hex digest of ip ("" for an empty ip).
func hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// HandleSubmit handles POST /api/v1/feedback (public). 200 {"ok":true} once stored; the
// webhook forward runs in the background and can never fail the request.
func (h *FeedbackHandler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	var dto submitFeedbackDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, feedbackBodyLimit)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	if msg := validateFeedbackDTO(&dto); msg != "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
		return
	}
	if !checkTurnstile(w, r, h.turnstile, h.logger) {
		return
	}
	fb := &models.Feedback{
		Kind: dto.Kind, Message: dto.Message, Path: dto.Path, Contact: dto.Contact, ContactVia: dto.ContactVia,
		WalletAddress: dto.WalletAddress, UserAgent: truncateRunes(r.UserAgent(), models.FeedbackMaxUserAgentChars),
		IPHash: hashIP(ratelimit.IPKey(r)), CreatedAt: h.clock.Now().UTC(),
	}
	if err := h.repo.Create(r.Context(), fb); err != nil {
		h.logger.Error("[FeedbackHandler.HandleSubmit] insert failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to store feedback")
		return
	}
	h.forwardAsync(fb)
	SendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// forwardAsync posts the stored submission to the webhook without blocking the response.
func (h *FeedbackHandler) forwardAsync(fb *models.Feedback) {
	if h.forwarder == nil {
		return
	}
	copied := *fb
	h.forwards.Add(1)
	go func() {
		defer h.forwards.Done()
		ctx, cancel := context.WithTimeout(context.Background(), feedbackForwardTimeout)
		defer cancel()
		if err := h.forwarder.Forward(ctx, &copied); err != nil {
			h.logger.Error("[FeedbackHandler] webhook forward failed", "feedback_id", copied.ID, "kind", copied.Kind, "error", err)
		}
	}()
}

// HandleAdminList handles GET /api/v1/admin/feedback?limit=&cursor= (X-Admin-Key).
// Newest first; cursor is the last id of the previous page (opaque to clients).
func (h *FeedbackHandler) HandleAdminList(w http.ResponseWriter, r *http.Request) {
	if !h.AdminEnabled() {
		SendError(w, http.StatusServiceUnavailable, AdminDisabledCode, adminDisabledMsg)
		return
	}
	if !h.authorizedAdmin(r) {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing admin key")
		return
	}
	limit, _ := parsePagination(r, feedbackAdminDefaultLimit, feedbackAdminMaxLimit)
	var before int64
	if c := strings.TrimSpace(r.URL.Query().Get("cursor")); c != "" {
		parsed, err := strconv.ParseInt(c, 10, 64)
		if err != nil || parsed <= 0 {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "cursor must be a positive integer id")
			return
		}
		before = parsed
	}
	items, err := h.repo.List(r.Context(), repository.ListFeedbackOptions{Limit: limit + 1, BeforeID: before})
	if err != nil {
		h.logger.Error("[FeedbackHandler.HandleAdminList] list failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list feedback")
		return
	}
	resp := feedbackListResponse{Data: items}
	if len(items) > limit {
		resp.Data = items[:limit]
		resp.NextCursor = strconv.FormatInt(items[limit-1].ID, 10)
	}
	if resp.Data == nil {
		resp.Data = []*models.Feedback{}
	}
	SendJSON(w, http.StatusOK, resp)
}
