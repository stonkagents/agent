// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: Shared badge evaluation — extracted from F-026 handler_profile.go, extended with veteran + trusted_network

package reputation

import (
	"math"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// Badge thresholds — shared constants for evaluation.
const (
	EarlyAdopterDays        = 90
	OGDays                  = 7
	TopSeederThreshold      = 0.8
	TrustedNodeThreshold    = 0.6
	GlobalRelayBytes        = 1 << 30 // 1 GiB
	CommunityHeroDrops      = 10
	VeteranUptimeSeconds    = 30 * 24 * 60 * 60 // 2,592,000 = 30 days
	TrustedNetworkThreshold = 5
)

// BadgeEntry represents a single badge with its evaluation status.
type BadgeEntry struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "earned", "rare", or "locked"
}

// EvaluateBadges computes the status of all 10 badges for a peer.
// Parameters:
//   - peer: the peer's data (FirstSeen, TotalUploadBytes, TotalUptimeSeconds)
//   - composite: the peer's EigenTrust composite score (0-1)
//   - drops: number of assets the peer has shared (non-quarantined)
//   - trustsReceived: how many distinct peers have trusted this peer
//   - launchDate: the network launch date (for early_adopter / og_status)
func EvaluateBadges(peer *models.Peer, composite float64, drops int, trustsReceived int, launchDate time.Time) []BadgeEntry {
	status := func(condition bool) string {
		if condition {
			return "earned"
		}
		return "locked"
	}

	daysSinceLaunch := peer.FirstSeen.Sub(launchDate).Hours() / 24

	return []BadgeEntry{
		{ID: "early_adopter", Status: status(daysSinceLaunch < EarlyAdopterDays)},
		{ID: "first_drop", Status: status(drops >= 1)},
		{ID: "swarm_joiner", Status: status(peer.TotalUploadBytes > 0)},
		{ID: "top_seeder", Status: func() string {
			if composite >= TopSeederThreshold {
				return "rare"
			}
			return "locked"
		}()},
		{ID: "trusted_node", Status: status(composite >= TrustedNodeThreshold)},
		{ID: "og_status", Status: status(math.Abs(daysSinceLaunch) <= OGDays)},
		{ID: "global_relay", Status: status(peer.TotalUploadBytes > GlobalRelayBytes)},
		{ID: "community_hero", Status: status(drops >= CommunityHeroDrops)},
		{ID: "veteran", Status: status(peer.TotalUptimeSeconds >= VeteranUptimeSeconds)},
		{ID: "trusted_network", Status: status(trustsReceived >= TrustedNetworkThreshold)},
	}
}

// TierToWeeklyBonus returns the weekly credit bonus for a reputation tier.
func TierToWeeklyBonus(tier string) int {
	switch tier {
	case "silver":
		return 20
	case "gold":
		return 50
	case "og":
		return 75
	default:
		return 0
	}
}
