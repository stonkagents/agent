// Package: tracker/internal/repository
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: TDD tests for in-memory ReputationSnapshotRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/reputation"
)

func TestMemoryReputationSnapshot_UpsertAndFind(t *testing.T) {
	repo := NewMemoryReputationSnapshotRepository()
	ctx := context.Background()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	// Insert a snapshot at now-24h
	_ = repo.Upsert(ctx, &reputation.ReputationSnapshot{
		PeerID:         "peer-a",
		CompositeScore: 0.45,
		SnappedAt:      now.Add(-24 * time.Hour),
	})

	// FindPrevious before now → should find the snapshot
	snap, err := repo.FindPrevious(ctx, "peer-a", now)
	if err != nil {
		t.Fatalf("FindPrevious: %v", err)
	}
	if snap == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if snap.CompositeScore != 0.45 {
		t.Errorf("CompositeScore = %v, want 0.45", snap.CompositeScore)
	}
}

func TestMemoryReputationSnapshot_NoPrevious(t *testing.T) {
	repo := NewMemoryReputationSnapshotRepository()
	ctx := context.Background()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	snap, err := repo.FindPrevious(ctx, "nonexistent", now)
	if err != nil {
		t.Fatalf("FindPrevious: %v", err)
	}
	if snap != nil {
		t.Errorf("expected nil, got %+v", snap)
	}
}

func TestMemoryReputationSnapshot_FindsMostRecent(t *testing.T) {
	repo := NewMemoryReputationSnapshotRepository()
	ctx := context.Background()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	// Insert two snapshots at different times
	_ = repo.Upsert(ctx, &reputation.ReputationSnapshot{
		PeerID:         "peer-b",
		CompositeScore: 0.30,
		SnappedAt:      now.Add(-48 * time.Hour),
	})
	_ = repo.Upsert(ctx, &reputation.ReputationSnapshot{
		PeerID:         "peer-b",
		CompositeScore: 0.50,
		SnappedAt:      now.Add(-24 * time.Hour),
	})

	// FindPrevious before now → should find the more recent one (0.50)
	snap, err := repo.FindPrevious(ctx, "peer-b", now)
	if err != nil {
		t.Fatalf("FindPrevious: %v", err)
	}
	if snap == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if snap.CompositeScore != 0.50 {
		t.Errorf("CompositeScore = %v, want 0.50 (most recent before now)", snap.CompositeScore)
	}
}
