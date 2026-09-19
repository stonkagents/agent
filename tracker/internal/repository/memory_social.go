// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: In-memory implementation of SocialRepository for testing

package repository

import (
	"context"
	"sync"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemorySocialRepository is a thread-safe in-memory SocialRepository.
type MemorySocialRepository struct {
	mu    sync.RWMutex
	conns map[string]*models.SocialConnection // key: "accountID:platform"
}

// NewMemorySocialRepository creates a new in-memory social repository.
func NewMemorySocialRepository() *MemorySocialRepository {
	return &MemorySocialRepository{
		conns: make(map[string]*models.SocialConnection),
	}
}

func socialKey(accountID, platform string) string {
	return accountID + ":" + platform
}

func (r *MemorySocialRepository) Insert(_ context.Context, conn *models.SocialConnection) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := socialKey(conn.AccountID, conn.Platform)
	if _, exists := r.conns[key]; exists {
		return models.ErrAlreadyExists
	}

	stored := *conn
	r.conns[key] = &stored
	return nil
}

func (r *MemorySocialRepository) GetByAccountID(_ context.Context, accountID string) ([]*models.SocialConnection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*models.SocialConnection
	for _, conn := range r.conns {
		if conn.AccountID == accountID {
			c := *conn
			result = append(result, &c)
		}
	}
	return result, nil
}

func (r *MemorySocialRepository) GetByAccountAndPlatform(_ context.Context, accountID, platform string) (*models.SocialConnection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	conn, ok := r.conns[socialKey(accountID, platform)]
	if !ok {
		return nil, models.ErrNotFound
	}
	c := *conn
	return &c, nil
}

func (r *MemorySocialRepository) Delete(_ context.Context, accountID, platform string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := socialKey(accountID, platform)
	if _, exists := r.conns[key]; !exists {
		return models.ErrNotFound
	}
	delete(r.conns, key)
	return nil
}
