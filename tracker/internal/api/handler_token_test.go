// Package: tracker/internal/api
// Feature: F-031 (Token Data Persistence)
// Story: US-031-01 (Backend Token Persistence)
// Purpose: Tests for token identity HTTP handlers (POST + GET)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// newTestServerWithToken builds a Server with TokenHandler for testing token routes.
func newTestServerWithToken(t *testing.T) (*Server, *repository.MemoryTokenRepository, *repository.MemoryPeerAPIKeyRepository) {
	t.Helper()
	tokenRepo := repository.NewMemoryTokenRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	tokenHandler := NewTokenHandler(tokenRepo)

	srv := NewServer(ServerDeps{
		TokenHandler: tokenHandler,
		APIKeyRepo:   apiKeyRepo,
		Address:      ":7842",
	})
	return srv, tokenRepo, apiKeyRepo
}

// --- POST /api/token (authenticated via RequireAPIKey) ---

func TestHandlePostToken_201_Created(t *testing.T) {
	srv, _, apiKeyRepo := newTestServerWithToken(t)
	apiKeyRepo.Store("peer-1", "test-api-key-1")

	body, _ := json.Marshal(map[string]string{
		"token_contract_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		"token_ticker":           "STNK",
		"token_name":             "StonkCoin",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key-1")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /api/token status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response missing data envelope, got: %v", resp)
	}
	if data["peer_id"] != "peer-1" {
		t.Errorf("peer_id = %v, want peer-1", data["peer_id"])
	}
	if data["token_contract_address"] != "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU" {
		t.Errorf("token_contract_address = %v, want 7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU", data["token_contract_address"])
	}
	if data["token_ticker"] != "STNK" {
		t.Errorf("token_ticker = %v, want STNK", data["token_ticker"])
	}
	if data["token_name"] != "StonkCoin" {
		t.Errorf("token_name = %v, want StonkCoin", data["token_name"])
	}
	if data["launched_at"] == nil || data["launched_at"] == "" {
		t.Error("launched_at should be set")
	}
}

func TestHandlePostToken_400_MissingFields(t *testing.T) {
	srv, _, apiKeyRepo := newTestServerWithToken(t)
	apiKeyRepo.Store("peer-1", "test-api-key-1")

	// Missing token_ticker and token_name
	body, _ := json.Marshal(map[string]string{
		"token_contract_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key-1")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/token status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}

	var resp ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("error code = %q, want VALIDATION_ERROR", resp.Error.Code)
	}
}

func TestHandlePostToken_400_InvalidBase58(t *testing.T) {
	srv, _, apiKeyRepo := newTestServerWithToken(t)
	apiKeyRepo.Store("peer-1", "test-api-key-1")

	// "INVALID0ADDRESS" contains 0 which is not valid base58
	body, _ := json.Marshal(map[string]string{
		"token_contract_address": "INVALID0ADDRESS",
		"token_ticker":           "BAD",
		"token_name":             "BadToken",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key-1")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/token status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}

	var resp ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("error code = %q, want VALIDATION_ERROR", resp.Error.Code)
	}
}

func TestHandlePostToken_401_NoAPIKey(t *testing.T) {
	srv, _, _ := newTestServerWithToken(t)

	body, _ := json.Marshal(map[string]string{
		"token_contract_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		"token_ticker":           "STNK",
		"token_name":             "StonkCoin",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-API-Key header
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/token (no key) status = %d, want %d; body: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}

	var resp ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != "UNAUTHORIZED" {
		t.Errorf("error code = %q, want UNAUTHORIZED", resp.Error.Code)
	}
}

func TestHandlePostToken_409_AlreadyExists(t *testing.T) {
	srv, _, apiKeyRepo := newTestServerWithToken(t)
	apiKeyRepo.Store("peer-1", "test-api-key-1")

	body, _ := json.Marshal(map[string]string{
		"token_contract_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		"token_ticker":           "STNK",
		"token_name":             "StonkCoin",
	})

	// First POST — should succeed
	req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key-1")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	// Second POST — same peer → 409
	body2, _ := json.Marshal(map[string]string{
		"token_contract_address": "DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263",
		"token_ticker":           "STNK2",
		"token_name":             "StonkCoin2",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-API-Key", "test-api-key-1")
	w2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("second POST status = %d, want %d; body: %s", w2.Code, http.StatusConflict, w2.Body.String())
	}

	var resp ErrorEnvelope
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != "ALREADY_EXISTS" {
		t.Errorf("error code = %q, want ALREADY_EXISTS", resp.Error.Code)
	}
}

// Gate 0 Finding #3: Length validation should return 400 VALIDATION_ERROR, not 500 from DB
func TestHandlePostToken_400_OversizedFields(t *testing.T) {
	testCases := []struct {
		name       string
		ticker     string
		tokenName  string
		wantErrMsg string
	}{
		{
			name:       "ticker too long",
			ticker:     "THISTICKERISWAYTOOLONG", // 23 chars > 20
			tokenName:  "Valid Name",
			wantErrMsg: "token_ticker must be 20 characters or less",
		},
		{
			name:       "name too long",
			ticker:     "STNK",
			tokenName:  string(make([]byte, 256)), // 256 > 255
			wantErrMsg: "token_name must be 255 characters or less",
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Each sub-test needs its own server + peer to avoid ALREADY_EXISTS
			srv, _, apiKeyRepo := newTestServerWithToken(t)
			peerID := fmt.Sprintf("peer-%d", i+100)
			apiKey := fmt.Sprintf("test-api-key-%d", i+100)
			apiKeyRepo.Store(peerID, apiKey)

			body, _ := json.Marshal(map[string]string{
				"token_contract_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
				"token_ticker":           tc.ticker,
				"token_name":             tc.tokenName,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-API-Key", apiKey)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (VALIDATION_ERROR, not DB 500); body: %s", w.Code, http.StatusBadRequest, w.Body.String())
			}

			var resp ErrorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if resp.Error.Code != "VALIDATION_ERROR" {
				t.Errorf("error code = %q, want VALIDATION_ERROR", resp.Error.Code)
			}
			if resp.Error.Message != tc.wantErrMsg {
				t.Errorf("error message = %q, want %q", resp.Error.Message, tc.wantErrMsg)
			}
		})
	}
}

// --- GET /api/peers/{id}/token (public read, no auth) ---

func TestHandleGetPeerToken_200(t *testing.T) {
	srv, _, apiKeyRepo := newTestServerWithToken(t)
	apiKeyRepo.Store("peer-1", "test-api-key-1")

	// First persist a token
	body, _ := json.Marshal(map[string]string{
		"token_contract_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		"token_ticker":           "STNK",
		"token_name":             "StonkCoin",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key-1")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("setup POST status = %d, want %d", w.Code, http.StatusCreated)
	}

	// Now GET the token
	req2 := httptest.NewRequest(http.MethodGet, "/api/peers/peer-1/token", nil)
	w2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("GET /api/peers/peer-1/token status = %d, want %d; body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response missing data envelope, got: %v", resp)
	}
	if data["peer_id"] != "peer-1" {
		t.Errorf("peer_id = %v, want peer-1", data["peer_id"])
	}
	if data["token_ticker"] != "STNK" {
		t.Errorf("token_ticker = %v, want STNK", data["token_ticker"])
	}
}

func TestHandleGetPeerToken_404(t *testing.T) {
	srv, _, _ := newTestServerWithToken(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers/nonexistent/token", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /api/peers/nonexistent/token status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}

	var resp ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", resp.Error.Code)
	}
}

// --- Stub PumpFunClient for handler tests ---

type handlerStubPumpFun struct {
	metrics *models.TokenMetrics
}

func (s *handlerStubPumpFun) GetCoinData(_ context.Context, _ string) (*models.TokenMetrics, error) {
	return s.metrics, nil
}

// newTestServerWithMetrics builds a Server with TokenHandler + MetricsService for testing metrics routes.
func newTestServerWithMetrics(t *testing.T) (*Server, *repository.MemoryTokenRepository) {
	t.Helper()
	tokenRepo := repository.NewMemoryTokenRepository()

	pump := &handlerStubPumpFun{metrics: &models.TokenMetrics{
		MarketCapUsd:        ptrF64(12400),
		SolRaised:           ptrF64(3.2),
		BondingCurvePercent: ptrI(4),
		Complete:            ptrB(false),
	}}

	metricsSvc := services.NewMetricsService(services.MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	tokenHandler := NewTokenHandler(tokenRepo)
	tokenHandler.SetMetricsService(metricsSvc)

	srv := NewServer(ServerDeps{
		TokenHandler: tokenHandler,
		Address:      ":7842",
	})
	return srv, tokenRepo
}

func ptrF64(v float64) *float64 { return &v }
func ptrI(v int) *int           { return &v }
func ptrB(v bool) *bool         { return &v }

// --- GET /api/peers/{id}/token/metrics (public read, rate limited) ---

func TestHandleGetTokenMetrics_200(t *testing.T) {
	srv, tokenRepo := newTestServerWithMetrics(t)

	// Seed a token
	_ = tokenRepo.Create(t.Context(), &models.PeerToken{
		PeerID:               "peer-1",
		TokenContractAddress: "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		TokenTicker:          "STNK",
		TokenName:            "StonkCoin",
		LaunchedAt:           time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-1/token/metrics", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/peers/peer-1/token/metrics status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response missing data envelope, got: %v", resp)
	}
	if data["marketCapUsd"] == nil {
		t.Error("marketCapUsd should be present")
	}
}

func TestHandleGetTokenMetrics_404(t *testing.T) {
	srv, _ := newTestServerWithMetrics(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers/nonexistent/token/metrics", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET metrics for nonexistent status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestHandleGetTokenMetrics_503_NoMetricsService(t *testing.T) {
	// Server with token handler but NO metrics service
	srv, _, _ := newTestServerWithToken(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-1/token/metrics", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET metrics without service status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

// TestRateLimit_TokenMetricsEndpoint verifies GET /api/peers/{id}/token/metrics is limited to 30 per min per IP.
func TestRateLimit_TokenMetricsEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, TokenMetricsRateLimitConfig())
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/peers/p1/token/metrics", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 31st should be 429
	req := httptest.NewRequest(http.MethodGet, "/api/peers/p1/token/metrics", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("31st request: status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429")
	}
	if w.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("X-RateLimit-Reset header missing on 429")
	}
}

// --- US-031-05: GET /api/tokens (paginated token listing) ---

func TestHandleListTokens_200_Empty(t *testing.T) {
	srv, _ := newTestServerWithMetrics(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tokens", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/tokens status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Total  int `json:"total"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("data len = %d, want 0", len(resp.Data))
	}
	if resp.Meta.Total != 0 {
		t.Errorf("meta.total = %d, want 0", resp.Meta.Total)
	}
	if resp.Meta.Limit != 20 {
		t.Errorf("meta.limit = %d, want 20 (default)", resp.Meta.Limit)
	}
	if resp.Meta.Offset != 0 {
		t.Errorf("meta.offset = %d, want 0", resp.Meta.Offset)
	}
}

func TestHandleListTokens_200_WithMetrics(t *testing.T) {
	srv, tokenRepo := newTestServerWithMetrics(t)

	_ = tokenRepo.Create(context.Background(), &models.PeerToken{
		PeerID:               "peer-1",
		TokenContractAddress: "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		TokenTicker:          "STNK",
		TokenName:            "StonkCoin",
		LaunchedAt:           time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/tokens", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/tokens status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			PeerID               string      `json:"peer_id"`
			TokenContractAddress string      `json:"token_contract_address"`
			TokenTicker          string      `json:"token_ticker"`
			Metrics              interface{} `json:"metrics"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].PeerID != "peer-1" {
		t.Errorf("data[0].peer_id = %q, want peer-1", resp.Data[0].PeerID)
	}
	if resp.Data[0].TokenTicker != "STNK" {
		t.Errorf("data[0].token_ticker = %q, want STNK", resp.Data[0].TokenTicker)
	}
	if resp.Meta.Total != 1 {
		t.Errorf("meta.total = %d, want 1", resp.Meta.Total)
	}
	// metrics field should be present (no Redis = nil, but struct present via stub pump.fun is not called here)
	// Since BatchGetCachedMetrics with nil Redis returns empty map → metrics = null in JSON
}

func TestHandleListTokens_Pagination(t *testing.T) {
	srv, tokenRepo := newTestServerWithMetrics(t)

	// Seed 3 tokens with different launch times
	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		_ = tokenRepo.Create(context.Background(), &models.PeerToken{
			PeerID:               "peer-" + name,
			TokenContractAddress: "contract-" + name,
			TokenTicker:          name[:1],
			TokenName:            name,
			LaunchedAt:           time.Date(2026, 2, 1, 10+i, 0, 0, 0, time.UTC),
		})
	}

	// Request limit=2, offset=0
	req := httptest.NewRequest(http.MethodGet, "/api/tokens?limit=2&offset=0", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			PeerID string `json:"peer_id"`
		} `json:"data"`
		Meta struct {
			Total  int `json:"total"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(resp.Data))
	}
	if resp.Meta.Total != 3 {
		t.Errorf("meta.total = %d, want 3", resp.Meta.Total)
	}
	if resp.Meta.Limit != 2 {
		t.Errorf("meta.limit = %d, want 2", resp.Meta.Limit)
	}
	if resp.Meta.Offset != 0 {
		t.Errorf("meta.offset = %d, want 0", resp.Meta.Offset)
	}
	// First item should be newest (Gamma, launched at 12:00)
	if resp.Data[0].PeerID != "peer-Gamma" {
		t.Errorf("data[0].peer_id = %q, want peer-Gamma (newest first)", resp.Data[0].PeerID)
	}
}

func TestHandleListTokens_NoRedis_MetricsNull(t *testing.T) {
	// Build server without Redis — metrics service exists but cache returns empty
	tokenRepo := repository.NewMemoryTokenRepository()
	pump := &handlerStubPumpFun{metrics: &models.TokenMetrics{
		MarketCapUsd: ptrF64(1000),
	}}
	metricsSvc := services.NewMetricsService(services.MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
		Redis:     nil, // no Redis
	})
	tokenHandler := NewTokenHandler(tokenRepo)
	tokenHandler.SetMetricsService(metricsSvc)
	srv := NewServer(ServerDeps{
		TokenHandler: tokenHandler,
		Address:      ":7842",
	})

	_ = tokenRepo.Create(context.Background(), &models.PeerToken{
		PeerID:               "peer-1",
		TokenContractAddress: "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		TokenTicker:          "STNK",
		TokenName:            "StonkCoin",
		LaunchedAt:           time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/tokens", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			PeerID  string      `json:"peer_id"`
			Metrics interface{} `json:"metrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(resp.Data))
	}
	// With no Redis cache, metrics should be null (no cached data)
	if resp.Data[0].Metrics != nil {
		t.Errorf("metrics = %v, want null (no Redis cache)", resp.Data[0].Metrics)
	}
}
