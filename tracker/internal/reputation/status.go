// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: Derives peer online status from activity timestamps

package reputation

import "time"

// StatusActivityWindow is the time window for seeding/leeching detection.
const StatusActivityWindow = 5 * time.Minute

// Peer status constants.
const (
	StatusOffline  = "offline"
	StatusSeeding  = "seeding"
	StatusLeeching = "leeching"
	StatusOnline   = "online"
)

// DeriveStatus computes a peer's status from their activity timestamps.
// Priority: offline > seeding > leeching > online.
func DeriveStatus(lastSeen time.Time, lastUploadAt, lastDownloadAt *time.Time, now time.Time) string {
	if now.Sub(lastSeen) > StatusActivityWindow {
		return StatusOffline
	}
	if lastUploadAt != nil && now.Sub(*lastUploadAt) <= StatusActivityWindow {
		return StatusSeeding
	}
	if lastDownloadAt != nil && now.Sub(*lastDownloadAt) <= StatusActivityWindow {
		return StatusLeeching
	}
	return StatusOnline
}
