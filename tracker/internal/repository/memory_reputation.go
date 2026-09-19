// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: In-memory implementation of ReputationRepository for unit/integration tests.
// Production uses PostgresReputationRepository (reputation_scores table).

package repository

import (
	"context"
	"sort"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// ReputationRepository defines the data access contract for reputation records.
type ReputationRepository interface {
	Upsert(ctx context.Context, record *reputation.ReputationRecord) error
	FindByPeerID(ctx context.Context, peerID string) (*reputation.ReputationRecord, error)
	// FindByPeerIDs returns reputation records for the given peer IDs. Missing peers are omitted. (F-032, US-032-01)
	FindByPeerIDs(ctx context.Context, peerIDs []string) (map[string]*reputation.ReputationRecord, error)
	ListByCompositeScore(ctx context.Context, limit, offset int) ([]*reputation.ReputationRecord, error)
	ListAll(ctx context.Context) ([]*reputation.ReputationRecord, error)
	// AverageCompositeScore returns the mean composite score across all peers. Returns 0.0 if no records. (F-004, US-004-01)
	AverageCompositeScore(ctx context.Context) (float64, error)
}

// MemoryReputationRepository is a thread-safe in-memory ReputationRepository.
type MemoryReputationRepository struct {
	mu      sync.RWMutex
	records map[string]*reputation.ReputationRecord
}

// NewMemoryReputationRepository creates a new in-memory reputation repository.
func NewMemoryReputationRepository() *MemoryReputationRepository {
	return &MemoryReputationRepository{
		records: make(map[string]*reputation.ReputationRecord),
	}
}

// Upsert creates or updates a reputation record.
func (r *MemoryReputationRepository) Upsert(ctx context.Context, record *reputation.ReputationRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cp := *record
	r.records[record.PeerID] = &cp
	return nil
}

// FindByPeerID returns a reputation record by peer ID. Returns ErrNotFound if absent.
func (r *MemoryReputationRepository) FindByPeerID(ctx context.Context, peerID string) (*reputation.ReputationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, exists := r.records[peerID]
	if !exists {
		return nil, models.ErrNotFound
	}

	cp := *rec
	return &cp, nil
}

// FindByPeerIDs returns reputation records for the given peer IDs as a map. Missing peers are omitted.
func (r *MemoryReputationRepository) FindByPeerIDs(ctx context.Context, peerIDs []string) (map[string]*reputation.ReputationRecord, error) {
	if len(peerIDs) == 0 {
		return map[string]*reputation.ReputationRecord{}, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]*reputation.ReputationRecord, len(peerIDs))
	for _, id := range peerIDs {
		if rec, exists := r.records[id]; exists {
			cp := *rec
			result[id] = &cp
		}
	}
	return result, nil
}

// ListByCompositeScore returns reputation records sorted by composite_score DESC.
func (r *MemoryReputationRepository) ListByCompositeScore(ctx context.Context, limit, offset int) ([]*reputation.ReputationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*reputation.ReputationRecord, 0, len(r.records))
	for _, rec := range r.records {
		cp := *rec
		all = append(all, &cp)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CompositeScore > all[j].CompositeScore
	})

	start := offset
	if start > len(all) {
		return []*reputation.ReputationRecord{}, nil
	}

	end := len(all)
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	return all[start:end], nil
}

// ListAll returns all reputation records (for batch recalculation).
func (r *MemoryReputationRepository) ListAll(ctx context.Context) ([]*reputation.ReputationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*reputation.ReputationRecord, 0, len(r.records))
	for _, rec := range r.records {
		cp := *rec
		all = append(all, &cp)
	}
	return all, nil
}

// AverageCompositeScore returns the mean composite score across all peers. Returns 0.0 if no records.
func (r *MemoryReputationRepository) AverageCompositeScore(ctx context.Context) (float64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.records) == 0 {
		return 0.0, nil
	}
	var sum float64
	for _, rec := range r.records {
		sum += rec.CompositeScore
	}
	return sum / float64(len(r.records)), nil
}
