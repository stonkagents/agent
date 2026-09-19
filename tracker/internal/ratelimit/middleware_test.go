// Package: tracker/internal/ratelimit
// Feature: F-013 (Credits & Identity)
// Story: US-013-04 (Rate Limiting)
// Purpose: Tests for rate limiting HTTP middleware

package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_AllowsUnderLimit(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:reg:ip:",
		Limit:     5,
		Window:    time.Hour,
	})

	handler := mw.Wrap(okHandler())

	req := httptest.NewRequest("POST", "/api/v1/tracker/register", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Middleware status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Header().Get("X-RateLimit-Limit") != "5" {
		t.Errorf("X-RateLimit-Limit = %q, want \"5\"", w.Header().Get("X-RateLimit-Limit"))
	}
}

func TestMiddleware_DeniesOverLimit(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:reg:ip:",
		Limit:     2,
		Window:    time.Hour,
	})

	handler := mw.Wrap(okHandler())

	// Exhaust limit
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// 3rd request should be denied
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Middleware status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header should be set")
	}
}

func TestMiddleware_SetsResetHeader(t *testing.T) {
	baseTime := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewMockClock(baseTime)
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:test:",
		Limit:     5,
		Window:    time.Hour,
	})

	handler := mw.Wrap(okHandler())

	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resetHeader := w.Header().Get("X-RateLimit-Reset")
	if resetHeader == "" {
		t.Fatal("X-RateLimit-Reset header should be set on allowed requests")
	}

	// Window is 1 hour → reset = baseTime + 1h
	expectedReset := baseTime.Add(time.Hour).Unix()
	want := fmt.Sprintf("%d", expectedReset)
	if resetHeader != want {
		t.Errorf("X-RateLimit-Reset = %q, want %q", resetHeader, want)
	}
}

func TestMiddleware_SubnetKeyExtraction(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	// /24 subnet: 1.2.3.0/24 → key should be "rl:reg:s24:1.2.3"
	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:reg:s24:",
		Limit:     25,
		Window:    time.Hour,
		KeyFunc:   Subnet24Key,
	})

	handler := mw.Wrap(okHandler())

	// Two IPs on same /24 should share a counter
	for _, ip := range []string{"1.2.3.4:12345", "1.2.3.99:12345"} {
		req := httptest.NewRequest("POST", "/", nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// Check count for the shared /24 key
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	count, _ := limiter.GetCount(ctx, "rl:reg:s24:1.2.3", time.Hour)
	if count != 2 {
		t.Errorf("Subnet /24 counter = %d, want 2 (two IPs on same subnet)", count)
	}
}

// TestMiddleware_429_IncludesResetHeader verifies X-RateLimit-Reset header is present on 429 and is a valid Unix timestamp.
func TestMiddleware_429_IncludesResetHeader(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:test:",
		Limit:     1,
		Window:    time.Hour,
	})

	handler := mw.Wrap(okHandler())

	// First request: allowed
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Second request: should be 429
	req2 := httptest.NewRequest("POST", "/", nil)
	req2.RemoteAddr = "1.2.3.4:12345"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w2.Code)
	}
	resetStr := w2.Header().Get("X-RateLimit-Reset")
	if resetStr == "" {
		t.Fatal("X-RateLimit-Reset header missing on 429 response")
	}
	// Should be a valid Unix timestamp (numeric only)
	for _, ch := range resetStr {
		if ch < '0' || ch > '9' {
			t.Fatalf("X-RateLimit-Reset = %q, want numeric Unix timestamp", resetStr)
		}
	}
}

func TestMiddleware_DifferentSubnetsIndependent(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:reg:s24:",
		Limit:     2,
		Window:    time.Hour,
		KeyFunc:   Subnet24Key,
	})

	handler := mw.Wrap(okHandler())

	// Exhaust limit for 1.2.3.0/24
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// Different /24 should still work
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "1.2.4.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Different subnet status = %d, want %d", w.Code, http.StatusOK)
	}
}

// TestMiddleware_XFFSpoofPrevention verifies that X-Forwarded-For cannot be spoofed when not behind a trusted proxy.
// Gate 0 Finding #1 (HIGH): X-Forwarded-For trust enables rate limit bypass
func TestMiddleware_XFFSpoofPrevention(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix: "rl:test:",
		Limit:     2,
		Window:    time.Hour,
		// No TrustProxy flag — should NOT trust X-Forwarded-For
	})

	handler := mw.Wrap(okHandler())

	// Attacker exhausts limit from IP 1.2.3.4
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d should succeed, got %d", i+1, w.Code)
		}
	}

	// Attacker tries to bypass by spoofing X-Forwarded-For
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "1.2.3.4:12345"             // Same real IP
	req.Header.Set("X-Forwarded-For", "8.8.8.8") // Spoofed different IP
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should be denied (rate limited by real IP, not spoofed XFF)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Spoofed XFF bypass attempt status = %d, want 429 (should rate-limit by RemoteAddr when not behind trusted proxy)", w.Code)
	}
}

// TestMiddleware_XFFTrustedProxyMode verifies X-Forwarded-For is trusted when behind trusted proxy.
func TestMiddleware_XFFTrustedProxyMode(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)

	mw := NewMiddleware(limiter, MiddlewareConfig{
		KeyPrefix:  "rl:test:",
		Limit:      2,
		Window:     time.Hour,
		TrustProxy: true, // Behind trusted proxy (e.g., CloudFlare, AWS LB)
	})

	handler := mw.Wrap(okHandler())

	// Request 1 from client IP 1.1.1.1 (via proxy)
	req1 := httptest.NewRequest("POST", "/", nil)
	req1.RemoteAddr = "10.0.0.1:12345" // Proxy IP
	req1.Header.Set("X-Forwarded-For", "1.1.1.1")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("request 1 should succeed, got %d", w1.Code)
	}

	// Request 2 from same client IP via proxy
	req2 := httptest.NewRequest("POST", "/", nil)
	req2.RemoteAddr = "10.0.0.1:12345"            // Same proxy
	req2.Header.Set("X-Forwarded-For", "1.1.1.1") // Same client
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("request 2 should succeed, got %d", w2.Code)
	}

	// Request 3 should be rate-limited (exceeded 2 req limit for 1.1.1.1)
	req3 := httptest.NewRequest("POST", "/", nil)
	req3.RemoteAddr = "10.0.0.1:12345"
	req3.Header.Set("X-Forwarded-For", "1.1.1.1")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("request 3 status = %d, want 429 (should rate-limit by XFF when TrustProxy=true)", w3.Code)
	}

	// Different client IP should have independent limit
	req4 := httptest.NewRequest("POST", "/", nil)
	req4.RemoteAddr = "10.0.0.1:12345"            // Same proxy
	req4.Header.Set("X-Forwarded-For", "2.2.2.2") // Different client
	w4 := httptest.NewRecorder()
	handler.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Errorf("Different XFF client status = %d, want 200", w4.Code)
	}
}

// TestMiddleware_InternalErrorReturnsJSONEnvelope verifies rate limiter internal errors return JSON, not plain text.
// Gate 0 Finding #7 (LOW): Rate limiter should return JSON envelope for all error responses.
func TestMiddleware_InternalErrorReturnsJSONEnvelope(t *testing.T) {
	// Create a failing limiter that always returns an error
	failingLimiter := &failingLimiter{}

	mw := NewMiddleware(failingLimiter, MiddlewareConfig{
		KeyPrefix: "rl:test:",
		Limit:     10,
		Window:    time.Hour,
	})

	handler := mw.Wrap(okHandler())

	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want \"application/json\"", contentType)
	}

	// Parse response body to verify JSON envelope structure
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode JSON response: %v", err)
	}

	errorMap, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response missing 'error' object: %+v", resp)
	}

	if code, _ := errorMap["code"].(string); code != "INTERNAL_ERROR" {
		t.Errorf("error.code = %q, want \"INTERNAL_ERROR\"", code)
	}

	if message, _ := errorMap["message"].(string); message == "" {
		t.Error("error.message should not be empty")
	}
}

// failingLimiter always returns an error
type failingLimiter struct{}

func (f *failingLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error) {
	return Result{}, fmt.Errorf("simulated limiter failure")
}

func (f *failingLimiter) GetCount(ctx context.Context, key string, window time.Duration) (int, error) {
	return 0, fmt.Errorf("simulated limiter failure")
}

func (f *failingLimiter) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return nil, fmt.Errorf("simulated limiter failure")
}
