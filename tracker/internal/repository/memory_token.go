// Package: tracker/internal/repository
// Feature: F-031 (Token Data Persistence)
// Story: US-031-01 (Backend Token Persistence)
// Purpose: In-memory implementation of TokenRepository for testing

package repository

import (
	"context"
	"sort"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryTokenRepository is a thread-safe in-memory TokenRepository.
type MemoryTokenRepository struct {
	mu             sync.RWMutex
	byPeerID       map[string]*models.PeerToken
	byContractAddr map[string]*models.PeerToken
}

// NewMemoryTokenRepository creates a new in-memory token repository.
func NewMemoryTokenRepository() *MemoryTokenRepository {
	return &MemoryTokenRepository{
		byPeerID:       make(map[string]*models.PeerToken),
		byContractAddr: make(map[string]*models.PeerToken),
	}
}

// Create persists a new peer token. Returns ErrAlreadyExists if peer_id or contract address already exists.
func (r *MemoryTokenRepository) Create(ctx context.Context, token *models.PeerToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byPeerID[token.PeerID]; exists {
		return models.ErrAlreadyExists
	}
	if _, exists := r.byContractAddr[token.TokenContractAddress]; exists {
		return models.ErrAlreadyExists
	}

	stored := *token
	r.byPeerID[stored.PeerID] = &stored
	r.byContractAddr[stored.TokenContractAddress] = &stored
	return nil
}

// GetByPeerID returns the token for a peer, or ErrNotFound.
func (r *MemoryTokenRepository) GetByPeerID(ctx context.Context, peerID string) (*models.PeerToken, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	token, exists := r.byPeerID[peerID]
	if !exists {
		return nil, models.ErrNotFound
	}
	result := *token
	return &result, nil
}

// GetByContractAddress returns the token with this contract address, or ErrNotFound.
func (r *MemoryTokenRepository) GetByContractAddress(ctx context.Context, contractAddr string) (*models.PeerToken, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	token, exists := r.byContractAddr[contractAddr]
	if !exists {
		return nil, models.ErrNotFound
	}
	result := *token
	return &result, nil
}

// List returns peer tokens ordered by launched_at DESC with pagination.
func (r *MemoryTokenRepository) List(ctx context.Context, limit, offset int) ([]*models.PeerToken, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Collect all tokens into a slice
	all := make([]*models.PeerToken, 0, len(r.byPeerID))
	for _, t := range r.byPeerID {
		copied := *t
		all = append(all, &copied)
	}

	// Sort by LaunchedAt DESC (newest first)
	sort.Slice(all, func(i, j int) bool {
		return all[i].LaunchedAt.After(all[j].LaunchedAt)
	})

	total := len(all)

	// Apply offset
	if offset >= total {
		return []*models.PeerToken{}, total, nil
	}
	all = all[offset:]

	// Apply limit
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}

	return all, total, nil
}

// Upsert inserts the token or replaces the peer's existing row.
// Returns ErrAlreadyExists if the contract address belongs to a different peer.
func (r *MemoryTokenRepository) Upsert(ctx context.Context, token *models.PeerToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if other, exists := r.byContractAddr[token.TokenContractAddress]; exists && other.PeerID != token.PeerID {
		return models.ErrAlreadyExists
	}
	if prev, exists := r.byPeerID[token.PeerID]; exists {
		delete(r.byContractAddr, prev.TokenContractAddress)
	}
	stored := *token
	r.byPeerID[stored.PeerID] = &stored
	r.byContractAddr[stored.TokenContractAddress] = &stored
	return nil
}

// UpdateImageURL sets the token_image_url for a peer's token (write-once: skips if already set).
func (r *MemoryTokenRepository) UpdateImageURL(ctx context.Context, peerID, imageURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	token, exists := r.byPeerID[peerID]
	if !exists {
		return models.ErrNotFound
	}
	// Write-once: only set if currently empty (matches Postgres WHERE token_image_url IS NULL)
	if token.TokenImageURL == "" {
		token.TokenImageURL = imageURL
	}
	return nil
}
