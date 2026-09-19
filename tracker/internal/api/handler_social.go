// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: HTTP handlers for social connections (confirm, list, disconnect)

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// SocialConfirmDTO is the request body for POST /api/v1/social/confirm.
type SocialConfirmDTO struct {
	AccountID      string `json:"account_id"`
	Platform       string `json:"platform"`
	PlatformUserID string `json:"platform_user_id"`
	Verified       bool   `json:"verified"`
}

// SocialConfirmResponseDTO is the response for a successful social confirmation.
type SocialConfirmResponseDTO struct {
	BonusGranted int              `json:"bonus_granted"`
	NewBalance   CreditSummaryDTO `json:"new_balance"`
}

// SocialConnectionDTO represents a single social connection.
type SocialConnectionDTO struct {
	Platform       string `json:"platform"`
	PlatformUserID string `json:"platform_user_id"`
	VerifiedAt     string `json:"verified_at"`
	BonusGranted   int    `json:"bonus_granted"`
}

// SocialConnectionListDTO is the response for GET /api/v1/social/connections.
type SocialConnectionListDTO struct {
	Connections []SocialConnectionDTO `json:"connections"`
}

// SocialHandler handles social connection HTTP endpoints.
type SocialHandler struct {
	socialSvc      *services.SocialService
	accounts       repository.AccountRepository
	callbackSecret string
}

// NewSocialHandler creates a new SocialHandler.
func NewSocialHandler(svc *services.SocialService, accounts repository.AccountRepository, callbackSecret string) *SocialHandler {
	return &SocialHandler{
		socialSvc:      svc,
		accounts:       accounts,
		callbackSecret: callbackSecret,
	}
}

// HandleConfirm handles POST /api/v1/social/confirm (authenticated via callback secret).
func (h *SocialHandler) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	// Authenticate via Bearer token
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") || strings.TrimPrefix(authHeader, "Bearer ") != h.callbackSecret {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid callback secret")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto SocialConfirmDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	if !dto.Verified {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Verification not confirmed")
		return
	}

	resp, err := h.socialSvc.ConfirmConnection(r.Context(), services.ConfirmConnectionRequest{
		AccountID:      dto.AccountID,
		Platform:       dto.Platform,
		PlatformUserID: dto.PlatformUserID,
	})
	if err == models.ErrAlreadyExists {
		SendError(w, http.StatusConflict, "ALREADY_CONNECTED", "Platform already connected for this account")
		return
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Account not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to confirm connection")
		return
	}

	SendData(w, SocialConfirmResponseDTO{
		BonusGranted: resp.BonusGranted,
		NewBalance: CreditSummaryDTO{
			Free: resp.NewBalance.Free,
			Paid: resp.NewBalance.Paid,
		},
	})
}

// HandleListConnections handles GET /api/v1/social/connections (API key auth).
func (h *SocialHandler) HandleListConnections(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	account, err := h.accounts.GetByPeerID(r.Context(), peerID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "No account found for this peer")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to look up account")
		return
	}

	conns, err := h.socialSvc.ListConnections(r.Context(), account.ID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list connections")
		return
	}

	dtos := make([]SocialConnectionDTO, 0, len(conns))
	for _, c := range conns {
		dtos = append(dtos, SocialConnectionDTO{
			Platform:       c.Platform,
			PlatformUserID: c.PlatformUserID,
			VerifiedAt:     c.VerifiedAt.Format("2006-01-02T15:04:05Z07:00"),
			BonusGranted:   c.BonusGranted,
		})
	}

	SendData(w, SocialConnectionListDTO{Connections: dtos})
}

// HandleDisconnect handles DELETE /api/v1/social/{platform} (API key auth).
func (h *SocialHandler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	account, err := h.accounts.GetByPeerID(r.Context(), peerID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "No account found for this peer")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to look up account")
		return
	}

	platform := mux.Vars(r)["platform"]
	if platform == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing platform")
		return
	}

	if err := h.socialSvc.Disconnect(r.Context(), account.ID, platform); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to disconnect")
		return
	}

	SendData(w, map[string]string{"status": "disconnected"})
}
