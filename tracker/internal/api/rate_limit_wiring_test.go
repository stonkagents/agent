// Package: tracker/internal/api
// Feature: sprint-02
// Story: TD-001 (Wire rate-limit middleware to routes)
// Purpose: Tests verifying rate-limit middleware is correctly wired to registration + sensitive routes

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// dummyOKHandler returns 200 for any request (stand-in for real handlers).
func dummyOKHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestRateLimit_ChallengeEndpoint verifies POST /challenge is limited to 6 per hour per IP.
func TestRateLimit_ChallengeEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, ChallengeRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/challenge", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
		if w.Header().Get("X-RateLimit-Limit") != "6" {
			t.Errorf("request %d: X-RateLimit-Limit = %q, want \"6\"", i+1, w.Header().Get("X-RateLimit-Limit"))
		}
	}

	// 7th should be 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/challenge", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("7th request: status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("7th request: Retry-After header missing")
	}
}

// TestRateLimit_RegisterIdentityEndpoint verifies POST /register/identity is limited to 5 per day per IP.
func TestRateLimit_RegisterIdentityEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, RegisterIdentityRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register/identity", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 6th should be 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register/identity", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("6th request: status = %d, want 429", w.Code)
	}
}

// TestRateLimit_RegistrationBodiesNameTheWindow verifies the 429 body of the registration
// limits carries retry_after_seconds and a message naming the window, so a daemon that hits
// the limit can tell the owner when to retry instead of showing a bare "try again later".
func TestRateLimit_RegistrationBodiesNameTheWindow(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ratelimit.MiddlewareConfig
		window  string
		maxWait int
	}{
		{name: "challenge", cfg: ChallengeRateLimitConfig(), window: "6 challenges per hour", maxWait: 3600},
		{name: "register identity", cfg: RegisterIdentityRateLimitConfig(), window: "5 registrations per day from this address", maxWait: 86400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
			handler := ratelimit.NewMiddleware(ratelimit.NewMemoryLimiter(clk), tc.cfg).Wrap(dummyOKHandler())
			var w *httptest.ResponseRecorder
			for i := 0; i <= tc.cfg.Limit; i++ {
				req := httptest.NewRequest(http.MethodPost, "/x", nil)
				req.RemoteAddr = "10.0.0.9:1"
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, req)
			}
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", w.Code)
			}
			var body ratelimit.RateLimitedResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (%s)", err, w.Body.String())
			}
			retryAfter, _ := strconv.Atoi(w.Header().Get("Retry-After"))
			if body.Error.Code != "RATE_LIMITED" || !strings.Contains(body.Error.Message, tc.window) {
				t.Errorf("body = %+v, want RATE_LIMITED naming %q", body, tc.window)
			}
			if body.RetryAfterSeconds < 1 || body.RetryAfterSeconds > tc.maxWait || body.RetryAfterSeconds != retryAfter {
				t.Errorf("retry_after_seconds = %d (Retry-After %d), want 1..%d and equal", body.RetryAfterSeconds, retryAfter, tc.maxWait)
			}
			if !strings.Contains(body.Error.Message, strconv.Itoa(body.RetryAfterSeconds)+" seconds") {
				t.Errorf("message %q does not say when to retry", body.Error.Message)
			}
		})
	}
}

// TestRateLimit_LegacyRegisterEndpoint verifies POST /register (legacy) is limited to 30 per hour per IP.
func TestRateLimit_LegacyRegisterEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, LegacyRegisterRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	// Send 30 allowed requests
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 31st should be 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("31st request: status = %d, want 429", w.Code)
	}
}

// TestRateLimit_SpendTokenEndpoint verifies POST /credits/spend-token is limited to 60 per hour per IP.
func TestRateLimit_SpendTokenEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, SpendTokenRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	for i := 0; i < 60; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/credits/spend-token", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 61st should be 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/credits/spend-token", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("61st request: status = %d, want 429", w.Code)
	}
}

// TestRateLimit_DifferentIPsIndependent verifies different IPs have independent limits.
func TestRateLimit_DifferentIPsIndependent(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, ChallengeRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	// Exhaust limit for 10.0.0.1
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// Different IP should still work
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("different IP status = %d, want 200", w.Code)
	}
}

// TestProtocolEndpointRateLimitConfig_Values verifies the config parameters for protocol endpoint rate limiting.
// TD-090: 6 unauthenticated POSTs (goodbye, announce, availability, dmca, stats, downloads/complete) had no rate limiter.
func TestProtocolEndpointRateLimitConfig_Values(t *testing.T) {
	cfg := ProtocolEndpointRateLimitConfig()
	if cfg.KeyPrefix != "protocol:" {
		t.Errorf("KeyPrefix = %q, want \"protocol:\"", cfg.KeyPrefix)
	}
	if cfg.Limit != 100 {
		t.Errorf("Limit = %d, want 100", cfg.Limit)
	}
	if cfg.Window != time.Minute {
		t.Errorf("Window = %v, want 1m", cfg.Window)
	}
	if cfg.KeyFunc == nil {
		t.Error("KeyFunc is nil, want non-nil")
	}
}

// TestRateLimit_ProtocolEndpoint verifies unauthenticated POST endpoints are limited to 100 per minute per IP.
func TestRateLimit_ProtocolEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, ProtocolEndpointRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	// 100 requests should succeed
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/goodbye", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}

	// 101st should be 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/goodbye", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("101st request: status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("101st request: Retry-After header missing")
	}
}

// TestRateLimit_GuestKeyEndpoint verifies POST /guest-key is limited to 3 per hour per IP.
// TD-087: Guest key endpoint had no rate limiting — attacker could farm API keys.
func TestRateLimit_GuestKeyEndpoint(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, GuestKeyRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	// 3 requests should succeed
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/portal/guest-key", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
		if w.Header().Get("X-RateLimit-Limit") != "3" {
			t.Errorf("request %d: X-RateLimit-Limit = %q, want \"3\"", i+1, w.Header().Get("X-RateLimit-Limit"))
		}
	}

	// 4th should be 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/portal/guest-key", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("4th request: Retry-After header missing")
	}
}

// TestGuestKeyRateLimitConfig_Values verifies the config parameters for guest-key rate limiting.
func TestGuestKeyRateLimitConfig_Values(t *testing.T) {
	cfg := GuestKeyRateLimitConfig()
	if cfg.KeyPrefix != "guest-key:" {
		t.Errorf("KeyPrefix = %q, want \"guest-key:\"", cfg.KeyPrefix)
	}
	if cfg.Limit != 3 {
		t.Errorf("Limit = %d, want 3", cfg.Limit)
	}
	if cfg.Window != time.Hour {
		t.Errorf("Window = %v, want 1h", cfg.Window)
	}
	if cfg.KeyFunc == nil {
		t.Error("KeyFunc is nil, want non-nil")
	}
}

// TestRateLimit_HeadersOnEveryResponse verifies X-RateLimit-* headers are present even on allowed requests.
func TestRateLimit_HeadersOnEveryResponse(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := ratelimit.NewMemoryLimiter(clk)
	mw := ratelimit.NewMiddleware(limiter, ChallengeRateLimitConfig())
	handler := mw.Wrap(dummyOKHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("X-RateLimit-Limit header missing")
	}
	if w.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("X-RateLimit-Remaining header missing")
	}
}
