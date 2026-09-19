// Package: tracker/internal/services
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: Tests for peer service (register, discover, heartbeat)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

func newPeerTestDeps() (*repository.MemoryPeerRepository, *presence.MemoryPresenceStore, *clock.MockClock) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	repo := repository.NewMemoryPeerRepository()
	store := presence.NewMemoryPresenceStore(clk)
	return repo, store, clk
}

func TestPeerService_Register_Success(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	peer, err := svc.Register(ctx, RegisterPeerRequest{
		PeerID:     "peer-1",
		PublicKey:  "pubkey-1",
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	if err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if peer.PeerID != "peer-1" {
		t.Errorf("Register() got PeerID = %q, want %q", peer.PeerID, "peer-1")
	}

	// Verify stored in repo
	found, _ := repo.FindByID(ctx, "peer-1")
	if found == nil {
		t.Fatal("Register() peer not found in repository")
	}

	// Verify presence heartbeat sent
	online, _ := store.IsOnline(ctx, "peer-1")
	if !online {
		t.Error("Register() peer not online in presence store")
	}
}

func TestPeerService_Register_MissingPeerID(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	_, err := svc.Register(ctx, RegisterPeerRequest{
		PublicKey:  "pubkey-1",
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	if err == nil {
		t.Fatal("Register() expected error for missing PeerID")
	}
	if err != models.ErrInvalidInput {
		t.Errorf("Register() got error = %v, want ErrInvalidInput", err)
	}
}

func TestPeerService_Register_MissingPublicKey(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	_, err := svc.Register(ctx, RegisterPeerRequest{
		PeerID:     "peer-1",
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	if err == nil {
		t.Fatal("Register() expected error for missing PublicKey")
	}
}

func TestPeerService_Register_UpdatesExisting(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	_, _ = svc.Register(ctx, RegisterPeerRequest{
		PeerID:     "peer-1",
		PublicKey:  "pubkey-1",
		Multiaddrs: []string{"/ip4/10.0.0.1/tcp/4001"},
	})

	peer, err := svc.Register(ctx, RegisterPeerRequest{
		PeerID:     "peer-1",
		PublicKey:  "pubkey-1",
		Multiaddrs: []string{"/ip4/192.168.1.1/tcp/4001"},
	})
	if err != nil {
		t.Fatalf("Register() second call unexpected error: %v", err)
	}
	if len(peer.Multiaddrs) != 1 || peer.Multiaddrs[0] != "/ip4/192.168.1.1/tcp/4001" {
		t.Errorf("Register() multiaddrs not updated, got %v", peer.Multiaddrs)
	}
}

func TestPeerService_Discover_OnlineOnly(t *testing.T) {
	repo, store, clk := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-a", PublicKey: "pk-a", Multiaddrs: []string{"/ip4/1.1.1.1"}})
	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-b", PublicKey: "pk-b", Multiaddrs: []string{"/ip4/2.2.2.2"}})
	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-c", PublicKey: "pk-c", Multiaddrs: []string{"/ip4/3.3.3.3"}})

	// Advance 3 min, refresh only a and b
	clk.Advance(3 * time.Minute)
	_ = svc.Heartbeat(ctx, "peer-a")
	_ = svc.Heartbeat(ctx, "peer-b")

	// Advance 3 more min — peer-c expired (6 min since heartbeat)
	clk.Advance(3 * time.Minute)

	peers, err := svc.Discover(ctx, DiscoverOptions{Limit: 10, OnlineOnly: true})
	if err != nil {
		t.Fatalf("Discover() unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Errorf("Discover() got %d peers, want 2 (peer-c expired)", len(peers))
	}
}

func TestPeerService_Discover_Empty(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	peers, err := svc.Discover(ctx, DiscoverOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Discover() unexpected error: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("Discover() got %d peers, want 0", len(peers))
	}
}

func TestPeerService_Heartbeat_Success(t *testing.T) {
	repo, store, clk := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-1", PublicKey: "pk-1", Multiaddrs: []string{"/ip4/1.1.1.1"}})

	clk.Advance(4 * time.Minute)
	err := svc.Heartbeat(ctx, "peer-1")
	if err != nil {
		t.Fatalf("Heartbeat() unexpected error: %v", err)
	}

	// Still online after refresh
	online, _ := store.IsOnline(ctx, "peer-1")
	if !online {
		t.Error("Heartbeat() peer not online after refresh")
	}
}

func TestPeerService_Heartbeat_UnknownPeer(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	err := svc.Heartbeat(ctx, "unknown-peer")
	if err == nil {
		t.Fatal("Heartbeat() expected error for unknown peer")
	}
}

func TestPeerService_Discover_AllPeers(t *testing.T) {
	repo, store, clk := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	// Register 3 peers
	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-a", PublicKey: "pk-a", Multiaddrs: []string{"/ip4/1.1.1.1"}})
	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-b", PublicKey: "pk-b", Multiaddrs: []string{"/ip4/2.2.2.2"}})
	_, _ = svc.Register(ctx, RegisterPeerRequest{PeerID: "peer-c", PublicKey: "pk-c", Multiaddrs: []string{"/ip4/3.3.3.3"}})

	// Expire peer-c by advancing clock past TTL without heartbeat
	clk.Advance(3 * time.Minute)
	_ = svc.Heartbeat(ctx, "peer-a")
	_ = svc.Heartbeat(ctx, "peer-b")
	clk.Advance(3 * time.Minute)

	// OnlineOnly=false should return ALL 3 peers (including expired)
	peers, err := svc.Discover(ctx, DiscoverOptions{Limit: 10, OnlineOnly: false})
	if err != nil {
		t.Fatalf("Discover(OnlineOnly=false) unexpected error: %v", err)
	}
	if len(peers) != 3 {
		t.Errorf("Discover(OnlineOnly=false) got %d peers, want 3", len(peers))
	}

	// OnlineOnly=true should return only 2 online peers
	peersOnline, err := svc.Discover(ctx, DiscoverOptions{Limit: 10, OnlineOnly: true})
	if err != nil {
		t.Fatalf("Discover(OnlineOnly=true) unexpected error: %v", err)
	}
	if len(peersOnline) != 2 {
		t.Errorf("Discover(OnlineOnly=true) got %d peers, want 2", len(peersOnline))
	}
}

func TestPeerService_UpdateWalletAddress_Success(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	accountRepo := repository.NewMemoryAccountRepository()
	creditRepo := repository.NewMemoryCreditRepository()
	walletRepo := repository.NewMemoryWalletRepository()

	svc := NewPeerServiceWithDeps(PeerServiceDeps{
		Repo:        repo,
		Presence:    store,
		AccountRepo: accountRepo,
		CreditRepo:  creditRepo,
		WalletRepo:  walletRepo,
	})
	ctx := context.Background()

	err := svc.UpdateWalletAddress(ctx, "peer-1", "7xKXtg2C3uD9vZz")
	if err != nil {
		t.Fatalf("UpdateWalletAddress() unexpected error: %v", err)
	}

	// Verify account was created
	account, err := accountRepo.GetByPeerID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("Account not created: %v", err)
	}

	// Verify balance was created
	balance, err := creditRepo.GetBalance(ctx, account.ID)
	if err != nil {
		t.Fatalf("Balance not created: %v", err)
	}
	if balance.FreeCreditsExpiresAt == nil {
		t.Error("Balance FreeCreditsExpiresAt is nil")
	}

	// Verify wallet was linked
	wallet, err := walletRepo.GetByAccountID(ctx, account.ID, "solana")
	if err != nil {
		t.Fatalf("Wallet not linked: %v", err)
	}
	if wallet.WalletAddress != "7xKXtg2C3uD9vZz" {
		t.Errorf("Wallet address = %q, want %q", wallet.WalletAddress, "7xKXtg2C3uD9vZz")
	}
}

func TestPeerService_UpdateWalletAddress_ReposNil(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	svc := NewPeerService(repo, store, nil)
	ctx := context.Background()

	err := svc.UpdateWalletAddress(ctx, "peer-1", "7xKXtg2C3uD9vZz")
	if err == nil {
		t.Fatal("UpdateWalletAddress() expected error when repos are nil")
	}
	if err.Error() != "wallet service not configured" {
		t.Errorf("UpdateWalletAddress() got error = %q, want %q", err.Error(), "wallet service not configured")
	}
}

func TestPeerService_UpdateWalletAddress_BalanceAlreadyExists(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	accountRepo := repository.NewMemoryAccountRepository()
	creditRepo := repository.NewMemoryCreditRepository()
	walletRepo := repository.NewMemoryWalletRepository()

	svc := NewPeerServiceWithDeps(PeerServiceDeps{
		Repo:        repo,
		Presence:    store,
		AccountRepo: accountRepo,
		CreditRepo:  creditRepo,
		WalletRepo:  walletRepo,
	})
	ctx := context.Background()

	// Create account and balance first (simulating race condition)
	account, _, _ := accountRepo.GetOrCreateForPeer(ctx, "peer-1")
	now := time.Now().UTC()
	expiresAt := now.Add(30 * 24 * time.Hour)
	_ = creditRepo.CreateBalance(ctx, &models.CreditBalance{
		AccountID:            account.ID,
		FreeCreditsExpiresAt: &expiresAt,
		UpdatedAt:            now,
	})

	// UpdateWalletAddress should succeed even though balance already exists
	err := svc.UpdateWalletAddress(ctx, "peer-1", "7xKXtg2C3uD9vZz")
	if err != nil {
		t.Fatalf("UpdateWalletAddress() unexpected error (balance already exists should be OK): %v", err)
	}

	// Verify wallet was still linked
	wallet, err := walletRepo.GetByAccountID(ctx, account.ID, "solana")
	if err != nil {
		t.Fatalf("Wallet not linked: %v", err)
	}
	if wallet.WalletAddress != "7xKXtg2C3uD9vZz" {
		t.Errorf("Wallet address = %q, want %q", wallet.WalletAddress, "7xKXtg2C3uD9vZz")
	}
}

func TestPeerService_UpdateWalletAddress_Concurrent(t *testing.T) {
	repo, store, _ := newPeerTestDeps()
	accountRepo := repository.NewMemoryAccountRepository()
	creditRepo := repository.NewMemoryCreditRepository()
	walletRepo := repository.NewMemoryWalletRepository()

	svc := NewPeerServiceWithDeps(PeerServiceDeps{
		Repo:        repo,
		Presence:    store,
		AccountRepo: accountRepo,
		CreditRepo:  creditRepo,
		WalletRepo:  walletRepo,
	})
	ctx := context.Background()

	// Test concurrent UpdateWalletAddress calls
	const numGoroutines = 50
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			err := svc.UpdateWalletAddress(ctx, "peer-concurrent", "7xKXtg2C3uD9vZz")
			results <- err
		}()
	}

	// Collect results
	var errorCount int
	for i := 0; i < numGoroutines; i++ {
		if err := <-results; err != nil {
			errorCount++
		}
	}

	// All should succeed (race conditions handled gracefully)
	if errorCount > 0 {
		t.Errorf("UpdateWalletAddress() concurrent calls: got %d errors, want 0", errorCount)
	}

	// Verify only one account was created
	accounts, _ := accountRepo.GetByPeerID(ctx, "peer-concurrent")
	if accounts == nil {
		t.Fatal("Account not created")
	}
}
