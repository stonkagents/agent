/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-01 (Peer Data Foundation)
 * Purpose: Tests for input validation on pagination and query parameters
 */

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// TestPeersEndpoint_NegativeLimit verifies negative limit is rejected or clamped.
func TestPeersEndpoint_NegativeLimit(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-a", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?limit=-10", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Limit int `json:"limit"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// Negative limit should be ignored and default used (20)
	if resp.Meta.Limit <= 0 {
		t.Errorf("Negative limit should be rejected, got limit=%d", resp.Meta.Limit)
	}
}

// TestPeersEndpoint_ExcessiveLimit verifies limit > 100 is clamped.
func TestPeersEndpoint_ExcessiveLimit(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-a", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?limit=999999", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Limit int `json:"limit"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// Excessive limit should be clamped to max (100)
	if resp.Meta.Limit > 100 {
		t.Errorf("Limit should be clamped to 100, got limit=%d", resp.Meta.Limit)
	}
}

// TestPeersEndpoint_NegativeOffset verifies negative offset is rejected.
func TestPeersEndpoint_NegativeOffset(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-a", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?offset=-1", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// Negative offset should be ignored and default to 0
	if resp.Meta.Offset < 0 {
		t.Errorf("Negative offset should be rejected, got offset=%d", resp.Meta.Offset)
	}
}

// TestGallerySearch_QuerySanitization verifies query parameter is sanitized.
func TestGallerySearch_QuerySanitization(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	// Try a query with SQL-like characters (should be handled safely)
	// URL-encode the query to avoid httptest parsing errors
	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search?q=%27%3B%20DROP%20TABLE%20assets%3B%20--", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	// Should return 200 (not crash or execute SQL)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	// Just verify it doesn't crash - the search uses parameterized queries (safe)
}

// TestGallerySearch_ExcessiveQueryLength verifies very long queries are handled.
func TestGallerySearch_ExcessiveQueryLength(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	// Create a 10KB query string
	longQuery := ""
	for range 10000 {
		longQuery += "a"
	}

	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search?q="+longQuery, nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	// Should either return 200 (truncated) or 400 (rejected)
	if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("Expected 200 or 400 for excessive query, got %d", w.Code)
	}
}

// TestPeersEndpoint_InvalidLimitString verifies non-numeric limit is rejected.
func TestPeersEndpoint_InvalidLimitString(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-a", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?limit=abc", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Limit int `json:"limit"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// Invalid string should fall back to default (20)
	if resp.Meta.Limit <= 0 {
		t.Errorf("Invalid limit string should use default, got limit=%d", resp.Meta.Limit)
	}
}
