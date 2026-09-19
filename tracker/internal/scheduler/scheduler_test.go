// Package: tracker/internal/scheduler
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: Tests for cron scheduler — daily reputation snapshots + weekly tier bonuses (TD-056)

package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// --- Mock dependencies ---

type mockStatsProvider struct {
	peerIDs []string
	stats   map[string]*reputation.PeerStats
}

func (m *mockStatsProvider) GetPeerStats(_ context.Context, peerID string) (*reputation.PeerStats, error) {
	ps, ok := m.stats[peerID]
	if !ok {
		return &reputation.PeerStats{PeerID: peerID}, nil
	}
	return ps, nil
}

func (m *mockStatsProvider) AllPeerIDs(_ context.Context) ([]string, error) {
	return m.peerIDs, nil
}

type mockReputationStore struct {
	mu      sync.Mutex
	records map[string]*reputation.ReputationRecord
}

func newMockReputationStore() *mockReputationStore {
	return &mockReputationStore{records: make(map[string]*reputation.ReputationRecord)}
}

func (m *mockReputationStore) Upsert(_ context.Context, rec *reputation.ReputationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[rec.PeerID] = rec
	return nil
}

func (m *mockReputationStore) FindByPeerID(_ context.Context, peerID string) (*reputation.ReputationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[peerID]
	if !ok {
		return nil, nil
	}
	return r, nil
}

type snapshotEntry struct {
	PeerID         string
	CompositeScore float64
}

type mockSnapshotRepo struct {
	mu        sync.Mutex
	snapshots []snapshotEntry
}

func (m *mockSnapshotRepo) Upsert(_ context.Context, s *reputation.ReputationSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots = append(m.snapshots, snapshotEntry{PeerID: s.PeerID, CompositeScore: s.CompositeScore})
	return nil
}

func (m *mockSnapshotRepo) FindPrevious(_ context.Context, peerID string, before time.Time) (*reputation.ReputationSnapshot, error) {
	return nil, nil
}

type creditGrant struct {
	AccountID string
	Amount    int
	Reason    string
}

type mockAccountRepo struct {
	accounts map[string]*models.Account // keyed by peer_id
}

func (m *mockAccountRepo) GetByPeerID(_ context.Context, peerID string) (*models.Account, error) {
	a, ok := m.accounts[peerID]
	if !ok {
		return nil, nil
	}
	return a, nil
}

type mockCreditRepo struct {
	mu     sync.Mutex
	grants []creditGrant
}

func (m *mockCreditRepo) CreditFree(_ context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants = append(m.grants, creditGrant{AccountID: accountID, Amount: amount, Reason: reason})
	return nil
}

// --- Tests ---

func TestDailySnapshot_SnapshotsAllPeers(t *testing.T) {
	stats := &mockStatsProvider{
		peerIDs: []string{"peer-a", "peer-b"},
		stats: map[string]*reputation.PeerStats{
			"peer-a": {PeerID: "peer-a", UploadBytes: 8000, DownloadBytes: 2000, TotalDownloads: 100},
			"peer-b": {PeerID: "peer-b", UploadBytes: 3000, DownloadBytes: 1000, TotalDownloads: 50},
		},
	}
	repStore := newMockReputationStore()
	snapRepo := &mockSnapshotRepo{}

	sched := New(Config{
		StatsProvider: stats,
		RepStore:      repStore,
		SnapshotRepo:  snapRepo,
	})

	err := sched.RunDailySnapshot(context.Background())
	if err != nil {
		t.Fatalf("RunDailySnapshot error: %v", err)
	}

	if len(snapRepo.snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapRepo.snapshots))
	}

	seen := map[string]bool{}
	for _, s := range snapRepo.snapshots {
		seen[s.PeerID] = true
		if s.CompositeScore <= 0 {
			t.Errorf("peer %s: expected positive composite score, got %f", s.PeerID, s.CompositeScore)
		}
	}
	if !seen["peer-a"] || !seen["peer-b"] {
		t.Error("not all peers were snapshotted")
	}
}

func TestWeeklyBonus_GrantsCorrectAmounts(t *testing.T) {
	stats := &mockStatsProvider{
		peerIDs: []string{"peer-og", "peer-gold", "peer-silver", "peer-bronze", "peer-new"},
		stats: map[string]*reputation.PeerStats{
			"peer-og":     {PeerID: "peer-og", UploadBytes: 10000, DownloadBytes: 5000, TotalDownloads: 500},
			"peer-gold":   {PeerID: "peer-gold", UploadBytes: 7000, DownloadBytes: 3000, TotalDownloads: 200},
			"peer-silver": {PeerID: "peer-silver", UploadBytes: 3000, DownloadBytes: 2000, TotalDownloads: 50},
			"peer-bronze": {PeerID: "peer-bronze", UploadBytes: 1000, DownloadBytes: 500, TotalDownloads: 10},
			"peer-new":    {PeerID: "peer-new", UploadBytes: 0, DownloadBytes: 0, TotalDownloads: 0},
		},
	}

	// Pre-populate reputation store with scores that yield known tiers
	repStore := newMockReputationStore()
	repStore.records["peer-og"] = &reputation.ReputationRecord{PeerID: "peer-og", CompositeScore: 0.85}
	repStore.records["peer-gold"] = &reputation.ReputationRecord{PeerID: "peer-gold", CompositeScore: 0.65}
	repStore.records["peer-silver"] = &reputation.ReputationRecord{PeerID: "peer-silver", CompositeScore: 0.45}
	repStore.records["peer-bronze"] = &reputation.ReputationRecord{PeerID: "peer-bronze", CompositeScore: 0.25}
	repStore.records["peer-new"] = &reputation.ReputationRecord{PeerID: "peer-new", CompositeScore: 0.10}

	accountRepo := &mockAccountRepo{
		accounts: map[string]*models.Account{
			"peer-og":     {ID: "acc-og", PeerID: "peer-og"},
			"peer-gold":   {ID: "acc-gold", PeerID: "peer-gold"},
			"peer-silver": {ID: "acc-silver", PeerID: "peer-silver"},
			"peer-bronze": {ID: "acc-bronze", PeerID: "peer-bronze"},
			"peer-new":    {ID: "acc-new", PeerID: "peer-new"},
		},
	}
	creditRepo := &mockCreditRepo{}

	sched := New(Config{
		StatsProvider: stats,
		RepStore:      repStore,
		AccountRepo:   accountRepo,
		CreditRepo:    creditRepo,
	})

	err := sched.RunWeeklyBonus(context.Background())
	if err != nil {
		t.Fatalf("RunWeeklyBonus error: %v", err)
	}

	// Expect grants for og(75), gold(50), silver(20) — not bronze or new
	if len(creditRepo.grants) != 3 {
		t.Fatalf("expected 3 grants, got %d: %+v", len(creditRepo.grants), creditRepo.grants)
	}

	grantByAccount := map[string]int{}
	for _, g := range creditRepo.grants {
		grantByAccount[g.AccountID] = g.Amount
	}

	tests := []struct {
		accountID string
		want      int
	}{
		{"acc-og", 75},
		{"acc-gold", 50},
		{"acc-silver", 20},
	}
	for _, tt := range tests {
		got, ok := grantByAccount[tt.accountID]
		if !ok {
			t.Errorf("expected grant for %s, got none", tt.accountID)
		} else if got != tt.want {
			t.Errorf("%s: expected %d credits, got %d", tt.accountID, tt.want, got)
		}
	}
}

func TestWeeklyBonus_SkipsPeersWithoutAccount(t *testing.T) {
	stats := &mockStatsProvider{
		peerIDs: []string{"peer-og"},
		stats: map[string]*reputation.PeerStats{
			"peer-og": {PeerID: "peer-og", UploadBytes: 10000, DownloadBytes: 5000, TotalDownloads: 500},
		},
	}
	repStore := newMockReputationStore()
	repStore.records["peer-og"] = &reputation.ReputationRecord{PeerID: "peer-og", CompositeScore: 0.85}

	// No account for this peer
	accountRepo := &mockAccountRepo{accounts: map[string]*models.Account{}}
	creditRepo := &mockCreditRepo{}

	sched := New(Config{
		StatsProvider: stats,
		RepStore:      repStore,
		AccountRepo:   accountRepo,
		CreditRepo:    creditRepo,
	})

	err := sched.RunWeeklyBonus(context.Background())
	if err != nil {
		t.Fatalf("RunWeeklyBonus error: %v", err)
	}

	if len(creditRepo.grants) != 0 {
		t.Errorf("expected 0 grants for peer without account, got %d", len(creditRepo.grants))
	}
}
