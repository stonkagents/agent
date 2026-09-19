// Package: tracker/internal/reputation
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: TDD tests for background reputation recalculation job

package reputation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

// =============================================================================
// StatsProvider Interface Tests
// =============================================================================

// mockStatsProvider implements StatsProvider for testing.
type mockStatsProvider struct {
	stats map[string]*PeerStats
}

func (m *mockStatsProvider) GetPeerStats(ctx context.Context, peerID string) (*PeerStats, error) {
	ps, ok := m.stats[peerID]
	if !ok {
		return NewPeerStats(peerID), nil
	}
	return ps, nil
}

func (m *mockStatsProvider) AllPeerIDs(ctx context.Context) ([]string, error) {
	ids := make([]string, 0, len(m.stats))
	for id := range m.stats {
		ids = append(ids, id)
	}
	return ids, nil
}

// mockReputationStore implements ReputationStore for testing.
type mockReputationStore struct {
	records map[string]*ReputationRecord
}

func newMockReputationStore() *mockReputationStore {
	return &mockReputationStore{records: make(map[string]*ReputationRecord)}
}

func (m *mockReputationStore) Upsert(ctx context.Context, record *ReputationRecord) error {
	cp := *record
	m.records[record.PeerID] = &cp
	return nil
}

func (m *mockReputationStore) FindByPeerID(ctx context.Context, peerID string) (*ReputationRecord, error) {
	r, ok := m.records[peerID]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

// =============================================================================
// Recalculator Tests
// =============================================================================

// TestRecalculator_RecalculateAll verifies batch recalculation
func TestRecalculator_RecalculateAll(t *testing.T) {
	clk := clock.NewMockClock(fixedTime())
	stats := &mockStatsProvider{
		stats: map[string]*PeerStats{
			"peer-a": {PeerID: "peer-a", UploadBytes: 8000, DownloadBytes: 2000, TotalDownloads: 100},
			"peer-b": {PeerID: "peer-b", UploadBytes: 0, DownloadBytes: 5000, TotalDownloads: 50},
		},
	}
	store := newMockReputationStore()

	recalc := NewRecalculator(stats, store, clk)

	err := recalc.RecalculateAll(context.Background())
	if err != nil {
		t.Fatalf("RecalculateAll() error: %v", err)
	}

	// peer-a should have higher score than peer-b
	recA, ok := store.records["peer-a"]
	if !ok {
		t.Fatal("peer-a reputation record not stored")
	}
	recB, ok := store.records["peer-b"]
	if !ok {
		t.Fatal("peer-b reputation record not stored")
	}

	if recA.CompositeScore <= recB.CompositeScore {
		t.Errorf("peer-a score (%f) should be > peer-b score (%f)", recA.CompositeScore, recB.CompositeScore)
	}
}

// TestRecalculator_RecalculateAll_Empty verifies no error with zero peers
func TestRecalculator_RecalculateAll_Empty(t *testing.T) {
	clk := clock.NewMockClock(fixedTime())
	stats := &mockStatsProvider{stats: map[string]*PeerStats{}}
	store := newMockReputationStore()

	recalc := NewRecalculator(stats, store, clk)

	err := recalc.RecalculateAll(context.Background())
	if err != nil {
		t.Fatalf("RecalculateAll() with zero peers error: %v", err)
	}
}

// TestRecalculator_RecalculateSingle verifies single-peer recalculation
func TestRecalculator_RecalculateSingle(t *testing.T) {
	clk := clock.NewMockClock(fixedTime())
	stats := &mockStatsProvider{
		stats: map[string]*PeerStats{
			"peer-1": {PeerID: "peer-1", UploadBytes: 5000, DownloadBytes: 5000, TotalDownloads: 100},
		},
	}
	store := newMockReputationStore()

	recalc := NewRecalculator(stats, store, clk)

	err := recalc.RecalculatePeer(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("RecalculatePeer() error: %v", err)
	}

	rec, ok := store.records["peer-1"]
	if !ok {
		t.Fatal("peer-1 reputation record not stored")
	}
	if rec.BandwidthScore != 0.5 {
		t.Errorf("BandwidthScore = %f, want 0.5", rec.BandwidthScore)
	}
}

// =============================================================================
// Background Runner Tests
// =============================================================================

// TestRecalculator_StartStop verifies the background ticker starts and stops
func TestRecalculator_StartStop(t *testing.T) {
	clk := clock.NewMockClock(fixedTime())
	stats := &mockStatsProvider{stats: map[string]*PeerStats{
		"peer-1": {PeerID: "peer-1", UploadBytes: 1000, DownloadBytes: 1000},
	}}
	store := newMockReputationStore()

	recalc := NewRecalculator(stats, store, clk)

	// Start with a fast interval for testing
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runCount int64
	recalc.OnRecalculate = func() { atomic.AddInt64(&runCount, 1) }
	recalc.Start(ctx, 10*time.Millisecond)

	// Wait for at least 1 tick
	time.Sleep(50 * time.Millisecond)
	cancel()

	count := atomic.LoadInt64(&runCount)
	if count < 1 {
		t.Errorf("Expected at least 1 recalculation, got %d", count)
	}
}

// TestRecalculator_DefaultInterval verifies the default interval constant
func TestRecalculator_DefaultInterval(t *testing.T) {
	if DefaultRecalcInterval != 1*time.Hour {
		t.Errorf("DefaultRecalcInterval = %v, want 1h", DefaultRecalcInterval)
	}
}
