// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: In-memory implementation of AccountRepository for testing

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryAccountRepository is a thread-safe in-memory AccountRepository.
type MemoryAccountRepository struct {
	mu       sync.RWMutex
	byID     map[string]*models.Account
	byPeerID map[string]*models.Account
}

// NewMemoryAccountRepository creates a new in-memory account repository.
func NewMemoryAccountRepository() *MemoryAccountRepository {
	return &MemoryAccountRepository{
		byID:     make(map[string]*models.Account),
		byPeerID: make(map[string]*models.Account),
	}
}

func (r *MemoryAccountRepository) Create(_ context.Context, account *models.Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byPeerID[account.PeerID]; exists {
		return models.ErrAlreadyExists
	}

	stored := *account
	r.byID[account.ID] = &stored
	r.byPeerID[account.PeerID] = &stored
	return nil
}

func (r *MemoryAccountRepository) GetByID(_ context.Context, id string) (*models.Account, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	acc, ok := r.byID[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	copy := *acc
	return &copy, nil
}

func (r *MemoryAccountRepository) GetByPeerID(_ context.Context, peerID string) (*models.Account, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	acc, ok := r.byPeerID[peerID]
	if !ok {
		return nil, models.ErrNotFound
	}
	copy := *acc
	return &copy, nil
}

func (r *MemoryAccountRepository) GetOrCreateForPeer(ctx context.Context, peerID string) (*models.Account, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if acc, ok := r.byPeerID[peerID]; ok {
		copy := *acc
		return &copy, false, nil
	}
	acc := &models.Account{PeerID: peerID, Status: models.AccountStatusActive}
	acc.ID = "acc-" + peerID
	acc.CreatedAt = time.Now().UTC()
	stored := *acc
	r.byID[acc.ID] = &stored
	r.byPeerID[acc.PeerID] = &stored
	return acc, true, nil
}

func (r *MemoryAccountRepository) UpdateStatus(_ context.Context, id, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acc, ok := r.byID[id]
	if !ok {
		return models.ErrNotFound
	}
	acc.Status = status
	return nil
}
