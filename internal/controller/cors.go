// Package: internal/controller
// Feature: sprint-02
// Story: TD-020 (Controller CORS gap)
// Purpose: CORS middleware for controller HTTP server — allows browser preflight from portal
//
// PERF-2: the allowlist is the same as the daemon's (local dev origins + the
// deployed portal hosts) plus CORS_ALLOWED_ORIGINS, and preflights that carry
// Access-Control-Request-Private-Network: true are answered with
// Access-Control-Allow-Private-Network: true for allowed origins.

package controller

import (
	"net/http"
	"os"
	"strings"

	"github.com/stonkagents/agent/internal/installenv"
)

// defaultControllerCORSOrigins are always allowed, together with the deployed
// portal origins (installenv.PortalOrigins); CORS_ALLOWED_ORIGINS adds to them.
var defaultControllerCORSOrigins = []string{
	"http://localhost:3000",
	"http://127.0.0.1:3000",
	"http://localhost:7841",
	"http://127.0.0.1:7841",
}

// allowedCORSOrigins returns the allowlist: defaults plus CORS_ALLOWED_ORIGINS
// (comma-separated). "*" in the env allows any origin.
func allowedCORSOrigins() map[string]bool {
	allowed := make(map[string]bool, len(defaultControllerCORSOrigins)+4)
	for _, o := range defaultControllerCORSOrigins {
		allowed[o] = true
	}
	// The deployed portals on every product domain (same list as the daemon).
	for _, o := range installenv.PortalOrigins() {
		allowed[o] = true
	}
	// This environment's own daemon origin (a dev install's daemon is on 7861, not 7841).
	for _, o := range installenv.Current().LoopbackOrigins() {
		allowed[o] = true
	}
	for _, o := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	return allowed
}

// corsMiddleware wraps a handler with CORS headers.
// Same pattern as tracker CORSMiddleware (tracker/internal/api/middleware.go).
func corsMiddleware(next http.Handler) http.Handler {
	allowed := allowedCORSOrigins()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		originAllowed := false
		if origin != "" {
			if allowed["*"] {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				originAllowed = true
			} else if allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				originAllowed = true
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "3600")

		if r.Method == http.MethodOptions {
			if originAllowed && strings.EqualFold(strings.TrimSpace(r.Header.Get("Access-Control-Request-Private-Network")), "true") {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
