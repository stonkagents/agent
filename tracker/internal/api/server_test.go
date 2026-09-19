// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Integration tests for server routing and middleware

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

func TestServer_HealthEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Health status = %d, want %d", w.Code, http.StatusOK)
	}

	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "healthy" {
		t.Errorf("Health status = %q, want %q", body["status"], "healthy")
	}
}

func TestServer_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("NotFound status = %d, want %d", w.Code, http.StatusNotFound)
	}

	var body ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("NotFound code = %q, want %q", body.Error.Code, "NOT_FOUND")
	}
}

func TestServer_MiddlewareChain_CorrelationID(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	id := w.Header().Get(CorrelationIDHeader)
	if id == "" {
		t.Error("CorrelationID missing from response")
	}
}

// TestServer_ForumMutation_NilAPIKeyRepo_Returns503 verifies that when apiKeyRepo is nil,
// forum mutation endpoints return 503 SERVICE_UNAVAILABLE instead of falling through unprotected.
// TD-089: Forum auth nil-guard — prevents unprotected write access when auth service is not configured.
func TestServer_ForumMutation_NilAPIKeyRepo_Returns503(t *testing.T) {
	forumRepo := repository.NewMemoryForumRepository()
	forumSvc := services.NewForumService(forumRepo, nil, nil)
	// Create server with ForumHandler but WITHOUT APIKeyRepo (nil)
	srv := NewServer(ServerDeps{
		PeerHandler:  newTestServer_peerHandler(t),
		ForumHandler: NewForumHandler(forumSvc, nil),
		Address:      ":7842",
	})

	mutationRoutes := []struct {
		path string
	}{
		{"/api/v1/tracker/forum/posts"},
		{"/api/v1/tracker/forum/posts/test-id/upvote"},
		{"/api/v1/tracker/forum/posts/test-id/replies"},
	}

	for _, tt := range mutationRoutes {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("POST %s status = %d, want 503", tt.path, w.Code)
			}

			var body ErrorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to unmarshal error body: %v", err)
			}
			if body.Error.Code != "SERVICE_UNAVAILABLE" {
				t.Errorf("POST %s error code = %q, want \"SERVICE_UNAVAILABLE\"", tt.path, body.Error.Code)
			}
		})
	}
}

// newTestServer_peerHandler creates a minimal PeerHandler for tests that need the server to boot.
func newTestServer_peerHandler(t *testing.T) *PeerHandler {
	t.Helper()
	peerRepo := repository.NewMemoryPeerRepository()
	peerSvc := services.NewPeerService(peerRepo, nil, nil)
	return NewPeerHandler(peerSvc)
}

// TestServer_RejectsNonJSONPost verifies that ContentTypeMiddleware is wired at the server level.
// TD-091: POST requests without Content-Type: application/json should be rejected with 415.
func TestServer_RejectsNonJSONPost(t *testing.T) {
	srv, _ := newTestServer(t)

	// POST to /api/v1/tracker/register without Content-Type header (route exists in test server via PeerHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("Non-JSON POST status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}

	var body ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "UNSUPPORTED_MEDIA_TYPE" {
		t.Errorf("error code = %q, want %q", body.Error.Code, "UNSUPPORTED_MEDIA_TYPE")
	}
}

func TestServer_AllRoutesRegistered(t *testing.T) {
	srv, _ := newTestServer(t)

	tests := []struct {
		method string
		path   string
		want   int // should NOT be 404
	}{
		{"GET", "/health", http.StatusOK},
		{"GET", "/api/v1/tracker/peers", http.StatusOK},
		{"GET", "/api/v1/tracker/search", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Errorf("Route %s %s returned 404, expected route to be registered", tt.method, tt.path)
			}
		})
	}
}
