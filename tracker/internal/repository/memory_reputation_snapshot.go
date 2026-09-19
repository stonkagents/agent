// Package: tracker/internal/repository
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: In-memory ReputationSnapshotRepository for testing

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// MemoryReputationSnapshotRepository is an in-memory implementation of ReputationSnapshotRepository.
type MemoryReputationSnapshotRepository struct {
	mu        sync.RWMutex
	snapshots []*reputation.ReputationSnapshot
}

// NewMemoryReputationSnapshotRepository creates a new in-memory reputation snapshot repository.
func NewMemoryReputationSnapshotRepository() *MemoryReputationSnapshotRepository {
	return &MemoryReputationSnapshotRepository{}
}

// Upsert inserts or updates a snapshot for a peer.
func (r *MemoryReputationSnapshotRepository) Upsert(_ context.Context, snapshot *reputation.ReputationSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, snapshot)
	return nil
}

// FindPrevious returns the most recent snapshot before the given time, or nil if none.
func (r *MemoryReputationSnapshotRepository) FindPrevious(_ context.Context, peerID string, before time.Time) (*reputation.ReputationSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var best *reputation.ReputationSnapshot
	for _, s := range r.snapshots {
		if s.PeerID != peerID || !s.SnappedAt.Before(before) {
			continue
		}
		if best == nil || s.SnappedAt.After(best.SnappedAt) {
			best = s
		}
	}
	return best, nil
}
