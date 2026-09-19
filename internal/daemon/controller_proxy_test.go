// Package: internal/daemon
// Feature: F-025 (Auto-Update System)
// Story: US-025-04 (Daemon Update Relay)
// Purpose: Tests for daemon → controller reverse proxy route

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerProxy_ForwardsRequest(t *testing.T) {
	// Stand up a mock "controller" backend
	var receivedPath string
	var receivedMethod string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer controller.Close()

	// Create daemon server with controllerURL pointing to mock
	s := &Server{
		config:        &Config{Version: "0.2.0"},
		controllerURL: controller.URL,
	}

	// Build mux with routes (the proxy must be wired)
	mux := s.buildControllerProxyMux()

	// Send request through daemon proxy
	req := httptest.NewRequest(http.MethodGet, "/api/v1/controller/update/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if receivedMethod != http.MethodGet {
		t.Errorf("expected GET forwarded, got %s", receivedMethod)
	}
	// Proxy strips /api/v1/controller prefix → controller sees /update/status
	if receivedPath != "/update/status" {
		t.Errorf("expected /update/status, controller received %s", receivedPath)
	}
}

func TestControllerProxy_ForwardsPostBody(t *testing.T) {
	var receivedMethod string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	}))
	defer controller.Close()

	s := &Server{
		config:        &Config{Version: "0.2.0"},
		controllerURL: controller.URL,
	}

	mux := s.buildControllerProxyMux()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/controller/update/apply", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST forwarded, got %s", receivedMethod)
	}
}

func TestControllerProxy_StripsCORSFromUpstream(t *testing.T) {
	// Controller sets its own CORS headers — the proxy must strip them so
	// the daemon's CORSMiddleware is the single source of truth. Without
	// stripping, browsers see duplicate Access-Control-Allow-Origin values.
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:3000")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "3600")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer controller.Close()

	s := &Server{
		config:        &Config{Version: "0.2.0"},
		controllerURL: controller.URL,
	}

	mux := s.buildControllerProxyMux()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/controller/update/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// All CORS headers from upstream controller must be stripped
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected Access-Control-Allow-Origin stripped, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("expected Access-Control-Allow-Methods stripped, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "" {
		t.Errorf("expected Access-Control-Allow-Headers stripped, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "" {
		t.Errorf("expected Access-Control-Max-Age stripped, got %q", got)
	}
	// Non-CORS headers from upstream should still pass through
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type preserved, got %q", got)
	}
}

func TestControllerProxy_Returns502_WhenControllerDown(t *testing.T) {
	s := &Server{
		config:        &Config{Version: "0.2.0"},
		controllerURL: "http://127.0.0.1:1", // nothing listening
	}

	mux := s.buildControllerProxyMux()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/controller/update/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when controller unreachable, got %d", w.Code)
	}
}
