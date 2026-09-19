// Package: tracker/internal/reputation
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: TDD tests for EigenTrust simplified reputation calculation

package reputation

import (
	"math"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

// --- Helpers ---

const floatTolerance = 0.0001

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < floatTolerance
}

func fixedTime() time.Time {
	return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
}

// =============================================================================
// PeerStats Tests
// =============================================================================

// TestNewPeerStats verifies default PeerStats initialization
func TestNewPeerStats(t *testing.T) {
	ps := NewPeerStats("peer-1")
	if ps.PeerID != "peer-1" {
		t.Errorf("PeerID = %q, want %q", ps.PeerID, "peer-1")
	}
	if ps.UploadBytes != 0 || ps.DownloadBytes != 0 {
		t.Error("Upload/Download should start at zero")
	}
	if ps.TotalDownloads != 0 || ps.VerificationFailures != 0 {
		t.Error("Downloads/Failures should start at zero")
	}
	if ps.DMCAFlagged {
		t.Error("DMCAFlagged should start false")
	}
	if ps.DMCAReportsFiled != 0 {
		t.Error("DMCAReportsFiled should start at zero")
	}
}

// =============================================================================
// Bandwidth Score Tests (Weight: 40%)
// =============================================================================

// TestBandwidthScore_EqualUploadDownload verifies score for 50/50 ratio
// Acceptance Criteria: Bandwidth = Upload / (Upload + Download). Range [0, 1].
func TestBandwidthScore_EqualUploadDownload(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 1000
	ps.DownloadBytes = 1000

	score := BandwidthScore(ps)
	if !approxEqual(score, 0.5) {
		t.Errorf("BandwidthScore() = %f, want 0.5", score)
	}
}

// TestBandwidthScore_OnlyUpload verifies score for pure uploader
func TestBandwidthScore_OnlyUpload(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 5000
	ps.DownloadBytes = 0

	score := BandwidthScore(ps)
	if !approxEqual(score, 1.0) {
		t.Errorf("BandwidthScore() = %f, want 1.0", score)
	}
}

// TestBandwidthScore_OnlyDownload verifies score for free-rider
func TestBandwidthScore_OnlyDownload(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 0
	ps.DownloadBytes = 5000

	score := BandwidthScore(ps)
	if !approxEqual(score, 0.0) {
		t.Errorf("BandwidthScore() = %f, want 0.0 (free-rider)", score)
	}
}

// TestBandwidthScore_NoActivity verifies score for new peer with no transfers
func TestBandwidthScore_NoActivity(t *testing.T) {
	ps := NewPeerStats("peer-1")

	score := BandwidthScore(ps)
	if !approxEqual(score, 0.0) {
		t.Errorf("BandwidthScore() = %f, want 0.0 (no activity)", score)
	}
}

// TestBandwidthScore_HighUploadRatio verifies score for generous peer
func TestBandwidthScore_HighUploadRatio(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 9000
	ps.DownloadBytes = 1000

	score := BandwidthScore(ps)
	if !approxEqual(score, 0.9) {
		t.Errorf("BandwidthScore() = %f, want 0.9", score)
	}
}

// =============================================================================
// Quality Score Tests (Weight: 30%)
// =============================================================================

// TestQualityScore_NoFailures verifies perfect quality score
// Acceptance Criteria: Quality = 1 - (Failures / TotalDownloads). Range [0, 1].
func TestQualityScore_NoFailures(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.TotalDownloads = 100
	ps.VerificationFailures = 0

	score := QualityScore(ps)
	if !approxEqual(score, 1.0) {
		t.Errorf("QualityScore() = %f, want 1.0", score)
	}
}

// TestQualityScore_SomeFailures verifies degraded quality score
func TestQualityScore_SomeFailures(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.TotalDownloads = 100
	ps.VerificationFailures = 10

	score := QualityScore(ps)
	if !approxEqual(score, 0.9) {
		t.Errorf("QualityScore() = %f, want 0.9", score)
	}
}

// TestQualityScore_AllFailures verifies worst quality score
func TestQualityScore_AllFailures(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.TotalDownloads = 50
	ps.VerificationFailures = 50

	score := QualityScore(ps)
	if !approxEqual(score, 0.0) {
		t.Errorf("QualityScore() = %f, want 0.0", score)
	}
}

// TestQualityScore_NoDownloads verifies score for new peer with no downloads
// SECURITY: Sybil protection — new peers start with 0.5 quality score (not 1.0)
// Prevents attackers from creating new identities to gain immediate full trust
func TestQualityScore_NoDownloads(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.TotalDownloads = 0
	ps.VerificationFailures = 0

	score := QualityScore(ps)
	// No data → default to 0.5 (Sybil protection: new peers must earn trust)
	if !approxEqual(score, 0.5) {
		t.Errorf("QualityScore() = %f, want 0.5 (Sybil protection)", score)
	}
}

// TestQualityScore_NewPeer_Returns0_5 verifies new peer (TotalDownloads=0) gets 0.5
// SECURITY: Sybil protection — prevents attackers from creating many identities
func TestQualityScore_NewPeer_Returns0_5(t *testing.T) {
	ps := NewPeerStats("sybil-test-peer")
	// Brand new peer: no downloads, no failures
	score := QualityScore(ps)
	if !approxEqual(score, DefaultInitialQualityScore) {
		t.Errorf("QualityScore() = %f, want %f (DefaultInitialQualityScore)", score, DefaultInitialQualityScore)
	}
}

// TestCompositeScore_NewPeer_BelowSeventy verifies composite score for brand-new peer is < 0.7
// SECURITY: New peers should not start with high trust to prevent Sybil attacks
func TestCompositeScore_NewPeer_BelowSeventy(t *testing.T) {
	ps := NewPeerStats("new-peer")
	// Brand new peer: all defaults (zero activity)
	score := CompositeScore(ps)
	// bandwidth=0.0*0.4 + quality=0.5*0.3 + security=1.0*0.2 + citizenship=1.0*0.1
	// = 0.0 + 0.15 + 0.2 + 0.1 = 0.45
	if score >= 0.7 {
		t.Errorf("CompositeScore() = %f, want < 0.7 for brand-new peer (Sybil protection)", score)
	}
}

// TestQualityScore_FailuresExceedDownloads verifies floor at 0.0
func TestQualityScore_FailuresExceedDownloads(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.TotalDownloads = 10
	ps.VerificationFailures = 20 // More failures than downloads (edge case)

	score := QualityScore(ps)
	if score < 0.0 {
		t.Errorf("QualityScore() = %f, should not be negative", score)
	}
}

// =============================================================================
// Security Score Tests (Weight: 20%)
// =============================================================================

// TestSecurityScore_Clean verifies clean peer gets 1.0
// Acceptance Criteria: Security = Binary (0 = DMCA flag, 1 = clean). Weight: 20%
func TestSecurityScore_Clean(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.DMCAFlagged = false

	score := SecurityScore(ps)
	if !approxEqual(score, 1.0) {
		t.Errorf("SecurityScore() = %f, want 1.0 (clean)", score)
	}
}

// TestSecurityScore_Flagged verifies DMCA-flagged peer gets 0.0
func TestSecurityScore_Flagged(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.DMCAFlagged = true

	score := SecurityScore(ps)
	if !approxEqual(score, 0.0) {
		t.Errorf("SecurityScore() = %f, want 0.0 (flagged)", score)
	}
}

// =============================================================================
// Citizenship Score Tests (Weight: 10%)
// =============================================================================

// TestCitizenshipScore_NoReports verifies score for peer who never filed reports
// Acceptance Criteria: Citizenship = 1 - (DMCAReportsFiled / 100). Weight: 10%
func TestCitizenshipScore_NoReports(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.DMCAReportsFiled = 0

	score := CitizenshipScore(ps)
	if !approxEqual(score, 1.0) {
		t.Errorf("CitizenshipScore() = %f, want 1.0", score)
	}
}

// TestCitizenshipScore_SomeReports verifies degraded citizenship
func TestCitizenshipScore_SomeReports(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.DMCAReportsFiled = 50

	score := CitizenshipScore(ps)
	if !approxEqual(score, 0.5) {
		t.Errorf("CitizenshipScore() = %f, want 0.5", score)
	}
}

// TestCitizenshipScore_MaxReports verifies floor at 0.0
func TestCitizenshipScore_MaxReports(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.DMCAReportsFiled = 100

	score := CitizenshipScore(ps)
	if !approxEqual(score, 0.0) {
		t.Errorf("CitizenshipScore() = %f, want 0.0", score)
	}
}

// TestCitizenshipScore_ExceedsMax verifies floor at 0.0 for excessive reports
func TestCitizenshipScore_ExceedsMax(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.DMCAReportsFiled = 200

	score := CitizenshipScore(ps)
	if score < 0.0 {
		t.Errorf("CitizenshipScore() = %f, should not be negative", score)
	}
}

// =============================================================================
// Composite Score Tests
// =============================================================================

// TestCompositeScore_PerfectPeer verifies max composite score
// Acceptance Criteria: Composite = weighted sum: bandwidth(40%) + quality(30%) + security(20%) + citizenship(10%)
func TestCompositeScore_PerfectPeer(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 10000
	ps.DownloadBytes = 0 // Pure uploader → bandwidth = 1.0
	ps.TotalDownloads = 100
	ps.VerificationFailures = 0 // quality = 1.0
	ps.DMCAFlagged = false      // security = 1.0
	ps.DMCAReportsFiled = 0     // citizenship = 1.0

	score := CompositeScore(ps)
	// 1.0*0.4 + 1.0*0.3 + 1.0*0.2 + 1.0*0.1 = 1.0
	if !approxEqual(score, 1.0) {
		t.Errorf("CompositeScore() = %f, want 1.0", score)
	}
}

// TestCompositeScore_FreeRider verifies low score for free-rider
func TestCompositeScore_FreeRider(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 0
	ps.DownloadBytes = 10000 // Pure downloader → bandwidth = 0.0
	ps.TotalDownloads = 100
	ps.VerificationFailures = 0
	ps.DMCAFlagged = false
	ps.DMCAReportsFiled = 0

	score := CompositeScore(ps)
	// 0.0*0.4 + 1.0*0.3 + 1.0*0.2 + 1.0*0.1 = 0.6
	if !approxEqual(score, 0.6) {
		t.Errorf("CompositeScore() = %f, want 0.6 (free-rider)", score)
	}
}

// TestCompositeScore_DMCAFlaggedPeer verifies low score for flagged peer
func TestCompositeScore_DMCAFlaggedPeer(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 5000
	ps.DownloadBytes = 5000 // bandwidth = 0.5
	ps.TotalDownloads = 100
	ps.VerificationFailures = 0 // quality = 1.0
	ps.DMCAFlagged = true       // security = 0.0
	ps.DMCAReportsFiled = 0     // citizenship = 1.0

	score := CompositeScore(ps)
	// 0.5*0.4 + 1.0*0.3 + 0.0*0.2 + 1.0*0.1 = 0.2 + 0.3 + 0.0 + 0.1 = 0.6
	if !approxEqual(score, 0.6) {
		t.Errorf("CompositeScore() = %f, want 0.6 (DMCA flagged)", score)
	}
}

// TestCompositeScore_WorstPeer verifies minimum composite score
func TestCompositeScore_WorstPeer(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 0
	ps.DownloadBytes = 10000
	ps.TotalDownloads = 100
	ps.VerificationFailures = 100
	ps.DMCAFlagged = true
	ps.DMCAReportsFiled = 100

	score := CompositeScore(ps)
	// 0*0.4 + 0*0.3 + 0*0.2 + 0*0.1 = 0.0
	if !approxEqual(score, 0.0) {
		t.Errorf("CompositeScore() = %f, want 0.0 (worst peer)", score)
	}
}

// TestCompositeScore_WeightsSum verifies weights sum to 1.0
func TestCompositeScore_WeightsSum(t *testing.T) {
	sum := WeightBandwidth + WeightQuality + WeightSecurity + WeightCitizenship
	if !approxEqual(sum, 1.0) {
		t.Errorf("Weights sum = %f, want 1.0", sum)
	}
}

// =============================================================================
// ReputationRecord Tests
// =============================================================================

// TestCalculateReputation verifies full reputation record calculation
func TestCalculateReputation(t *testing.T) {
	clk := clock.NewMockClock(fixedTime())

	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 8000
	ps.DownloadBytes = 2000 // bandwidth = 0.8
	ps.TotalDownloads = 200
	ps.VerificationFailures = 20 // quality = 0.9
	ps.DMCAFlagged = false       // security = 1.0
	ps.DMCAReportsFiled = 10     // citizenship = 0.9

	record := CalculateReputation(ps, clk)

	if record.PeerID != "peer-1" {
		t.Errorf("PeerID = %q, want %q", record.PeerID, "peer-1")
	}
	if !approxEqual(record.BandwidthScore, 0.8) {
		t.Errorf("BandwidthScore = %f, want 0.8", record.BandwidthScore)
	}
	if !approxEqual(record.QualityScore, 0.9) {
		t.Errorf("QualityScore = %f, want 0.9", record.QualityScore)
	}
	if !approxEqual(record.SecurityScore, 1.0) {
		t.Errorf("SecurityScore = %f, want 1.0", record.SecurityScore)
	}
	if !approxEqual(record.CitizenshipScore, 0.9) {
		t.Errorf("CitizenshipScore = %f, want 0.9", record.CitizenshipScore)
	}

	// Composite: 0.8*0.4 + 0.9*0.3 + 1.0*0.2 + 0.9*0.1 = 0.32+0.27+0.20+0.09 = 0.88
	if !approxEqual(record.CompositeScore, 0.88) {
		t.Errorf("CompositeScore = %f, want 0.88", record.CompositeScore)
	}

	if !record.UpdatedAt.Equal(fixedTime()) {
		t.Errorf("UpdatedAt = %v, want %v", record.UpdatedAt, fixedTime())
	}
}

// =============================================================================
// Decay Function Tests
// =============================================================================

// TestDecay_OneDayInactive verifies -0.01 per day
// Acceptance Criteria: composite_score -= 0.01 per day of inactivity (min 0.1 floor)
func TestDecay_OneDayInactive(t *testing.T) {
	record := &ReputationRecord{
		PeerID:         "peer-1",
		CompositeScore: 0.88,
		UpdatedAt:      fixedTime(),
	}

	clk := clock.NewMockClock(fixedTime())
	clk.Advance(24 * time.Hour) // 1 day later

	decayed := ApplyDecay(record, clk)
	// 0.88 - (1 * 0.01) = 0.87
	if !approxEqual(decayed, 0.87) {
		t.Errorf("ApplyDecay() = %f, want 0.87 (1 day decay)", decayed)
	}
}

// TestDecay_TenDaysInactive verifies cumulative decay
func TestDecay_TenDaysInactive(t *testing.T) {
	record := &ReputationRecord{
		PeerID:         "peer-1",
		CompositeScore: 0.5,
		UpdatedAt:      fixedTime(),
	}

	clk := clock.NewMockClock(fixedTime())
	clk.Advance(10 * 24 * time.Hour) // 10 days later

	decayed := ApplyDecay(record, clk)
	// 0.5 - (10 * 0.01) = 0.4
	if !approxEqual(decayed, 0.4) {
		t.Errorf("ApplyDecay() = %f, want 0.4 (10 day decay)", decayed)
	}
}

// TestDecay_FloorAt01 verifies decay doesn't go below 0.1
func TestDecay_FloorAt01(t *testing.T) {
	record := &ReputationRecord{
		PeerID:         "peer-1",
		CompositeScore: 0.15,
		UpdatedAt:      fixedTime(),
	}

	clk := clock.NewMockClock(fixedTime())
	clk.Advance(100 * 24 * time.Hour) // 100 days later

	decayed := ApplyDecay(record, clk)
	// 0.15 - (100 * 0.01) = -0.85, floored at 0.1
	if !approxEqual(decayed, 0.1) {
		t.Errorf("ApplyDecay() = %f, want 0.1 (floor)", decayed)
	}
}

// TestDecay_NoInactivity verifies no decay when recently updated
func TestDecay_NoInactivity(t *testing.T) {
	record := &ReputationRecord{
		PeerID:         "peer-1",
		CompositeScore: 0.88,
		UpdatedAt:      fixedTime(),
	}

	clk := clock.NewMockClock(fixedTime()) // Same time = 0 days

	decayed := ApplyDecay(record, clk)
	if !approxEqual(decayed, 0.88) {
		t.Errorf("ApplyDecay() = %f, want 0.88 (no decay)", decayed)
	}
}

// TestDecay_PartialDay verifies that partial days are floored to whole days
func TestDecay_PartialDay(t *testing.T) {
	record := &ReputationRecord{
		PeerID:         "peer-1",
		CompositeScore: 0.88,
		UpdatedAt:      fixedTime(),
	}

	clk := clock.NewMockClock(fixedTime())
	clk.Advance(36 * time.Hour) // 1.5 days → 1 full day of decay

	decayed := ApplyDecay(record, clk)
	// 0.88 - (1 * 0.01) = 0.87
	if !approxEqual(decayed, 0.87) {
		t.Errorf("ApplyDecay() = %f, want 0.87 (partial day floors to 1)", decayed)
	}
}

// =============================================================================
// Breaker Tests
// =============================================================================

// TestCompositeScore_Breaker_VeryLargeBytes verifies no overflow with large values
func TestCompositeScore_Breaker_VeryLargeBytes(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 1 << 62   // ~4.6 exabytes
	ps.DownloadBytes = 1 << 62 // ~4.6 exabytes
	ps.TotalDownloads = 1000000
	ps.VerificationFailures = 0

	score := CompositeScore(ps)
	if math.IsNaN(score) || math.IsInf(score, 0) {
		t.Errorf("CompositeScore() = %f, should not be NaN or Inf", score)
	}
	if score < 0.0 || score > 1.0 {
		t.Errorf("CompositeScore() = %f, should be in [0, 1]", score)
	}
}

// TestBandwidthScore_Breaker_BothZero verifies zero division safety
func TestBandwidthScore_Breaker_BothZero(t *testing.T) {
	ps := NewPeerStats("peer-1")
	ps.UploadBytes = 0
	ps.DownloadBytes = 0

	score := BandwidthScore(ps)
	if math.IsNaN(score) || math.IsInf(score, 0) {
		t.Errorf("BandwidthScore() = %f, should not be NaN or Inf", score)
	}
}
