// Package: tracker/internal/api
// Feature: F-026 (Profile Endpoint)
// Story: US-026-01 (Profile Aggregation)
// Purpose: HTTP handler tests for HandleGetMyProfile — auth, 404, 500, empty peer, success

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
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// ErrorEnvelope is already defined in response.go (same package) — no redefinition needed.
// Mock types (mockRepFinder, etc.) are defined in handler_profile_build_test.go (same package).

// newProfileTestRouter sets up a router with RequireAPIKey middleware and profile route.
func newProfileTestRouter(t *testing.T, h *ProfileHandler, apiKeys repository.PeerAPIKeyRepository) *mux.Router {
	t.Helper()
	r := mux.NewRouter()
	portal := r.PathPrefix("/api").Subrouter()
	portal.Handle("/profile/me",
		RequireAPIKey(apiKeys)(http.HandlerFunc(h.HandleGetMyProfile)),
	).Methods(http.MethodGet)
	return r
}

func TestHandleGetMyProfile_Unauthorized(t *testing.T) {
	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	h := NewProfileHandler(
		&mockRepFinder{}, &mockPeerFinder{}, &mockAssetSearcher{}, &mockPresenceChecker{}, &mockTrustCounter{},
	)
	r := newProfileTestRouter(t, h, apiKeys)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	// No X-API-Key header
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleGetMyProfile_PeerNotFound(t *testing.T) {
	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	key, _ := apiKeys.Create(context.Background(), "peer-unknown")

	h := NewProfileHandler(
		&mockRepFinder{},
		&mockPeerFinder{err: models.ErrNotFound},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	r := newProfileTestRouter(t, h, apiKeys)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	var env ErrorEnvelope
	_ = json.NewDecoder(w.Body).Decode(&env)
	if env.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", env.Error.Code)
	}
}

func TestHandleGetMyProfile_FindByID_InfraError(t *testing.T) {
	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	key, _ := apiKeys.Create(context.Background(), "peer-infra")

	h := NewProfileHandler(
		&mockRepFinder{},
		&mockPeerFinder{err: errInfra},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	r := newProfileTestRouter(t, h, apiKeys)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleGetMyProfile_EmptyPeerID(t *testing.T) {
	// Call handler directly without RequireAPIKey middleware (no context key set)
	h := NewProfileHandler(
		&mockRepFinder{}, &mockPeerFinder{}, &mockAssetSearcher{}, &mockPresenceChecker{}, &mockTrustCounter{},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	w := httptest.NewRecorder()
	h.HandleGetMyProfile(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleGetMyProfile_Success(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	key, _ := apiKeys.Create(context.Background(), "peer-success")

	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{
			PeerID: "peer-success", CompositeScore: 0.45,
			BandwidthScore: 0.40, QualityScore: 0.55,
			SecurityScore: 1.0, CitizenshipScore: 0.80,
		}, above: 2, total: 5},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer-success", MaskedPeerID: "claw-test-1a2b", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 3},
		&mockPresenceChecker{online: true}, &mockTrustCounter{},
	)
	r := newProfileTestRouter(t, h, apiKeys)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data ProfileResponseDTO `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	dto := envelope.Data

	// Verify key fields from the happy-path aggregation
	if dto.PeerID != "peer-success" {
		t.Errorf("PeerID = %q, want peer-success", dto.PeerID)
	}
	if dto.MaskedPeerID != "claw-test-1a2b" {
		t.Errorf("MaskedPeerID = %q, want claw-test-1a2b", dto.MaskedPeerID)
	}
	if dto.Rank != "silver" {
		t.Errorf("Rank = %q, want silver", dto.Rank)
	}
	if !dto.IsOnline {
		t.Error("IsOnline = false, want true")
	}
	if dto.Stats.Clout != 45 {
		t.Errorf("Clout = %d, want 45", dto.Stats.Clout)
	}
	if dto.Stats.TopPercent != 40 {
		t.Errorf("TopPercent = %d, want 40", dto.Stats.TopPercent)
	}
	if dto.Stats.Drops != 3 {
		t.Errorf("Drops = %d, want 3", dto.Stats.Drops)
	}
	if dto.EigenTrust.CompositeScore != 0.45 {
		t.Errorf("CompositeScore = %f, want 0.45", dto.EigenTrust.CompositeScore)
	}
	// Verify response Content-Type
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// TD-039: Verify Cache-Control header (browser caching for direct tracker access)
	if cc := w.Header().Get("Cache-Control"); cc != "private, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", cc, "private, max-age=60")
	}
}

// TD-027: Profile rate limiting — keyed by peer_id, 30 req/min
func TestHandleGetMyProfile_RateLimited(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	key, _ := apiKeys.Create(context.Background(), "peer-ratelimit")

	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{PeerID: "peer-ratelimit", CompositeScore: 0.45}},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer-ratelimit", MaskedPeerID: "claw-rl-1a2b", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 1},
		&mockPresenceChecker{online: true}, &mockTrustCounter{},
	)

	clk := clock.NewMockClock(time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	rlMw := ratelimit.NewMiddleware(limiter, ProfileRateLimitConfig())

	r := mux.NewRouter()
	portal := r.PathPrefix("/api").Subrouter()
	// Rate limiter wraps INSIDE RequireAPIKey (peer_id must be in context for PeerIDKey)
	portal.Handle("/profile/me",
		RequireAPIKey(apiKeys)(rlMw.Wrap(http.HandlerFunc(h.HandleGetMyProfile))),
	).Methods(http.MethodGet)

	// Send 30 requests — all should succeed
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// Request 31 should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("request 31: status = %d, want 429", w.Code)
	}

	var env ErrorEnvelope
	_ = json.NewDecoder(w.Body).Decode(&env)
	if env.Error.Code != "RATE_LIMITED" {
		t.Errorf("error code = %q, want RATE_LIMITED", env.Error.Code)
	}

	// Verify X-RateLimit headers are present
	if w.Header().Get("X-RateLimit-Limit") != "30" {
		t.Errorf("X-RateLimit-Limit = %q, want 30", w.Header().Get("X-RateLimit-Limit"))
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429 response")
	}
}

// TD-027: Verify rate limiting is per-peer (different peer_ids get independent limits)
func TestHandleGetMyProfile_RateLimitPerPeer(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	key1, _ := apiKeys.Create(context.Background(), "peer-rl-1")
	key2, _ := apiKeys.Create(context.Background(), "peer-rl-2")

	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{CompositeScore: 0.5}},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer-rl-1", MaskedPeerID: "claw-rl1", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 1},
		&mockPresenceChecker{online: true}, &mockTrustCounter{},
	)

	clk := clock.NewMockClock(time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	rlMw := ratelimit.NewMiddleware(limiter, ProfileRateLimitConfig())

	r := mux.NewRouter()
	portal := r.PathPrefix("/api").Subrouter()
	portal.Handle("/profile/me",
		RequireAPIKey(apiKeys)(rlMw.Wrap(http.HandlerFunc(h.HandleGetMyProfile))),
	).Methods(http.MethodGet)

	// Exhaust peer-rl-1's limit (30 requests)
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
		req.Header.Set("X-API-Key", key1)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("peer-1 request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// peer-rl-2 should still be allowed (independent limit)
	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", key2)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("peer-2 request: status = %d, want 200 (independent limit)", w.Code)
	}
}
