/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-01 (Peer Data Foundation)
 * Purpose: Integration tests verifying portal handler repo wiring in main.go
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
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// TestPortalHandler_ActivityEndpointWiring verifies peerEventRepo is wired (not nil).
func TestPortalHandler_ActivityEndpointWiring(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	// Seed a peer
	peer := &models.Peer{
		PeerID:   "test-peer-activity",
		LastSeen: time.Now().UTC(),
	}
	seedPeer(t, env.peerRepo, env.store, peer)

	// Insert event via repo
	event := &models.PeerEvent{
		ID:        "event-1",
		PeerID:    peer.PeerID,
		Action:    "shared",
		Details:   "test-file.txt",
		CreatedAt: time.Now().UTC(),
	}
	if err := env.peerEventRepo.Insert(ctx, event); err != nil {
		t.Fatalf("Failed to insert event: %v", err)
	}

	// Call activity endpoint
	req := httptest.NewRequest(http.MethodGet, "/api/peers/"+peer.PeerID+"/activity", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []map[string]interface{} `json:"data"`
		Meta map[string]interface{}   `json:"meta"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(resp.Data) == 0 {
		t.Errorf("Expected non-empty activity, got empty array. peerEventRepo likely nil.")
	}

	if resp.Data[0]["action"] != "shared" {
		t.Errorf("Expected action=shared, got %v", resp.Data[0]["action"])
	}
}

// TestPortalHandler_ReputationTrendWiring verifies snapshotRepo is wired (not nil).
func TestPortalHandler_ReputationTrendWiring(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	// Seed a peer
	peer := &models.Peer{
		PeerID:   "test-peer-trend",
		LastSeen: time.Now().UTC(),
	}
	seedPeer(t, env.peerRepo, env.store, peer)

	// Insert snapshot via repo
	now := time.Now().UTC()
	snapshot := &reputation.ReputationSnapshot{
		PeerID:         peer.PeerID,
		CompositeScore: 0.50,
		SnappedAt:      now.Add(-24 * time.Hour),
	}
	if err := env.snapshotRepo.Upsert(ctx, snapshot); err != nil {
		t.Fatalf("Failed to insert snapshot: %v", err)
	}

	// Set current reputation higher
	if err := env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         peer.PeerID,
		CompositeScore: 0.65,
		BandwidthScore: 0.70,
		QualityScore:   0.60,
		SecurityScore:  0.50,
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Failed to set reputation: %v", err)
	}

	// Call reputation endpoint
	req := httptest.NewRequest(http.MethodGet, "/api/peers/"+peer.PeerID+"/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Trend *float64 `json:"trend"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.Data.Trend == nil {
		t.Errorf("Expected trend to be computed, got nil. snapshotRepo likely nil.")
	} else {
		expected := 0.15
		delta := 0.001
		got := *resp.Data.Trend
		if got < expected-delta || got > expected+delta {
			t.Errorf("Expected trend=%.4f, got %.4f", expected, got)
		}
	}
}
