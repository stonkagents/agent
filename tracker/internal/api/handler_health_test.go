// Package: api
// Feature: F-014 (Auto-Update)
// Story: F-014 Phase 0 (Version identity)
// Purpose: Tests for GET /health version reporting

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoint_ReportsVersion(t *testing.T) {
	// Build a minimal server with a known version
	deps := ServerDeps{
		Address: ":0",
		Version: "1.2.3-test",
	}
	// We can't call NewServer without handler deps being set, so test the handler directly.
	s := &Server{version: "1.2.3-test"}

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	s.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if got := resp["version"]; got != "1.2.3-test" {
		t.Errorf("version = %q, want %q", got, "1.2.3-test")
	}
	_ = deps // used only to document the intended ServerDeps field
}

func TestHealthEndpoint_DefaultVersion(t *testing.T) {
	s := &Server{version: "dev"}

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	s.handleHealth(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if got := resp["version"]; got != "dev" {
		t.Errorf("version = %q, want %q", got, "dev")
	}
}
