// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: HTTP handler for wallet linking

package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// LinkWalletDTO is the request body for POST /api/v1/wallet/link.
type LinkWalletDTO struct {
	WalletAddress string `json:"wallet_address"`
	Chain         string `json:"chain"`
	Signature     string `json:"signature"` // base64-encoded
}

// LinkWalletResponseDTO is the response for a successful wallet link.
type LinkWalletResponseDTO struct {
	Linked       bool             `json:"linked"`
	BonusGranted int              `json:"bonus_granted"`
	NewBalance   CreditSummaryDTO `json:"new_balance"`
}

// WalletHandler handles wallet-related HTTP endpoints.
type WalletHandler struct {
	walletSvc *services.WalletService
	accounts  repository.AccountRepository
}

// NewWalletHandler creates a new WalletHandler.
func NewWalletHandler(svc *services.WalletService, accounts repository.AccountRepository) *WalletHandler {
	return &WalletHandler{walletSvc: svc, accounts: accounts}
}

// HandleLinkWallet handles POST /api/v1/wallet/link (API key auth required).
func (h *WalletHandler) HandleLinkWallet(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto LinkWalletDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	sig, err := base64.StdEncoding.DecodeString(dto.Signature)
	if err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid base64 in signature")
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

	resp, err := h.walletSvc.LinkWallet(r.Context(), services.LinkWalletRequest{
		AccountID:     account.ID,
		WalletAddress: dto.WalletAddress,
		Chain:         dto.Chain,
		Signature:     sig,
	})
	if err == models.ErrAlreadyExists {
		SendError(w, http.StatusConflict, "ALREADY_LINKED", "Wallet already linked")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to link wallet")
		return
	}

	SendData(w, LinkWalletResponseDTO{
		Linked:       resp.Linked,
		BonusGranted: resp.BonusGranted,
		NewBalance: CreditSummaryDTO{
			Free: resp.NewBalance.Free,
			Paid: resp.NewBalance.Paid,
		},
	})
}
