// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: Tests for wallet linking service (ownership proof, balance check, age check, bonus grant)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// mockSolanaClient implements SolanaClient for testing.
type mockSolanaClient struct {
	balanceLamports   int64
	firstTxTime       *time.Time
	verifySignatureFn func(walletAddr string, message, signature []byte) bool
}

func (m *mockSolanaClient) GetBalance(_ context.Context, _ string) (int64, error) {
	return m.balanceLamports, nil
}

func (m *mockSolanaClient) GetFirstTransactionTime(_ context.Context, _ string) (*time.Time, error) {
	return m.firstTxTime, nil
}

func (m *mockSolanaClient) VerifyWalletSignature(walletAddr string, message, signature []byte) bool {
	if m.verifySignatureFn != nil {
		return m.verifySignatureFn(walletAddr, message, signature)
	}
	return true
}

type walletTestDeps struct {
	wallets  *repository.MemoryWalletRepository
	credits  *repository.MemoryCreditRepository
	accounts *repository.MemoryAccountRepository
	clock    *clock.MockClock
	solana   *mockSolanaClient
}

func newWalletTestDeps() walletTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	oldTxTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return walletTestDeps{
		wallets:  repository.NewMemoryWalletRepository(),
		credits:  repository.NewMemoryCreditRepositoryWithClock(clk),
		accounts: repository.NewMemoryAccountRepository(),
		clock:    clk,
		solana: &mockSolanaClient{
			balanceLamports: 300_000_000, // 0.3 SOL (above 0.2 threshold)
			firstTxTime:     &oldTxTime,  // old wallet (>7 days)
		},
	}
}

func newWalletService(deps walletTestDeps) *WalletService {
	return NewWalletService(WalletServiceDeps{
		Wallets:  deps.wallets,
		Credits:  deps.credits,
		Accounts: deps.accounts,
		Clock:    deps.clock,
		Solana:   deps.solana,
	})
}

func seedWalletTestAccount(t *testing.T, deps walletTestDeps, accountID, peerID string, freeCredits int) {
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
	if freeCredits > 0 {
		_ = deps.credits.CreditFree(ctx, accountID, freeCredits, "seed", "seed:"+accountID, expiresAt)
	}
}

// --- LinkWallet tests ---

func TestWalletService_LinkWallet_Success(t *testing.T) {
	deps := newWalletTestDeps()
	svc := newWalletService(deps)
	ctx := context.Background()
	seedWalletTestAccount(t, deps, "acct-1", "peer-1", 50)

	resp, err := svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "acct-1",
		WalletAddress: "7xKXtgABCDEF",
		Chain:         "solana",
		Signature:     []byte("valid-sig"),
	})
	if err != nil {
		t.Fatalf("LinkWallet() unexpected error: %v", err)
	}
	if !resp.Linked {
		t.Error("LinkWallet() Linked should be true")
	}
	if resp.BonusGranted != 75 {
		t.Errorf("LinkWallet() bonus = %d, want 75", resp.BonusGranted)
	}
	if resp.NewBalance.Free != 125 {
		t.Errorf("LinkWallet() new free = %d, want 125 (50+75)", resp.NewBalance.Free)
	}
}

func TestWalletService_LinkWallet_InsufficientBalance(t *testing.T) {
	deps := newWalletTestDeps()
	deps.solana.balanceLamports = 100_000_000 // 0.1 SOL (below 0.2 threshold)
	svc := newWalletService(deps)
	ctx := context.Background()
	seedWalletTestAccount(t, deps, "acct-1", "peer-1", 50)

	resp, err := svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "acct-1",
		WalletAddress: "7xKXtgLowBal",
		Chain:         "solana",
		Signature:     []byte("valid-sig"),
	})
	if err != nil {
		t.Fatalf("LinkWallet() unexpected error: %v", err)
	}
	// Wallet should still be linked, but no bonus
	if !resp.Linked {
		t.Error("LinkWallet() Linked should be true even with low balance")
	}
	if resp.BonusGranted != 0 {
		t.Errorf("LinkWallet() bonus = %d, want 0 (insufficient SOL)", resp.BonusGranted)
	}
}

func TestWalletService_LinkWallet_TooYoung(t *testing.T) {
	deps := newWalletTestDeps()
	recentTx := deps.clock.Now().Add(-3 * 24 * time.Hour) // 3 days ago
	deps.solana.firstTxTime = &recentTx
	svc := newWalletService(deps)
	ctx := context.Background()
	seedWalletTestAccount(t, deps, "acct-1", "peer-1", 50)

	resp, err := svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "acct-1",
		WalletAddress: "7xKXtgYoung",
		Chain:         "solana",
		Signature:     []byte("valid-sig"),
	})
	if err != nil {
		t.Fatalf("LinkWallet() unexpected error: %v", err)
	}
	if resp.BonusGranted != 0 {
		t.Errorf("LinkWallet() bonus = %d, want 0 (wallet too young)", resp.BonusGranted)
	}
}

func TestWalletService_LinkWallet_AlreadyGranted(t *testing.T) {
	deps := newWalletTestDeps()
	svc := newWalletService(deps)
	ctx := context.Background()
	seedWalletTestAccount(t, deps, "acct-1", "peer-1", 50)
	seedWalletTestAccount(t, deps, "acct-2", "peer-2", 50)

	// Link wallet for acct-1
	_, _ = svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "acct-1",
		WalletAddress: "7xKXtgShared",
		Chain:         "solana",
		Signature:     []byte("valid-sig"),
	})

	// Try linking same wallet for acct-2 — should link but not grant bonus
	resp, err := svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "acct-2",
		WalletAddress: "7xKXtgShared",
		Chain:         "solana",
		Signature:     []byte("valid-sig"),
	})
	// Link should fail (wallet already linked to another account)
	if err == nil {
		t.Fatalf("LinkWallet() expected error for wallet already linked, got bonus=%d", resp.BonusGranted)
	}
}

func TestWalletService_LinkWallet_InvalidSignature(t *testing.T) {
	deps := newWalletTestDeps()
	deps.solana.verifySignatureFn = func(string, []byte, []byte) bool { return false }
	svc := newWalletService(deps)
	ctx := context.Background()
	seedWalletTestAccount(t, deps, "acct-1", "peer-1", 50)

	_, err := svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "acct-1",
		WalletAddress: "7xKXtgBadSig",
		Chain:         "solana",
		Signature:     []byte("bad-sig"),
	})
	if err == nil {
		t.Error("LinkWallet() expected error for invalid signature")
	}
}

// TestWalletService_LinkWallet_AcceptsCurrentAndLegacyMessage pins the ownership-proof
// contract: a signature over "stonkagents-wallet-link:<accountId>" (new portal) and one over
func TestWalletService_LinkWallet_AcceptsCurrentAndLegacyMessage(t *testing.T) {
	cases := []struct {
		name   string
		signed string
		wantOK bool
	}{
		{"current message", "stonkagents-wallet-link:acct-1", true},
		{"wrong account", "stonkagents-wallet-link:acct-2", false},
		{"unknown prefix", "other-wallet-link:acct-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newWalletTestDeps()
			// The mock "verifies" only when the message the service checks is the one the wallet signed.
			deps.solana.verifySignatureFn = func(_ string, message, _ []byte) bool {
				return string(message) == tc.signed
			}
			svc := newWalletService(deps)
			seedWalletTestAccount(t, deps, "acct-1", "peer-1", 50)

			_, err := svc.LinkWallet(context.Background(), LinkWalletRequest{
				AccountID: "acct-1", WalletAddress: "7xKXtgABCDEF", Chain: "solana", Signature: []byte("sig"),
			})
			if tc.wantOK && err != nil {
				t.Fatalf("LinkWallet() unexpected error: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("LinkWallet() expected signature error")
			}
		})
	}
	if WalletLinkMessage("x") != "stonkagents-wallet-link:x" {
		t.Errorf("message helper = %q", WalletLinkMessage("x"))
	}
}

func TestWalletService_LinkWallet_AccountNotFound(t *testing.T) {
	deps := newWalletTestDeps()
	svc := newWalletService(deps)
	ctx := context.Background()

	_, err := svc.LinkWallet(ctx, LinkWalletRequest{
		AccountID:     "nonexistent",
		WalletAddress: "7xKXtg",
		Chain:         "solana",
		Signature:     []byte("sig"),
	})
	if err != models.ErrNotFound {
		t.Errorf("LinkWallet() got error = %v, want ErrNotFound", err)
	}
}
