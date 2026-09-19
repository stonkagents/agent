// Package: tracker/internal/repository
// Feature: StonkAgents portal feedback
// Purpose: In-memory FeedbackRepository for tests

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryFeedbackRepository is a thread-safe in-memory FeedbackRepository.
type MemoryFeedbackRepository struct {
	mu    sync.RWMutex
	items []*models.Feedback // ascending id
	next  int64
}

// NewMemoryFeedbackRepository creates an empty in-memory feedback repository.
func NewMemoryFeedbackRepository() *MemoryFeedbackRepository {
	return &MemoryFeedbackRepository{next: 1}
}

// Create appends a submission, assigning the next id and a CreatedAt when unset.
func (r *MemoryFeedbackRepository) Create(_ context.Context, fb *models.Feedback) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	fb.ID = r.next
	r.next++
	if fb.CreatedAt.IsZero() {
		fb.CreatedAt = time.Now().UTC()
	}
	stored := *fb
	r.items = append(r.items, &stored)
	return nil
}

// List returns submissions newest-first (id DESC) honouring BeforeID and Limit.
func (r *MemoryFeedbackRepository) List(_ context.Context, opts ListFeedbackOptions) ([]*models.Feedback, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*models.Feedback{}
	for i := len(r.items) - 1; i >= 0; i-- {
		it := r.items[i]
		if opts.BeforeID > 0 && it.ID >= opts.BeforeID {
			continue
		}
		c := *it
		out = append(out, &c)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}
