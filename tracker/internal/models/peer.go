// Package: tracker/internal/models
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Domain models for tracker entities

package models

import "time"

// Peer represents a registered agent in the tracker.
type Peer struct {
	PeerID                  string    `json:"peer_id"`
	PublicKey               string    `json:"ed25519_pubkey"`
	Multiaddrs              []string  `json:"multiaddrs"`
	FirstSeen               time.Time `json:"first_seen"`
	LastSeen                time.Time `json:"last_seen"`
	TotalUptimeSeconds      int64     `json:"total_uptime_seconds"`
	Country                 string    `json:"country,omitempty"`
	Region                  string    `json:"region,omitempty"`
	MaskedPeerID            string    `json:"masked_peer_id,omitempty"`
	TotalUploadBytes        int64     `json:"total_upload_bytes"`
	TotalDownloadBytes      int64     `json:"total_download_bytes"`
	AverageSpeedBytesPerSec *int64    `json:"average_speed_bytes_per_sec,omitempty"`
	// DisplayName is the user-chosen name from daemon config. Optional.
	DisplayName string `json:"display_name,omitempty"`
	// City from GeoIP lookup. Optional.
	City string `json:"city,omitempty"`
	// Lat/Lng from GeoIP lookup, rounded to 2 decimal places (~1km).
	Lat float64 `json:"lat,omitempty"`
	Lng float64 `json:"lng,omitempty"`
	// LastUploadAt tracks when this peer last uploaded (for seeding status).
	LastUploadAt *time.Time `json:"last_upload_at,omitempty"`
	// LastDownloadAt tracks when this peer last downloaded (for leeching status).
	LastDownloadAt *time.Time `json:"last_download_at,omitempty"`
	// WalletAddress is the Solana wallet address linked from the frontend (optional).
	WalletAddress string `json:"wallet_address,omitempty"`
	// CurrentSessionStart is set on first heartbeat after an offline gap. Used for presence grant timing.
	CurrentSessionStart *time.Time `json:"current_session_start,omitempty"`
	// APIKey is set only on first register response; not stored in peers table. Omit from list/discover.
	APIKey string `json:"api_key,omitempty"`
}

// Asset represents a shared asset announced to the tracker.
type Asset struct {
	CID           string    `json:"cid"`
	Filename      string    `json:"filename"`
	MimeType      string    `json:"mime_type"`
	Size          int64     `json:"size"`
	PeerID        string    `json:"peer_id"`
	AnnouncedAt   time.Time `json:"announced_at"`
	ManifestType  string    `json:"manifest_type"` // "raw", "vec", "traj", "claw-skill", "claw-prompt", "claw-memory", "claw-workflow", "claw-context", "claw-tool"
	ManifestData  []byte    `json:"manifest_data"`
	Quarantined   bool      `json:"quarantined"`
	DownloadCount int64     `json:"download_count"`
	// Embedding is the 384-dim semantic embedding vector for search (F-002, US-002-01)
	// Generated from filename + description + tags on announce
	// Zero vector if embedding model unavailable (graceful fallback)
	Embedding []float32 `json:"embedding,omitempty"`
	// Tags are auto-extracted metadata (F-002, US-002-04)
	// Examples: size category, MIME type, dimensions, model framework
	Tags []string `json:"tags,omitempty"`
}

// PeerEvent is a single entry in a peer's activity timeline (F-032, US-032-02).
type PeerEvent struct {
	ID        string    `json:"id"`
	PeerID    string    `json:"peer_id"`
	Action    string    `json:"action"`  // "share", "install", "trust", "badge_earned", etc.
	Details   string    `json:"details"` // human-readable description
	CreatedAt time.Time `json:"created_at"`
}

// DMCANotice represents a DMCA takedown request.
type DMCANotice struct {
	ID            string    `json:"id"`
	CID           string    `json:"cid"`
	ReporterEmail string    `json:"reporter_email"`
	ComplaintText string    `json:"complaint_text"`
	QuarantinedAt time.Time `json:"quarantined_at"`
	Status        string    `json:"status"` // "pending", "confirmed", "rejected"
}

// ReplicationStatus constants for S3 replication lifecycle.
const (
	ReplicationStatusPending     = "pending"
	ReplicationStatusRunning     = "running"
	ReplicationStatusCompleted   = "completed"
	ReplicationStatusFailed      = "failed"
	ReplicationStatusQuarantined = "quarantined"
)

// AssetReplication stores S3 replication metadata for an asset CID.
type AssetReplication struct {
	CID          string     `json:"cid"`
	S3Key        string     `json:"s3_key"`
	Status       string     `json:"status"`
	SizeBytes    int64      `json:"size_bytes"`
	ETag         string     `json:"etag,omitempty"`
	ReplicatedAt *time.Time `json:"replicated_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ReplicationJob records one worker attempt for a CID.
type ReplicationJob struct {
	ID             string     `json:"id"`
	CID            string     `json:"cid"`
	AssignedWorker string     `json:"assigned_worker"`
	Attempt        int        `json:"attempt"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// DMCAStatus constants.
const (
	DMCAStatusPending   = "pending"
	DMCAStatusConfirmed = "confirmed"
	DMCAStatusRejected  = "rejected"
)

// ValidManifestTypes for asset announcements.
var ValidManifestTypes = map[string]bool{
	"raw":           true,
	"vec":           true,
	"traj":          true,
	"claw-skill":    true,
	"claw-prompt":   true,
	"claw-memory":   true,
	"claw-workflow": true,
	"claw-context":  true,
	"claw-tool":     true,
}
