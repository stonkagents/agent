// Package: tracker/internal/models
// Feature: StonkAgents portal feedback
// Purpose: Domain model for feedback submitted from the portal (table feedback, migration 016)

package models

import "time"

// Feedback kinds accepted by POST /api/v1/feedback.
const (
	FeedbackKindBug    = "bug"
	FeedbackKindIdea   = "idea"
	FeedbackKindOther  = "other"
	FeedbackKindWanted = "wanted"
)

// FeedbackKinds lists every valid kind (order used for stable messages).
var FeedbackKinds = []string{FeedbackKindBug, FeedbackKindIdea, FeedbackKindOther, FeedbackKindWanted}

// IsValidFeedbackKind reports whether kind is accepted.
func IsValidFeedbackKind(kind string) bool {
	for _, k := range FeedbackKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Feedback limits (characters, not bytes; the table enforces the same with char_length).
const (
	FeedbackMaxMessageChars    = 2000
	FeedbackMaxPathChars       = 200
	FeedbackMaxContactChars    = 200
	FeedbackMaxContactViaChars = 40
	FeedbackMaxUserAgentChars  = 512
)

// Feedback is one submission. IPHash is a SHA-256 hex digest of the client IP, never the address.
type Feedback struct {
	ID            int64     `json:"id"`
	Kind          string    `json:"kind"`
	Message       string    `json:"message"`
	Path          string    `json:"path,omitempty"`
	Contact       string    `json:"contact,omitempty"`
	ContactVia    string    `json:"contact_via,omitempty"`
	WalletAddress string    `json:"wallet_address,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	IPHash        string    `json:"ip_hash,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
