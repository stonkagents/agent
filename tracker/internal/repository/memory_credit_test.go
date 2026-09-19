// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-03 (Credit System)
// Purpose: Tests for in-memory CreditRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func setupCreditRepo(t *testing.T) (CreditRepository, string) {
	t.Helper()
	repo := NewMemoryCreditRepository()
	ctx := context.Background()
	accountID := "acc-test-1"
	now := time.Now()
	expires := now.Add(30 * 24 * time.Hour)

	balance := &models.CreditBalance{
		AccountID:            accountID,
		FreeBalance:          0,
		PaidBalance:          0,
		FreeCreditsExpiresAt: &expires,
		UpdatedAt:            now,
	}
	if err := repo.CreateBalance(ctx, balance); err != nil {
		t.Fatalf("CreateBalance() setup error: %v", err)
	}
	return repo, accountID
}

func TestMemoryCreditRepo_CreateBalance_Success(t *testing.T) {
	repo := NewMemoryCreditRepository()
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	balance := &models.CreditBalance{
		AccountID:            "acc-1",
		FreeCreditsExpiresAt: &expires,
		UpdatedAt:            time.Now(),
	}

	err := repo.CreateBalance(ctx, balance)
	if err != nil {
		t.Fatalf("CreateBalance() unexpected error: %v", err)
	}

	found, err := repo.GetBalance(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetBalance() unexpected error: %v", err)
	}
	if found.FreeBalance != 0 {
		t.Errorf("GetBalance() FreeBalance = %d, want 0", found.FreeBalance)
	}
}

func TestMemoryCreditRepo_CreateBalance_Duplicate(t *testing.T) {
	repo := NewMemoryCreditRepository()
	ctx := context.Background()

	balance := &models.CreditBalance{AccountID: "acc-1", UpdatedAt: time.Now()}
	_ = repo.CreateBalance(ctx, balance)
	err := repo.CreateBalance(ctx, balance)

	if err != models.ErrAlreadyExists {
		t.Errorf("CreateBalance() duplicate got error = %v, want ErrAlreadyExists", err)
	}
}

func TestMemoryCreditRepo_GetBalance_NotFound(t *testing.T) {
	repo := NewMemoryCreditRepository()
	ctx := context.Background()

	_, err := repo.GetBalance(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetBalance() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryCreditRepo_CreditFree(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	err := repo.CreditFree(ctx, accID, 50, "registration_grant", "reg:acc-test-1", expires)
	if err != nil {
		t.Fatalf("CreditFree() unexpected error: %v", err)
	}

	bal, _ := repo.GetBalance(ctx, accID)
	if bal.FreeBalance != 50 {
		t.Errorf("CreditFree() FreeBalance = %d, want 50", bal.FreeBalance)
	}
}

func TestMemoryCreditRepo_CreditPaid(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()

	err := repo.CreditPaid(ctx, accID, 100, "purchase", "purchase:intent-1")
	if err != nil {
		t.Fatalf("CreditPaid() unexpected error: %v", err)
	}

	bal, _ := repo.GetBalance(ctx, accID)
	if bal.PaidBalance != 100 {
		t.Errorf("CreditPaid() PaidBalance = %d, want 100", bal.PaidBalance)
	}
	if bal.LifetimePurchased != 100 {
		t.Errorf("CreditPaid() LifetimePurchased = %d, want 100", bal.LifetimePurchased)
	}
}

func TestMemoryCreditRepo_Spend_FreeFirst(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 50, "grant", "req-1", expires)
	_ = repo.CreditPaid(ctx, accID, 100, "purchase", "req-2")

	// Spend 30 — should come entirely from free balance
	err := repo.Spend(ctx, accID, 30, "ai_chat", "spend-1")
	if err != nil {
		t.Fatalf("Spend() unexpected error: %v", err)
	}

	bal, _ := repo.GetBalance(ctx, accID)
	if bal.FreeBalance != 20 {
		t.Errorf("Spend() FreeBalance = %d, want 20", bal.FreeBalance)
	}
	if bal.PaidBalance != 100 {
		t.Errorf("Spend() PaidBalance = %d, want 100 (untouched)", bal.PaidBalance)
	}
}

func TestMemoryCreditRepo_Spend_MixedFreeAndPaid(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 50, "grant", "req-1", expires)
	_ = repo.CreditPaid(ctx, accID, 100, "purchase", "req-2")

	// Spend 80 — 50 from free, 30 from paid
	err := repo.Spend(ctx, accID, 80, "ai_chat", "spend-1")
	if err != nil {
		t.Fatalf("Spend() unexpected error: %v", err)
	}

	bal, _ := repo.GetBalance(ctx, accID)
	if bal.FreeBalance != 0 {
		t.Errorf("Spend() FreeBalance = %d, want 0", bal.FreeBalance)
	}
	if bal.PaidBalance != 70 {
		t.Errorf("Spend() PaidBalance = %d, want 70", bal.PaidBalance)
	}
}

func TestMemoryCreditRepo_Spend_Insufficient(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 50, "grant", "req-1", expires)

	err := repo.Spend(ctx, accID, 100, "ai_chat", "spend-1")
	if err != models.ErrInsufficientCredits {
		t.Errorf("Spend() got error = %v, want ErrInsufficientCredits", err)
	}

	// Balance should be unchanged
	bal, _ := repo.GetBalance(ctx, accID)
	if bal.FreeBalance != 50 {
		t.Errorf("Spend() FreeBalance = %d, want 50 (unchanged)", bal.FreeBalance)
	}
}

func TestMemoryCreditRepo_Spend_ResetsExpiryClock(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	oldExpiry := time.Now().Add(1 * 24 * time.Hour) // 1 day left

	_ = repo.CreditFree(ctx, accID, 50, "grant", "req-1", oldExpiry)

	before, _ := repo.GetBalance(ctx, accID)
	beforeExpiry := *before.FreeCreditsExpiresAt

	// Spend resets the clock to 30 days from now
	_ = repo.Spend(ctx, accID, 10, "ai_chat", "spend-1")

	after, _ := repo.GetBalance(ctx, accID)
	afterExpiry := *after.FreeCreditsExpiresAt

	if !afterExpiry.After(beforeExpiry) {
		t.Errorf("Spend() expiry clock not reset: before=%v, after=%v", beforeExpiry, afterExpiry)
	}
}

func TestMemoryCreditRepo_Spend_Idempotent(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 100, "grant", "req-1", expires)

	// First spend
	_ = repo.Spend(ctx, accID, 30, "ai_chat", "spend-1")
	// Same request_id — should be idempotent (no error, no double deduction)
	err := repo.Spend(ctx, accID, 30, "ai_chat", "spend-1")
	if err != nil {
		t.Fatalf("Spend() idempotent call got error: %v", err)
	}

	bal, _ := repo.GetBalance(ctx, accID)
	if bal.FreeBalance != 70 {
		t.Errorf("Spend() idempotent FreeBalance = %d, want 70 (not 40)", bal.FreeBalance)
	}
}

func TestMemoryCreditRepo_GetTransactionByRequestID(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 50, "registration_grant", "reg:acc-1", expires)

	tx, err := repo.GetTransactionByRequestID(ctx, accID, "reg:acc-1")
	if err != nil {
		t.Fatalf("GetTransactionByRequestID() unexpected error: %v", err)
	}
	if tx.Amount != 50 {
		t.Errorf("GetTransactionByRequestID() Amount = %d, want 50", tx.Amount)
	}
	if tx.Reason != "registration_grant" {
		t.Errorf("GetTransactionByRequestID() Reason = %q, want %q", tx.Reason, "registration_grant")
	}
}

func TestMemoryCreditRepo_GetTransactionByRequestID_NotFound(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()

	_, err := repo.GetTransactionByRequestID(ctx, accID, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetTransactionByRequestID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryCreditRepo_ListTransactions(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 50, "grant", "req-1", expires)
	_ = repo.CreditPaid(ctx, accID, 100, "purchase", "req-2")
	_ = repo.Spend(ctx, accID, 20, "ai_chat", "req-3")

	txs, err := repo.ListTransactions(ctx, accID, 10, 0)
	if err != nil {
		t.Fatalf("ListTransactions() unexpected error: %v", err)
	}
	if len(txs) != 3 {
		t.Errorf("ListTransactions() got %d transactions, want 3", len(txs))
	}
}

func TestMemoryCreditRepo_SetFreeBalance(t *testing.T) {
	repo, accID := setupCreditRepo(t)
	ctx := context.Background()
	expires := time.Now().Add(30 * 24 * time.Hour)

	_ = repo.CreditFree(ctx, accID, 250, "grant", "req-1", expires)

	err := repo.SetFreeBalance(ctx, accID, 0)
	if err != nil {
		t.Fatalf("SetFreeBalance() unexpected error: %v", err)
	}

	bal, _ := repo.GetBalance(ctx, accID)
	if bal.FreeBalance != 0 {
		t.Errorf("SetFreeBalance() FreeBalance = %d, want 0", bal.FreeBalance)
	}
}
