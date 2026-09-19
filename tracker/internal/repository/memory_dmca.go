// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-06 (DMCA Takedown Endpoint)
// Purpose: In-memory implementation of DMCARepository for MVP/testing

package repository

import (
	"context"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryDMCARepository is a thread-safe in-memory DMCARepository.
type MemoryDMCARepository struct {
	mu      sync.RWMutex
	notices map[string]*models.DMCANotice // keyed by ID
}

// NewMemoryDMCARepository creates a new in-memory DMCA repository.
func NewMemoryDMCARepository() *MemoryDMCARepository {
	return &MemoryDMCARepository{
		notices: make(map[string]*models.DMCANotice),
	}
}

// Create adds a new DMCA notice.
func (r *MemoryDMCARepository) Create(ctx context.Context, notice *models.DMCANotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored := *notice
	r.notices[notice.ID] = &stored
	return nil
}

// FindByID returns a DMCA notice by ID.
func (r *MemoryDMCARepository) FindByID(ctx context.Context, id string) (*models.DMCANotice, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	notice, exists := r.notices[id]
	if !exists {
		return nil, models.ErrNotFound
	}
	result := *notice
	return &result, nil
}

// FindByCID returns all DMCA notices for a given asset CID.
func (r *MemoryDMCARepository) FindByCID(ctx context.Context, cid string) ([]*models.DMCANotice, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*models.DMCANotice, 0)
	for _, n := range r.notices {
		if n.CID == cid {
			cp := *n
			result = append(result, &cp)
		}
	}
	return result, nil
}

// List returns all DMCA notices.
func (r *MemoryDMCARepository) List(ctx context.Context) ([]*models.DMCANotice, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*models.DMCANotice, 0, len(r.notices))
	for _, n := range r.notices {
		cp := *n
		result = append(result, &cp)
	}
	return result, nil
}

// UpdateStatus updates the status of a DMCA notice.
func (r *MemoryDMCARepository) UpdateStatus(ctx context.Context, id string, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	notice, exists := r.notices[id]
	if !exists {
		return models.ErrNotFound
	}
	notice.Status = status
	return nil
}
