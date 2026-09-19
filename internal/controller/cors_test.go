// Package: internal/controller
// Feature: sprint-02
// Story: TD-020 (Controller CORS gap)
// Purpose: Tests for CORS middleware on controller HTTP server

package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// dummyHandler returns a simple 200 OK handler for CORS tests.
func dummyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCORS_PreflightReturns204(t *testing.T) {
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodOptions, "/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q, want %q", got, "http://localhost:3000")
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Allow-Methods header missing on preflight")
	}
}

func TestCORS_AllowedOriginEchoed(t *testing.T) {
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:3000" {
		t.Errorf("Allow-Origin = %q, want %q", got, "http://127.0.0.1:3000")
	}
}

func TestCORS_DisallowedOriginNoHeader(t *testing.T) {
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty (disallowed origin)", got)
	}
}

func TestCORS_NoOriginHeaderPassesThrough(t *testing.T) {
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	// No Origin header
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	// No CORS headers should be set
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty (no Origin in request)", got)
	}
}

func TestCORS_WildcardAllowsAnyOrigin(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Origin", "https://anything.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want %q", got, "*")
	}
}

func TestCORS_CustomOriginsFromEnv(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.stonkagents.com,https://staging.stonkagents.com")
	handler := corsMiddleware(dummyHandler())

	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"production origin allowed", "https://app.stonkagents.com", "https://app.stonkagents.com"},
		{"staging origin allowed", "https://staging.stonkagents.com", "https://staging.stonkagents.com"},
		{"random origin blocked", "https://evil.com", ""},
		// PERF-2: CORS_ALLOWED_ORIGINS adds to the built-in origins (same as the daemon)
		{"localhost still included when env adds origins", "http://localhost:3000", "http://localhost:3000"},
		{"deployed portal always allowed", "https://stonkagents.com", "https://stonkagents.com"},
		{"new portal domain always allowed", "https://stonkagents.com", "https://stonkagents.com"},
		{"new dev portal always allowed", "https://dev.stonkagents.com", "https://dev.stonkagents.com"},
		{"new stg portal always allowed", "https://stg.stonkagents.com", "https://stg.stonkagents.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/status", nil)
			req.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			got := w.Header().Get("Access-Control-Allow-Origin")
			if got != tt.want {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCORS_MaxAgeHeaderPresent(t *testing.T) {
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodOptions, "/start", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Error("Max-Age header missing on preflight")
	}
}

func TestNewServer_CORSIntegration(t *testing.T) {
	// Verify NewServer wraps mux with CORS so preflight on any path returns 204
	s, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	req := httptest.NewRequest(http.MethodOptions, "/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("NewServer preflight status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("NewServer Allow-Origin = %q, want %q", got, "http://localhost:3000")
	}
}
