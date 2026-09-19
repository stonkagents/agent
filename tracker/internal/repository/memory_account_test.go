// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: Tests for in-memory AccountRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestAccount(peerID string) *models.Account {
	return &models.Account{
		ID:        "acc-" + peerID,
		PeerID:    peerID,
		Status:    models.AccountStatusActive,
		CreatedAt: time.Now(),
	}
}

func TestMemoryAccountRepo_Create_Success(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()
	acc := newTestAccount("peer-1")

	err := repo.Create(ctx, acc)
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}

	found, err := repo.GetByID(ctx, "acc-peer-1")
	if err != nil {
		t.Fatalf("GetByID() unexpected error: %v", err)
	}
	if found.PeerID != "peer-1" {
		t.Errorf("GetByID() PeerID = %q, want %q", found.PeerID, "peer-1")
	}
	if found.Status != models.AccountStatusActive {
		t.Errorf("GetByID() Status = %q, want %q", found.Status, models.AccountStatusActive)
	}
}

func TestMemoryAccountRepo_Create_DuplicatePeerID(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAccount("peer-1"))
	err := repo.Create(ctx, newTestAccount("peer-1"))

	if err != models.ErrAlreadyExists {
		t.Errorf("Create() duplicate got error = %v, want ErrAlreadyExists", err)
	}
}

func TestMemoryAccountRepo_GetByID_NotFound(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetByID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryAccountRepo_GetByPeerID_Success(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()
	_ = repo.Create(ctx, newTestAccount("peer-1"))

	found, err := repo.GetByPeerID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("GetByPeerID() unexpected error: %v", err)
	}
	if found.ID != "acc-peer-1" {
		t.Errorf("GetByPeerID() ID = %q, want %q", found.ID, "acc-peer-1")
	}
}

func TestMemoryAccountRepo_GetByPeerID_NotFound(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()

	_, err := repo.GetByPeerID(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetByPeerID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryAccountRepo_UpdateStatus(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()
	_ = repo.Create(ctx, newTestAccount("peer-1"))

	err := repo.UpdateStatus(ctx, "acc-peer-1", models.AccountStatusRecovered)
	if err != nil {
		t.Fatalf("UpdateStatus() unexpected error: %v", err)
	}

	found, _ := repo.GetByID(ctx, "acc-peer-1")
	if found.Status != models.AccountStatusRecovered {
		t.Errorf("UpdateStatus() Status = %q, want %q", found.Status, models.AccountStatusRecovered)
	}
}

func TestMemoryAccountRepo_UpdateStatus_NotFound(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()

	err := repo.UpdateStatus(ctx, "nonexistent", models.AccountStatusSuspended)
	if err != models.ErrNotFound {
		t.Errorf("UpdateStatus() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryAccountRepo_GetOrCreateForPeer_Concurrent(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()
	peerID := "peer-concurrent"

	// Test concurrent GetOrCreateForPeer calls
	const numGoroutines = 100
	results := make(chan struct {
		account *models.Account
		created bool
		err     error
	}, numGoroutines)

	// Launch concurrent goroutines
	for i := 0; i < numGoroutines; i++ {
		go func() {
			acc, created, err := repo.GetOrCreateForPeer(ctx, peerID)
			results <- struct {
				account *models.Account
				created bool
				err     error
			}{acc, created, err}
		}()
	}

	// Collect results
	var createdCount int
	var accounts []*models.Account
	for i := 0; i < numGoroutines; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("GetOrCreateForPeer() unexpected error: %v", result.err)
		}
		if result.created {
			createdCount++
		}
		accounts = append(accounts, result.account)
	}

	// Verify only one account was created
	if createdCount != 1 {
		t.Errorf("GetOrCreateForPeer() concurrent calls: got %d created, want 1", createdCount)
	}

	// Verify all accounts have same ID
	firstID := accounts[0].ID
	for i, acc := range accounts {
		if acc.ID != firstID {
			t.Errorf("GetOrCreateForPeer() concurrent call %d: got ID = %q, want %q", i, acc.ID, firstID)
		}
		if acc.PeerID != peerID {
			t.Errorf("GetOrCreateForPeer() concurrent call %d: got PeerID = %q, want %q", i, acc.PeerID, peerID)
		}
	}
}

func TestMemoryAccountRepo_GetOrCreateForPeer_Existing(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()

	// Create account first
	acc := newTestAccount("peer-existing")
	_ = repo.Create(ctx, acc)

	// GetOrCreate should return existing account
	found, created, err := repo.GetOrCreateForPeer(ctx, "peer-existing")
	if err != nil {
		t.Fatalf("GetOrCreateForPeer() unexpected error: %v", err)
	}
	if created {
		t.Error("GetOrCreateForPeer() got created = true, want false (account already exists)")
	}
	if found.ID != acc.ID {
		t.Errorf("GetOrCreateForPeer() got ID = %q, want %q", found.ID, acc.ID)
	}
}

func TestMemoryAccountRepo_GetOrCreateForPeer_New(t *testing.T) {
	repo := NewMemoryAccountRepository()
	ctx := context.Background()

	acc, created, err := repo.GetOrCreateForPeer(ctx, "peer-new")
	if err != nil {
		t.Fatalf("GetOrCreateForPeer() unexpected error: %v", err)
	}
	if !created {
		t.Error("GetOrCreateForPeer() got created = false, want true (new account)")
	}
	if acc.PeerID != "peer-new" {
		t.Errorf("GetOrCreateForPeer() got PeerID = %q, want %q", acc.PeerID, "peer-new")
	}
	if acc.Status != models.AccountStatusActive {
		t.Errorf("GetOrCreateForPeer() got Status = %q, want %q", acc.Status, models.AccountStatusActive)
	}
}
