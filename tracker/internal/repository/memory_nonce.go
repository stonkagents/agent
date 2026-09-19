// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-02 (Challenge-Response Registration)
// Purpose: In-memory implementation of NonceRepository for testing

package repository

import (
	"context"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryNonceRepository is a thread-safe in-memory NonceRepository.
type MemoryNonceRepository struct {
	mu     sync.RWMutex
	byID   map[string]*models.RegistrationNonce
	byPeer map[string]*models.RegistrationNonce // active (unconsumed) nonce per peer
}

// NewMemoryNonceRepository creates a new in-memory nonce repository.
func NewMemoryNonceRepository() *MemoryNonceRepository {
	return &MemoryNonceRepository{
		byID:   make(map[string]*models.RegistrationNonce),
		byPeer: make(map[string]*models.RegistrationNonce),
	}
}

func (r *MemoryNonceRepository) Upsert(_ context.Context, nonce *models.RegistrationNonce) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove any existing active nonce for this peer
	if existing, ok := r.byPeer[nonce.PeerID]; ok {
		delete(r.byID, existing.ID)
	}

	stored := *nonce
	stored.Nonce = make([]byte, len(nonce.Nonce))
	copy(stored.Nonce, nonce.Nonce)

	r.byID[nonce.ID] = &stored
	r.byPeer[nonce.PeerID] = &stored
	return nil
}

func (r *MemoryNonceRepository) GetActiveByPeerID(_ context.Context, peerID string) (*models.RegistrationNonce, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nonce, ok := r.byPeer[peerID]
	if !ok || nonce.Consumed {
		return nil, models.ErrNotFound
	}
	result := *nonce
	result.Nonce = make([]byte, len(nonce.Nonce))
	copy(result.Nonce, nonce.Nonce)
	return &result, nil
}

func (r *MemoryNonceRepository) MarkConsumed(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	nonce, ok := r.byID[id]
	if !ok || nonce.Consumed {
		return models.ErrNotFound
	}
	nonce.Consumed = true
	delete(r.byPeer, nonce.PeerID)
	return nil
}
