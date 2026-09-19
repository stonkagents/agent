// Package: tracker/internal/presence
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: PresenceStore interface for peer online/offline tracking

package presence

import (
	"context"
	"time"
)

// PresenceStore tracks which peers are currently online via heartbeats.
type PresenceStore interface {
	Heartbeat(ctx context.Context, peerID string, ttl time.Duration) error
	IsOnline(ctx context.Context, peerID string) (bool, error)
	OnlinePeerIDs(ctx context.Context) ([]string, error)
	Remove(ctx context.Context, peerID string) error
}
