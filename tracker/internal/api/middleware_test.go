// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Tests for HTTP middleware

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCorrelationID_Generated(t *testing.T) {
	handler := CorrelationIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	id := w.Header().Get(CorrelationIDHeader)
	if id == "" {
		t.Error("CorrelationIDMiddleware() did not generate correlation ID")
	}
}

func TestCorrelationID_Passthrough(t *testing.T) {
	handler := CorrelationIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(CorrelationIDHeader, "my-custom-id")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	id := w.Header().Get(CorrelationIDHeader)
	if id != "my-custom-id" {
		t.Errorf("CorrelationIDMiddleware() got ID = %q, want %q", id, "my-custom-id")
	}
}

func TestContentType_RejectsNonJSON(t *testing.T) {
	handler := ContentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("ContentTypeMiddleware() status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
}

func TestContentType_AcceptsJSON(t *testing.T) {
	handler := ContentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("ContentTypeMiddleware() status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestContentType_SkipsGET(t *testing.T) {
	handler := ContentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("ContentTypeMiddleware() GET status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_OPTIONS_Returns204(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := CORSMiddleware(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/home", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want %d (preflight must succeed for CORS)", w.Code, http.StatusNoContent)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", w.Header().Get("Access-Control-Allow-Origin"), "http://localhost:3000")
	}
}

// TestCORSMiddleware_DefaultOrigins pins the built-in origin list used when
// CORS_ALLOWED_ORIGINS is unset: the local dev hosts only.
func TestCORSMiddleware_DefaultOrigins(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := CORSMiddleware(next)

	allowed := []string{
		"http://localhost:3000",
		"http://localhost:7841",
	}
	for _, origin := range allowed {
		req := httptest.NewRequest(http.MethodOptions, "/api/home", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %q: Access-Control-Allow-Origin = %q, want %q", origin, got, origin)
		}
	}

	// An unlisted origin is still refused.
	req := httptest.NewRequest(http.MethodOptions, "/api/home", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin allowed: Access-Control-Allow-Origin = %q, want empty", got)
	}
}
