// Package: tracker/internal/api
// Feature: F-026 (Profile Endpoint)
// Story: US-026-01 (Profile Aggregation)
// Purpose: TDD tests for pure helpers — computeTopPercent, badges evaluation
//
// NOTE: Rank logic is now tested in reputation/rank_test.go.
// ProfileHandler delegates to reputation.DeriveRank() for consistency with F-032 endpoints.

package api

import (
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// fixedNow is a deterministic timestamp for all tests (testing-backend: no time.Now()).
// Shared across handler_profile_test.go and handler_profile_build_test.go via package scope.
var fixedNow = time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)

func TestComputeTopPercent(t *testing.T) {
	// 5 peers, peer at 0.6: above=2, total=5 -> 40
	got := computeTopPercent(2, 5)
	if got != 40 {
		t.Errorf("computeTopPercent(2, 5) = %d, want 40", got)
	}
}

func TestComputeTopPercent_SinglePeer(t *testing.T) {
	// above=0, total=1 -> 1 (top of the pack)
	got := computeTopPercent(0, 1)
	if got != 1 {
		t.Errorf("computeTopPercent(0, 1) = %d, want 1", got)
	}
}

func TestComputeTopPercent_Ties(t *testing.T) {
	// above=0, total=5 (all tied) -> top_percent 1
	got := computeTopPercent(0, 5)
	if got != 1 {
		t.Errorf("computeTopPercent(0, 5) = %d, want 1", got)
	}
}

func TestComputeTopPercent_NoPeers(t *testing.T) {
	// total=0 -> 99 (no peers scored yet)
	got := computeTopPercent(0, 0)
	if got != 99 {
		t.Errorf("computeTopPercent(0, 0) = %d, want 99", got)
	}
}

func TestComputeTopPercent_AllAbove(t *testing.T) {
	// above=total -> clamped to 99
	got := computeTopPercent(10, 10)
	if got != 99 {
		t.Errorf("computeTopPercent(10, 10) = %d, want 99", got)
	}
}

func TestEvaluateBadges_EarlyAdopter(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	// firstSeen = LaunchDate+30d -> earned (within reputation.EarlyAdopterDays)
	peer := &models.Peer{FirstSeen: LaunchDate.Add(30 * 24 * time.Hour)}
	badges := reputation.EvaluateBadges(peer, 0.0, 0, 0, LaunchDate)
	found := findBadge(badges, "early_adopter")
	if found.Status != "earned" {
		t.Errorf("early_adopter: got %q, want earned (firstSeen within %dd)", found.Status, reputation.EarlyAdopterDays)
	}

	// firstSeen = LaunchDate+90d exactly -> locked (spec: strictly < 90d)
	peer.FirstSeen = LaunchDate.Add(90 * 24 * time.Hour)
	badges = reputation.EvaluateBadges(peer, 0.0, 0, 0, LaunchDate)
	found = findBadge(badges, "early_adopter")
	if found.Status != "locked" {
		t.Errorf("early_adopter at exactly 90d: got %q, want locked (spec: < 90d, not <=)", found.Status)
	}

	// firstSeen = LaunchDate+120d -> locked (well beyond reputation.EarlyAdopterDays)
	peer.FirstSeen = LaunchDate.Add(120 * 24 * time.Hour)
	badges = reputation.EvaluateBadges(peer, 0.0, 0, 0, LaunchDate)
	found = findBadge(badges, "early_adopter")
	if found.Status != "locked" {
		t.Errorf("early_adopter: got %q, want locked (firstSeen > %dd)", found.Status, reputation.EarlyAdopterDays)
	}
}

func TestEvaluateBadges_TopSeeder(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	// Use fixedNow (within reputation.EarlyAdopterDays of LaunchDate) so badge logic is deterministic
	peer := &models.Peer{FirstSeen: fixedNow}
	// 0.85 >= topSeederThreshold -> rare
	badges := reputation.EvaluateBadges(peer, 0.85, 0, 0, LaunchDate)
	found := findBadge(badges, "top_seeder")
	if found.Status != "rare" {
		t.Errorf("top_seeder at 0.85: got %q, want rare", found.Status)
	}
	// 0.5 < topSeederThreshold -> locked
	badges = reputation.EvaluateBadges(peer, 0.5, 0, 0, LaunchDate)
	found = findBadge(badges, "top_seeder")
	if found.Status != "locked" {
		t.Errorf("top_seeder at 0.5: got %q, want locked", found.Status)
	}
}

func TestEvaluateBadges_OGStatus(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	// LaunchDate+3d -> earned
	peer := &models.Peer{FirstSeen: LaunchDate.Add(3 * 24 * time.Hour)}
	badges := reputation.EvaluateBadges(peer, 0.0, 0, 0, LaunchDate)
	found := findBadge(badges, "og_status")
	if found.Status != "earned" {
		t.Errorf("og_status +3d: got %q, want earned", found.Status)
	}
	// LaunchDate+30d -> locked
	peer.FirstSeen = LaunchDate.Add(30 * 24 * time.Hour)
	badges = reputation.EvaluateBadges(peer, 0.0, 0, 0, LaunchDate)
	found = findBadge(badges, "og_status")
	if found.Status != "locked" {
		t.Errorf("og_status +30d: got %q, want locked", found.Status)
	}
}

func TestEvaluateBadges_AllEarned(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	peer := &models.Peer{
		FirstSeen:          LaunchDate.Add(1 * 24 * time.Hour),  // within ogDays AND reputation.EarlyAdopterDays
		TotalUploadBytes:   2 * reputation.GlobalRelayBytes,     // > reputation.GlobalRelayBytes (1 GiB)
		TotalUptimeSeconds: reputation.VeteranUptimeSeconds + 1, // > VeteranUptimeSeconds (30 days)
	}
	trustsReceived := reputation.TrustedNetworkThreshold + 1 // > TrustedNetworkThreshold (5)
	badges := reputation.EvaluateBadges(peer, 0.85, reputation.CommunityHeroDrops+5, trustsReceived, LaunchDate)

	earned := 0
	rare := 0
	for _, b := range badges {
		switch b.Status {
		case "earned":
			earned++
		case "rare":
			rare++
		}
	}
	if len(badges) != 10 {
		t.Errorf("expected 10 badges, got %d", len(badges))
	}
	// top_seeder is "rare", rest are "earned"
	if rare != 1 {
		t.Errorf("expected 1 rare badge (top_seeder), got %d", rare)
	}
	if earned != 9 {
		t.Errorf("expected 9 earned badges, got %d", earned)
	}
}

// findBadge is a test helper to find a badge by ID.
func findBadge(badges []BadgeDTO, id string) BadgeDTO {
	for _, b := range badges {
		if b.ID == id {
			return b
		}
	}
	return BadgeDTO{ID: id, Status: "NOT_FOUND"}
}
