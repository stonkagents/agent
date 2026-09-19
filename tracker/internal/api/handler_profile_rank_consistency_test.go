/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-02 (Rank & Badge Display)
 * Purpose: RED test for rank inconsistency between Profile and F-032 endpoints
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
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// TestProfileHandler_RankConsistency_Bootstrap verifies ProfileHandler uses the same rank logic
// as F-032 portal endpoints (with bootstrap rule: all zeros = "new" rank).
//
// BUG: ProfileHandler.deriveRank() doesn't include bootstrap rule, so a peer with
// composite=0.25 but zero activity shows "bronze" instead of "new".
func TestProfileHandler_RankConsistency_Bootstrap(t *testing.T) {
	targetPeerID := "peer-bootstrap-test"

	// Peer with composite score >= bronze threshold BUT zero actual activity
	peer := &models.Peer{
		PeerID:             targetPeerID,
		FirstSeen:          time.Now().Add(-24 * time.Hour),
		LastSeen:           time.Now(),
		TotalUptimeSeconds: 0,
		TotalUploadBytes:   0, // Zero upload
		TotalDownloadBytes: 0, // Zero download
	}

	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	actorKey, _ := apiKeyRepo.Create(context.Background(), targetPeerID)

	// Mock composite=0.25 (bronze tier by score alone)
	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{
			PeerID:         targetPeerID,
			CompositeScore: 0.25, // >= 0.2 = bronze threshold
		}},
		&mockPeerFinder{peer: peer},
		&mockAssetSearcher{count: 0}, // Zero shared files
		&mockPresenceChecker{online: true},
		&mockTrustCounter{},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req = req.WithContext(WithForumPeerID(req.Context(), targetPeerID))
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	h.HandleGetMyProfile(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Rank string `json:"rank"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// BUG: ProfileHandler returns "bronze" because it only looks at composite score
	// Expected: "new" (bootstrap rule: zero activity = new regardless of composite)
	if resp.Data.Rank != "new" {
		t.Errorf("Rank = %s, want 'new' (bootstrap rule). ProfileHandler doesn't match reputation.DeriveRank() behavior.", resp.Data.Rank)
	}

	// Verify the CORRECT behavior from reputation package
	correctRank := reputation.DeriveRank(0.25, 0, 0, 0)
	if correctRank != "new" {
		t.Fatalf("reputation.DeriveRank() = %s, want 'new' (sanity check of expected behavior)", correctRank)
	}
}
