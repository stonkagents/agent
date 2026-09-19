/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-01 (Peer Data Foundation)
 * Purpose: RED tests for ADR-001 envelope compliance on trusted/blocked lists
 */

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// TestPortalTrustedList_ADR001Envelope verifies trusted list returns { data: [], meta: { total } }.
func TestPortalTrustedList_ADR001Envelope(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	_ = env.trustBlockRepo.Trust(ctx, "actor-peer", "peer-a")
	_ = env.trustBlockRepo.Trust(ctx, "actor-peer", "peer-b")
	_ = env.trustBlockRepo.Trust(ctx, "actor-peer", "peer-c")

	req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []string `json:"data"` // ADR-001: direct array
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Meta.Total != 3 {
		t.Errorf("meta.total = %d, want 3", resp.Meta.Total)
	}

	if len(resp.Data) != 3 {
		t.Errorf("data length = %d, want 3", len(resp.Data))
	}

	// Verify peer IDs are returned
	found := make(map[string]bool)
	for _, id := range resp.Data {
		found[id] = true
	}
	for _, expected := range []string{"peer-a", "peer-b", "peer-c"} {
		if !found[expected] {
			t.Errorf("expected peer %s in data, not found", expected)
		}
	}
}

// TestPortalBlockedList_ADR001Envelope verifies blocked list returns { data: [], meta: { total } }.
func TestPortalBlockedList_ADR001Envelope(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	_ = env.trustBlockRepo.Block(ctx, "actor-peer", "peer-x")
	_ = env.trustBlockRepo.Block(ctx, "actor-peer", "peer-y")

	req := httptest.NewRequest(http.MethodGet, "/api/peers/blocked", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []string `json:"data"` // ADR-001: direct array
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Meta.Total != 2 {
		t.Errorf("meta.total = %d, want 2", resp.Meta.Total)
	}

	if len(resp.Data) != 2 {
		t.Errorf("data length = %d, want 2", len(resp.Data))
	}

	// Verify peer IDs are returned
	found := make(map[string]bool)
	for _, id := range resp.Data {
		found[id] = true
	}
	for _, expected := range []string{"peer-x", "peer-y"} {
		if !found[expected] {
			t.Errorf("expected peer %s in data, not found", expected)
		}
	}
}

// TestPortalTrustedList_EmptyADR001 verifies empty list returns { data: [], meta: { total: 0 } }.
func TestPortalTrustedList_EmptyADR001(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data []string `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Meta.Total != 0 {
		t.Errorf("meta.total = %d, want 0", resp.Meta.Total)
	}

	if len(resp.Data) != 0 {
		t.Errorf("data length = %d, want 0 (empty array)", len(resp.Data))
	}
}
