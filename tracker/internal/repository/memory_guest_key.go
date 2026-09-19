// Package repository: In-memory implementation of GuestKeyRepository for tests.

package repository

import (
	"context"
	"sync"
)

// MemoryGuestKeyRepository implements GuestKeyRepository in memory.
type MemoryGuestKeyRepository struct {
	mu    sync.Mutex
	keys  map[string]int // api_key -> credits_remaining
	count int
}

// NewMemoryGuestKeyRepository creates a new in-memory guest key repository.
func NewMemoryGuestKeyRepository() *MemoryGuestKeyRepository {
	return &MemoryGuestKeyRepository{keys: make(map[string]int)}
}

// CreateGuestKey creates a new guest API key with the given default credit and returns it.
func (r *MemoryGuestKeyRepository) CreateGuestKey(ctx context.Context, defaultCredits int) (apiKey string, err error) {
	key, err := GenerateAPIKey()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.keys[key] = defaultCredits
	r.count++
	r.mu.Unlock()
	return key, nil
}
