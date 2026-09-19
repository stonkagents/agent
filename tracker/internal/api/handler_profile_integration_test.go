// Package: tracker/internal/api
// Feature: F-026 (Profile Endpoint)
// Story: US-026-01 (Profile Aggregation)
// Purpose: TD-031 Integration tests — verify profile route is registered in real server.go router
//          with RequireAPIKey middleware. Tests use NewServer() instead of custom test routers.

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// newIntegrationServer creates a real Server via NewServer() with minimal deps for profile testing.
// This exercises the actual route registration and middleware wiring in setupRoutes().
func newIntegrationServer(t *testing.T, apiKeys repository.PeerAPIKeyRepository, profile *ProfileHandler) *Server {
	t.Helper()

	clk := clock.NewMockClock(time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)

	// Minimal PeerHandler to satisfy NewServer (required for /register route).
	// We don't test peer routes here — only profile.
	peerHandler := &PeerHandler{}
	assetHandler := &AssetHandler{}

	// PortalHandler must be non-nil for the /api portal subrouter to be created
	// (profile route is registered inside the portal block in setupRoutes).
	portalHandler := &PortalHandler{}

	return NewServer(ServerDeps{
		PeerHandler:    peerHandler,
		AssetHandler:   assetHandler,
		PortalHandler:  portalHandler,
		ProfileHandler: profile,
		APIKeyRepo:     apiKeys,
		Limiter:        limiter,
		Address:        ":0",
		Version:        "test",
	})
}

// TestIntegration_ProfileRoute_Unauthorized verifies that GET /api/profile/me
// returns 401 when no X-API-Key header is provided, proving the route is registered
// in the real server.go router with RequireAPIKey middleware.
func TestIntegration_ProfileRoute_Unauthorized(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	h := NewProfileHandler(
		&mockRepFinder{}, &mockPeerFinder{}, &mockAssetSearcher{}, &mockPresenceChecker{}, &mockTrustCounter{},
	)

	srv := newIntegrationServer(t, apiKeys, h)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (RequireAPIKey should reject)", w.Code)
	}
}

// TestIntegration_ProfileRoute_Success verifies that GET /api/profile/me returns 200
// with a valid API key through the real server.go router, proving the full middleware
// chain works: CORS → RequireAPIKey → rate-limiter → handler.
func TestIntegration_ProfileRoute_Success(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	key, _ := apiKeys.Create(context.Background(), "peer-integration")

	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{PeerID: "peer-integration", CompositeScore: 0.5}},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer-integration", MaskedPeerID: "claw-int-1234", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 2},
		&mockPresenceChecker{online: true}, &mockTrustCounter{},
	)

	srv := newIntegrationServer(t, apiKeys, h)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify response has X-RateLimit headers (proves rate limiter is wired)
	if w.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("X-RateLimit-Limit header missing — rate limiter not wired in server.go")
	}
	if w.Header().Get("Cache-Control") != "private, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", w.Header().Get("Cache-Control"), "private, max-age=60")
	}
}

// TestIntegration_ProfileRoute_MiddlewareOrder verifies RequireAPIKey fires BEFORE
// the profile handler by confirming an invalid API key returns 401 (not 500 or 200).
func TestIntegration_ProfileRoute_MiddlewareOrder(t *testing.T) {
	apiKeys := repository.NewMemoryPeerAPIKeyRepository()
	h := NewProfileHandler(
		&mockRepFinder{}, &mockPeerFinder{}, &mockAssetSearcher{}, &mockPresenceChecker{}, &mockTrustCounter{},
	)

	srv := newIntegrationServer(t, apiKeys, h)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req.Header.Set("X-API-Key", "invalid-key-that-does-not-exist")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (invalid key should be rejected by middleware, not handler)", w.Code)
	}
}
