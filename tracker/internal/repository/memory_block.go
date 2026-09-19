// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-04 (Rate Limiting)
// Purpose: In-memory implementation of BlockRepository for testing

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryBlockRepository is a thread-safe in-memory BlockRepository.
type MemoryBlockRepository struct {
	mu     sync.RWMutex
	blocks map[string]*models.RegistrationBlock // key: "blockType:blockValue"
}

// NewMemoryBlockRepository creates a new in-memory block repository.
func NewMemoryBlockRepository() *MemoryBlockRepository {
	return &MemoryBlockRepository{
		blocks: make(map[string]*models.RegistrationBlock),
	}
}

func blockKey(blockType, blockValue string) string {
	return blockType + ":" + blockValue
}

func (r *MemoryBlockRepository) IsBlocked(_ context.Context, blockType, blockValue string, now time.Time) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	block, ok := r.blocks[blockKey(blockType, blockValue)]
	if !ok {
		return false, nil
	}
	return block.ExpiresAt.After(now), nil
}

func (r *MemoryBlockRepository) Insert(_ context.Context, block *models.RegistrationBlock) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored := *block
	r.blocks[blockKey(block.BlockType, block.BlockValue)] = &stored
	return nil
}

func (r *MemoryBlockRepository) CleanExpired(_ context.Context, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cleaned := 0
	for key, block := range r.blocks {
		if block.ExpiresAt.Before(now) {
			delete(r.blocks, key)
			cleaned++
		}
	}
	return cleaned, nil
}
