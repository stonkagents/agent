// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase Flow)
// Purpose: Tests for in-memory PurchaseRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestIntent(id, accountID string) *models.PurchaseIntent {
	return &models.PurchaseIntent{
		ID:             id,
		AccountID:      accountID,
		AmountLamports: 100000000,
		CreditAmount:   100,
		Status:         models.PurchaseIntentPending,
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(15 * time.Minute),
	}
}

func TestMemoryPurchaseRepo_CreateIntent_Success(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	intent := newTestIntent("intent-1", "acc-1")
	err := repo.CreateIntent(ctx, intent)
	if err != nil {
		t.Fatalf("CreateIntent() unexpected error: %v", err)
	}

	found, err := repo.GetIntent(ctx, "intent-1")
	if err != nil {
		t.Fatalf("GetIntent() unexpected error: %v", err)
	}
	if found.CreditAmount != 100 {
		t.Errorf("GetIntent() CreditAmount = %d, want 100", found.CreditAmount)
	}
	if found.Status != models.PurchaseIntentPending {
		t.Errorf("GetIntent() Status = %q, want %q", found.Status, models.PurchaseIntentPending)
	}
}

func TestMemoryPurchaseRepo_GetIntent_NotFound(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	_, err := repo.GetIntent(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetIntent() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryPurchaseRepo_MarkVerified(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	_ = repo.CreateIntent(ctx, newTestIntent("intent-1", "acc-1"))

	now := time.Now()
	err := repo.MarkVerified(ctx, "intent-1", "5Jz...txsig", now)
	if err != nil {
		t.Fatalf("MarkVerified() unexpected error: %v", err)
	}

	found, _ := repo.GetIntent(ctx, "intent-1")
	if found.Status != models.PurchaseIntentVerified {
		t.Errorf("MarkVerified() Status = %q, want %q", found.Status, models.PurchaseIntentVerified)
	}
	if found.TxSignature != "5Jz...txsig" {
		t.Errorf("MarkVerified() TxSignature = %q, want %q", found.TxSignature, "5Jz...txsig")
	}
}

func TestMemoryPurchaseRepo_MarkVerified_NotFound(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	err := repo.MarkVerified(ctx, "nonexistent", "sig", time.Now())
	if err != models.ErrNotFound {
		t.Errorf("MarkVerified() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryPurchaseRepo_ExpireStaleIntents(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	// Expired intent
	expired := newTestIntent("intent-1", "acc-1")
	expired.ExpiresAt = time.Now().Add(-1 * time.Hour)
	_ = repo.CreateIntent(ctx, expired)

	// Active intent
	_ = repo.CreateIntent(ctx, newTestIntent("intent-2", "acc-1"))

	count, err := repo.ExpireStaleIntents(ctx, time.Now())
	if err != nil {
		t.Fatalf("ExpireStaleIntents() unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("ExpireStaleIntents() = %d, want 1", count)
	}

	found, _ := repo.GetIntent(ctx, "intent-1")
	if found.Status != models.PurchaseIntentExpired {
		t.Errorf("Expired intent Status = %q, want %q", found.Status, models.PurchaseIntentExpired)
	}
}

func TestMemoryPurchaseRepo_ProcessedSignature(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	_ = repo.CreateIntent(ctx, newTestIntent("intent-1", "acc-1"))

	// Not processed yet
	has, err := repo.HasProcessedSignature(ctx, "5Jz...txsig")
	if err != nil {
		t.Fatalf("HasProcessedSignature() unexpected error: %v", err)
	}
	if has {
		t.Error("HasProcessedSignature() = true, want false")
	}

	// Record it
	err = repo.RecordProcessedSignature(ctx, "5Jz...txsig", "intent-1")
	if err != nil {
		t.Fatalf("RecordProcessedSignature() unexpected error: %v", err)
	}

	// Now exists
	has, _ = repo.HasProcessedSignature(ctx, "5Jz...txsig")
	if !has {
		t.Error("HasProcessedSignature() = false, want true")
	}
}

func TestMemoryPurchaseRepo_ProcessedSignature_Duplicate(t *testing.T) {
	repo := NewMemoryPurchaseRepository()
	ctx := context.Background()

	_ = repo.CreateIntent(ctx, newTestIntent("intent-1", "acc-1"))
	_ = repo.RecordProcessedSignature(ctx, "5Jz...txsig", "intent-1")

	err := repo.RecordProcessedSignature(ctx, "5Jz...txsig", "intent-1")
	if err != models.ErrAlreadyExists {
		t.Errorf("RecordProcessedSignature() duplicate got error = %v, want ErrAlreadyExists", err)
	}
}
