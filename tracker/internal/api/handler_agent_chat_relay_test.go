package api

import (
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

func TestHandleResult_DeductsCreditsIdempotently(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewMockClock(time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC))

	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	accounts := repository.NewMemoryAccountRepository()
	credits := repository.NewMemoryCreditRepositoryWithClock(clk)
	creditSvc := services.NewCreditService(services.CreditServiceDeps{
		Credits:   credits,
		Accounts:  accounts,
		Clock:     clk,
		JWTSecret: "test-jwt-secret-32-bytes-long!!!",
	})

	peerID := "holder-peer-1"
	apiKey, _ := apiKeys.Create(ctx, peerID)
	_ = accounts.Create(ctx, &models.Account{
		ID:        "acct-1",
		PeerID:    peerID,
		Status:    models.AccountStatusActive,
		CreatedAt: clk.Now(),
	})
	exp := clk.Now().Add(30 * 24 * time.Hour)
	_ = credits.CreateBalance(ctx, &models.CreditBalance{
		AccountID:            "acct-1",
		FreeBalance:          0,
		PaidBalance:          0,
		FreeCreditsExpiresAt: &exp,
		UpdatedAt:            clk.Now(),
	})
	_ = credits.CreditPaid(ctx, "acct-1", 50, "seed", "seed:paid")

	store := NewAgentChatRelayStore()
	store.Add(&agentChatRelayRequest{
		RequestID:            "req-1",
		OwnerPeerID:          "owner-peer-1",
		HolderPeerID:         peerID,
		TokenContractAddress: "token-addr-1",
		HolderUserID:         "holder-wallet-1",
		Status:               "completed",
		Response:             "hello from owner",
		CreatedAt:            clk.Now(),
		CompletedAt:          clk.Now(),
		ExpiresAt:            clk.Now().Add(1 * time.Minute),
	})

	h := NewAgentChatRelayHandler(store, nil, apiKeys, accounts, nil, nil, creditSvc, nil)
	r := mux.NewRouter()
	r.HandleFunc("/api/v1/agent/chat/result/{request_id}", h.HandleResult).Methods(http.MethodGet)

	firstReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/chat/result/req-1", nil)
	firstReq.Header.Set("X-API-Key", apiKey)
	firstW := httptest.NewRecorder()
	r.ServeHTTP(firstW, firstReq)
	if firstW.Code != http.StatusOK {
		t.Fatalf("first poll status=%d body=%s", firstW.Code, firstW.Body.String())
	}
	var firstResp ResultResponseDTO
	if err := json.Unmarshal(firstW.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	if firstResp.CreditsDeducted != 10 {
		t.Fatalf("expected credits_deducted=10, got %d", firstResp.CreditsDeducted)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/chat/result/req-1", nil)
	secondReq.Header.Set("X-API-Key", apiKey)
	secondW := httptest.NewRecorder()
	r.ServeHTTP(secondW, secondReq)
	if secondW.Code != http.StatusOK {
		t.Fatalf("second poll status=%d body=%s", secondW.Code, secondW.Body.String())
	}
	var secondResp ResultResponseDTO
	if err := json.Unmarshal(secondW.Body.Bytes(), &secondResp); err != nil {
		t.Fatalf("unmarshal second response: %v", err)
	}
	if secondResp.CreditsDeducted != 10 {
		t.Fatalf("expected credits_deducted=10 on idempotent retry, got %d", secondResp.CreditsDeducted)
	}

	bal, err := credits.GetBalance(ctx, "acct-1")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if bal.PaidBalance != 40 {
		t.Fatalf("expected paid balance 40 after single deduction, got %d", bal.PaidBalance)
	}
}
