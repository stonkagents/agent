// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: HTTP handlers for purchase intent creation and verification

package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// CreateIntentDTO is the request body for POST /api/v1/purchase/intent.
type CreateIntentDTO struct {
	AmountLamports int64 `json:"amount_lamports"`
}

// CreateIntentResponseDTO is the response for a successful intent creation.
type CreateIntentResponseDTO struct {
	IntentID        string `json:"intent_id"`
	TreasuryAddress string `json:"treasury_address"`
	AmountLamports  int64  `json:"amount_lamports"`
	CreditAmount    int    `json:"credit_amount"`
	Memo            string `json:"memo"`
	ExpiresAt       string `json:"expires_at"`
}

// VerifyPurchaseDTO is the request body for POST /api/v1/purchase/verify.
type VerifyPurchaseDTO struct {
	IntentID    string `json:"intent_id"`
	TxSignature string `json:"tx_signature"`
}

// VerifyPurchaseResponseDTO is the response for a successful purchase verification.
type VerifyPurchaseResponseDTO struct {
	CreditsGranted int              `json:"credits_granted"`
	NewBalance     CreditSummaryDTO `json:"new_balance"`
}

// PurchaseHandler handles purchase-related HTTP endpoints.
type PurchaseHandler struct {
	purchaseSvc *services.PurchaseService
	accounts    repository.AccountRepository
}

// NewPurchaseHandler creates a new PurchaseHandler.
func NewPurchaseHandler(svc *services.PurchaseService, accounts repository.AccountRepository) *PurchaseHandler {
	return &PurchaseHandler{purchaseSvc: svc, accounts: accounts}
}

// HandleCreateIntent handles POST /api/v1/purchase/intent (API key auth required).
func (h *PurchaseHandler) HandleCreateIntent(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto CreateIntentDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	if dto.AmountLamports <= 0 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Amount must be positive")
		return
	}

	// Minimum purchase: 100 credits (smallest tier). Frontend enforces 2001+ for custom amounts.
	const minLamports = 100 * services.LamportsPerCredit
	if dto.AmountLamports < minLamports {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Minimum purchase is 100 credits")
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

	resp, err := h.purchaseSvc.CreateIntent(r.Context(), account.ID, dto.AmountLamports)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create purchase intent")
		return
	}

	SendData(w, CreateIntentResponseDTO{
		IntentID:        resp.IntentID,
		TreasuryAddress: resp.TreasuryAddress,
		AmountLamports:  resp.AmountLamports,
		CreditAmount:    resp.CreditAmount,
		Memo:            resp.Memo,
		ExpiresAt:       resp.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// HandleVerifyPurchase handles POST /api/v1/purchase/verify (API key auth required).
func (h *PurchaseHandler) HandleVerifyPurchase(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto VerifyPurchaseDTO
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

	resp, err := h.purchaseSvc.VerifyPurchase(r.Context(), account.ID, dto.IntentID, dto.TxSignature)
	if err != nil {
		SendError(w, http.StatusBadRequest, "VERIFICATION_FAILED", err.Error())
		return
	}

	SendData(w, VerifyPurchaseResponseDTO{
		CreditsGranted: resp.CreditsGranted,
		NewBalance: CreditSummaryDTO{
			Free: resp.NewBalance.Free,
			Paid: resp.NewBalance.Paid,
		},
	})
}
