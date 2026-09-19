// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-03 (Credit Lifecycle)
// Purpose: Tests for credit service (spend, JWT, expiry, presence grant)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type creditTestDeps struct {
	credits  *repository.MemoryCreditRepository
	accounts *repository.MemoryAccountRepository
	clock    *clock.MockClock
}

func newCreditTestDeps() creditTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	return creditTestDeps{
		credits:  repository.NewMemoryCreditRepositoryWithClock(clk),
		accounts: repository.NewMemoryAccountRepository(),
		clock:    clk,
	}
}

func newCreditService(deps creditTestDeps, jwtSecret string) *CreditService {
	return NewCreditService(CreditServiceDeps{
		Credits:   deps.credits,
		Accounts:  deps.accounts,
		Clock:     deps.clock,
		JWTSecret: jwtSecret,
	})
}

// seedAccountWithCredits creates an account and grants free+paid credits for testing.
func seedAccountWithCredits(t *testing.T, deps creditTestDeps, accountID, peerID string, free, paid int) {
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
}

// --- GetBalance tests ---

func TestCreditService_GetBalance_Success(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 100)

	bal, err := svc.GetBalance(ctx, "acct-1")
	if err != nil {
		t.Fatalf("GetBalance() unexpected error: %v", err)
	}
	if bal.FreeBalance != 50 {
		t.Errorf("GetBalance() FreeBalance = %d, want 50", bal.FreeBalance)
	}
	if bal.PaidBalance != 100 {
		t.Errorf("GetBalance() PaidBalance = %d, want 100", bal.PaidBalance)
	}
	if bal.Total != 150 {
		t.Errorf("GetBalance() Total = %d, want 150", bal.Total)
	}
}

func TestCreditService_GetBalance_NotFound(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	_, err := svc.GetBalance(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("GetBalance() got error = %v, want ErrNotFound", err)
	}
}

// --- Spend tests ---

func TestCreditService_Spend_FreeFirst(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 100)

	err := svc.Spend(ctx, "acct-1", 30, "ai_chat", "req-1")
	if err != nil {
		t.Fatalf("Spend() unexpected error: %v", err)
	}

	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeBalance != 20 {
		t.Errorf("Spend() FreeBalance = %d, want 20 (50-30)", bal.FreeBalance)
	}
	if bal.PaidBalance != 100 {
		t.Errorf("Spend() PaidBalance = %d, want 100 (unchanged)", bal.PaidBalance)
	}
}

func TestCreditService_Spend_MixedFreeAndPaid(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 30, 100)

	err := svc.Spend(ctx, "acct-1", 50, "ai_chat", "req-1")
	if err != nil {
		t.Fatalf("Spend() unexpected error: %v", err)
	}

	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeBalance != 0 {
		t.Errorf("Spend() FreeBalance = %d, want 0 (30 free consumed)", bal.FreeBalance)
	}
	if bal.PaidBalance != 80 {
		t.Errorf("Spend() PaidBalance = %d, want 80 (100-20)", bal.PaidBalance)
	}
}

func TestCreditService_Spend_InsufficientCredits(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 20, 10)

	err := svc.Spend(ctx, "acct-1", 50, "ai_chat", "req-1")
	if err != models.ErrInsufficientCredits {
		t.Errorf("Spend() got error = %v, want ErrInsufficientCredits", err)
	}
}

func TestCreditService_Spend_Idempotent(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	// First spend
	_ = svc.Spend(ctx, "acct-1", 10, "ai_chat", "req-1")
	// Second spend with same requestID
	err := svc.Spend(ctx, "acct-1", 10, "ai_chat", "req-1")
	if err != nil {
		t.Fatalf("Spend() idempotent call unexpected error: %v", err)
	}

	// Should only deduct once
	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeBalance != 40 {
		t.Errorf("Spend() FreeBalance = %d, want 40 (only deducted once)", bal.FreeBalance)
	}
}

func TestCreditService_Spend_ResetsExpiryClockOnFreeSpend(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	// Advance 20 days
	deps.clock.Advance(20 * 24 * time.Hour)

	// Spend some free credits — should reset expiry to 30 days from now
	_ = svc.Spend(ctx, "acct-1", 5, "ai_chat", "req-1")

	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeCreditsExpiresAt == nil {
		t.Fatal("Spend() FreeCreditsExpiresAt is nil after spend")
	}

	expectedExpiry := deps.clock.Now().Add(30 * 24 * time.Hour)
	diff := bal.FreeCreditsExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("Spend() FreeCreditsExpiresAt = %v, want ~%v", bal.FreeCreditsExpiresAt, expectedExpiry)
	}
}

// --- JWT Spend Token tests ---

func TestCreditService_IssueSpendToken_Success(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-jwt-secret-32-bytes-long!!!")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	token, err := svc.IssueSpendToken(ctx, "acct-1", 5, "ai_chat")
	if err != nil {
		t.Fatalf("IssueSpendToken() unexpected error: %v", err)
	}
	if token.Token == "" {
		t.Error("IssueSpendToken() token is empty")
	}
	if token.ExpiresAt.IsZero() {
		t.Error("IssueSpendToken() ExpiresAt is zero")
	}

	// Token should expire 5 minutes from now
	expectedExpiry := deps.clock.Now().Add(5 * time.Minute)
	diff := token.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("IssueSpendToken() ExpiresAt = %v, want ~%v", token.ExpiresAt, expectedExpiry)
	}
}

func TestCreditService_IssueSpendToken_InsufficientCredits(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-jwt-secret-32-bytes-long!!!")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 5, 0)

	_, err := svc.IssueSpendToken(ctx, "acct-1", 10, "ai_chat")
	if err != models.ErrInsufficientCredits {
		t.Errorf("IssueSpendToken() got error = %v, want ErrInsufficientCredits", err)
	}
}

func TestCreditService_ValidateSpendToken_Success(t *testing.T) {
	deps := newCreditTestDeps()
	secret := "test-jwt-secret-32-bytes-long!!!"
	svc := newCreditService(deps, secret)
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	tokenResp, _ := svc.IssueSpendToken(ctx, "acct-1", 5, "ai_chat")

	claims, err := svc.ValidateSpendToken(tokenResp.Token)
	if err != nil {
		t.Fatalf("ValidateSpendToken() unexpected error: %v", err)
	}
	if claims.AccountID != "acct-1" {
		t.Errorf("ValidateSpendToken() AccountID = %q, want %q", claims.AccountID, "acct-1")
	}
	if claims.Amount != 5 {
		t.Errorf("ValidateSpendToken() Amount = %d, want 5", claims.Amount)
	}
	if claims.Purpose != "ai_chat" {
		t.Errorf("ValidateSpendToken() Purpose = %q, want %q", claims.Purpose, "ai_chat")
	}
}

func TestCreditService_ValidateSpendToken_Expired(t *testing.T) {
	deps := newCreditTestDeps()
	secret := "test-jwt-secret-32-bytes-long!!!"
	svc := newCreditService(deps, secret)
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	tokenResp, _ := svc.IssueSpendToken(ctx, "acct-1", 5, "ai_chat")

	// Advance past token expiry
	deps.clock.Advance(6 * time.Minute)

	_, err := svc.ValidateSpendToken(tokenResp.Token)
	if err == nil {
		t.Error("ValidateSpendToken() expected error for expired token")
	}
}

// --- Key rotation tests (TD-080) ---

func TestCreditService_ValidateSpendToken_AcceptsOldKeyDuringRotation(t *testing.T) {
	deps := newCreditTestDeps()
	oldSecret := "old-jwt-secret-32-bytes-long!!!!"
	newSecret := "new-jwt-secret-32-bytes-long!!!!"

	// Create service with old secret, issue token
	svcOld := newCreditServiceWithPreviousKey(deps, oldSecret, "")
	ctx := context.Background()
	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)
	tokenResp, _ := svcOld.IssueSpendToken(ctx, "acct-1", 5, "ai_chat")

	// "Rotate" — create new service with new secret + previous secret
	svcNew := newCreditServiceWithPreviousKey(deps, newSecret, oldSecret)

	// Token signed with old key should still validate
	claims, err := svcNew.ValidateSpendToken(tokenResp.Token)
	if err != nil {
		t.Fatalf("ValidateSpendToken() with rotated key unexpected error: %v", err)
	}
	if claims.AccountID != "acct-1" {
		t.Errorf("ValidateSpendToken() AccountID = %q, want %q", claims.AccountID, "acct-1")
	}
}

func TestCreditService_ValidateSpendToken_RejectsUnknownKey(t *testing.T) {
	deps := newCreditTestDeps()
	ctx := context.Background()
	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	// Issue token with secret "A"
	svcA := newCreditServiceWithPreviousKey(deps, "secret-A-32-bytes-long!!!!!!!!!!", "")
	tokenResp, _ := svcA.IssueSpendToken(ctx, "acct-1", 5, "ai_chat")

	// Create service with secret "B" and previous "C" (neither is "A")
	svcBC := newCreditServiceWithPreviousKey(deps, "secret-B-32-bytes-long!!!!!!!!!!", "secret-C-32-bytes-long!!!!!!!!!!")

	_, err := svcBC.ValidateSpendToken(tokenResp.Token)
	if err == nil {
		t.Error("ValidateSpendToken() expected error for token signed with unknown key")
	}
}

func TestCreditService_ValidateSpendToken_NoPreviousKey(t *testing.T) {
	deps := newCreditTestDeps()
	ctx := context.Background()
	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	// Issue with secret "A"
	svcA := newCreditServiceWithPreviousKey(deps, "secret-A-32-bytes-long!!!!!!!!!!", "")
	tokenResp, _ := svcA.IssueSpendToken(ctx, "acct-1", 5, "ai_chat")

	// Service with secret "B" and NO previous key — should reject
	svcB := newCreditServiceWithPreviousKey(deps, "secret-B-32-bytes-long!!!!!!!!!!", "")
	_, err := svcB.ValidateSpendToken(tokenResp.Token)
	if err == nil {
		t.Error("ValidateSpendToken() expected error for mismatched key without previous")
	}
}

// newCreditServiceWithPreviousKey creates a CreditService with optional previous JWT key.
func newCreditServiceWithPreviousKey(deps creditTestDeps, jwtSecret, jwtPreviousSecret string) *CreditService {
	return NewCreditService(CreditServiceDeps{
		Credits:           deps.credits,
		Accounts:          deps.accounts,
		Clock:             deps.clock,
		JWTSecret:         jwtSecret,
		JWTPreviousSecret: jwtPreviousSecret,
	})
}

// --- Expiry tests ---

func TestCreditService_ExpireStaleCredits(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 100)

	// Advance past free credit expiry (30 days)
	deps.clock.Advance(31 * 24 * time.Hour)

	expired, err := svc.ExpireStaleCredits(ctx)
	if err != nil {
		t.Fatalf("ExpireStaleCredits() unexpected error: %v", err)
	}
	if expired != 1 {
		t.Errorf("ExpireStaleCredits() = %d, want 1", expired)
	}

	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeBalance != 0 {
		t.Errorf("ExpireStaleCredits() FreeBalance = %d, want 0", bal.FreeBalance)
	}
	if bal.PaidBalance != 100 {
		t.Errorf("ExpireStaleCredits() PaidBalance = %d, want 100 (unchanged)", bal.PaidBalance)
	}
}

func TestCreditService_ExpireStaleCredits_NotYetExpired(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "test-secret")
	ctx := context.Background()

	seedAccountWithCredits(t, deps, "acct-1", "peer-1", 50, 0)

	// Only 15 days — not expired yet
	deps.clock.Advance(15 * 24 * time.Hour)

	expired, err := svc.ExpireStaleCredits(ctx)
	if err != nil {
		t.Fatalf("ExpireStaleCredits() unexpected error: %v", err)
	}
	if expired != 0 {
		t.Errorf("ExpireStaleCredits() = %d, want 0 (not yet expired)", expired)
	}

	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeBalance != 50 {
		t.Errorf("ExpireStaleCredits() FreeBalance = %d, want 50 (unchanged)", bal.FreeBalance)
	}
}

// --- Refund tests ---

// TestCreditService_Refund_PaidDoesNotCountAsPurchase: a Stop-button refund of a paid spend
// returns the credits to the paid bucket without bumping lifetime_purchased again.
func TestCreditService_Refund_PaidDoesNotCountAsPurchase(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "secret")
	ctx := context.Background()
	seedAccountWithCredits(t, deps, "acc-refund", "peer-refund", 0, 500)

	if err := deps.credits.SpendPaidOnly(ctx, "acc-refund", 200, "agent_completion_detailed", "req-1"); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if err := svc.Refund(ctx, "acc-refund", 200, "agent_refund", "req-1", SpendSourcePaid); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if err := svc.Refund(ctx, "acc-refund", 200, "agent_refund", "req-1", SpendSourcePaid); err != nil {
		t.Fatalf("second refund: %v", err)
	}
	bal, _ := deps.credits.GetBalance(ctx, "acc-refund")
	if bal.PaidBalance != 500 || bal.LifetimePurchased != 500 {
		t.Fatalf("after paid refund: paid=%d lifetime_purchased=%d, want 500 and 500", bal.PaidBalance, bal.LifetimePurchased)
	}
}

// TestCreditService_RefundEscrow_PaidPartDoesNotCountAsPurchase covers the escrow refund path.
func TestCreditService_RefundEscrow_PaidPartDoesNotCountAsPurchase(t *testing.T) {
	deps := newCreditTestDeps()
	svc := newCreditService(deps, "secret")
	ctx := context.Background()
	seedAccountWithCredits(t, deps, "acc-escrow", "peer-escrow", 100, 400)

	fromFree, fromPaid, err := deps.credits.SpendSplit(ctx, "acc-escrow", 300, "bounty_escrow", "escrow-1")
	if err != nil || fromFree != 100 || fromPaid != 200 {
		t.Fatalf("split = %d/%d err=%v", fromFree, fromPaid, err)
	}
	if err := svc.RefundEscrow(ctx, "acc-escrow", fromFree, fromPaid, "bounty_refund", "escrow-1"); err != nil {
		t.Fatalf("refund escrow: %v", err)
	}
	bal, _ := deps.credits.GetBalance(ctx, "acc-escrow")
	if bal.PaidBalance != 400 || bal.FreeBalance != 100 || bal.LifetimePurchased != 400 {
		t.Fatalf("after escrow refund: free=%d paid=%d lifetime_purchased=%d, want 100 / 400 / 400", bal.FreeBalance, bal.PaidBalance, bal.LifetimePurchased)
	}
}
