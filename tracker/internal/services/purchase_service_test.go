// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: Tests for Solana purchase flow (create intent, verify transaction, credit account)

package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// mockTxVerifier implements TransactionVerifier for testing.
type mockTxVerifier struct {
	result *TxVerifyResult
	err    error
	// onVerify, when set, receives the params the service passed (memo contract checks).
	onVerify func(VerifyParams)
}

func (m *mockTxVerifier) VerifyTransaction(_ context.Context, _ string, p VerifyParams) (*TxVerifyResult, error) {
	if m.onVerify != nil {
		m.onVerify(p)
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

type purchaseTestDeps struct {
	purchases *repository.MemoryPurchaseRepository
	credits   *repository.MemoryCreditRepository
	accounts  *repository.MemoryAccountRepository
	clock     *clock.MockClock
	verifier  *mockTxVerifier
}

func newPurchaseTestDeps() purchaseTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	return purchaseTestDeps{
		purchases: repository.NewMemoryPurchaseRepository(),
		credits:   repository.NewMemoryCreditRepositoryWithClock(clk),
		accounts:  repository.NewMemoryAccountRepository(),
		clock:     clk,
		verifier: &mockTxVerifier{
			result: &TxVerifyResult{
				Success:        true,
				ActualLamports: 100_000_000,
				MemoMatch:      true,
			},
		},
	}
}

func newPurchaseService(deps purchaseTestDeps, treasuryAddr string) *PurchaseService {
	return NewPurchaseService(PurchaseServiceDeps{
		Purchases:       deps.purchases,
		Credits:         deps.credits,
		Accounts:        deps.accounts,
		Clock:           deps.clock,
		TxVerifier:      deps.verifier,
		TreasuryAddress: treasuryAddr,
	})
}

func seedPurchaseTestAccount(t *testing.T, deps purchaseTestDeps, accountID, peerID string, freeCredits int) {
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

// --- CreateIntent tests ---

func TestPurchaseService_CreateIntent_Success(t *testing.T) {
	deps := newPurchaseTestDeps()
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	resp, err := svc.CreateIntent(ctx, "acct-1", 100_000_000)
	if err != nil {
		t.Fatalf("CreateIntent() unexpected error: %v", err)
	}
	if resp.IntentID == "" {
		t.Error("CreateIntent() IntentID is empty")
	}
	if resp.TreasuryAddress != "TreasuryWallet123" {
		t.Errorf("CreateIntent() TreasuryAddress = %q, want TreasuryWallet123", resp.TreasuryAddress)
	}
	if resp.AmountLamports != 100_000_000 {
		t.Errorf("CreateIntent() AmountLamports = %d, want 100000000", resp.AmountLamports)
	}
	// 100M lamports / 20K lamports-per-credit (new rate) = 5000 credits.
	if resp.CreditAmount != 5000 {
		t.Errorf("CreateIntent() CreditAmount = %d, want 5000", resp.CreditAmount)
	}
	// New intents carry the StonkAgents memo prefix (user-visible in the wallet dialog).
	expectedMemo := fmt.Sprintf("stonkagents:purchase:%s", resp.IntentID)
	if resp.Memo != expectedMemo {
		t.Errorf("CreateIntent() Memo = %q, want %q", resp.Memo, expectedMemo)
	}
}

// TestPurchaseService_VerifyPurchase_AcceptsBothMemoPrefixes pins the verifier contract:
func TestPurchaseService_VerifyPurchase_AcceptsBothMemoPrefixes(t *testing.T) {
	deps := newPurchaseTestDeps()
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	intent, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)

	var got VerifyParams
	deps.verifier.onVerify = func(p VerifyParams) { got = p }
	if _, err := svc.VerifyPurchase(ctx, "acct-1", intent.IntentID, "5JzTxSignature"); err != nil {
		t.Fatalf("VerifyPurchase() unexpected error: %v", err)
	}

	if got.ExpectedMemo != "stonkagents:purchase:"+intent.IntentID {
		t.Errorf("ExpectedMemo = %q, want stonkagents prefix", got.ExpectedMemo)
	}
	for _, observed := range []string{
		"Program log: Memo: stonkagents:purchase:" + intent.IntentID,
	} {
		if !got.MemoMatches(observed) {
			t.Errorf("MemoMatches(%q) = false, want true", observed)
		}
	}
	if got.MemoMatches("Program log: Memo: stonkagents:purchase:other-intent") {
		t.Error("MemoMatches accepted a memo for a different intent")
	}
	if got.MemoMatches("Program log: Memo: stonkagents:purchase:") {
		t.Error("MemoMatches accepted a memo with no intent id")
	}
}

// --- VerifyPurchase tests ---

func TestPurchaseService_VerifyPurchase_Success(t *testing.T) {
	deps := newPurchaseTestDeps()
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	intent, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)

	resp, err := svc.VerifyPurchase(ctx, "acct-1", intent.IntentID, "5JzTxSignature")
	if err != nil {
		t.Fatalf("VerifyPurchase() unexpected error: %v", err)
	}
	if resp.CreditsGranted != 5000 {
		t.Errorf("VerifyPurchase() credits = %d, want 5000", resp.CreditsGranted)
	}
	if resp.NewBalance.Paid != 5000 {
		t.Errorf("VerifyPurchase() paid balance = %d, want 5000", resp.NewBalance.Paid)
	}
}

func TestPurchaseService_VerifyPurchase_ExpiredIntent(t *testing.T) {
	deps := newPurchaseTestDeps()
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	intent, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)

	// Advance past intent expiry (15 min)
	deps.clock.Advance(16 * time.Minute)

	_, err := svc.VerifyPurchase(ctx, "acct-1", intent.IntentID, "5JzTxSignature")
	if err == nil {
		t.Error("VerifyPurchase() expected error for expired intent")
	}
}

func TestPurchaseService_VerifyPurchase_WrongAmount(t *testing.T) {
	deps := newPurchaseTestDeps()
	deps.verifier.result = &TxVerifyResult{
		Success:        true,
		ActualLamports: 50_000_000, // Wrong amount
		MemoMatch:      true,
	}
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	intent, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)

	_, err := svc.VerifyPurchase(ctx, "acct-1", intent.IntentID, "5JzTxSignature")
	if err == nil {
		t.Error("VerifyPurchase() expected error for wrong amount")
	}
}

func TestPurchaseService_VerifyPurchase_TxFailed(t *testing.T) {
	deps := newPurchaseTestDeps()
	deps.verifier.result = &TxVerifyResult{
		Success:        false,
		ActualLamports: 100_000_000,
		MemoMatch:      true,
	}
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	intent, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)

	_, err := svc.VerifyPurchase(ctx, "acct-1", intent.IntentID, "5JzFailedTx")
	if err == nil {
		t.Error("VerifyPurchase() expected error for failed transaction")
	}
}

func TestPurchaseService_VerifyPurchase_ReplayPrevented(t *testing.T) {
	deps := newPurchaseTestDeps()
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	intent, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)

	// First verification succeeds
	_, _ = svc.VerifyPurchase(ctx, "acct-1", intent.IntentID, "5JzReplaySig")

	// Create a second intent and try to verify with same signature
	intent2, _ := svc.CreateIntent(ctx, "acct-1", 100_000_000)
	_, err := svc.VerifyPurchase(ctx, "acct-1", intent2.IntentID, "5JzReplaySig")
	if err == nil {
		t.Error("VerifyPurchase() expected error for replayed tx signature")
	}
}

// --- ExpireStaleIntents tests ---

func TestPurchaseService_ExpireStaleIntents(t *testing.T) {
	deps := newPurchaseTestDeps()
	svc := newPurchaseService(deps, "TreasuryWallet123")
	ctx := context.Background()
	seedPurchaseTestAccount(t, deps, "acct-1", "peer-1", 50)

	_, _ = svc.CreateIntent(ctx, "acct-1", 100_000_000)

	deps.clock.Advance(16 * time.Minute)

	expired, err := svc.ExpireStaleIntents(ctx)
	if err != nil {
		t.Fatalf("ExpireStaleIntents() unexpected error: %v", err)
	}
	if expired != 1 {
		t.Errorf("ExpireStaleIntents() = %d, want 1", expired)
	}
}
