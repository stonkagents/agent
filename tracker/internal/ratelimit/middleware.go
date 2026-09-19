// Package: tracker/internal/ratelimit
// Feature: F-013 (Credits & Identity)
// Story: US-013-04 (Rate Limiting)
// Purpose: HTTP middleware for rate limiting registration endpoints

package ratelimit

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// KeyFunc extracts the rate limit key suffix from an HTTP request.
type KeyFunc func(r *http.Request) string

// IPKey extracts the client IP (default key function).
func IPKey(r *http.Request) string {
	return extractIP(r)
}

// Subnet24Key extracts the /24 subnet from the client IP.
func Subnet24Key(r *http.Request) string {
	ip := extractIP(r)
	parts := strings.Split(ip, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".")
	}
	return ip
}

// Subnet16Key extracts the /16 subnet from the client IP.
func Subnet16Key(r *http.Request) string {
	ip := extractIP(r)
	parts := strings.Split(ip, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[:2], ".")
	}
	return ip
}

// MiddlewareConfig configures a rate limit middleware layer.
type MiddlewareConfig struct {
	KeyPrefix  string
	Limit      int
	Window     time.Duration
	KeyFunc    KeyFunc // nil defaults to IPKey
	TrustProxy bool    // If true, trust X-Forwarded-For (only enable behind trusted reverse proxy)
	// Message names the window in the 429 body ("6 challenges per hour"); empty falls back
	// to the generic text.
	Message string
}

// RateLimitedResponse is the 429 body: the error envelope plus when to try again, in seconds
// (the same number as the Retry-After header).
type RateLimitedResponse struct {
	Error             RateLimitedError `json:"error"`
	RetryAfterSeconds int              `json:"retry_after_seconds"`
}

// RateLimitedError is the error object of a RateLimitedResponse.
type RateLimitedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// rateLimitedMessage is the 429 message: the configured window when set, generic otherwise,
// followed by when to try again.
func rateLimitedMessage(cfg MiddlewareConfig, retryAfterSec int) string {
	if cfg.Message == "" {
		return fmt.Sprintf("Too many requests. Try again in %d seconds.", retryAfterSec)
	}
	return fmt.Sprintf("Too many requests: limit is %s. Try again in %d seconds.", cfg.Message, retryAfterSec)
}

// Middleware applies rate limiting to HTTP handlers.
type Middleware struct {
	limiter Limiter
	config  MiddlewareConfig
}

// NewMiddleware creates a new rate limiting middleware.
func NewMiddleware(limiter Limiter, config MiddlewareConfig) *Middleware {
	if config.KeyFunc == nil {
		config.KeyFunc = IPKey
	}
	return &Middleware{
		limiter: limiter,
		config:  config,
	}
}

// Wrap returns an http.Handler that rate-limits requests before passing to next.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pass TrustProxy config to KeyFunc via request context
		keySuffix := m.extractKeySuffix(r)
		fullKey := m.config.KeyPrefix + keySuffix

		result, err := m.limiter.Allow(r.Context(), fullKey, m.config.Limit, m.config.Window)
		if err != nil {
			// Gate 0 Finding #7 fix: Return JSON envelope instead of plain text
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "INTERNAL_ERROR",
					"message": "Rate limiter error.",
				},
			})
			return
		}

		// Set rate limit headers
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", m.config.Limit))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", result.Remaining))
		if !result.ResetAt.IsZero() {
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", result.ResetAt.Unix()))
		}

		if !result.Allowed {
			retryAfterSec := int(time.Until(result.RetryAfter).Seconds()) + 1
			if retryAfterSec < 1 {
				retryAfterSec = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", result.RetryAfter.Unix()))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(RateLimitedResponse{
				Error:             RateLimitedError{Code: "RATE_LIMITED", Message: rateLimitedMessage(m.config, retryAfterSec)},
				RetryAfterSeconds: retryAfterSec,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// extractKeySuffix extracts the rate limit key suffix, respecting TrustProxy config.
func (m *Middleware) extractKeySuffix(r *http.Request) string {
	// First, extract the base IP with TrustProxy awareness
	baseIP := m.extractIPWithTrustProxy(r)

	// If no custom KeyFunc, return the IP directly
	if m.config.KeyFunc == nil {
		return baseIP
	}

	// For custom KeyFunc, we need to apply subnet logic on the trusted IP
	// Since KeyFunc operates on the request, create a modified request with the trusted IP as RemoteAddr
	modifiedReq := *r
	modifiedReq.RemoteAddr = baseIP + ":0"
	// Clear X-Forwarded-For so KeyFunc uses our trusted RemoteAddr
	modifiedReq.Header = r.Header.Clone()
	modifiedReq.Header.Del("X-Forwarded-For")

	return m.config.KeyFunc(&modifiedReq)
}

// extractIPWithTrustProxy extracts the client IP, only trusting X-Forwarded-For if TrustProxy is enabled.
// Gate 0 Finding #1 fix: Prevent X-Forwarded-For spoofing when not behind trusted proxy.
func (m *Middleware) extractIPWithTrustProxy(r *http.Request) string {
	// Only trust X-Forwarded-For if explicitly enabled (behind trusted reverse proxy like CloudFlare, AWS LB)
	if m.config.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.SplitN(xff, ",", 2)
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	// Use RemoteAddr (actual TCP connection IP)
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// extractIP extracts the client IP from the request, checking X-Forwarded-For.
// DEPRECATED: Use extractIPWithTrustProxy via Middleware instead.
// Kept for compatibility with legacy KeyFunc implementations.
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		ip := strings.TrimSpace(parts[0])
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
