// Package repository: In-memory implementation of PeerTrustBlockRepository for testing.

package repository

import (
	"context"
	"sync"
)

// MemoryPeerTrustBlockRepository is a thread-safe in-memory implementation.
type MemoryPeerTrustBlockRepository struct {
	mu     sync.RWMutex
	trust  map[string]map[string]struct{} // actor -> set of targets
	blocks map[string]map[string]struct{} // actor -> set of targets
}

// NewMemoryPeerTrustBlockRepository creates a new in-memory peer trust/block repository.
func NewMemoryPeerTrustBlockRepository() *MemoryPeerTrustBlockRepository {
	return &MemoryPeerTrustBlockRepository{
		trust:  make(map[string]map[string]struct{}),
		blocks: make(map[string]map[string]struct{}),
	}
}

// Trust adds actor->target trust.
func (r *MemoryPeerTrustBlockRepository) Trust(ctx context.Context, actorPeerID, targetPeerID string) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.trust[actorPeerID] == nil {
		r.trust[actorPeerID] = make(map[string]struct{})
	}
	r.trust[actorPeerID][targetPeerID] = struct{}{}
	return nil
}

// Untrust removes actor->target trust.
func (r *MemoryPeerTrustBlockRepository) Untrust(ctx context.Context, actorPeerID, targetPeerID string) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.trust[actorPeerID]; m != nil {
		delete(m, targetPeerID)
	}
	return nil
}

// Block adds actor->target block.
func (r *MemoryPeerTrustBlockRepository) Block(ctx context.Context, actorPeerID, targetPeerID string) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocks[actorPeerID] == nil {
		r.blocks[actorPeerID] = make(map[string]struct{})
	}
	r.blocks[actorPeerID][targetPeerID] = struct{}{}
	return nil
}

// Unblock removes actor->target block.
func (r *MemoryPeerTrustBlockRepository) Unblock(ctx context.Context, actorPeerID, targetPeerID string) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.blocks[actorPeerID]; m != nil {
		delete(m, targetPeerID)
	}
	return nil
}

// IsTrusted returns true if actor has trusted target.
func (r *MemoryPeerTrustBlockRepository) IsTrusted(ctx context.Context, actorPeerID, targetPeerID string) (bool, error) {
	_ = ctx
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.trust[actorPeerID][targetPeerID]
	return ok, nil
}

// IsBlocked returns true if actor has blocked target.
func (r *MemoryPeerTrustBlockRepository) IsBlocked(ctx context.Context, actorPeerID, targetPeerID string) (bool, error) {
	_ = ctx
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.blocks[actorPeerID][targetPeerID]
	return ok, nil
}

// TrustedByActor returns peer IDs that actor has trusted.
func (r *MemoryPeerTrustBlockRepository) TrustedByActor(ctx context.Context, actorPeerID string) ([]string, error) {
	_ = ctx
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := r.trust[actorPeerID]
	if m == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	return ids, nil
}

// BlockedByActor returns peer IDs that actor has blocked.
func (r *MemoryPeerTrustBlockRepository) BlockedByActor(ctx context.Context, actorPeerID string) ([]string, error) {
	_ = ctx
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := r.blocks[actorPeerID]
	if m == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	return ids, nil
}

// CountTrustsReceived returns how many distinct actors have trusted the target peer. (F-032, US-032-02)
func (r *MemoryPeerTrustBlockRepository) CountTrustsReceived(ctx context.Context, targetPeerID string) (int, error) {
	_ = ctx
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, targets := range r.trust {
		if _, ok := targets[targetPeerID]; ok {
			count++
		}
	}
	return count, nil
}
