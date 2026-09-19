// Package: tracker/internal/models
// Feature: StonkAgents roadmap interest
// Purpose: Domain model for capability interest captured from the portal
//          (table agent_interest, migration 017). Not feedback: a row is a vote for what an
//          agent should be able to do, mined by product development.

package models

import "time"

// Capability keys accepted by POST /api/v1/interest (the portal shows one chip per key).
const (
	InterestCapabilityTrade     = "trade"
	InterestCapabilityKnowledge = "knowledge"
	InterestCapabilityLearn     = "learn"
	InterestCapabilityCommunity = "community"
	InterestCapabilityAlerts    = "alerts"
	InterestCapabilityToken     = "token"
	InterestCapabilityAutomate  = "automate"
	InterestCapabilityOther     = "other"
)

// InterestCapabilities lists every valid capability key in display order.
var InterestCapabilities = []string{
	InterestCapabilityTrade, InterestCapabilityKnowledge, InterestCapabilityLearn, InterestCapabilityCommunity,
	InterestCapabilityAlerts, InterestCapabilityToken, InterestCapabilityAutomate, InterestCapabilityOther,
}

var interestCapabilityLabels = map[string]string{
	InterestCapabilityTrade:     "Trade for me (sniper / copy trades)",
	InterestCapabilityKnowledge: "Share and sell knowledge",
	InterestCapabilityLearn:     "Learn from other agents",
	InterestCapabilityCommunity: "Run my community",
	InterestCapabilityAlerts:    "Send me alerts",
	InterestCapabilityToken:     "Manage my token",
	InterestCapabilityAutomate:  "Automate tasks on my machine",
	InterestCapabilityOther:     "Something else",
}

// InterestCapabilityLabel returns the human label for a capability key (the key itself when unknown).
func InterestCapabilityLabel(key string) string {
	if l, ok := interestCapabilityLabels[key]; ok {
		return l
	}
	return key
}

// IsValidInterestCapability reports whether key is an accepted capability.
func IsValidInterestCapability(key string) bool {
	_, ok := interestCapabilityLabels[key]
	return ok
}

// Priority keys: how much the capability would matter to the person.
const (
	InterestPriorityNice      = "nice"
	InterestPriorityImportant = "important"
	InterestPriorityPay       = "pay"
)

// InterestPriorities lists every valid priority (order used for stable messages).
var InterestPriorities = []string{InterestPriorityNice, InterestPriorityImportant, InterestPriorityPay}

// IsValidInterestPriority reports whether priority is accepted.
func IsValidInterestPriority(priority string) bool {
	for _, p := range InterestPriorities {
		if p == priority {
			return true
		}
	}
	return false
}

// Interest limits (characters, not bytes; the table enforces the same with char_length).
const (
	InterestMaxCapabilities     = 8
	InterestMaxDescriptionChars = 600
	InterestMaxPathChars        = 200
	InterestMaxContactChars     = 200
	InterestMaxContactViaChars  = 40
	InterestMaxUserAgentChars   = 512
)

// AgentInterest is one submission. IPHash is a SHA-256 hex digest of the client IP, never the address.
type AgentInterest struct {
	ID            int64     `json:"id"`
	Capabilities  []string  `json:"capabilities"`
	Description   string    `json:"description,omitempty"`
	Priority      string    `json:"priority"`
	Contact       string    `json:"contact,omitempty"`
	ContactVia    string    `json:"contact_via,omitempty"`
	WalletAddress string    `json:"wallet_address,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	IPHash        string    `json:"ip_hash,omitempty"`
	Path          string    `json:"path,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// InterestSummary is the roll-up served by GET /api/v1/admin/interest/summary. Every allowed
// capability and priority is present (0 when nobody asked for it yet).
type InterestSummary struct {
	Total        int64            `json:"total"`
	Capabilities map[string]int64 `json:"capabilities"`
	Priorities   map[string]int64 `json:"priorities"`
}

// NewInterestSummary returns a summary with every key zeroed.
func NewInterestSummary() *InterestSummary {
	s := &InterestSummary{Capabilities: map[string]int64{}, Priorities: map[string]int64{}}
	for _, c := range InterestCapabilities {
		s.Capabilities[c] = 0
	}
	for _, p := range InterestPriorities {
		s.Priorities[p] = 0
	}
	return s
}
