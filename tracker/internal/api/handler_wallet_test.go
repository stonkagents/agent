// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: Tests for wallet linking HTTP handler

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

// stubSolanaClient satisfies services.SolanaClient for handler tests.
type stubSolanaClient struct {
	balance   int64
	firstTx   *time.Time
	verifySig bool
}

func (s *stubSolanaClient) GetBalance(_ context.Context, _ string) (int64, error) {
	return s.balance, nil
}
func (s *stubSolanaClient) GetFirstTransactionTime(_ context.Context, _ string) (*time.Time, error) {
	return s.firstTx, nil
}
func (s *stubSolanaClient) VerifyWalletSignature(_ string, _, _ []byte) bool {
	return s.verifySig
}

func newWalletTestRouter(t *testing.T) (*mux.Router, string) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	accountRepo := repository.NewMemoryAccountRepository()
	creditRepo := repository.NewMemoryCreditRepository()
	walletRepo := repository.NewMemoryWalletRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()

	// Create account + credit balance directly
	acc := &models.Account{
		ID:     "acc-wallet-test",
		PeerID: "peer-wallet-test",
		Status: models.AccountStatusActive,
	}
	_ = accountRepo.Create(context.Background(), acc)
	_ = creditRepo.CreateBalance(context.Background(), &models.CreditBalance{
		AccountID:   "acc-wallet-test",
		FreeBalance: 50,
	})
	apiKey, _ := apiKeyRepo.Create(context.Background(), "peer-wallet-test")

	oldTx := clk.Now().Add(-30 * 24 * time.Hour)
	solana := &stubSolanaClient{
		balance:   500_000_000,
		firstTx:   &oldTx,
		verifySig: true,
	}

	walletSvc := services.NewWalletService(services.WalletServiceDeps{
		Wallets:  walletRepo,
		Credits:  creditRepo,
		Accounts: accountRepo,
		Clock:    clk,
		Solana:   solana,
	})

	handler := NewWalletHandler(walletSvc, accountRepo)
	r := mux.NewRouter()
	r.Handle("/api/v1/wallet/link", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleLinkWallet))).Methods(http.MethodPost)
	return r, apiKey
}

func TestHandleWalletLink_Success(t *testing.T) {
	router, apiKey := newWalletTestRouter(t)

	body, _ := json.Marshal(LinkWalletDTO{
		WalletAddress: "SoLWaLLeTaDdReSs123456789012345678901234",
		Chain:         "solana",
		Signature:     "c2lnbmF0dXJl",
	})
	req := httptest.NewRequest("POST", "/api/v1/wallet/link", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleLinkWallet() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data LinkWalletResponseDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if !envelope.Data.Linked {
		t.Error("HandleLinkWallet() linked = false, want true")
	}
	if envelope.Data.BonusGranted != 75 {
		t.Errorf("HandleLinkWallet() bonus = %d, want 75", envelope.Data.BonusGranted)
	}
}

func TestHandleWalletLink_NoAPIKey(t *testing.T) {
	router, _ := newWalletTestRouter(t)

	body, _ := json.Marshal(LinkWalletDTO{
		WalletAddress: "SoLWaLLeTaDdReSs123456789012345678901234",
		Chain:         "solana",
		Signature:     "c2lnbmF0dXJl",
	})
	req := httptest.NewRequest("POST", "/api/v1/wallet/link", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleLinkWallet() status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
