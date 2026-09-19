// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: Tests for in-memory WalletRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestMemoryWalletRepo_LinkWallet_Success(t *testing.T) {
	repo := NewMemoryWalletRepository()
	ctx := context.Background()

	balance := int64(200000000) // 0.2 SOL
	wallet := &models.AccountWallet{
		AccountID:     "acc-1",
		WalletAddress: "7xKXtg...",
		Chain:         "solana",
		LinkedAt:      time.Now(),
		BalanceAtLink: &balance,
	}

	err := repo.LinkWallet(ctx, wallet)
	if err != nil {
		t.Fatalf("LinkWallet() unexpected error: %v", err)
	}

	found, err := repo.GetByAccountID(ctx, "acc-1", "solana")
	if err != nil {
		t.Fatalf("GetByAccountID() unexpected error: %v", err)
	}
	if found.WalletAddress != "7xKXtg..." {
		t.Errorf("GetByAccountID() WalletAddress = %q, want %q", found.WalletAddress, "7xKXtg...")
	}
}

func TestMemoryWalletRepo_LinkWallet_DuplicateChain(t *testing.T) {
	repo := NewMemoryWalletRepository()
	ctx := context.Background()

	wallet1 := &models.AccountWallet{AccountID: "acc-1", WalletAddress: "addr-1", Chain: "solana", LinkedAt: time.Now()}
	wallet2 := &models.AccountWallet{AccountID: "acc-1", WalletAddress: "addr-2", Chain: "solana", LinkedAt: time.Now()}

	_ = repo.LinkWallet(ctx, wallet1)
	err := repo.LinkWallet(ctx, wallet2)

	if err != models.ErrAlreadyExists {
		t.Errorf("LinkWallet() duplicate chain got error = %v, want ErrAlreadyExists", err)
	}
}

func TestMemoryWalletRepo_GetByAccountID_NotFound(t *testing.T) {
	repo := NewMemoryWalletRepository()
	ctx := context.Background()

	_, err := repo.GetByAccountID(ctx, "acc-1", "solana")
	if err != models.ErrNotFound {
		t.Errorf("GetByAccountID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryWalletRepo_GrantHistory(t *testing.T) {
	repo := NewMemoryWalletRepository()
	ctx := context.Background()

	// No history initially
	has, err := repo.HasGrantHistory(ctx, "7xKXtg...", "solana")
	if err != nil {
		t.Fatalf("HasGrantHistory() unexpected error: %v", err)
	}
	if has {
		t.Error("HasGrantHistory() = true, want false")
	}

	// Record a grant
	grant := &models.WalletGrantHistory{
		WalletAddress: "7xKXtg...",
		Chain:         "solana",
		GrantedAt:     time.Now(),
		Amount:        75,
	}
	err = repo.InsertGrantHistory(ctx, grant)
	if err != nil {
		t.Fatalf("InsertGrantHistory() unexpected error: %v", err)
	}

	// Now has history
	has, _ = repo.HasGrantHistory(ctx, "7xKXtg...", "solana")
	if !has {
		t.Error("HasGrantHistory() = false, want true")
	}
}
