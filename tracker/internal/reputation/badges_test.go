// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: Tests for shared badge evaluation function (extracted from F-026 handler_profile.go, extended with 2 new badges)

package reputation

import (
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// testLaunchDate is a fixed launch date for deterministic badge tests.
var testLaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

func TestEvaluateBadges_AllEarned(t *testing.T) {
	peer := &models.Peer{
		FirstSeen:          testLaunchDate.Add(24 * time.Hour), // 1 day after launch
		TotalUploadBytes:   2 << 30,                            // 2 GiB
		TotalUptimeSeconds: 3_000_000,                          // > 30 days
	}
	composite := 0.85 // above top_seeder threshold
	drops := 15       // above community_hero threshold
	trustsReceived := 7

	badges := EvaluateBadges(peer, composite, drops, trustsReceived, testLaunchDate)

	if len(badges) != 10 {
		t.Fatalf("badge count = %d, want 10", len(badges))
	}

	expected := map[string]string{
		"early_adopter":   "earned",
		"first_drop":      "earned",
		"swarm_joiner":    "earned",
		"top_seeder":      "rare",
		"trusted_node":    "earned",
		"og_status":       "earned",
		"global_relay":    "earned",
		"community_hero":  "earned",
		"veteran":         "earned",
		"trusted_network": "earned",
	}

	for _, b := range badges {
		want, ok := expected[b.ID]
		if !ok {
			t.Errorf("unexpected badge %q", b.ID)
			continue
		}
		if b.Status != want {
			t.Errorf("badge %q: status = %q, want %q", b.ID, b.Status, want)
		}
	}
}

func TestEvaluateBadges_AllLocked(t *testing.T) {
	peer := &models.Peer{
		FirstSeen:          testLaunchDate.Add(365 * 24 * time.Hour), // 1 year after launch
		TotalUploadBytes:   0,
		TotalUptimeSeconds: 0,
	}
	composite := 0.1
	drops := 0
	trustsReceived := 0

	badges := EvaluateBadges(peer, composite, drops, trustsReceived, testLaunchDate)

	for _, b := range badges {
		if b.Status != "locked" {
			t.Errorf("badge %q: status = %q, want %q (all should be locked)", b.ID, b.Status, "locked")
		}
	}
}

func TestEvaluateBadges_VeteranThreshold(t *testing.T) {
	thirtyDays := int64(30 * 24 * 60 * 60) // 2,592,000 seconds

	tests := []struct {
		name   string
		uptime int64
		want   string
	}{
		{"exactly 30 days", thirtyDays, "earned"},
		{"29 days", thirtyDays - 86400, "locked"},
		{"31 days", thirtyDays + 86400, "earned"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := &models.Peer{
				FirstSeen:          testLaunchDate.Add(365 * 24 * time.Hour),
				TotalUptimeSeconds: tt.uptime,
			}
			badges := EvaluateBadges(peer, 0, 0, 0, testLaunchDate)
			for _, b := range badges {
				if b.ID == "veteran" && b.Status != tt.want {
					t.Errorf("veteran status = %q, want %q", b.Status, tt.want)
				}
			}
		})
	}
}

func TestEvaluateBadges_TrustedNetworkThreshold(t *testing.T) {
	tests := []struct {
		name   string
		trusts int
		want   string
	}{
		{"5 trusts (threshold)", 5, "earned"},
		{"4 trusts (below)", 4, "locked"},
		{"10 trusts (above)", 10, "earned"},
		{"0 trusts", 0, "locked"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := &models.Peer{
				FirstSeen: testLaunchDate.Add(365 * 24 * time.Hour),
			}
			badges := EvaluateBadges(peer, 0, 0, tt.trusts, testLaunchDate)
			for _, b := range badges {
				if b.ID == "trusted_network" && b.Status != tt.want {
					t.Errorf("trusted_network status = %q, want %q", b.Status, tt.want)
				}
			}
		})
	}
}

func TestTierToWeeklyBonus(t *testing.T) {
	tests := []struct {
		tier string
		want int
	}{
		{"new", 0},
		{"bronze", 0},
		{"silver", 20},
		{"gold", 50},
		{"og", 75},
		{"unknown", 0},
	}

	for _, tt := range tests {
		t.Run(tt.tier, func(t *testing.T) {
			got := TierToWeeklyBonus(tt.tier)
			if got != tt.want {
				t.Errorf("TierToWeeklyBonus(%q) = %d, want %d", tt.tier, got, tt.want)
			}
		})
	}
}
