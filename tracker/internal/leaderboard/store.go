// Package leaderboard provides leaderboard storage abstraction (Postgres or Redis).
// Feature: Peer and Asset Analytics. Purpose: Top seeders/leechers with optional Redis cache.

package leaderboard

import (
	"context"
	"time"
)

// Entry is a single leaderboard row (seeders or leechers).
type Entry struct {
	Rank         int    `json:"rank"`
	MaskedPeerID string `json:"masked_peer_id"`
	// DisplayName is the owner-set agent name; empty (omitted) when unset.
	DisplayName             string `json:"display_name,omitempty"`
	Country                 string `json:"country,omitempty"`
	Region                  string `json:"region,omitempty"`
	TotalUploadBytes        int64  `json:"total_upload_bytes,omitempty"`
	TotalDownloadBytes      int64  `json:"total_download_bytes,omitempty"`
	AverageSpeedBytesPerSec *int64 `json:"average_speed_bytes_per_sec,omitempty"`
	FirstSeen               string `json:"first_seen,omitempty"`
	LastSeen                string `json:"last_seen,omitempty"`
}

// Store defines leaderboard read and optional write (for cache updates).
type Store interface {
	TopSeeders(ctx context.Context, limit, offset int) ([]Entry, error)
	TopLeechers(ctx context.Context, limit, offset int) ([]Entry, error)
	// UpdateTransferStats notifies the store of new upload/download bytes (e.g. for Redis cache).
	// No-op if the implementation does not use a cache.
	UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64) error
}

// Clock is used for formatting timestamps in entries.
type Clock interface {
	Now() time.Time
}
