// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-03 (Credit Lifecycle)
// Purpose: HTTP handlers for credit balance and spend tokens

package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// BalanceDTO is the response for GET /api/v1/credits/balance.
type BalanceDTO struct {
	FreeBalance            int    `json:"free_balance"`
	PaidBalance            int    `json:"paid_balance"`
	Total                  int    `json:"total"`
	FreeExpiresAt          string `json:"free_expires_at,omitempty"`
	LifetimePurchased      int    `json:"lifetime_purchased"`
	LifetimeSocialGranted  int    `json:"lifetime_social_granted"`
	DetailedTrialRemaining int    `json:"detailed_trial_remaining"`
	DetailedTrialExpiresAt string `json:"detailed_trial_expires_at,omitempty"`
}

// SpendTokenRequestDTO is the request body for POST /api/v1/credits/spend-token.
type SpendTokenRequestDTO struct {
	Amount  int    `json:"amount"`
	Purpose string `json:"purpose"`
}

// SpendTokenResponseDTO is the response for POST /api/v1/credits/spend-token.
type SpendTokenResponseDTO struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// TransactionDTO is the response element for GET /api/v1/credits/transactions.
type TransactionDTO struct {
	ID          string `json:"id"`
	Amount      int    `json:"amount"`
	BalanceType string `json:"balance_type"`
	Reason      string `json:"reason"`
	CreatedAt   string `json:"created_at"`
}

// CreditHandler handles credit-related HTTP endpoints.
type CreditHandler struct {
	creditSvc *services.CreditService
	credits   repository.CreditRepository
	accounts  repository.AccountRepository
}

// NewCreditHandler creates a new CreditHandler.
func NewCreditHandler(creditSvc *services.CreditService, credits repository.CreditRepository, accounts repository.AccountRepository) *CreditHandler {
	return &CreditHandler{
		creditSvc: creditSvc,
		credits:   credits,
		accounts:  accounts,
	}
}

// HandleGetBalance handles GET /api/v1/credits/balance (API key auth required).
func (h *CreditHandler) HandleGetBalance(w http.ResponseWriter, r *http.Request) {
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

	bal, err := h.creditSvc.GetBalance(r.Context(), account.ID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "No credit balance found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get balance")
		return
	}

	resp := BalanceDTO{
		FreeBalance:            bal.FreeBalance,
		PaidBalance:            bal.PaidBalance,
		Total:                  bal.Total,
		LifetimePurchased:      bal.LifetimePurchased,
		LifetimeSocialGranted:  bal.LifetimeSocialGranted,
		DetailedTrialRemaining: bal.DetailedTrialRemaining,
	}
	if bal.FreeCreditsExpiresAt != nil {
		resp.FreeExpiresAt = bal.FreeCreditsExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if bal.DetailedTrialExpiresAt != nil {
		resp.DetailedTrialExpiresAt = bal.DetailedTrialExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}

	SendData(w, resp)
}

// HandleIssueSpendToken handles POST /api/v1/credits/spend-token (API key auth required).
func (h *CreditHandler) HandleIssueSpendToken(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto SpendTokenRequestDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	if dto.Amount <= 0 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Amount must be positive")
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

	tokenResp, err := h.creditSvc.IssueSpendToken(r.Context(), account.ID, dto.Amount, dto.Purpose)
	if err == models.ErrInsufficientCredits {
		SendError(w, http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", "Not enough credits")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to issue spend token")
		return
	}

	SendData(w, SpendTokenResponseDTO{
		Token:     tokenResp.Token,
		ExpiresAt: tokenResp.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// HandleListTransactions handles GET /api/v1/credits/transactions (API key auth required).
func (h *CreditHandler) HandleListTransactions(w http.ResponseWriter, r *http.Request) {
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

	limit, offset := parsePagination(r, 20, 100)

	txs, err := h.credits.ListTransactions(r.Context(), account.ID, limit, offset)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list transactions")
		return
	}

	dtos := make([]TransactionDTO, 0, len(txs))
	for _, tx := range txs {
		dtos = append(dtos, TransactionDTO{
			ID:          tx.ID,
			Amount:      tx.Amount,
			BalanceType: tx.BalanceType,
			Reason:      tx.Reason,
			CreatedAt:   tx.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	SendData(w, dtos)
}
