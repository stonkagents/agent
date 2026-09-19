// Package repository: In-memory implementation of PeerAPIKeyRepository.
// Feature: Peer Forum + API Key Auth

package repository

import (
	"context"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryPeerAPIKeyRepository implements PeerAPIKeyRepository in memory.
type MemoryPeerAPIKeyRepository struct {
	mu     sync.RWMutex
	byPeer map[string]string // peer_id -> api_key
	byKey  map[string]string // api_key -> peer_id
}

// NewMemoryPeerAPIKeyRepository creates a new in-memory peer API key repository.
func NewMemoryPeerAPIKeyRepository() *MemoryPeerAPIKeyRepository {
	return &MemoryPeerAPIKeyRepository{
		byPeer: make(map[string]string),
		byKey:  make(map[string]string),
	}
}

// Create generates and stores an API key for the peer. Returns the key. Call only on first register.
func (r *MemoryPeerAPIKeyRepository) Create(ctx context.Context, peerID string) (apiKey string, err error) {
	key, err := GenerateAPIKey()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byPeer[peerID] = key
	r.byKey[key] = peerID
	return key, nil
}

// GetByAPIKey returns the peer_id for the given API key, or models.ErrNotFound.
func (r *MemoryPeerAPIKeyRepository) GetByAPIKey(ctx context.Context, apiKey string) (peerID string, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pid, ok := r.byKey[apiKey]
	if !ok {
		return "", models.ErrNotFound
	}
	return pid, nil
}

// GetByPeerID returns the api_key for the given peer, or models.ErrNotFound.
func (r *MemoryPeerAPIKeyRepository) GetByPeerID(ctx context.Context, peerID string) (apiKey string, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.byPeer[peerID]
	if !ok {
		return "", models.ErrNotFound
	}
	return key, nil
}

// ExistsForPeer returns true if the peer already has an API key.
func (r *MemoryPeerAPIKeyRepository) ExistsForPeer(ctx context.Context, peerID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byPeer[peerID]
	return ok, nil
}

// Store directly sets a peer_id ↔ api_key mapping (for testing).
func (r *MemoryPeerAPIKeyRepository) Store(peerID, apiKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byPeer[peerID] = apiKey
	r.byKey[apiKey] = peerID
}
