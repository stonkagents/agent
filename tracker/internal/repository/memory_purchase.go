// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase Flow)
// Purpose: In-memory implementation of PurchaseRepository for testing

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryPurchaseRepository is a thread-safe in-memory PurchaseRepository.
type MemoryPurchaseRepository struct {
	mu         sync.RWMutex
	intents    map[string]*models.PurchaseIntent
	signatures map[string]string // txSignature → intentID
}

// NewMemoryPurchaseRepository creates a new in-memory purchase repository.
func NewMemoryPurchaseRepository() *MemoryPurchaseRepository {
	return &MemoryPurchaseRepository{
		intents:    make(map[string]*models.PurchaseIntent),
		signatures: make(map[string]string),
	}
}

func (r *MemoryPurchaseRepository) CreateIntent(_ context.Context, intent *models.PurchaseIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored := *intent
	r.intents[intent.ID] = &stored
	return nil
}

func (r *MemoryPurchaseRepository) GetIntent(_ context.Context, id string) (*models.PurchaseIntent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	intent, ok := r.intents[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	copy := *intent
	return &copy, nil
}

func (r *MemoryPurchaseRepository) MarkVerified(_ context.Context, id, txSignature string, verifiedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	intent, ok := r.intents[id]
	if !ok {
		return models.ErrNotFound
	}
	intent.Status = models.PurchaseIntentVerified
	intent.TxSignature = txSignature
	intent.VerifiedAt = &verifiedAt
	return nil
}

func (r *MemoryPurchaseRepository) ExpireStaleIntents(_ context.Context, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, intent := range r.intents {
		if intent.Status == models.PurchaseIntentPending && intent.ExpiresAt.Before(now) {
			intent.Status = models.PurchaseIntentExpired
			count++
		}
	}
	return count, nil
}

func (r *MemoryPurchaseRepository) HasProcessedSignature(_ context.Context, txSignature string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.signatures[txSignature]
	return ok, nil
}

func (r *MemoryPurchaseRepository) RecordProcessedSignature(_ context.Context, txSignature, intentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.signatures[txSignature]; exists {
		return models.ErrAlreadyExists
	}
	r.signatures[txSignature] = intentID
	return nil
}
