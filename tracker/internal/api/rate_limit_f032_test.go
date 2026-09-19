/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-01 (Peer Data Foundation)
 * Purpose: RED tests for rate limiting on F-032 endpoints
 */

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
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// TestTrustMutation_RateLimited verifies POST /peers/{id}/trust is rate limited.
func TestTrustMutation_RateLimited(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "target-peer", LastSeen: time.Now()})

	// Create limiter and apply to server
	clk := clock.NewMockClock(time.Now())
	limiter := ratelimit.NewMemoryLimiter(clk)

	// Wrap the portal handler with rate limiting
	trustConfig := TrustBlockMutationRateLimitConfig()
	rlMw := ratelimit.NewMiddleware(limiter, trustConfig)

	var handler http.Handler = http.HandlerFunc(env.srv.Router().ServeHTTP)
	handler = rlMw.Wrap(handler)

	// Make requests up to limit (assume limit is 30/hour)
	for i := 0; i < trustConfig.Limit; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/peers/target-peer/trust", nil)
		req.Header.Set("X-API-Key", actorKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// Next request should be rate limited
	req := httptest.NewRequest(http.MethodPost, "/api/peers/target-peer/trust", nil)
	req.Header.Set("X-API-Key", actorKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 after limit exceeded, got %d. Body: %s", w.Code, w.Body.String())
	}

	// Verify Retry-After header is present
	if w.Header().Get("Retry-After") == "" {
		t.Error("Expected Retry-After header in 429 response")
	}
}

// TestPublicPeerDetail_RateLimited verifies GET /peers/{id}/reputation is rate limited by IP.
func TestPublicPeerDetail_RateLimited(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "test-peer", LastSeen: time.Now()})
	_ = env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "test-peer",
		CompositeScore: 0.75,
		UpdatedAt:      time.Now(),
	})

	// Create limiter and apply to server
	clk := clock.NewMockClock(time.Now())
	limiter := ratelimit.NewMemoryLimiter(clk)

	// Wrap with rate limiting
	publicConfig := PublicPeerDetailRateLimitConfig()
	rlMw := ratelimit.NewMiddleware(limiter, publicConfig)

	var handler http.Handler = http.HandlerFunc(env.srv.Router().ServeHTTP)
	handler = rlMw.Wrap(handler)

	// Make requests up to limit (assume limit is 100/hour)
	for i := 0; i < publicConfig.Limit; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/peers/test-peer/reputation", nil)
		req.RemoteAddr = "192.168.1.100:12345" // Same IP
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// Next request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/peers/test-peer/reputation", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 after limit exceeded, got %d", w.Code)
	}
}

// TestTrustedList_RateLimited verifies GET /peers/trusted is rate limited by peer_id.
func TestTrustedList_RateLimited(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	// Create limiter and apply to server
	clk := clock.NewMockClock(time.Now())
	limiter := ratelimit.NewMemoryLimiter(clk)

	// Wrap with rate limiting
	listConfig := TrustBlockListRateLimitConfig()
	rlMw := ratelimit.NewMiddleware(limiter, listConfig)

	var handler http.Handler = http.HandlerFunc(env.srv.Router().ServeHTTP)
	handler = rlMw.Wrap(handler)

	// Make requests up to limit (assume limit is 60/hour)
	for i := 0; i < listConfig.Limit; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
		req.Header.Set("X-API-Key", actorKey)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// Next request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 after limit exceeded, got %d", w.Code)
	}
}
