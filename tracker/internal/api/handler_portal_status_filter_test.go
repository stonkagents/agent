/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-02 (Rank & Badge Display)
 * Purpose: RED test for ?status= parameter filtering on GET /api/peers
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

// TestPortalPeers_StatusFilter_Online verifies ?status=online filters to only online peers.
func TestPortalPeers_StatusFilter_Online(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	now := time.Now() // Use real time since handler uses time.Now(), not clock

	// Peer 1: Online (last seen within 5 min, no upload/download)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "peer-online",
		LastSeen: now.Add(-2 * time.Minute),
	})

	// Peer 2: Offline (last seen 10 min ago)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "peer-offline",
		LastSeen: now.Add(-10 * time.Minute),
	})

	// Peer 3: Seeding (last seen recent + last upload recent)
	uploadTime := now.Add(-30 * time.Second)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:       "peer-seeding",
		LastSeen:     now.Add(-1 * time.Minute),
		LastUploadAt: &uploadTime,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?status=online", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp PaginatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// BUG: Current code doesn't filter by status, so returns all 3 peers
	// Expected: Only peers with status="online" (peer-online only, NOT peer-seeding)
	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 1 {
		t.Errorf("Expected 1 online peer, got %d", len(dataArr))
	}

	if len(dataArr) > 0 {
		peer := dataArr[0].(map[string]interface{})
		if peer["status"].(string) != "online" {
			t.Errorf("Expected status=online, got %s", peer["status"].(string))
		}
	}

	if resp.Meta.Total != 1 {
		t.Errorf("Expected total=1 (filtered), got %d", resp.Meta.Total)
	}
}

// TestPortalPeers_StatusFilter_Seeding verifies ?status=seeding filters to only seeding peers.
func TestPortalPeers_StatusFilter_Seeding(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	now := time.Now() // Use real time since handler uses time.Now(), not clock

	// Peer 1: Online only (no upload/download activity)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "peer-online",
		LastSeen: now.Add(-2 * time.Minute),
	})

	// Peer 2: Seeding (recent upload, no download)
	uploadTime := now.Add(-30 * time.Second)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:       "peer-seeding",
		LastSeen:     now.Add(-1 * time.Minute),
		LastUploadAt: &uploadTime,
	})

	// Peer 3: Leeching (recent download, no upload)
	downloadTime := now.Add(-30 * time.Second)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:         "peer-leeching",
		LastSeen:       now.Add(-1 * time.Minute),
		LastDownloadAt: &downloadTime,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?status=seeding", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp PaginatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Expected: Only peer-seeding
	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 1 {
		t.Errorf("Expected 1 seeding peer, got %d", len(dataArr))
	}

	if len(dataArr) > 0 {
		peer := dataArr[0].(map[string]interface{})
		peerID, _ := peer["peerId"].(string) // camelCase, not snake_case
		status, _ := peer["status"].(string)
		if peerID != "peer-seeding" {
			t.Errorf("Expected peer-seeding, got %s", peerID)
		}
		if status != "seeding" {
			t.Errorf("Expected status=seeding, got %s", status)
		}
	}
}

// TestPortalPeers_StatusFilter_Combined verifies ?status= works with ?online=true.
func TestPortalPeers_StatusFilter_Combined(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	now := time.Now() // Use real time since handler uses time.Now(), not clock

	// Peer 1: Online + seeding (last seen recent + upload recent)
	uploadTime1 := now.Add(-30 * time.Second)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:       "peer-online-seeding",
		LastSeen:     now.Add(-1 * time.Minute),
		LastUploadAt: &uploadTime1,
	})

	// Peer 2: Offline + seeding (last seen 10 min ago, upload timestamp doesn't matter for online check)
	uploadTime2 := now.Add(-30 * time.Second)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:       "peer-offline-seeding",
		LastSeen:     now.Add(-10 * time.Minute),
		LastUploadAt: &uploadTime2,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?online=true&status=seeding", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp PaginatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Expected: Only peer-online-seeding (online AND seeding)
	// peer-offline-seeding is filtered out by ?online=true before status filter runs
	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 1 {
		t.Errorf("Expected 1 peer (online + seeding), got %d", len(dataArr))
	}

	if len(dataArr) > 0 {
		peer := dataArr[0].(map[string]interface{})
		peerID, _ := peer["peerId"].(string) // camelCase, not snake_case
		if peerID != "peer-online-seeding" {
			t.Errorf("Expected peer-online-seeding, got %s", peerID)
		}
	}
}

// TestPortalPeers_StatusFilter_InvalidValue verifies invalid status values are ignored (no filter).
func TestPortalPeers_StatusFilter_InvalidValue(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	now := time.Now() // Use real time since handler uses time.Now(), not clock

	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "peer-1",
		LastSeen: now.Add(-2 * time.Minute),
	})
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "peer-2",
		LastSeen: now.Add(-1 * time.Minute),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?status=invalid", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp PaginatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Invalid status value is ignored, so returns all peers (no filtering)
	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 2 {
		t.Errorf("Expected 2 peers (invalid status ignored), got %d", len(dataArr))
	}
}
