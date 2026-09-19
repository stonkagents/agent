// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-08 (Account Recovery)
// Purpose: Tests for account recovery HTTP handler

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

func newRecoveryTestRouter(t *testing.T) (*mux.Router, string) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	accountRepo := repository.NewMemoryAccountRepository()
	creditRepo := repository.NewMemoryCreditRepository()
	socialRepo := repository.NewMemorySocialRepository()
	walletRepo := repository.NewMemoryWalletRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()

	// Old account (to recover from)
	oldAcc := &models.Account{
		ID:     "old-acc",
		PeerID: "old-peer",
		Status: models.AccountStatusActive,
	}
	_ = accountRepo.Create(context.Background(), oldAcc)
	_ = creditRepo.CreateBalance(context.Background(), &models.CreditBalance{
		AccountID:   "old-acc",
		FreeBalance: 50,
		PaidBalance: 200,
	})
	// Social connections on old account
	_ = socialRepo.Insert(context.Background(), &models.SocialConnection{
		AccountID:      "old-acc",
		Platform:       "github",
		PlatformUserID: "user123",
		VerifiedAt:     clk.Now(),
	})
	_ = socialRepo.Insert(context.Background(), &models.SocialConnection{
		AccountID:      "old-acc",
		Platform:       "twitter",
		PlatformUserID: "user456",
		VerifiedAt:     clk.Now(),
	})

	// New account (recovery target, authenticated via API key)
	newAcc := &models.Account{
		ID:     "new-acc",
		PeerID: "new-peer",
		Status: models.AccountStatusActive,
	}
	_ = accountRepo.Create(context.Background(), newAcc)
	_ = creditRepo.CreateBalance(context.Background(), &models.CreditBalance{
		AccountID: "new-acc",
	})
	apiKey, _ := apiKeyRepo.Create(context.Background(), "new-peer")

	recoverySvc := services.NewRecoveryService(services.RecoveryServiceDeps{
		Accounts: accountRepo,
		Credits:  creditRepo,
		Social:   socialRepo,
		Wallets:  walletRepo,
		APIKeys:  apiKeyRepo,
		Clock:    clk,
	})

	handler := NewRecoveryHandler(recoverySvc, accountRepo)
	r := mux.NewRouter()
	r.Handle("/api/v1/account/recover", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleRecover))).Methods(http.MethodPost)
	return r, apiKey
}

func TestHandleRecover_Success(t *testing.T) {
	router, apiKey := newRecoveryTestRouter(t)

	body, _ := json.Marshal(RecoverDTO{
		OldPeerID:           "old-peer",
		SocialVerifications: []string{"github", "twitter"},
	})
	req := httptest.NewRequest("POST", "/api/v1/account/recover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleRecover() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data RecoverResponseDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if !envelope.Data.Recovered {
		t.Error("HandleRecover() recovered = false, want true")
	}
	if envelope.Data.PaidCreditsTransferred != 200 {
		t.Errorf("HandleRecover() paid_credits_transferred = %d, want 200", envelope.Data.PaidCreditsTransferred)
	}
}

func TestHandleRecover_InsufficientProof(t *testing.T) {
	router, apiKey := newRecoveryTestRouter(t)

	body, _ := json.Marshal(RecoverDTO{
		OldPeerID:           "old-peer",
		SocialVerifications: []string{"github"}, // only 1, need 2
	})
	req := httptest.NewRequest("POST", "/api/v1/account/recover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleRecover() status = %d, want %d, body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
