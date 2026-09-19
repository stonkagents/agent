/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-02 (Trust/Block Interactions)
 * Purpose: RED test for trustBlockRepo wiring bug - trusted_network badge always locked
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

// TestProfileHandler_TrustedNetworkBadgeLocked_BUG demonstrates the bug where trusted_network badge
// is always locked because ProfileHandler hardcodes trustsReceived=0.
func TestProfileHandler_TrustedNetworkBadgeLocked_BUG(t *testing.T) {
	targetPeerID := "peer-target"

	// Mock peer with enough data to unlock other badges
	peer := &models.Peer{
		PeerID:             targetPeerID,
		FirstSeen:          time.Now().Add(-30 * 24 * time.Hour),
		LastSeen:           time.Now(),
		TotalUptimeSeconds: 3000000, // veteran badge
		TotalUploadBytes:   2 << 30, // 2 GiB - global_relay badge
		TotalDownloadBytes: 1 << 30,
	}

	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	actorKey, _ := apiKeyRepo.Create(context.Background(), targetPeerID)

	// BUG: ProfileHandler doesn't accept trustBlockRepo, so it always passes trustsReceived=0
	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{
			PeerID:         targetPeerID,
			CompositeScore: 0.65, // Gold rank
		}},
		&mockPeerFinder{peer: peer},
		&mockAssetSearcher{count: 12}, // community_hero badge (needs 10)
		&mockPresenceChecker{online: true},
		&mockTrustCounter{}, // Still passes count=0, demonstrating the bug
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
			Badges []reputation.BadgeEntry `json:"badges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Find trusted_network badge
	var trustedNetworkBadge *reputation.BadgeEntry
	for i := range resp.Data.Badges {
		if resp.Data.Badges[i].ID == "trusted_network" {
			trustedNetworkBadge = &resp.Data.Badges[i]
			break
		}
	}

	if trustedNetworkBadge == nil {
		t.Fatal("trusted_network badge not found in response")
	}

	// BUG: Badge is locked because ProfileHandler hardcodes trustsReceived=0
	// Even if the peer has 5+ trusts in reality, badge shows locked
	if trustedNetworkBadge.Status != "locked" {
		t.Errorf("Expected badge to be locked (demonstrating bug), got %s", trustedNetworkBadge.Status)
	}

	// Verify some other badges work correctly (to prove endpoint is functional)
	badgeMap := make(map[string]string)
	for _, b := range resp.Data.Badges {
		badgeMap[b.ID] = b.Status
	}

	// These should be earned based on our mock data (simpler badges that don't require trust)
	if status, ok := badgeMap["veteran"]; !ok || status != "earned" {
		t.Errorf("veteran badge status = %s, want earned (proves endpoint is functional)", status)
	}
	if status, ok := badgeMap["community_hero"]; !ok || status != "earned" {
		t.Errorf("community_hero badge status = %s, want earned (proves endpoint is functional)", status)
	}
}

// TestProfileHandler_TrustedNetworkBadgeUnlocked_WithRepo verifies that when trustBlockRepo is wired,
// the badge unlocks correctly.
func TestProfileHandler_TrustedNetworkBadgeUnlocked_WithRepo(t *testing.T) {
	targetPeerID := "peer-target"

	peer := &models.Peer{
		PeerID:             targetPeerID,
		FirstSeen:          time.Now().Add(-30 * 24 * time.Hour),
		LastSeen:           time.Now(),
		TotalUptimeSeconds: 3000000,
		TotalUploadBytes:   2 << 30,
		TotalDownloadBytes: 1 << 30,
	}

	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	actorKey, _ := apiKeyRepo.Create(context.Background(), targetPeerID)

	// FIX: ProfileHandler now accepts trustBlockRepo and counts trusts correctly
	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{
			PeerID:         targetPeerID,
			CompositeScore: 0.65,
		}},
		&mockPeerFinder{peer: peer},
		&mockAssetSearcher{count: 12},
		&mockPresenceChecker{online: true},
		&mockTrustCounter{count: 5}, // 5 trusts received = exactly at threshold
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
			Badges []reputation.BadgeEntry `json:"badges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Find trusted_network badge
	var trustedNetworkBadge *reputation.BadgeEntry
	for i := range resp.Data.Badges {
		if resp.Data.Badges[i].ID == "trusted_network" {
			trustedNetworkBadge = &resp.Data.Badges[i]
			break
		}
	}

	if trustedNetworkBadge == nil {
		t.Fatal("trusted_network badge not found in response")
	}

	// FIX: Badge is now earned because ProfileHandler correctly counts trusts
	if trustedNetworkBadge.Status != "earned" {
		t.Errorf("trusted_network badge status = %s, want earned (5 trusts >= threshold)", trustedNetworkBadge.Status)
	}
}

// TestProfileHandler_TrustedNetworkBadge_CountError verifies soft degradation when trust count fails.
func TestProfileHandler_TrustedNetworkBadge_CountError(t *testing.T) {
	targetPeerID := "peer-target"

	peer := &models.Peer{
		PeerID:             targetPeerID,
		FirstSeen:          time.Now().Add(-30 * 24 * time.Hour),
		LastSeen:           time.Now(),
		TotalUptimeSeconds: 3000000,
		TotalUploadBytes:   2 << 30,
		TotalDownloadBytes: 1 << 30,
	}

	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	actorKey, _ := apiKeyRepo.Create(context.Background(), targetPeerID)

	// Simulate trust counter error
	h := NewProfileHandler(
		&mockRepFinder{record: &reputation.ReputationRecord{
			PeerID:         targetPeerID,
			CompositeScore: 0.65,
		}},
		&mockPeerFinder{peer: peer},
		&mockAssetSearcher{count: 12},
		&mockPresenceChecker{online: true},
		&mockTrustCounter{err: context.DeadlineExceeded}, // Simulate error
	)

	req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	req = req.WithContext(WithForumPeerID(req.Context(), targetPeerID))
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	h.HandleGetMyProfile(w, req)

	// Profile endpoint should still return 200 (soft degrade)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 (soft degrade), got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Badges []reputation.BadgeEntry `json:"badges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Find trusted_network badge
	var trustedNetworkBadge *reputation.BadgeEntry
	for i := range resp.Data.Badges {
		if resp.Data.Badges[i].ID == "trusted_network" {
			trustedNetworkBadge = &resp.Data.Badges[i]
			break
		}
	}

	if trustedNetworkBadge == nil {
		t.Fatal("trusted_network badge not found in response")
	}

	// Badge should default to locked (safe fallback when count fails)
	if trustedNetworkBadge.Status != "locked" {
		t.Errorf("trusted_network badge status = %s, want locked (safe fallback on error)", trustedNetworkBadge.Status)
	}
}

// Helper to inject peer_id into context for testing
func WithForumPeerID(ctx context.Context, peerID string) context.Context {
	return context.WithValue(ctx, forumPeerIDKey, peerID)
}
