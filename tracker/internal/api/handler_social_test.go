// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: Tests for social connection HTTP handlers

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

const testCallbackSecret = "test-openclaw-secret"

type socialHandlerTestDeps struct {
	accounts *repository.MemoryAccountRepository
	credits  *repository.MemoryCreditRepository
	social   *repository.MemorySocialRepository
	apiKeys  repository.PeerAPIKeyRepository
	clock    *clock.MockClock
}

func newSocialTestRouter(t *testing.T) (*mux.Router, socialHandlerTestDeps) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	accounts := repository.NewMemoryAccountRepository()
	credits := repository.NewMemoryCreditRepositoryWithClock(clk)
	social := repository.NewMemorySocialRepository()
	apiKeys := repository.NewMemoryPeerAPIKeyRepository()

	socialSvc := services.NewSocialService(services.SocialServiceDeps{
		Social:   social,
		Credits:  credits,
		Accounts: accounts,
		Clock:    clk,
	})

	handler := NewSocialHandler(socialSvc, accounts, testCallbackSecret)
	r := mux.NewRouter()

	// Callback endpoint (authenticated via shared secret)
	r.HandleFunc("/api/v1/social/confirm", handler.HandleConfirm).Methods(http.MethodPost)

	// User endpoints (authenticated via API key)
	userRouter := r.PathPrefix("/api/v1/social").Subrouter()
	userRouter.Use(RequireAPIKey(apiKeys))
	userRouter.HandleFunc("/connections", handler.HandleListConnections).Methods(http.MethodGet)
	userRouter.HandleFunc("/{platform}", handler.HandleDisconnect).Methods(http.MethodDelete)

	return r, socialHandlerTestDeps{
		accounts: accounts,
		credits:  credits,
		social:   social,
		apiKeys:  apiKeys,
		clock:    clk,
	}
}

func seedSocialHandlerAccount(t *testing.T, deps socialHandlerTestDeps, peerID, accountID string, freeCredits int) string {
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
	if freeCredits > 0 {
		_ = deps.credits.CreditFree(ctx, accountID, freeCredits, "seed", "seed:"+accountID, expiresAt)
	}
	apiKey, _ := deps.apiKeys.Create(ctx, peerID)
	return apiKey
}

func TestHandleSocialConfirm_Success(t *testing.T) {
	router, deps := newSocialTestRouter(t)
	seedSocialHandlerAccount(t, deps, "peer-1", "acct-1", 50)

	body, _ := json.Marshal(SocialConfirmDTO{
		AccountID:      "acct-1",
		Platform:       "github",
		PlatformUserID: "octocat-123",
		Verified:       true,
	})
	req := httptest.NewRequest("POST", "/api/v1/social/confirm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testCallbackSecret)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleConfirm() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data SocialConfirmResponseDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Data.BonusGranted != 50 {
		t.Errorf("HandleConfirm() bonus = %d, want 50", envelope.Data.BonusGranted)
	}
}

func TestHandleSocialConfirm_BadSecret(t *testing.T) {
	router, _ := newSocialTestRouter(t)

	body, _ := json.Marshal(SocialConfirmDTO{
		AccountID:      "acct-1",
		Platform:       "github",
		PlatformUserID: "octocat-123",
		Verified:       true,
	})
	req := httptest.NewRequest("POST", "/api/v1/social/confirm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-secret")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("HandleConfirm() bad secret status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleSocialConfirm_DuplicatePlatform(t *testing.T) {
	router, deps := newSocialTestRouter(t)
	seedSocialHandlerAccount(t, deps, "peer-1", "acct-1", 50)

	body, _ := json.Marshal(SocialConfirmDTO{
		AccountID:      "acct-1",
		Platform:       "github",
		PlatformUserID: "octocat-123",
		Verified:       true,
	})

	// First call
	req := httptest.NewRequest("POST", "/api/v1/social/confirm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testCallbackSecret)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Duplicate call
	req2 := httptest.NewRequest("POST", "/api/v1/social/confirm", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+testCallbackSecret)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("HandleConfirm() duplicate status = %d, want %d, body: %s", w2.Code, http.StatusConflict, w2.Body.String())
	}
}

func TestHandleListConnections_Success(t *testing.T) {
	router, deps := newSocialTestRouter(t)
	apiKey := seedSocialHandlerAccount(t, deps, "peer-1", "acct-1", 50)

	// Add a connection via confirm endpoint
	body, _ := json.Marshal(SocialConfirmDTO{
		AccountID:      "acct-1",
		Platform:       "github",
		PlatformUserID: "octocat",
		Verified:       true,
	})
	confirmReq := httptest.NewRequest("POST", "/api/v1/social/confirm", bytes.NewReader(body))
	confirmReq.Header.Set("Content-Type", "application/json")
	confirmReq.Header.Set("Authorization", "Bearer "+testCallbackSecret)
	confirmW := httptest.NewRecorder()
	router.ServeHTTP(confirmW, confirmReq)

	// List connections
	req := httptest.NewRequest("GET", "/api/v1/social/connections", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleListConnections() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope struct {
		Data SocialConnectionListDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if len(envelope.Data.Connections) != 1 {
		t.Errorf("HandleListConnections() count = %d, want 1", len(envelope.Data.Connections))
	}
}

func TestHandleDisconnect_Success(t *testing.T) {
	router, deps := newSocialTestRouter(t)
	apiKey := seedSocialHandlerAccount(t, deps, "peer-1", "acct-1", 50)

	// Add a connection first
	body, _ := json.Marshal(SocialConfirmDTO{
		AccountID:      "acct-1",
		Platform:       "github",
		PlatformUserID: "octocat",
		Verified:       true,
	})
	confirmReq := httptest.NewRequest("POST", "/api/v1/social/confirm", bytes.NewReader(body))
	confirmReq.Header.Set("Content-Type", "application/json")
	confirmReq.Header.Set("Authorization", "Bearer "+testCallbackSecret)
	confirmW := httptest.NewRecorder()
	router.ServeHTTP(confirmW, confirmReq)

	// Disconnect
	req := httptest.NewRequest("DELETE", "/api/v1/social/github", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDisconnect() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}
