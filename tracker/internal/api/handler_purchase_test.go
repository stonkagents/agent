// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: Tests for purchase intent/verify HTTP handlers

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// stubTxVerifier satisfies services.TransactionVerifier for handler tests.
type stubTxVerifier struct {
	result *services.TxVerifyResult
	err    error
}

func (s *stubTxVerifier) VerifyTransaction(_ context.Context, _ string, _ services.VerifyParams) (*services.TxVerifyResult, error) {
	return s.result, s.err
}

func newPurchaseTestRouter(t *testing.T) (*mux.Router, *clock.MockClock, string) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	accountRepo := repository.NewMemoryAccountRepository()
	creditRepo := repository.NewMemoryCreditRepository()
	purchaseRepo := repository.NewMemoryPurchaseRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()

	acc := &models.Account{
		ID:     "acc-purchase-test",
		PeerID: "peer-purchase-test",
		Status: models.AccountStatusActive,
	}
	_ = accountRepo.Create(context.Background(), acc)
	_ = creditRepo.CreateBalance(context.Background(), &models.CreditBalance{
		AccountID:   "acc-purchase-test",
		FreeBalance: 50,
	})
	apiKey, _ := apiKeyRepo.Create(context.Background(), "peer-purchase-test")

	verifier := &stubTxVerifier{
		result: &services.TxVerifyResult{
			Success:        true,
			ActualLamports: 100_000_000, // 100 credits (minimum)
			MemoMatch:      true,
		},
	}

	purchaseSvc := services.NewPurchaseService(services.PurchaseServiceDeps{
		Purchases:       purchaseRepo,
		Credits:         creditRepo,
		Accounts:        accountRepo,
		Clock:           clk,
		TxVerifier:      verifier,
		TreasuryAddress: "TrEaSuRyAdDrEsS123456789012345678901234",
	})

	handler := NewPurchaseHandler(purchaseSvc, accountRepo)
	r := mux.NewRouter()
	r.Handle("/api/v1/purchase/intent", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleCreateIntent))).Methods(http.MethodPost)
	r.Handle("/api/v1/purchase/verify", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleVerifyPurchase))).Methods(http.MethodPost)
	return r, clk, apiKey
}

func TestHandlePurchaseIntent_Success(t *testing.T) {
	router, _, apiKey := newPurchaseTestRouter(t)

	body, _ := json.Marshal(CreateIntentDTO{AmountLamports: 100_000_000}) // 100 credits
	req := httptest.NewRequest("POST", "/api/v1/purchase/intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleCreateIntent() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data CreateIntentResponseDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.IntentID == "" {
		t.Error("HandleCreateIntent() intent_id is empty")
	}
	// 100M lamports / 20K (new lamport-per-credit rate) = 5000 credits.
	if envelope.Data.CreditAmount != 5000 {
		t.Errorf("HandleCreateIntent() credit_amount = %d, want 5000", envelope.Data.CreditAmount)
	}
}

func TestHandlePurchaseIntent_BelowMinimum(t *testing.T) {
	router, _, apiKey := newPurchaseTestRouter(t)

	// 1,000,000 lamports / 20,000 lamports-per-credit = 50 credits, below the 100-credit minimum.
	body, _ := json.Marshal(CreateIntentDTO{AmountLamports: 1_000_000})
	req := httptest.NewRequest("POST", "/api/v1/purchase/intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleCreateIntent() below-min status = %d, want %d, body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandlePurchaseIntent_NoAPIKey(t *testing.T) {
	router, _, _ := newPurchaseTestRouter(t)

	body, _ := json.Marshal(CreateIntentDTO{AmountLamports: 10_000_000})
	req := httptest.NewRequest("POST", "/api/v1/purchase/intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleCreateIntent() status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
