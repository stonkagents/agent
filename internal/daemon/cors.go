// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Purpose: CORS middleware for browser clients
//
// SECURITY: Configurable CORS policy to prevent unauthorized cross-origin requests
//
// PERF-2: Chrome's Local Network Access check sends
// Access-Control-Request-Private-Network: true on the preflight when a public
// page (the portal) calls a loopback address (this daemon). The preflight must
// answer Access-Control-Allow-Private-Network: true or the browser blocks the
// call. The header is only added for allowed origins.

package daemon

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/setup"
)

// PortalOrigins are the deployed portal hosts that call the daemon on the
// user's machine (dev, stg and prd on every product domain: stonkagents.com). Shared with the controller allowlist.
var PortalOrigins = installenv.PortalOrigins()

// localDevOrigins are the local frontend / daemon UI origins.
var localDevOrigins = []string{
	"http://localhost:3000", // Development frontend
	"http://127.0.0.1:3000", // Development frontend (alternate)
	"http://localhost:7841", // Daemon UI
	"http://127.0.0.1:7841", // Daemon UI (alternate)
}

// EnvAllowedOrigins parses CORS_ALLOWED_ORIGINS (comma-separated).
func EnvAllowedOrigins() []string {
	var out []string
	for _, origin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			out = append(out, origin)
		}
	}
	return out
}

// CORSConfig represents CORS configuration. AllowedOrigins may be extended at
// runtime (AddOrigin) by the setup surface; reads and writes go through mu.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           int // Preflight cache duration in seconds

	mu sync.RWMutex
}

// DefaultCORSConfig returns the default CORS configuration for development
func DefaultCORSConfig() *CORSConfig {
	// Load allowed origins from environment variable or use defaults
	allowedOrigins := append([]string{}, localDevOrigins...)
	// This environment's own daemon origin (a dev install listens on 7861, not 7841).
	allowedOrigins = append(allowedOrigins, installenv.Current().LoopbackOrigins()...)
	allowedOrigins = append(allowedOrigins, PortalOrigins...)
	allowedOrigins = append(allowedOrigins, "null") // F-025: file:// SPA sends Origin: null

	// Allow additional origins from CORS_ALLOWED_ORIGINS env var (comma-separated)
	allowedOrigins = append(allowedOrigins, EnvAllowedOrigins()...)

	return &CORSConfig{
		AllowedOrigins: allowedOrigins,
		AllowedMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		},
		AllowedHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"X-Request-ID",
			setup.MutationHeader, // setup POSTs (RUN-1); its presence forces the preflight
		},
		ExposedHeaders: []string{
			"X-Request-ID",
			"X-RateLimit-Limit",
			"X-RateLimit-Remaining",
			"X-RateLimit-Reset",
		},
		AllowCredentials: true,
		MaxAge:           3600, // 1 hour preflight cache
	}
}

// IsOriginAllowed reports whether origin is in the allowlist (or "*" is).
func (c *CORSConfig) IsOriginAllowed(origin string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, allowedOrigin := range c.AllowedOrigins {
		if origin == allowedOrigin || allowedOrigin == "*" {
			return true
		}
	}
	return false
}

// AddOrigin appends origin to the live allowlist. Returns false when it was
// already present.
func (c *CORSConfig) AddOrigin(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.AllowedOrigins {
		if existing == origin {
			return false
		}
	}
	c.AllowedOrigins = append(c.AllowedOrigins, origin)
	return true
}

// AddOrigins appends every non-empty origin (config.yaml cors_allowed_origins).
func (c *CORSConfig) AddOrigins(origins []string) {
	for _, o := range origins {
		c.AddOrigin(o)
	}
}

// Origins returns a copy of the current allowlist.
func (c *CORSConfig) Origins() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string{}, c.AllowedOrigins...)
}

// privateNetworkRequested reports whether a preflight carries Chrome's Local
// Network Access request header (PERF-2).
func privateNetworkRequested(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Access-Control-Request-Private-Network")), "true")
}

// CORSMiddleware creates a CORS middleware handler
func CORSMiddleware(config *CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Set CORS headers if origin is allowed
			if origin != "" && config.IsOriginAllowed(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", strings.Join(config.AllowedMethods, ", "))
				w.Header().Set("Access-Control-Allow-Headers", strings.Join(config.AllowedHeaders, ", "))
				w.Header().Set("Access-Control-Expose-Headers", strings.Join(config.ExposedHeaders, ", "))
				if config.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if config.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", config.MaxAge))
				}
				if privateNetworkRequested(r) {
					w.Header().Set("Access-Control-Allow-Private-Network", "true")
				}
			}

			// Handle preflight OPTIONS request
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// Call next handler
			next.ServeHTTP(w, r)
		})
	}
}
