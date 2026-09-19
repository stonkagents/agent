// Package: tracker/internal/reputation
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: Simplified EigenTrust reputation calculation with 4-factor weighted scoring

package reputation

import (
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

// Weight constants for composite score calculation.
// Sum must equal 1.0.
const (
	WeightBandwidth   = 0.4
	WeightQuality     = 0.3
	WeightSecurity    = 0.2
	WeightCitizenship = 0.1
)

// Decay constants
const (
	DecayPerDay = 0.01
	DecayFloor  = 0.1
)

// SECURITY: Sybil protection — new peers start with 0.5 quality score (not 1.0)
// Prevents attackers from creating new identities to gain immediate full trust
const DefaultInitialQualityScore = 0.5

// PeerStats holds the raw metrics for a peer used to compute reputation.
type PeerStats struct {
	PeerID               string
	UploadBytes          int64
	DownloadBytes        int64
	TotalDownloads       int64
	VerificationFailures int64
	DMCAFlagged          bool
	DMCAReportsFiled     int64
}

// NewPeerStats creates a PeerStats with default zero values.
func NewPeerStats(peerID string) *PeerStats {
	return &PeerStats{PeerID: peerID}
}

// ReputationRecord holds the computed reputation for a peer.
type ReputationRecord struct {
	PeerID           string
	BandwidthScore   float64
	QualityScore     float64
	SecurityScore    float64
	CitizenshipScore float64
	CompositeScore   float64
	UpdatedAt        time.Time
}

// BandwidthScore computes Upload / (Upload + Download). Range [0, 1].
// Returns 0.0 if no activity.
func BandwidthScore(ps *PeerStats) float64 {
	total := ps.UploadBytes + ps.DownloadBytes
	if total == 0 {
		return 0.0
	}
	return float64(ps.UploadBytes) / float64(total)
}

// QualityScore computes 1 - (Failures / TotalDownloads). Range [0, 1].
// SECURITY: Returns DefaultInitialQualityScore (0.5) if no downloads — Sybil protection.
// New peers must earn trust through verified downloads rather than starting at maximum.
func QualityScore(ps *PeerStats) float64 {
	if ps.TotalDownloads == 0 {
		return DefaultInitialQualityScore
	}
	score := 1.0 - float64(ps.VerificationFailures)/float64(ps.TotalDownloads)
	if score < 0.0 {
		return 0.0
	}
	return score
}

// SecurityScore returns 0.0 if DMCA-flagged, 1.0 otherwise.
func SecurityScore(ps *PeerStats) float64 {
	if ps.DMCAFlagged {
		return 0.0
	}
	return 1.0
}

// CitizenshipScore computes 1 - (DMCAReportsFiled / 100). Range [0, 1].
func CitizenshipScore(ps *PeerStats) float64 {
	score := 1.0 - float64(ps.DMCAReportsFiled)/100.0
	if score < 0.0 {
		return 0.0
	}
	return score
}

// CompositeScore computes the weighted sum of all four factor scores.
func CompositeScore(ps *PeerStats) float64 {
	return BandwidthScore(ps)*WeightBandwidth +
		QualityScore(ps)*WeightQuality +
		SecurityScore(ps)*WeightSecurity +
		CitizenshipScore(ps)*WeightCitizenship
}

// CalculateReputation computes a full ReputationRecord for the given peer stats.
func CalculateReputation(ps *PeerStats, clk clock.Clock) *ReputationRecord {
	return &ReputationRecord{
		PeerID:           ps.PeerID,
		BandwidthScore:   BandwidthScore(ps),
		QualityScore:     QualityScore(ps),
		SecurityScore:    SecurityScore(ps),
		CitizenshipScore: CitizenshipScore(ps),
		CompositeScore:   CompositeScore(ps),
		UpdatedAt:        clk.Now(),
	}
}

// ApplyDecay reduces the composite score by DecayPerDay for each full day
// of inactivity since UpdatedAt. The score is floored at DecayFloor.
func ApplyDecay(record *ReputationRecord, clk clock.Clock) float64 {
	elapsed := clk.Now().Sub(record.UpdatedAt)
	days := int(elapsed.Hours() / 24)

	decayed := record.CompositeScore - float64(days)*DecayPerDay
	if decayed < DecayFloor {
		return DecayFloor
	}
	return decayed
}
