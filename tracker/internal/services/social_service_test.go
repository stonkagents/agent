// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: Tests for social connection service (verify, grant bonus, disconnect)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type socialTestDeps struct {
	social   *repository.MemorySocialRepository
	credits  *repository.MemoryCreditRepository
	accounts *repository.MemoryAccountRepository
	clock    *clock.MockClock
}

func newSocialTestDeps() socialTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	return socialTestDeps{
		social:   repository.NewMemorySocialRepository(),
		credits:  repository.NewMemoryCreditRepositoryWithClock(clk),
		accounts: repository.NewMemoryAccountRepository(),
		clock:    clk,
	}
}

func newSocialService(deps socialTestDeps) *SocialService {
	return NewSocialService(SocialServiceDeps{
		Social:   deps.social,
		Credits:  deps.credits,
		Accounts: deps.accounts,
		Clock:    deps.clock,
	})
}

// seedSocialTestAccount creates an account with initial free credits.
func seedSocialTestAccount(t *testing.T, deps socialTestDeps, accountID, peerID string, freeCredits int) {
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

// --- ConfirmConnection tests ---

func TestSocialService_ConfirmConnection_GitHub(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()
	seedSocialTestAccount(t, deps, "acct-1", "peer-1", 50)

	resp, err := svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformGitHub,
		PlatformUserID: "octocat-123",
	})
	if err != nil {
		t.Fatalf("ConfirmConnection() unexpected error: %v", err)
	}
	if resp.BonusGranted != 50 {
		t.Errorf("ConfirmConnection() bonus = %d, want 50", resp.BonusGranted)
	}
	if resp.NewBalance.Free != 100 {
		t.Errorf("ConfirmConnection() new free balance = %d, want 100 (50+50)", resp.NewBalance.Free)
	}
}

func TestSocialService_ConfirmConnection_Email(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()
	seedSocialTestAccount(t, deps, "acct-1", "peer-1", 50)

	resp, err := svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformEmail,
		PlatformUserID: "user@example.com",
	})
	if err != nil {
		t.Fatalf("ConfirmConnection() unexpected error: %v", err)
	}
	if resp.BonusGranted != 25 {
		t.Errorf("ConfirmConnection() bonus = %d, want 25 (email)", resp.BonusGranted)
	}
}

func TestSocialService_ConfirmConnection_DuplicatePlatform(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()
	seedSocialTestAccount(t, deps, "acct-1", "peer-1", 50)

	// First connection
	_, _ = svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformGitHub,
		PlatformUserID: "octocat-123",
	})

	// Duplicate should fail
	_, err := svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformGitHub,
		PlatformUserID: "octocat-456",
	})
	if err != models.ErrAlreadyExists {
		t.Errorf("ConfirmConnection() duplicate got error = %v, want ErrAlreadyExists", err)
	}
}

func TestSocialService_ConfirmConnection_SocialCapEnforced(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()
	seedSocialTestAccount(t, deps, "acct-1", "peer-1", 50)

	// Grant all platforms to approach cap
	platforms := []struct {
		platform string
		userID   string
	}{
		{models.PlatformGitHub, "gh-1"},
		{models.PlatformTwitter, "tw-1"},
		{models.PlatformEmail, "e@e.com"},
		{models.PlatformDiscord, "disc-1"},
		{models.PlatformTelegram, "tg-1"},
		{models.PlatformCalendar, "cal-1"},
	}
	for _, p := range platforms {
		_, _ = svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
			AccountID:      "acct-1",
			Platform:       p.platform,
			PlatformUserID: p.userID,
		})
	}

	// Total social granted: 50+25+25+25+25+50 = 200 (Plan B amounts)
	// Wallet (100) is excluded — it would push total to 300, still under the 400 cap.
	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	// 50 (seed) + 200 (social) = 250
	if bal.FreeBalance != 250 {
		t.Errorf("After all social connections, FreeBalance = %d, want 250 (50+200)", bal.FreeBalance)
	}
}

func TestSocialService_ConfirmConnection_AccountNotFound(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()

	_, err := svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "nonexistent",
		Platform:       models.PlatformGitHub,
		PlatformUserID: "octocat",
	})
	if err != models.ErrNotFound {
		t.Errorf("ConfirmConnection() got error = %v, want ErrNotFound", err)
	}
}

// --- ListConnections tests ---

func TestSocialService_ListConnections(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()
	seedSocialTestAccount(t, deps, "acct-1", "peer-1", 50)

	_, _ = svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformGitHub,
		PlatformUserID: "octocat",
	})
	_, _ = svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformEmail,
		PlatformUserID: "e@e.com",
	})

	conns, err := svc.ListConnections(ctx, "acct-1")
	if err != nil {
		t.Fatalf("ListConnections() unexpected error: %v", err)
	}
	if len(conns) != 2 {
		t.Errorf("ListConnections() count = %d, want 2", len(conns))
	}
}

// --- Disconnect tests ---

func TestSocialService_Disconnect(t *testing.T) {
	deps := newSocialTestDeps()
	svc := newSocialService(deps)
	ctx := context.Background()
	seedSocialTestAccount(t, deps, "acct-1", "peer-1", 50)

	_, _ = svc.ConfirmConnection(ctx, ConfirmConnectionRequest{
		AccountID:      "acct-1",
		Platform:       models.PlatformGitHub,
		PlatformUserID: "octocat",
	})

	err := svc.Disconnect(ctx, "acct-1", models.PlatformGitHub)
	if err != nil {
		t.Fatalf("Disconnect() unexpected error: %v", err)
	}

	// Should be disconnected now
	conns, _ := svc.ListConnections(ctx, "acct-1")
	if len(conns) != 0 {
		t.Errorf("Disconnect() connections remaining = %d, want 0", len(conns))
	}

	// Balance should NOT be revoked (bonus stays in ledger)
	bal, _ := deps.credits.GetBalance(ctx, "acct-1")
	if bal.FreeBalance != 100 {
		t.Errorf("Disconnect() FreeBalance = %d, want 100 (bonus not revoked)", bal.FreeBalance)
	}
}
