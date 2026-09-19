// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-08 (Account Recovery)
// Purpose: Tests for account recovery service (transfer paid credits, deactivate old)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type recoveryTestDeps struct {
	accounts *repository.MemoryAccountRepository
	credits  *repository.MemoryCreditRepository
	social   *repository.MemorySocialRepository
	wallets  *repository.MemoryWalletRepository
	apiKeys  *repository.MemoryPeerAPIKeyRepository
	clock    *clock.MockClock
}

func newRecoveryTestDeps() recoveryTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	return recoveryTestDeps{
		accounts: repository.NewMemoryAccountRepository(),
		credits:  repository.NewMemoryCreditRepositoryWithClock(clk),
		social:   repository.NewMemorySocialRepository(),
		wallets:  repository.NewMemoryWalletRepository(),
		apiKeys:  repository.NewMemoryPeerAPIKeyRepository(),
		clock:    clk,
	}
}

func newRecoveryService(deps recoveryTestDeps) *RecoveryService {
	return NewRecoveryService(RecoveryServiceDeps{
		Accounts: deps.accounts,
		Credits:  deps.credits,
		Social:   deps.social,
		Wallets:  deps.wallets,
		APIKeys:  deps.apiKeys,
		Clock:    deps.clock,
	})
}

// seedRecoveryAccount creates an account with free and paid credits.
func seedRecoveryAccount(t *testing.T, deps recoveryTestDeps, accountID, peerID string, free, paid int) {
	t.Helper()
	ctx := context.Background()
	now := deps.clock.Now()
	_ = deps.accounts.Create(ctx, &models.Account{
		ID:        accountID,
		PeerID:    peerID,
		Status:    models.AccountStatusActive,
		CreatedAt: now,
	})
	expiresAt := now.Add(30 * 24 * time.Hour)
	_ = deps.credits.CreateBalance(ctx, &models.CreditBalance{
		AccountID:            accountID,
		FreeBalance:          0,
		PaidBalance:          0,
		FreeCreditsExpiresAt: &expiresAt,
		UpdatedAt:            now,
	})
	if free > 0 {
		_ = deps.credits.CreditFree(ctx, accountID, free, "seed_free", "seed-free:"+accountID, expiresAt)
	}
	if paid > 0 {
		_ = deps.credits.CreditPaid(ctx, accountID, paid, "seed_paid", "seed-paid:"+accountID)
	}
	_, _ = deps.apiKeys.Create(ctx, peerID)
}

func addSocialConnection(t *testing.T, deps recoveryTestDeps, accountID, platform, userID string) {
	t.Helper()
	ctx := context.Background()
	_ = deps.social.Insert(ctx, &models.SocialConnection{
		ID:             accountID + ":" + platform,
		AccountID:      accountID,
		Platform:       platform,
		PlatformUserID: userID,
		VerifiedAt:     deps.clock.Now(),
		BonusGranted:   50,
	})
}

// --- Recovery tests ---

func TestRecoveryService_Recover_WithSocialProof(t *testing.T) {
	deps := newRecoveryTestDeps()
	svc := newRecoveryService(deps)
	ctx := context.Background()

	// Old account with paid credits and social connections
	seedRecoveryAccount(t, deps, "old-acct", "old-peer", 50, 200)
	addSocialConnection(t, deps, "old-acct", models.PlatformGitHub, "octocat")
	addSocialConnection(t, deps, "old-acct", models.PlatformTwitter, "tweeter")

	// New account
	seedRecoveryAccount(t, deps, "new-acct", "new-peer", 50, 0)

	resp, err := svc.Recover(ctx, RecoverRequest{
		NewAccountID:        "new-acct",
		OldPeerID:           "old-peer",
		SocialVerifications: []string{models.PlatformGitHub, models.PlatformTwitter},
	})
	if err != nil {
		t.Fatalf("Recover() unexpected error: %v", err)
	}
	if !resp.Recovered {
		t.Error("Recover() should be recovered")
	}
	if resp.PaidCreditsTransferred != 200 {
		t.Errorf("Recover() paid transferred = %d, want 200", resp.PaidCreditsTransferred)
	}

	// Old account should be deactivated
	oldAcct, _ := deps.accounts.GetByID(ctx, "old-acct")
	if oldAcct.Status != models.AccountStatusRecovered {
		t.Errorf("Old account status = %q, want %q", oldAcct.Status, models.AccountStatusRecovered)
	}

	// Old account paid credits should be 0
	oldBal, _ := deps.credits.GetBalance(ctx, "old-acct")
	if oldBal.PaidBalance != 0 {
		t.Errorf("Old account paid balance = %d, want 0", oldBal.PaidBalance)
	}

	// New account should have the transferred paid credits
	newBal, _ := deps.credits.GetBalance(ctx, "new-acct")
	if newBal.PaidBalance != 200 {
		t.Errorf("New account paid balance = %d, want 200", newBal.PaidBalance)
	}
}

func TestRecoveryService_Recover_InsufficientSocialProof(t *testing.T) {
	deps := newRecoveryTestDeps()
	svc := newRecoveryService(deps)
	ctx := context.Background()

	seedRecoveryAccount(t, deps, "old-acct", "old-peer", 50, 200)
	addSocialConnection(t, deps, "old-acct", models.PlatformGitHub, "octocat")
	seedRecoveryAccount(t, deps, "new-acct", "new-peer", 50, 0)

	// Only 1 social verification (need >= 2)
	_, err := svc.Recover(ctx, RecoverRequest{
		NewAccountID:        "new-acct",
		OldPeerID:           "old-peer",
		SocialVerifications: []string{models.PlatformGitHub},
	})
	if err == nil {
		t.Error("Recover() expected error for insufficient social proof")
	}
}

func TestRecoveryService_Recover_OldAccountNotFound(t *testing.T) {
	deps := newRecoveryTestDeps()
	svc := newRecoveryService(deps)
	ctx := context.Background()

	seedRecoveryAccount(t, deps, "new-acct", "new-peer", 50, 0)

	_, err := svc.Recover(ctx, RecoverRequest{
		NewAccountID:        "new-acct",
		OldPeerID:           "nonexistent-peer",
		SocialVerifications: []string{models.PlatformGitHub, models.PlatformTwitter},
	})
	if err != models.ErrNotFound {
		t.Errorf("Recover() got error = %v, want ErrNotFound", err)
	}
}

func TestRecoveryService_Recover_NoPaidCreditsToTransfer(t *testing.T) {
	deps := newRecoveryTestDeps()
	svc := newRecoveryService(deps)
	ctx := context.Background()

	// Old account with 0 paid credits
	seedRecoveryAccount(t, deps, "old-acct", "old-peer", 50, 0)
	addSocialConnection(t, deps, "old-acct", models.PlatformGitHub, "octocat")
	addSocialConnection(t, deps, "old-acct", models.PlatformTwitter, "tweeter")
	seedRecoveryAccount(t, deps, "new-acct", "new-peer", 50, 0)

	resp, err := svc.Recover(ctx, RecoverRequest{
		NewAccountID:        "new-acct",
		OldPeerID:           "old-peer",
		SocialVerifications: []string{models.PlatformGitHub, models.PlatformTwitter},
	})
	if err != nil {
		t.Fatalf("Recover() unexpected error: %v", err)
	}
	if resp.PaidCreditsTransferred != 0 {
		t.Errorf("Recover() paid transferred = %d, want 0", resp.PaidCreditsTransferred)
	}
}

func TestRecoveryService_Recover_FreeCreditsNotTransferred(t *testing.T) {
	deps := newRecoveryTestDeps()
	svc := newRecoveryService(deps)
	ctx := context.Background()

	seedRecoveryAccount(t, deps, "old-acct", "old-peer", 500, 100)
	addSocialConnection(t, deps, "old-acct", models.PlatformGitHub, "octocat")
	addSocialConnection(t, deps, "old-acct", models.PlatformTwitter, "tweeter")
	seedRecoveryAccount(t, deps, "new-acct", "new-peer", 50, 0)

	resp, _ := svc.Recover(ctx, RecoverRequest{
		NewAccountID:        "new-acct",
		OldPeerID:           "old-peer",
		SocialVerifications: []string{models.PlatformGitHub, models.PlatformTwitter},
	})

	// Only paid credits transferred, not free
	if resp.PaidCreditsTransferred != 100 {
		t.Errorf("Recover() paid transferred = %d, want 100 (only paid, not free)", resp.PaidCreditsTransferred)
	}

	// New account free balance should be unchanged (only their own 50)
	newBal, _ := deps.credits.GetBalance(ctx, "new-acct")
	if newBal.FreeBalance != 50 {
		t.Errorf("New account free balance = %d, want 50 (unchanged)", newBal.FreeBalance)
	}
}
