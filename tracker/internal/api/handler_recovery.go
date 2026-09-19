// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-08 (Account Recovery)
// Purpose: HTTP handler for account recovery

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// RecoverDTO is the request body for POST /api/v1/account/recover.
type RecoverDTO struct {
	OldPeerID           string   `json:"old_peer_id"`
	SocialVerifications []string `json:"social_verifications"`
}

// RecoverResponseDTO is the response for a successful recovery.
type RecoverResponseDTO struct {
	Recovered              bool `json:"recovered"`
	PaidCreditsTransferred int  `json:"paid_credits_transferred"`
}

// RecoveryHandler handles account recovery HTTP endpoints.
type RecoveryHandler struct {
	recoverySvc *services.RecoveryService
	accounts    repository.AccountRepository
}

// NewRecoveryHandler creates a new RecoveryHandler.
func NewRecoveryHandler(svc *services.RecoveryService, accounts repository.AccountRepository) *RecoveryHandler {
	return &RecoveryHandler{recoverySvc: svc, accounts: accounts}
}

// HandleRecover handles POST /api/v1/account/recover (API key auth required).
func (h *RecoveryHandler) HandleRecover(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto RecoverDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
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

	resp, err := h.recoverySvc.Recover(r.Context(), services.RecoverRequest{
		NewAccountID:        account.ID,
		OldPeerID:           dto.OldPeerID,
		SocialVerifications: dto.SocialVerifications,
	})
	if err != nil {
		if strings.Contains(err.Error(), "insufficient social proof") || strings.Contains(err.Error(), "could not verify") {
			SendError(w, http.StatusBadRequest, "INSUFFICIENT_PROOF", err.Error())
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to recover account")
		return
	}

	SendData(w, RecoverResponseDTO{
		Recovered:              resp.Recovered,
		PaidCreditsTransferred: resp.PaidCreditsTransferred,
	})
}
