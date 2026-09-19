// Package: tracker/internal/api
// Feature: F-026 (Profile Endpoint)
// Story: US-026-01 (Profile Aggregation)
// Purpose: TDD tests for buildProfile aggregation, degradation, and HTTP handler

package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// ── Mock implementations ──

type mockRepFinder struct {
	record   *reputation.ReputationRecord
	findErr  error
	above    int
	total    int
	countErr error
}

func (m *mockRepFinder) FindByPeerID(_ context.Context, _ string) (*reputation.ReputationRecord, error) {
	return m.record, m.findErr
}
func (m *mockRepFinder) CountAboveScore(_ context.Context, _ float64) (int, int, error) {
	return m.above, m.total, m.countErr
}

type mockPeerFinder struct {
	peer *models.Peer
	err  error
}

func (m *mockPeerFinder) FindByID(_ context.Context, _ string) (*models.Peer, error) {
	return m.peer, m.err
}

type mockAssetSearcher struct {
	count        int
	countErr     error
	topAssets    []*models.Asset
	topErr       error
	searchAssets []*models.Asset
	searchTotal  int
	searchErr    error
}

func (m *mockAssetSearcher) Search(_ context.Context, _ repository.SearchAssetsOptions) ([]*models.Asset, int, error) {
	return m.searchAssets, m.searchTotal, m.searchErr
}
func (m *mockAssetSearcher) CountByPeerID(_ context.Context, _ string) (int, error) {
	return m.count, m.countErr
}
func (m *mockAssetSearcher) TopByDownloads(_ context.Context, _ string, _ int) ([]*models.Asset, error) {
	return m.topAssets, m.topErr
}

type mockPresenceChecker struct {
	online bool
	err    error
}

func (m *mockPresenceChecker) IsOnline(_ context.Context, _ string) (bool, error) {
	return m.online, m.err
}

type mockTrustCounter struct {
	count int
	err   error
}

func (m *mockTrustCounter) CountTrustsReceived(_ context.Context, _ string) (int, error) {
	return m.count, m.err
}

// ── Task 4: buildProfile happy path + edge cases ──

func TestBuildProfile_HappyPath(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{
			record: &reputation.ReputationRecord{
				PeerID: "peer123", BandwidthScore: 0.40, QualityScore: 0.55,
				SecurityScore: 1.0, CitizenshipScore: 0.80, CompositeScore: 0.45,
			},
			above: 2, total: 5,
		},
		&mockPeerFinder{peer: &models.Peer{
			PeerID: "peer123", MaskedPeerID: "claw-alpha-7f3a",
			FirstSeen:          LaunchDate.Add(5 * 24 * time.Hour),
			TotalUptimeSeconds: 450180, TotalUploadBytes: 500,
		}},
		&mockAssetSearcher{
			count: 3,
			topAssets: []*models.Asset{
				{Filename: "a.at-vec", DownloadCount: 42, Size: 1048576, AnnouncedAt: fixedNow},
			},
			searchAssets: []*models.Asset{
				{Filename: "b.at-raw", AnnouncedAt: fixedNow},
			},
		},
		&mockPresenceChecker{online: true}, &mockTrustCounter{},
	)

	dto, err := h.buildProfile(context.Background(), "peer123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.PeerID != "peer123" {
		t.Errorf("PeerID = %q, want peer123", dto.PeerID)
	}
	if dto.MaskedPeerID != "claw-alpha-7f3a" {
		t.Errorf("MaskedPeerID = %q, want claw-alpha-7f3a", dto.MaskedPeerID)
	}
	if dto.Rank != "silver" {
		t.Errorf("Rank = %q, want silver (composite 0.45)", dto.Rank)
	}
	if !dto.IsOnline {
		t.Error("IsOnline = false, want true")
	}
	if dto.Stats.Clout != 45 {
		t.Errorf("Clout = %d, want 45", dto.Stats.Clout)
	}
	if dto.Stats.TopPercent != 40 {
		t.Errorf("TopPercent = %d, want 40", dto.Stats.TopPercent)
	}
	if dto.Stats.Drops != 3 {
		t.Errorf("Drops = %d, want 3", dto.Stats.Drops)
	}
	if dto.Stats.UptimeSeconds != 450180 {
		t.Errorf("UptimeSeconds = %d, want 450180", dto.Stats.UptimeSeconds)
	}
	if dto.EigenTrust.CompositeScore != 0.45 {
		t.Errorf("CompositeScore = %f, want 0.45", dto.EigenTrust.CompositeScore)
	}
	if dto.EigenTrust.Weights.Bandwidth != reputation.WeightBandwidth {
		t.Errorf("Weights.Bandwidth = %f, want %f", dto.EigenTrust.Weights.Bandwidth, reputation.WeightBandwidth)
	}
	if len(dto.Badges) != 10 {
		t.Errorf("Badges count = %d, want 10", len(dto.Badges))
	}
	if len(dto.TopDrops) != 1 {
		t.Errorf("TopDrops count = %d, want 1", len(dto.TopDrops))
	}
	if dto.TopDrops[0].FileType != ".at-vec" {
		t.Errorf("TopDrops[0].FileType = %q, want .at-vec", dto.TopDrops[0].FileType)
	}
	if len(dto.RecentActivity) != 1 {
		t.Errorf("RecentActivity count = %d, want 1", len(dto.RecentActivity))
	}
}

func TestBuildProfile_NoReputation(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer1", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "peer1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Rank != "new" {
		t.Errorf("Rank = %q, want new (no reputation)", dto.Rank)
	}
	if dto.Stats.TopPercent != 99 {
		t.Errorf("TopPercent = %d, want 99 (no reputation → degraded)", dto.Stats.TopPercent)
	}
	if dto.Stats.Clout != 0 {
		t.Errorf("Clout = %d, want 0", dto.Stats.Clout)
	}
	if dto.EigenTrust.CompositeScore != 0 {
		t.Errorf("CompositeScore = %f, want 0", dto.EigenTrust.CompositeScore)
	}
}

func TestBuildProfile_NoAssets(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer1", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 0, topAssets: nil, searchAssets: nil},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "peer1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Stats.Drops != 0 {
		t.Errorf("Drops = %d, want 0", dto.Stats.Drops)
	}
	if len(dto.TopDrops) != 0 {
		t.Errorf("TopDrops = %d, want 0", len(dto.TopDrops))
	}
	if len(dto.RecentActivity) != 0 {
		t.Errorf("RecentActivity = %d, want 0", len(dto.RecentActivity))
	}
}

func TestBuildProfile_MaskedPeerID(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	// Has masked -> uses it
	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "12D3KooWRkGLz4YvbR3", MaskedPeerID: "claw-test-1234", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, _ := h.buildProfile(context.Background(), "12D3KooWRkGLz4YvbR3")
	if dto.MaskedPeerID != "claw-test-1234" {
		t.Errorf("MaskedPeerID = %q, want claw-test-1234", dto.MaskedPeerID)
	}

	// Empty masked -> falls back to geo.MaskPeerID
	h2 := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "12D3KooWRkGLz4YvbR3", MaskedPeerID: "", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto2, _ := h2.buildProfile(context.Background(), "12D3KooWRkGLz4YvbR3")
	if dto2.MaskedPeerID == "" {
		t.Error("MaskedPeerID should not be empty when falling back to geo.MaskPeerID")
	}
}

func TestBuildProfile_MaskedPeerID_ShortID(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	// peerID < 13 chars, no maskedPeerID -> geo.MaskPeerID returns full ID, no panic
	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "short", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "short")
	if err != nil {
		t.Fatalf("unexpected error with short peerID: %v", err)
	}
	if dto.MaskedPeerID != "short" {
		t.Errorf("MaskedPeerID = %q, want short (passthrough for short IDs)", dto.MaskedPeerID)
	}
}

func TestBuildProfile_TopDropsSorted(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{
			topAssets: []*models.Asset{
				{Filename: "top1.vec", DownloadCount: 100, Size: 1000, AnnouncedAt: fixedNow},
				{Filename: "top2.vec", DownloadCount: 50, Size: 2000, AnnouncedAt: fixedNow},
				{Filename: "top3.vec", DownloadCount: 10, Size: 500, AnnouncedAt: fixedNow},
			},
		},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, _ := h.buildProfile(context.Background(), "p1")
	if len(dto.TopDrops) != 3 {
		t.Fatalf("TopDrops count = %d, want 3", len(dto.TopDrops))
	}
	// Verify order preserved from repo (already sorted by download_count DESC)
	if dto.TopDrops[0].DownloadCount != 100 {
		t.Errorf("TopDrops[0].DownloadCount = %d, want 100", dto.TopDrops[0].DownloadCount)
	}
	if dto.TopDrops[2].DownloadCount != 10 {
		t.Errorf("TopDrops[2].DownloadCount = %d, want 10", dto.TopDrops[2].DownloadCount)
	}
}

func TestBuildProfile_TopDropsTieBreaking(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{
			// Mock returns in the ORDER the repo SQL would produce:
			// download_count DESC, announced_at DESC, cid ASC
			topAssets: []*models.Asset{
				{CID: "cid-b", Filename: "b.vec", DownloadCount: 10, AnnouncedAt: fixedNow},
				{CID: "cid-c", Filename: "c.vec", DownloadCount: 10, AnnouncedAt: fixedNow},
				{CID: "cid-a", Filename: "a.vec", DownloadCount: 10, AnnouncedAt: fixedNow.Add(-1 * time.Hour)},
			},
		},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, _ := h.buildProfile(context.Background(), "p1")
	if len(dto.TopDrops) != 3 {
		t.Fatalf("TopDrops count = %d, want 3", len(dto.TopDrops))
	}
	// TopDropDTO has no CID field — assert via Filename (unique per mock item)
	if dto.TopDrops[0].Filename != "b.vec" {
		t.Errorf("TopDrops[0].Filename = %q, want b.vec (same count+time, cid ASC)", dto.TopDrops[0].Filename)
	}
	if dto.TopDrops[1].Filename != "c.vec" {
		t.Errorf("TopDrops[1].Filename = %q, want c.vec (same count+time, cid ASC)", dto.TopDrops[1].Filename)
	}
	if dto.TopDrops[2].Filename != "a.vec" {
		t.Errorf("TopDrops[2].Filename = %q, want a.vec (older announced_at)", dto.TopDrops[2].Filename)
	}
}

func TestBuildProfile_RecentActivitySorted(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{
			searchAssets: []*models.Asset{
				{Filename: "recent1.vec", AnnouncedAt: fixedNow},
				{Filename: "recent2.vec", AnnouncedAt: fixedNow.Add(-1 * time.Hour)},
			},
		},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, _ := h.buildProfile(context.Background(), "p1")
	if len(dto.RecentActivity) != 2 {
		t.Fatalf("RecentActivity count = %d, want 2", len(dto.RecentActivity))
	}
	// Order preserved from Search (already ORDER BY announced_at DESC)
	if dto.RecentActivity[0].Filename != "recent1.vec" {
		t.Errorf("RecentActivity[0].Filename = %q, want recent1.vec", dto.RecentActivity[0].Filename)
	}
}

// ── Task 5: Degradation tests ──

var errInfra = errors.New("database connection lost")

func TestBuildProfile_ReputationRepoFails(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: errInfra},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade, not fail: %v", err)
	}
	if dto.EigenTrust.CompositeScore != 0 {
		t.Errorf("CompositeScore = %f, want 0 (degraded)", dto.EigenTrust.CompositeScore)
	}
	if dto.Rank != "new" {
		t.Errorf("Rank = %q, want new (degraded)", dto.Rank)
	}
}

func TestBuildProfile_PresenceStoreFails(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{err: errInfra}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade: %v", err)
	}
	if dto.IsOnline {
		t.Error("IsOnline = true, want false (degraded)")
	}
}

func TestBuildProfile_CountAboveScoreFails(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	// Provide a valid reputation record so FindByPeerID succeeds,
	// but countErr causes CountAboveScore to fail — exercises the
	// degradation path at buildEigenTrust line that was previously
	// unreachable due to findErr: ErrNotFound returning early.
	h := NewProfileHandler(
		&mockRepFinder{
			record:   &reputation.ReputationRecord{CompositeScore: 0.5, BandwidthScore: 0.4},
			countErr: errInfra,
		},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade: %v", err)
	}
	if dto.Stats.TopPercent != 99 {
		t.Errorf("TopPercent = %d, want 99 (degraded)", dto.Stats.TopPercent)
	}
	// EigenTrust scores should still be populated from the found record
	if dto.EigenTrust.BandwidthScore != 0.4 {
		t.Errorf("BandwidthScore = %f, want 0.4 (from found record)", dto.EigenTrust.BandwidthScore)
	}
}

func TestBuildProfile_CountByPeerIDFails(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{countErr: errInfra},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade: %v", err)
	}
	if dto.Stats.Drops != 0 {
		t.Errorf("Drops = %d, want 0 (degraded)", dto.Stats.Drops)
	}
}

func TestBuildProfile_SearchFails(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{searchErr: errInfra},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade: %v", err)
	}
	if len(dto.RecentActivity) != 0 {
		t.Errorf("RecentActivity = %d, want 0 (degraded)", len(dto.RecentActivity))
	}
}

func TestBuildProfile_TopByDownloadsFails(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{topErr: errInfra},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	dto, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade: %v", err)
	}
	if len(dto.TopDrops) != 0 {
		t.Errorf("TopDrops = %d, want 0 (degraded)", len(dto.TopDrops))
	}
}

// ── Task TD-025: Prometheus degradation counter ──

func TestBuildProfile_DegradationCounter_Reputation(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	before := testutil.ToFloat64(profileDegradationTotal.WithLabelValues("reputation"))

	h := NewProfileHandler(
		&mockRepFinder{findErr: errInfra}, // triggers degradation
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	_, err := h.buildProfile(context.Background(), "p1")
	if err != nil {
		t.Fatalf("buildProfile should degrade: %v", err)
	}

	after := testutil.ToFloat64(profileDegradationTotal.WithLabelValues("reputation"))
	if after-before != 1 {
		t.Errorf("reputation degradation counter delta = %f, want 1", after-before)
	}
}

func TestBuildProfile_DegradationCounter_Presence(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	before := testutil.ToFloat64(profileDegradationTotal.WithLabelValues("presence"))

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{},
		&mockPresenceChecker{err: errInfra}, &mockTrustCounter{},
	)
	_, _ = h.buildProfile(context.Background(), "p1")

	after := testutil.ToFloat64(profileDegradationTotal.WithLabelValues("presence"))
	if after-before != 1 {
		t.Errorf("presence degradation counter delta = %f, want 1", after-before)
	}
}

func TestBuildProfile_DegradationCounter_Assets(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	before := testutil.ToFloat64(profileDegradationTotal.WithLabelValues("assets"))

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "p1", FirstSeen: fixedNow}},
		&mockAssetSearcher{countErr: errInfra},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	_, _ = h.buildProfile(context.Background(), "p1")

	after := testutil.ToFloat64(profileDegradationTotal.WithLabelValues("assets"))
	if after-before != 1 {
		t.Errorf("assets degradation counter delta = %f, want 1", after-before)
	}
}

// mockLibraryCounter implements profileLibraryCounter for testing.
type mockLibraryCounter struct {
	count int
	err   error
}

func (m *mockLibraryCounter) CountByPeerIDAndAction(_ context.Context, _, _ string) (int, error) {
	return m.count, m.err
}

// TestBuildProfile_LibraryCountsSeparateFromDrops verifies Stats.Library uses download events, not drops.
// Feature: F-032 (Peers & Reputation), Story: US-032-02
// TD-045: Fix Stats.Library
func TestBuildProfile_LibraryCountsSeparateFromDrops(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer1", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 5}, // 5 drops
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	h.SetLibraryCounter(&mockLibraryCounter{count: 12}) // 12 downloads

	dto, err := h.buildProfile(context.Background(), "peer1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Stats.Drops != 5 {
		t.Errorf("Drops = %d, want 5", dto.Stats.Drops)
	}
	if dto.Stats.Library != 12 {
		t.Errorf("Library = %d, want 12 (should be download count, not drops)", dto.Stats.Library)
	}
}

// TestBuildProfile_LibraryDegrades verifies Library degrades to 0 when counter is nil.
func TestBuildProfile_LibraryDegrades(t *testing.T) {
	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	h := NewProfileHandler(
		&mockRepFinder{findErr: models.ErrNotFound},
		&mockPeerFinder{peer: &models.Peer{PeerID: "peer1", FirstSeen: fixedNow}},
		&mockAssetSearcher{count: 3},
		&mockPresenceChecker{}, &mockTrustCounter{},
	)
	// No library counter set — should degrade to 0

	dto, err := h.buildProfile(context.Background(), "peer1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Stats.Library != 0 {
		t.Errorf("Library = %d, want 0 (no counter → degraded)", dto.Stats.Library)
	}
}
