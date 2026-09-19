// Package: tracker/internal/repository
// Feature: StonkAgents devnet drip
// Purpose: In-memory DevDripRepository for tests

package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryDevDripRepository is a thread-safe in-memory DevDripRepository.
type MemoryDevDripRepository struct {
	mu    sync.RWMutex
	items map[string]*models.DevDrip
}

// NewMemoryDevDripRepository creates an empty repository.
func NewMemoryDevDripRepository() *MemoryDevDripRepository {
	return &MemoryDevDripRepository{items: map[string]*models.DevDrip{}}
}

// Get returns a copy of the wallet's row or models.ErrNotFound.
func (r *MemoryDevDripRepository) Get(_ context.Context, wallet string) (*models.DevDrip, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.items[wallet]
	if !ok {
		return nil, models.ErrNotFound
	}
	c := *d
	return &c, nil
}

// Upsert stores a copy of drip keyed by wallet.
func (r *MemoryDevDripRepository) Upsert(_ context.Context, drip *models.DevDrip) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := *drip
	r.items[drip.Wallet] = &c
	return nil
}

// ListByIPSince returns dripped_at times for ipHash at or after since, oldest first.
func (r *MemoryDevDripRepository) ListByIPSince(_ context.Context, ipHash string, since time.Time) ([]time.Time, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []time.Time{}
	for _, d := range r.items {
		if d.IPHash == ipHash && !d.DrippedAt.Before(since) {
			out = append(out, d.DrippedAt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

// Len reports the number of rows (tests).
func (r *MemoryDevDripRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}
