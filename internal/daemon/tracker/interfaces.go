// Package: internal/daemon/tracker
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: Tracker client interfaces for peer discovery and announcements

package tracker

import (
	"context"
	"time"
)

// TrackerClient is the interface for communicating with the centralized tracker.
// Upload managers use this to announce shared files and discover peers for downloads.
type TrackerClient interface {
	// Register registers this daemon with the tracker on startup.
	// Sends peer ID, multiaddrs, and Ed25519 public key.
	// Returns an error if registration fails.
	Register(ctx context.Context) error

	// Announce announces that this peer has a complete file or chunks available.
	// The tracker records this peer as a seeder for the given CID.
	// Returns an error if the announcement fails.
	Announce(ctx context.Context, req *AnnounceRequest) (*AnnounceResponse, error)

	// Search searches for peers that have a specific file CID.
	// Returns a list of peer records with their multiaddrs and reputation.
	// Returns empty list if no peers are found.
	Search(ctx context.Context, cid string) ([]*PeerInfo, error)

	// Heartbeat sends periodic heartbeat to tracker (every 2 minutes).
	// Updates last_seen timestamp and uptime statistics.
	// Returns an error if the heartbeat fails.
	Heartbeat(ctx context.Context) error

	// Unregister removes this peer from the tracker (called on daemon shutdown).
	Unregister(ctx context.Context) error

	// GetTrackerURL returns the configured tracker URL.
	GetTrackerURL() string

	// SetRetryPolicy configures retry behavior (max retries, backoff delay).
	SetRetryPolicy(maxRetries int, baseDelay time.Duration)

	// Close shuts down the tracker client gracefully.
	Close() error
}

// AnnounceRequest contains information about what this peer is sharing.
type AnnounceRequest struct {
	// CID is the content identifier being announced
	CID string
	// Filename is the original filename (optional, for search results)
	Filename string
	// ManifestType is the asset type (e.g., "raw", "vector", "model")
	ManifestType string
	// PeerID is this peer's libp2p identifier
	PeerID string
	// TotalSize is the complete file size in bytes
	TotalSize int64
	// TotalChunks is the number of 256KB chunks available
	TotalChunks int
	// Multiaddrs are this peer's libp2p addresses for connection
	Multiaddrs []string
}

// AnnounceResponse is the tracker's response to an announcement.
type AnnounceResponse struct {
	// Success indicates if the announcement was recorded
	Success bool
	// Message is a human-readable status message
	Message string
	// Interval is the recommended time until next announcement (seconds)
	Interval int
	// Seeders is the number of peers with complete file
	Seeders int
	// Leechers is the number of peers downloading
	Leechers int
}

// PeerInfo contains information about a peer discovered via the tracker.
type PeerInfo struct {
	// PeerID is the libp2p peer identifier
	PeerID string
	// Multiaddrs are the peer's connection addresses
	Multiaddrs []string
	// Reputation is the peer's upload/download ratio score (0-100)
	Reputation float64
	// LastSeen is the timestamp of the peer's last heartbeat
	LastSeen time.Time
	// UptimeSeconds is the peer's total uptime
	UptimeSeconds int64
}

// TrackerStats provides statistics from the tracker.
type TrackerStats struct {
	// TotalPeers is the number of registered peers
	TotalPeers int
	// TotalAssets is the number of unique CIDs tracked
	TotalAssets int
	// ActivePeers is the number of peers seen in last 5 minutes
	ActivePeers int
}

// TrackerConfig holds tracker client configuration.
type TrackerConfig struct {
	// URL is the tracker HTTP endpoint (e.g., "http://localhost:7842")
	URL string
	// AnnounceRetryMax is the maximum number of retries for announcements
	AnnounceRetryMax int
	// AnnounceRetryDelay is the base delay for exponential backoff
	AnnounceRetryDelay time.Duration
	// HeartbeatInterval is how often to send heartbeats (default: 120s)
	HeartbeatInterval time.Duration
	// Timeout is the HTTP request timeout (default: 10s)
	Timeout time.Duration
}

// DefaultTrackerConfig returns sensible defaults for tracker configuration.
func DefaultTrackerConfig() *TrackerConfig {
	return &TrackerConfig{
		URL:                "https://tracker.dev.stonkagents.com",
		AnnounceRetryMax:   3,
		AnnounceRetryDelay: 1 * time.Second,
		HeartbeatInterval:  120 * time.Second,
		Timeout:            10 * time.Second,
	}
}
