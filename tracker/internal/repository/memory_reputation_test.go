// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: TDD tests for in-memory reputation repository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/reputation"
)

func newTestRecord(peerID string, composite float64) *reputation.ReputationRecord {
	return &reputation.ReputationRecord{
		PeerID:           peerID,
		BandwidthScore:   0.8,
		QualityScore:     0.9,
		SecurityScore:    1.0,
		CitizenshipScore: 0.9,
		CompositeScore:   composite,
		UpdatedAt:        time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC),
	}
}

// =============================================================================
// Upsert + FindByPeerID Tests
// =============================================================================

func TestMemoryReputationRepo_Upsert_New(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()
	record := newTestRecord("peer-1", 0.88)

	err := repo.Upsert(ctx, record)
	if err != nil {
		t.Fatalf("Upsert() error: %v", err)
	}

	found, err := repo.FindByPeerID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("FindByPeerID() error: %v", err)
	}
	if found.PeerID != "peer-1" {
		t.Errorf("PeerID = %q, want %q", found.PeerID, "peer-1")
	}
	if found.CompositeScore != 0.88 {
		t.Errorf("CompositeScore = %f, want 0.88", found.CompositeScore)
	}
}

func TestMemoryReputationRepo_Upsert_UpdateExisting(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.5))
	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.9))

	found, err := repo.FindByPeerID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("FindByPeerID() error: %v", err)
	}
	if found.CompositeScore != 0.9 {
		t.Errorf("CompositeScore = %f, want 0.9 (updated)", found.CompositeScore)
	}
}

func TestMemoryReputationRepo_FindByPeerID_NotFound(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_, err := repo.FindByPeerID(ctx, "nonexistent")
	if err == nil {
		t.Fatal("FindByPeerID() expected error for nonexistent peer")
	}
}

// =============================================================================
// ListByCompositeScore Tests (sorted DESC)
// =============================================================================

func TestMemoryReputationRepo_ListByCompositeScore_SortedDesc(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("low", 0.3))
	_ = repo.Upsert(ctx, newTestRecord("high", 0.9))
	_ = repo.Upsert(ctx, newTestRecord("mid", 0.6))

	records, err := repo.ListByCompositeScore(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListByCompositeScore() error: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("ListByCompositeScore() got %d records, want 3", len(records))
	}

	// Verify DESC order
	if records[0].PeerID != "high" {
		t.Errorf("records[0].PeerID = %q, want %q", records[0].PeerID, "high")
	}
	if records[1].PeerID != "mid" {
		t.Errorf("records[1].PeerID = %q, want %q", records[1].PeerID, "mid")
	}
	if records[2].PeerID != "low" {
		t.Errorf("records[2].PeerID = %q, want %q", records[2].PeerID, "low")
	}
}

func TestMemoryReputationRepo_ListByCompositeScore_Limit(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("a", 0.9))
	_ = repo.Upsert(ctx, newTestRecord("b", 0.8))
	_ = repo.Upsert(ctx, newTestRecord("c", 0.7))

	records, err := repo.ListByCompositeScore(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListByCompositeScore() error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("ListByCompositeScore(limit=2) got %d records, want 2", len(records))
	}
}

func TestMemoryReputationRepo_ListByCompositeScore_Offset(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("a", 0.9))
	_ = repo.Upsert(ctx, newTestRecord("b", 0.8))
	_ = repo.Upsert(ctx, newTestRecord("c", 0.7))

	records, err := repo.ListByCompositeScore(ctx, 10, 1)
	if err != nil {
		t.Fatalf("ListByCompositeScore() error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("ListByCompositeScore(offset=1) got %d records, want 2", len(records))
	}
	if records[0].PeerID != "b" {
		t.Errorf("records[0].PeerID = %q, want %q (skipped highest)", records[0].PeerID, "b")
	}
}

func TestMemoryReputationRepo_ListByCompositeScore_Empty(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	records, err := repo.ListByCompositeScore(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListByCompositeScore() error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("ListByCompositeScore() got %d records, want 0", len(records))
	}
}

// =============================================================================
// ListAll Tests (for batch recalculation)
// =============================================================================

func TestMemoryReputationRepo_ListAll(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("a", 0.5))
	_ = repo.Upsert(ctx, newTestRecord("b", 0.7))

	records, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll() error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("ListAll() got %d records, want 2", len(records))
	}
}

// =============================================================================
// Isolation Tests
// =============================================================================

func TestMemoryReputationRepo_ReturnsCopy(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.5))

	found, _ := repo.FindByPeerID(ctx, "peer-1")
	found.CompositeScore = 999.0

	original, _ := repo.FindByPeerID(ctx, "peer-1")
	if original.CompositeScore == 999.0 {
		t.Error("FindByPeerID returned mutable reference instead of copy")
	}
}

// =============================================================================
// F-032: FindByPeerIDs Batch Tests
// =============================================================================

func TestMemoryReputationRepo_FindByPeerIDs_PartialMatch(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.8))
	_ = repo.Upsert(ctx, newTestRecord("peer-2", 0.6))

	result, err := repo.FindByPeerIDs(ctx, []string{"peer-1", "peer-2", "peer-3"})
	if err != nil {
		t.Fatalf("FindByPeerIDs() error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("FindByPeerIDs() got %d entries, want 2", len(result))
	}
	if result["peer-1"] == nil || result["peer-1"].CompositeScore != 0.8 {
		t.Errorf("FindByPeerIDs() peer-1 score = %v, want 0.8", result["peer-1"])
	}
	if result["peer-2"] == nil || result["peer-2"].CompositeScore != 0.6 {
		t.Errorf("FindByPeerIDs() peer-2 score = %v, want 0.6", result["peer-2"])
	}
	if result["peer-3"] != nil {
		t.Error("FindByPeerIDs() peer-3 should not be in result")
	}
}

func TestMemoryReputationRepo_FindByPeerIDs_EmptySlice(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.8))

	result, err := repo.FindByPeerIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("FindByPeerIDs() error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("FindByPeerIDs(empty) got %d entries, want 0", len(result))
	}
}

func TestMemoryReputationRepo_FindByPeerIDs_NoMatches(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.8))

	result, err := repo.FindByPeerIDs(ctx, []string{"peer-x", "peer-y"})
	if err != nil {
		t.Fatalf("FindByPeerIDs() error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("FindByPeerIDs(no matches) got %d entries, want 0", len(result))
	}
}

func TestMemoryReputationRepo_FindByPeerIDs_ReturnsCopies(t *testing.T) {
	repo := NewMemoryReputationRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestRecord("peer-1", 0.8))

	result, _ := repo.FindByPeerIDs(ctx, []string{"peer-1"})
	result["peer-1"].CompositeScore = 999.0

	original, _ := repo.FindByPeerID(ctx, "peer-1")
	if original.CompositeScore == 999.0 {
		t.Error("FindByPeerIDs returned mutable reference instead of copy")
	}
}
