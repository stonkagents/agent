// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: Tests for challenge-response registration service

package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// registrationTestDeps bundles all dependencies needed for RegistrationService tests.
type registrationTestDeps struct {
	accounts *repository.MemoryAccountRepository
	credits  *repository.MemoryCreditRepository
	nonces   *repository.MemoryNonceRepository
	blocks   *repository.MemoryBlockRepository
	peers    *repository.MemoryPeerRepository
	apiKeys  *repository.MemoryPeerAPIKeyRepository
	presence *presence.MemoryPresenceStore
	clock    *clock.MockClock
}

func newRegistrationTestDeps() registrationTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	return registrationTestDeps{
		accounts: repository.NewMemoryAccountRepository(),
		credits:  repository.NewMemoryCreditRepository(),
		nonces:   repository.NewMemoryNonceRepository(),
		blocks:   repository.NewMemoryBlockRepository(),
		peers:    repository.NewMemoryPeerRepository(),
		apiKeys:  repository.NewMemoryPeerAPIKeyRepository(),
		presence: presence.NewMemoryPresenceStore(clk),
		clock:    clk,
	}
}

func newRegistrationService(deps registrationTestDeps) *RegistrationService {
	return NewRegistrationService(RegistrationServiceDeps{
		Accounts: deps.accounts,
		Credits:  deps.credits,
		Nonces:   deps.nonces,
		Blocks:   deps.blocks,
		Peers:    deps.peers,
		APIKeys:  deps.apiKeys,
		Presence: deps.presence,
		Clock:    deps.clock,
	})
}

func generateTestKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error: %v", err)
	}
	return pub, priv
}

// signChallenge produces the expected domain-separated signature: "stonkagents-register-v1:" + base64(nonce)
func signChallenge(t *testing.T, nonce []byte, privKey ed25519.PrivateKey) []byte {
	t.Helper()
	message := []byte("stonkagents-register-v1:" + base64.StdEncoding.EncodeToString(nonce))
	return ed25519.Sign(privKey, message)
}

// --- Challenge tests ---

func TestRegistrationService_Challenge_Success(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()

	resp, err := svc.Challenge(ctx, ChallengeRequest{
		PeerID:   "12D3KooWTestPeer1",
		ClientIP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Challenge() unexpected error: %v", err)
	}
	if len(resp.Nonce) != 32 {
		t.Errorf("Challenge() nonce length = %d, want 32", len(resp.Nonce))
	}
	if resp.ExpiresIn != 300 {
		t.Errorf("Challenge() ExpiresIn = %d, want 300", resp.ExpiresIn)
	}

	// Verify nonce stored in repo
	stored, err := deps.nonces.GetActiveByPeerID(ctx, "12D3KooWTestPeer1")
	if err != nil {
		t.Fatalf("GetActiveByPeerID() unexpected error: %v", err)
	}
	if len(stored.Nonce) != 32 {
		t.Errorf("stored nonce length = %d, want 32", len(stored.Nonce))
	}
}

func TestRegistrationService_Challenge_IPBlocked(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()

	// Block the IP
	_ = deps.blocks.Insert(ctx, &models.RegistrationBlock{
		ID:         "block-1",
		BlockType:  models.BlockTypeIP,
		BlockValue: "1.2.3.4",
		Reason:     "S-VEL1",
		BlockedAt:  deps.clock.Now(),
		ExpiresAt:  deps.clock.Now().Add(24 * time.Hour),
	})

	_, err := svc.Challenge(ctx, ChallengeRequest{
		PeerID:   "12D3KooWTestPeer1",
		ClientIP: "1.2.3.4",
	})
	if err != ErrIPBlocked {
		t.Errorf("Challenge() got error = %v, want ErrIPBlocked", err)
	}
}

// TestRegistrationService_Challenge_ReturnsExistingValidNonce verifies that a second
// challenge request for the same peer returns the existing nonce if it's still valid.
// TD-075: Previously UPSERT replaced the nonce, enabling DoS by flooding challenges
// (victim's daemon would have a stale nonce and fail registration).
func TestRegistrationService_Challenge_ReturnsExistingValidNonce(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()

	resp1, err := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-1", ClientIP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("first Challenge() error: %v", err)
	}

	// Second challenge within TTL window → same nonce
	resp2, err := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-1", ClientIP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("second Challenge() error: %v", err)
	}

	if string(resp1.Nonce) != string(resp2.Nonce) {
		t.Error("Challenge() second call within TTL should return the same nonce, got different")
	}
}

// TestRegistrationService_Challenge_NewNonceAfterExpiry verifies that a challenge request
// generates a new nonce after the previous one has expired.
func TestRegistrationService_Challenge_NewNonceAfterExpiry(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()

	resp1, err := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-1", ClientIP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("first Challenge() error: %v", err)
	}

	// Advance clock past nonce expiry (300s + 1s)
	deps.clock.Advance(301 * time.Second)

	resp2, err := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-1", ClientIP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("second Challenge() after expiry error: %v", err)
	}

	if string(resp1.Nonce) == string(resp2.Nonce) {
		t.Error("Challenge() after expiry should produce a new nonce, got same")
	}
}

// --- Register tests ---

func TestRegistrationService_Register_NewPeer_Success(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	// Step 1: challenge
	challengeResp, _ := svc.Challenge(ctx, ChallengeRequest{
		PeerID:   "12D3KooWNewPeer",
		ClientIP: "1.2.3.4",
	})

	// Step 2: register
	sig := signChallenge(t, challengeResp.Nonce, priv)
	regResp, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "12D3KooWNewPeer",
		Ed25519Pubkey: pub,
		Signature:     sig,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	// Account created
	if regResp.AccountID == "" {
		t.Error("Register() AccountID is empty")
	}
	if !regResp.IsNew {
		t.Error("Register() IsNew should be true for new peer")
	}

	// 150 free credits granted (Plan B baseline)
	if regResp.Credits.Free != 150 {
		t.Errorf("Register() Credits.Free = %d, want 150", regResp.Credits.Free)
	}
	if regResp.Credits.Paid != 0 {
		t.Errorf("Register() Credits.Paid = %d, want 0", regResp.Credits.Paid)
	}

	// API key issued
	if regResp.APIKey == "" {
		t.Error("Register() APIKey is empty")
	}

	// Verify account in repo
	acct, err := deps.accounts.GetByPeerID(ctx, "12D3KooWNewPeer")
	if err != nil {
		t.Fatalf("GetByPeerID() unexpected error: %v", err)
	}
	if acct.Status != models.AccountStatusActive {
		t.Errorf("Account status = %q, want %q", acct.Status, models.AccountStatusActive)
	}

	// Verify credit balance in repo
	bal, err := deps.credits.GetBalance(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetBalance() unexpected error: %v", err)
	}
	if bal.FreeBalance != 150 {
		t.Errorf("FreeBalance = %d, want 150", bal.FreeBalance)
	}

	// Verify nonce consumed
	_, err = deps.nonces.GetActiveByPeerID(ctx, "12D3KooWNewPeer")
	if err != models.ErrNotFound {
		t.Errorf("GetActiveByPeerID() should return ErrNotFound after consume, got: %v", err)
	}
}

func TestRegistrationService_Register_ReturningPeer_Success(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	// First registration
	ch1, _ := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-ret", ClientIP: "1.2.3.4"})
	sig1 := signChallenge(t, ch1.Nonce, priv)
	first, _ := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-ret",
		Ed25519Pubkey: pub,
		Signature:     sig1,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})

	// Second registration (returning peer)
	deps.clock.Advance(10 * time.Minute)
	ch2, _ := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-ret", ClientIP: "1.2.3.4"})
	sig2 := signChallenge(t, ch2.Nonce, priv)
	second, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-ret",
		Ed25519Pubkey: pub,
		Signature:     sig2,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register() returning peer unexpected error: %v", err)
	}

	// Same account
	if second.AccountID != first.AccountID {
		t.Errorf("Register() returning AccountID = %q, want %q", second.AccountID, first.AccountID)
	}
	if second.IsNew {
		t.Error("Register() IsNew should be false for returning peer")
	}

	// Same API key
	if second.APIKey != first.APIKey {
		t.Errorf("Register() returning APIKey changed")
	}

	// Credits unchanged (no double grant)
	if second.Credits.Free != 150 {
		t.Errorf("Register() returning Credits.Free = %d, want 150 (no double grant)", second.Credits.Free)
	}
}

func TestRegistrationService_Register_InvalidSignature(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, _ := generateTestKeypair(t)

	ch, _ := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-badsig", ClientIP: "1.2.3.4"})

	// Sign with wrong key
	_, wrongPriv := generateTestKeypair(t)
	badSig := signChallenge(t, ch.Nonce, wrongPriv)

	_, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-badsig",
		Ed25519Pubkey: pub,
		Signature:     badSig,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})
	if err != ErrInvalidSignature {
		t.Errorf("Register() got error = %v, want ErrInvalidSignature", err)
	}
}

func TestRegistrationService_Register_ExpiredNonce(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	ch, _ := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-expired", ClientIP: "1.2.3.4"})

	// Advance past nonce expiry (5 minutes)
	deps.clock.Advance(6 * time.Minute)

	sig := signChallenge(t, ch.Nonce, priv)
	_, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-expired",
		Ed25519Pubkey: pub,
		Signature:     sig,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})
	if err != models.ErrNonceExpired {
		t.Errorf("Register() got error = %v, want ErrNonceExpired", err)
	}
}

func TestRegistrationService_Register_NoNonce(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	// No challenge requested — sign arbitrary nonce
	fakeNonce := make([]byte, 32)
	sig := signChallenge(t, fakeNonce, priv)

	_, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-no-nonce",
		Ed25519Pubkey: pub,
		Signature:     sig,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})
	if err != models.ErrNotFound {
		t.Errorf("Register() got error = %v, want ErrNotFound (no nonce)", err)
	}
}

func TestRegistrationService_Register_IPBlocked(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	// Challenge first (unblocked)
	ch, _ := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-blocked", ClientIP: "1.2.3.4"})

	// Block IP after challenge
	_ = deps.blocks.Insert(ctx, &models.RegistrationBlock{
		ID:         "block-1",
		BlockType:  models.BlockTypeIP,
		BlockValue: "1.2.3.4",
		Reason:     "S-VEL1",
		BlockedAt:  deps.clock.Now(),
		ExpiresAt:  deps.clock.Now().Add(24 * time.Hour),
	})

	sig := signChallenge(t, ch.Nonce, priv)
	_, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-blocked",
		Ed25519Pubkey: pub,
		Signature:     sig,
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "1.2.3.4",
	})
	if err != ErrIPBlocked {
		t.Errorf("Register() got error = %v, want ErrIPBlocked", err)
	}
}

func TestRegistrationService_Register_Subnet24Blocked(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	ch, _ := svc.Challenge(ctx, ChallengeRequest{PeerID: "peer-subnet", ClientIP: "10.20.30.40"})

	// Block the /24 subnet
	_ = deps.blocks.Insert(ctx, &models.RegistrationBlock{
		ID:         "block-s24",
		BlockType:  models.BlockTypeSubnet24,
		BlockValue: "10.20.30",
		Reason:     "S-LOC1",
		BlockedAt:  deps.clock.Now(),
		ExpiresAt:  deps.clock.Now().Add(24 * time.Hour),
	})

	sig := signChallenge(t, ch.Nonce, priv)
	_, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "peer-subnet",
		Ed25519Pubkey: pub,
		Signature:     sig,
		Multiaddrs:    []string{"/ip4/10.20.30.40/tcp/4001"},
		ClientVersion: "0.1.0",
		ClientIP:      "10.20.30.40",
	})
	if err != ErrIPBlocked {
		t.Errorf("Register() got error = %v, want ErrIPBlocked", err)
	}
}

func TestRegistrationService_Register_MissingPeerID(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	ctx := context.Background()
	pub, priv := generateTestKeypair(t)

	fakeNonce := make([]byte, 32)
	sig := signChallenge(t, fakeNonce, priv)

	_, err := svc.Register(ctx, RegisterRequest{
		PeerID:        "",
		Ed25519Pubkey: pub,
		Signature:     sig,
		ClientIP:      "1.2.3.4",
	})
	if err != models.ErrInvalidInput {
		t.Errorf("Register() got error = %v, want ErrInvalidInput", err)
	}
}

// registerNamed runs one challenge-response registration for peerID carrying displayName.
func registerNamed(t *testing.T, svc *RegistrationService, deps registrationTestDeps, peerID string, pub ed25519.PublicKey, priv ed25519.PrivateKey, displayName string) {
	t.Helper()
	ctx := context.Background()
	deps.clock.Advance(10 * time.Minute)
	ch, err := svc.Challenge(ctx, ChallengeRequest{PeerID: peerID, ClientIP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("Challenge(): %v", err)
	}
	if _, err := svc.Register(ctx, RegisterRequest{
		PeerID: peerID, Ed25519Pubkey: pub, Signature: signChallenge(t, ch.Nonce, priv),
		Multiaddrs: []string{"/ip4/1.2.3.4/tcp/4001"}, ClientVersion: "2.2.2", ClientIP: "1.2.3.4",
		DisplayName: displayName,
	}); err != nil {
		t.Fatalf("Register(%q): %v", displayName, err)
	}
}

// TestRegistrationService_Register_DisplayNameRule: a registration stores the daemon's
// name when nothing is stored, but a placeholder never overwrites a name the tracker
// (launch claim) or the owner already set; an empty name never clears.
func TestRegistrationService_Register_DisplayNameRule(t *testing.T) {
	deps := newRegistrationTestDeps()
	svc := newRegistrationService(deps)
	pub, priv := generateTestKeypair(t)
	ctx := context.Background()
	name := func() string {
		p, err := deps.peers.FindByID(ctx, "peer-name")
		if err != nil {
			t.Fatalf("peer missing: %v", err)
		}
		return p.DisplayName
	}

	registerNamed(t, svc, deps, "peer-name", pub, priv, "swarm_operator_42")
	if got := name(); got != "swarm_operator_42" {
		t.Fatalf("first registration stored %q, want placeholder", got)
	}
	// Tracker-side rename (launch claim) then a re-registration with the placeholder: kept.
	if err := deps.peers.UpdateDisplayName(ctx, "peer-name", "lalala_agent"); err != nil {
		t.Fatal(err)
	}
	registerNamed(t, svc, deps, "peer-name", pub, priv, "swarm_operator_42")
	if got := name(); got != "lalala_agent" {
		t.Errorf("placeholder overwrote tracker name: %q", got)
	}
	registerNamed(t, svc, deps, "peer-name", pub, priv, "")
	if got := name(); got != "lalala_agent" {
		t.Errorf("empty name cleared tracker name: %q", got)
	}
	// Owner rename from the daemon wins.
	registerNamed(t, svc, deps, "peer-name", pub, priv, "Atlas Prime")
	if got := name(); got != "Atlas Prime" {
		t.Errorf("owner name not applied: %q", got)
	}
}
