/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-01 (Peer Data Foundation)
 * Purpose: RED test for pagination bug - onlineOnly filter doesn't affect meta.total
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

// TestPeersEndpoint_OnlineOnly_CorrectTotal verifies meta.total reflects filtered count when onlineOnly=true.
func TestPeersEndpoint_OnlineOnly_CorrectTotal(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	// Seed 5 peers: 2 online (heartbeated), 3 offline (not heartbeated)
	now := time.Now()

	// Online peers (seedPeer heartbeats them)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-online-1", LastSeen: now})
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-online-2", LastSeen: now})

	// Offline peers (insert to repo only, don't heartbeat)
	for _, id := range []string{"peer-offline-1", "peer-offline-2", "peer-offline-3"} {
		_ = env.peerRepo.Upsert(ctx, &models.Peer{PeerID: id, LastSeen: now.Add(-10 * time.Minute)})
	}

	// Request with onlineOnly=true
	req := httptest.NewRequest(http.MethodGet, "/api/peers?online=true", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// BUG: meta.total returns 5 (all peers) instead of 2 (online peers)
	if resp.Meta.Total != 2 {
		t.Errorf("meta.total = %d, want 2 (only online peers). Bug: Count() doesn't account for onlineOnly filter.", resp.Meta.Total)
	}

	if len(resp.Data) != 2 {
		t.Errorf("data length = %d, want 2 (only online peers)", len(resp.Data))
	}
}
