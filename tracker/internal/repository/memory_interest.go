// Package: tracker/internal/repository
// Feature: StonkAgents roadmap interest
// Purpose: In-memory InterestRepository for tests

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryInterestRepository is a thread-safe in-memory InterestRepository.
type MemoryInterestRepository struct {
	mu    sync.RWMutex
	items []*models.AgentInterest // ascending id
	next  int64
}

// NewMemoryInterestRepository creates an empty in-memory interest repository.
func NewMemoryInterestRepository() *MemoryInterestRepository {
	return &MemoryInterestRepository{next: 1}
}

func copyInterest(it *models.AgentInterest) *models.AgentInterest {
	c := *it
	c.Capabilities = append([]string(nil), it.Capabilities...)
	return &c
}

// Create appends a submission, assigning the next id and a CreatedAt when unset.
func (r *MemoryInterestRepository) Create(_ context.Context, it *models.AgentInterest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	it.ID = r.next
	r.next++
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now().UTC()
	}
	r.items = append(r.items, copyInterest(it))
	return nil
}

// List returns submissions newest-first (id DESC) honouring BeforeID and Limit.
func (r *MemoryInterestRepository) List(_ context.Context, opts ListInterestOptions) ([]*models.AgentInterest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*models.AgentInterest{}
	for i := len(r.items) - 1; i >= 0; i-- {
		it := r.items[i]
		if opts.BeforeID > 0 && it.ID >= opts.BeforeID {
			continue
		}
		out = append(out, copyInterest(it))
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

// CountByCapability counts rows containing each requested key (absent keys count 0).
func (r *MemoryInterestRepository) CountByCapability(_ context.Context, keys []string) (map[string]int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]int64{}
	for _, k := range keys {
		out[k] = 0
	}
	for _, it := range r.items {
		for _, c := range it.Capabilities {
			if _, want := out[c]; want {
				out[c]++
			}
		}
	}
	return out, nil
}

// Summary rolls up every row by capability and priority.
func (r *MemoryInterestRepository) Summary(_ context.Context) (*models.InterestSummary, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := models.NewInterestSummary()
	for _, it := range r.items {
		s.Total++
		for _, c := range it.Capabilities {
			s.Capabilities[c]++
		}
		s.Priorities[it.Priority]++
	}
	return s, nil
}
