// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-03 (Credit Lifecycle)
// Purpose: Tests for credit HTTP handlers (balance, spend token)

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

const testJWTSecret = "test-jwt-secret-32-bytes-long!!!"

func newCreditTestRouter(t *testing.T) (*mux.Router, creditHandlerTestDeps) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	accounts := repository.NewMemoryAccountRepository()
	credits := repository.NewMemoryCreditRepositoryWithClock(clk)
	apiKeys := repository.NewMemoryPeerAPIKeyRepository()

	creditSvc := services.NewCreditService(services.CreditServiceDeps{
		Credits:   credits,
		Accounts:  accounts,
		Clock:     clk,
		JWTSecret: testJWTSecret,
	})

	handler := NewCreditHandler(creditSvc, credits, accounts)
	r := mux.NewRouter()
	// Wrap credit routes with API key middleware
	creditRouter := r.PathPrefix("/api/v1/credits").Subrouter()
	creditRouter.Use(RequireAPIKey(apiKeys))
	creditRouter.HandleFunc("/balance", handler.HandleGetBalance).Methods(http.MethodGet)
	creditRouter.HandleFunc("/spend-token", handler.HandleIssueSpendToken).Methods(http.MethodPost)
	creditRouter.HandleFunc("/transactions", handler.HandleListTransactions).Methods(http.MethodGet)

	return r, creditHandlerTestDeps{
		accounts: accounts,
		credits:  credits,
		apiKeys:  apiKeys,
		clock:    clk,
	}
}

type creditHandlerTestDeps struct {
	accounts *repository.MemoryAccountRepository
	credits  *repository.MemoryCreditRepository
	apiKeys  repository.PeerAPIKeyRepository
	clock    *clock.MockClock
}

// seedCreditTestAccount creates an account, API key, and credits for handler testing.
func seedCreditTestAccount(t *testing.T, deps creditHandlerTestDeps, peerID, accountID string, free, paid int) string {
	t.Helper()
	ctx := context.Background()
	now := deps.clock.Now()

	_ = deps.accounts.Create(ctx, &models.Account{
		ID:        accountID,
		PeerID:    peerID,
		Status:    models.AccountStatusActive,
		CreatedAt: now,
	})

	expiresAt := now.Add(30 * 24 * time.Hour)
	_ = deps.credits.CreateBalance(ctx, &models.CreditBalance{
		AccountID:            accountID,
		FreeBalance:          0,
		PaidBalance:          0,
		FreeCreditsExpiresAt: &expiresAt,
		UpdatedAt:            now,
	})

	if free > 0 {
		_ = deps.credits.CreditFree(ctx, accountID, free, "seed_free", "seed-free:"+accountID, expiresAt)
	}
	if paid > 0 {
		_ = deps.credits.CreditPaid(ctx, accountID, paid, "seed_paid", "seed-paid:"+accountID)
	}

	// Create API key
	apiKey, _ := deps.apiKeys.Create(ctx, peerID)
	return apiKey
}

// --- GetBalance tests ---

func TestHandleGetBalance_Success(t *testing.T) {
	router, deps := newCreditTestRouter(t)
	apiKey := seedCreditTestAccount(t, deps, "peer-1", "acct-1", 50, 100)

	req := httptest.NewRequest("GET", "/api/v1/credits/balance", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleGetBalance() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data BalanceDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.FreeBalance != 50 {
		t.Errorf("HandleGetBalance() free_balance = %d, want 50", envelope.Data.FreeBalance)
	}
	if envelope.Data.PaidBalance != 100 {
		t.Errorf("HandleGetBalance() paid_balance = %d, want 100", envelope.Data.PaidBalance)
	}
	if envelope.Data.Total != 150 {
		t.Errorf("HandleGetBalance() total = %d, want 150", envelope.Data.Total)
	}
}

func TestHandleGetBalance_NoAPIKey(t *testing.T) {
	router, _ := newCreditTestRouter(t)

	req := httptest.NewRequest("GET", "/api/v1/credits/balance", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleGetBalance() no key status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleGetBalance_NoAccount(t *testing.T) {
	router, deps := newCreditTestRouter(t)
	ctx := context.Background()
	// Create API key for a peer that has no account
	apiKey, _ := deps.apiKeys.Create(ctx, "peer-orphan")

	req := httptest.NewRequest("GET", "/api/v1/credits/balance", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("HandleGetBalance() no account status = %d, want %d, body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

// --- IssueSpendToken tests ---

func TestHandleIssueSpendToken_Success(t *testing.T) {
	router, deps := newCreditTestRouter(t)
	apiKey := seedCreditTestAccount(t, deps, "peer-1", "acct-1", 50, 0)

	body, _ := json.Marshal(SpendTokenRequestDTO{Amount: 5, Purpose: "ai_chat"})
	req := httptest.NewRequest("POST", "/api/v1/credits/spend-token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleIssueSpendToken() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data SpendTokenResponseDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.Token == "" {
		t.Error("HandleIssueSpendToken() token is empty")
	}
	if envelope.Data.ExpiresAt == "" {
		t.Error("HandleIssueSpendToken() expires_at is empty")
	}
}

func TestHandleIssueSpendToken_InsufficientCredits(t *testing.T) {
	router, deps := newCreditTestRouter(t)
	apiKey := seedCreditTestAccount(t, deps, "peer-1", "acct-1", 5, 0)

	body, _ := json.Marshal(SpendTokenRequestDTO{Amount: 50, Purpose: "ai_chat"})
	req := httptest.NewRequest("POST", "/api/v1/credits/spend-token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("HandleIssueSpendToken() status = %d, want %d", w.Code, http.StatusPaymentRequired)
	}
}

func TestHandleIssueSpendToken_InvalidAmount(t *testing.T) {
	router, deps := newCreditTestRouter(t)
	apiKey := seedCreditTestAccount(t, deps, "peer-1", "acct-1", 50, 0)

	body, _ := json.Marshal(SpendTokenRequestDTO{Amount: 0, Purpose: "ai_chat"})
	req := httptest.NewRequest("POST", "/api/v1/credits/spend-token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleIssueSpendToken() zero amount status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
