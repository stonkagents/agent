// Package: tracker/internal/repository
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: In-memory PeerEventRepository for testing

package repository

import (
	"context"
	"sort"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryPeerEventRepository is an in-memory implementation of PeerEventRepository.
type MemoryPeerEventRepository struct {
	mu     sync.RWMutex
	events []*models.PeerEvent
}

// NewMemoryPeerEventRepository creates a new in-memory peer event repository.
func NewMemoryPeerEventRepository() *MemoryPeerEventRepository {
	return &MemoryPeerEventRepository{}
}

// Insert adds a new peer event.
func (r *MemoryPeerEventRepository) Insert(_ context.Context, event *models.PeerEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

// ListByPeerID returns events for a peer, ordered by CreatedAt DESC. Returns (events, total, error).
func (r *MemoryPeerEventRepository) ListByPeerID(_ context.Context, peerID string, limit, offset int) ([]*models.PeerEvent, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Filter by peerID
	var filtered []*models.PeerEvent
	for _, e := range r.events {
		if e.PeerID == peerID {
			filtered = append(filtered, e)
		}
	}

	// Sort DESC by CreatedAt
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	total := len(filtered)

	// Apply offset
	if offset >= len(filtered) {
		return []*models.PeerEvent{}, total, nil
	}
	filtered = filtered[offset:]

	// Apply limit
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}

	return filtered, total, nil
}

// CountByPeerIDAndAction returns the count of events for a peer with the given action.
func (r *MemoryPeerEventRepository) CountByPeerIDAndAction(_ context.Context, peerID, action string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, e := range r.events {
		if e.PeerID == peerID && e.Action == action {
			count++
		}
	}
	return count, nil
}
