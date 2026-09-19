// Package: internal/daemon
// Purpose: Tests for CORS middleware

package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORSMiddleware_AllowedOrigin - SECURITY TEST
// Verifies that CORS headers are set for allowed origins
func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	// Arrange
	config := &CORSConfig{
		AllowedOrigins:   []string{"http://localhost:3000", "http://127.0.0.1:3000"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           3600,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	corsHandler := CORSMiddleware(config)(handler)

	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()

	// Act
	corsHandler.ServeHTTP(rec, req)

	// Assert
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Expected Access-Control-Allow-Origin to be set, got: %s", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("Expected Access-Control-Allow-Credentials to be 'true', got: %s", rec.Header().Get("Access-Control-Allow-Credentials"))
	}
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

// TestCORSMiddleware_DisallowedOrigin - SECURITY TEST
// Verifies that CORS headers are NOT set for disallowed origins
func TestCORSMiddleware_DisallowedOrigin(t *testing.T) {
	// Arrange
	config := &CORSConfig{
		AllowedOrigins:   []string{"http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Authorization"},
		ExposedHeaders:   []string{},
		AllowCredentials: false,
		MaxAge:           0,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	corsHandler := CORSMiddleware(config)(handler)

	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Header.Set("Origin", "http://malicious.com") // Disallowed origin
	rec := httptest.NewRecorder()

	// Act
	corsHandler.ServeHTTP(rec, req)

	// Assert - CORS headers should NOT be set for disallowed origin
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS headers should not be set for disallowed origin - SECURITY VIOLATION")
	}
	// Handler should still execute (CORS is permissive, not enforced server-side)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

// TestCORSMiddleware_PreflightRequest - SECURITY TEST
// Verifies that OPTIONS preflight requests return correct headers
func TestCORSMiddleware_PreflightRequest(t *testing.T) {
	// Arrange
	config := &CORSConfig{
		AllowedOrigins:   []string{"http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           3600,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called for OPTIONS preflight")
	})

	corsHandler := CORSMiddleware(config)(handler)

	req := httptest.NewRequest("OPTIONS", "/api/v1/share", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()

	// Act
	corsHandler.ServeHTTP(rec, req)

	// Assert
	if rec.Code != http.StatusNoContent {
		t.Errorf("Expected status 204 No Content for preflight, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Error("Preflight should set Access-Control-Allow-Origin")
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("Preflight should set Access-Control-Allow-Methods")
	}
	if rec.Header().Get("Access-Control-Max-Age") != "3600" {
		t.Errorf("Expected Max-Age 3600, got: %s", rec.Header().Get("Access-Control-Max-Age"))
	}
}

// TestCORSMiddleware_NoOriginHeader
// Verifies that requests without Origin header are processed normally
func TestCORSMiddleware_NoOriginHeader(t *testing.T) {
	// Arrange
	config := DefaultCORSConfig()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	corsHandler := CORSMiddleware(config)(handler)

	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	// No Origin header set
	rec := httptest.NewRecorder()

	// Act
	corsHandler.ServeHTTP(rec, req)

	// Assert - Request should succeed even without Origin header
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
	// CORS headers should not be set for same-origin requests (no Origin header)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS headers should not be set when no Origin header present")
	}
}

// TestCORS_AllowsNullOrigin — F-025: file:// SPA sends Origin: null
func TestCORS_AllowsNullOrigin(t *testing.T) {
	config := DefaultCORSConfig()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	corsHandler := CORSMiddleware(config)(handler)

	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Header.Set("Origin", "null")
	rec := httptest.NewRecorder()
	corsHandler.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Error("CORS should allow Origin: null for file:// SPA")
	}
}

// TestDefaultCORSConfig
// Verifies default CORS configuration includes required origins
func TestDefaultCORSConfig(t *testing.T) {
	config := DefaultCORSConfig()

	// Assert - Default origins include localhost variations + null for file:// SPA
	expectedOrigins := []string{
		"http://localhost:3000",
		"http://127.0.0.1:3000",
		"http://localhost:7841",
		"http://127.0.0.1:7841",
		"null",
	}

	for _, expected := range expectedOrigins {
		found := false
		for _, origin := range config.AllowedOrigins {
			if origin == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected default config to include origin %s", expected)
		}
	}

	// Assert - Default methods include required HTTP verbs
	if len(config.AllowedMethods) < 4 {
		t.Error("Default config should include at least GET, POST, PUT, DELETE, OPTIONS")
	}

	// Assert - Default headers include Authorization
	foundAuth := false
	for _, header := range config.AllowedHeaders {
		if header == "Authorization" {
			foundAuth = true
			break
		}
	}
	if !foundAuth {
		t.Error("Default config should include Authorization header")
	}
}

// TestCORSMiddleware_PrivateNetworkPreflight - PERF-2
// Chrome Local Network Access: a preflight carrying
// Access-Control-Request-Private-Network: true gets
// Access-Control-Allow-Private-Network: true, for allowed origins only.
func TestCORSMiddleware_PrivateNetworkPreflight(t *testing.T) {
	config := DefaultCORSConfig()
	corsHandler := CORSMiddleware(config)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name, origin, requestHeader, want string
	}{
		{"allowed origin with request header", "https://stonkagents.com", "true", "true"},
		{"allowed origin, header case-insensitive", "https://dev.stonkagents.com", "TRUE", "true"},
		{"allowed origin without request header", "https://stg.stonkagents.com", "", ""},
		{"new domain allowed with request header", "https://stonkagents.com", "true", "true"},
		{"new dev domain allowed", "https://dev.stonkagents.com", "true", "true"},
		{"new stg domain allowed", "https://stg.stonkagents.com", "true", "true"},
		{"disallowed origin", "https://evil.example.com", "true", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/api/v1/setup/status", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Access-Control-Request-Method", "GET")
			if tc.requestHeader != "" {
				req.Header.Set("Access-Control-Request-Private-Network", tc.requestHeader)
			}
			rec := httptest.NewRecorder()
			corsHandler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("preflight status = %d", rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != tc.want {
				t.Errorf("Allow-Private-Network = %q, want %q", got, tc.want)
			}
		})
	}

	// Non-preflight requests never carry the grant
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Origin", "https://stonkagents.com")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	corsHandler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("GET got Allow-Private-Network = %q", got)
	}
}

// TestCORSConfig_AddOriginLive verifies the allowlist can grow at runtime
// (POST /api/v1/setup/origin) and is de-duplicated.
func TestCORSConfig_AddOriginLive(t *testing.T) {
	config := &CORSConfig{AllowedOrigins: []string{"http://localhost:3000"}}
	handler := CORSMiddleware(config)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	probe := func() string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
		req.Header.Set("Origin", "https://new.example")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Header().Get("Access-Control-Allow-Origin")
	}
	if probe() != "" {
		t.Fatal("origin allowed before AddOrigin")
	}
	if !config.AddOrigin("https://new.example") {
		t.Error("AddOrigin returned false for a new origin")
	}
	if config.AddOrigin("https://new.example") {
		t.Error("AddOrigin returned true for a duplicate")
	}
	if probe() != "https://new.example" {
		t.Error("origin not allowed after AddOrigin")
	}
	if got := len(config.Origins()); got != 2 {
		t.Errorf("Origins() = %d, want 2", got)
	}
}
