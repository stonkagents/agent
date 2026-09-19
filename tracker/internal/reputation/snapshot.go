// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: ReputationSnapshot model for tracking score trends over time

package reputation

import "time"

// ReputationSnapshot captures a peer's composite score at a point in time.
// Used to compute trend (current - previous snapshot).
type ReputationSnapshot struct {
	PeerID         string    `json:"peer_id"`
	CompositeScore float64   `json:"composite_score"`
	SnappedAt      time.Time `json:"snapped_at"`
}
