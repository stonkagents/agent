// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-02 (Challenge-Response Registration)
// Purpose: Tests for in-memory NonceRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestNonce(peerID string) *models.RegistrationNonce {
	return &models.RegistrationNonce{
		ID:        "nonce-" + peerID,
		PeerID:    peerID,
		Nonce:     []byte("test-nonce-32-bytes-padding-here"),
		Consumed:  false,
		ExpiresAt: time.Now().Add(5 * time.Minute),
		CreatedAt: time.Now(),
	}
}

func TestMemoryNonceRepo_Upsert_Create(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()
	nonce := newTestNonce("peer-1")

	err := repo.Upsert(ctx, nonce)
	if err != nil {
		t.Fatalf("Upsert() unexpected error: %v", err)
	}

	found, err := repo.GetActiveByPeerID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("GetActiveByPeerID() unexpected error: %v", err)
	}
	if found.ID != "nonce-peer-1" {
		t.Errorf("GetActiveByPeerID() ID = %q, want %q", found.ID, "nonce-peer-1")
	}
}

func TestMemoryNonceRepo_Upsert_Replaces(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()

	nonce1 := newTestNonce("peer-1")
	nonce1.Nonce = []byte("first-nonce-padding-here-32bytes")
	_ = repo.Upsert(ctx, nonce1)

	nonce2 := &models.RegistrationNonce{
		ID:        "nonce-peer-1-v2",
		PeerID:    "peer-1",
		Nonce:     []byte("second-nonce-padding-here32bytes"),
		ExpiresAt: time.Now().Add(5 * time.Minute),
		CreatedAt: time.Now(),
	}
	err := repo.Upsert(ctx, nonce2)
	if err != nil {
		t.Fatalf("Upsert() replace unexpected error: %v", err)
	}

	found, _ := repo.GetActiveByPeerID(ctx, "peer-1")
	if found.ID != "nonce-peer-1-v2" {
		t.Errorf("Upsert() did not replace: got ID = %q, want %q", found.ID, "nonce-peer-1-v2")
	}
}

func TestMemoryNonceRepo_GetActiveByPeerID_NotFound(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()

	_, err := repo.GetActiveByPeerID(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetActiveByPeerID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryNonceRepo_GetActiveByPeerID_ConsumedNotReturned(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()

	nonce := newTestNonce("peer-1")
	_ = repo.Upsert(ctx, nonce)
	_ = repo.MarkConsumed(ctx, "nonce-peer-1")

	_, err := repo.GetActiveByPeerID(ctx, "peer-1")
	if err != models.ErrNotFound {
		t.Errorf("GetActiveByPeerID() consumed nonce should return ErrNotFound, got %v", err)
	}
}

func TestMemoryNonceRepo_MarkConsumed_Success(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestNonce("peer-1"))

	err := repo.MarkConsumed(ctx, "nonce-peer-1")
	if err != nil {
		t.Fatalf("MarkConsumed() unexpected error: %v", err)
	}
}

func TestMemoryNonceRepo_MarkConsumed_NotFound(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()

	err := repo.MarkConsumed(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("MarkConsumed() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryNonceRepo_MarkConsumed_AlreadyConsumed(t *testing.T) {
	repo := NewMemoryNonceRepository()
	ctx := context.Background()

	_ = repo.Upsert(ctx, newTestNonce("peer-1"))
	_ = repo.MarkConsumed(ctx, "nonce-peer-1")

	err := repo.MarkConsumed(ctx, "nonce-peer-1")
	if err != models.ErrNotFound {
		t.Errorf("MarkConsumed() already consumed got error = %v, want ErrNotFound", err)
	}
}
