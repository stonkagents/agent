// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: Challenge-response registration with Ed25519 signature verification

package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Registration constants.
const (
	NonceSize              = 32
	NonceExpirySeconds     = 300 // 5 minutes
	InitialFreeCredits     = 150
	PresenceGrantCredits   = 200
	PresenceGrantThreshold = 15 * time.Minute
	SignDomainPrefix       = "stonkagents-register-v1:"
)

// Registration service errors.
var (
	ErrIPBlocked        = errors.New("ip blocked")
	ErrInvalidSignature = errors.New("invalid signature")
)

// ChallengeRequest is the input for requesting a registration challenge.
type ChallengeRequest struct {
	PeerID   string
	ClientIP string
}

// ChallengeResponse is returned from a successful challenge.
type ChallengeResponse struct {
	Nonce     []byte
	ExpiresIn int // seconds
}

// RegisterRequest is the input for completing registration.
type RegisterRequest struct {
	PeerID        string
	Ed25519Pubkey []byte
	Signature     []byte
	Multiaddrs    []string
	ClientVersion string
	// DisplayName is the daemon's current name (already sanitized). Applied through
	// models.ResolveDisplayName: a placeholder never overwrites a stored name.
	DisplayName string
	ClientIP    string
	Country     string
	Region      string
}

// RegisterResponse is returned from a successful registration.
type RegisterResponse struct {
	APIKey    string
	AccountID string
	Credits   CreditSummary
	IsNew     bool
}

// CreditSummary is a snapshot of free/paid balances.
type CreditSummary struct {
	Free int
	Paid int
}

// RegistrationServiceDeps holds all dependencies for RegistrationService.
type RegistrationServiceDeps struct {
	Accounts repository.AccountRepository
	Credits  repository.CreditRepository
	Nonces   repository.NonceRepository
	Blocks   repository.BlockRepository
	Peers    repository.PeerRepository
	APIKeys  repository.PeerAPIKeyRepository
	Presence presence.PresenceStore
	Clock    clock.Clock
}

// RegistrationService handles challenge-response peer registration.
type RegistrationService struct {
	accounts repository.AccountRepository
	credits  repository.CreditRepository
	nonces   repository.NonceRepository
	blocks   repository.BlockRepository
	peers    repository.PeerRepository
	apiKeys  repository.PeerAPIKeyRepository
	presence presence.PresenceStore
	clock    clock.Clock
}

// NewRegistrationService creates a new RegistrationService.
func NewRegistrationService(deps RegistrationServiceDeps) *RegistrationService {
	return &RegistrationService{
		accounts: deps.Accounts,
		credits:  deps.Credits,
		nonces:   deps.Nonces,
		blocks:   deps.Blocks,
		peers:    deps.Peers,
		apiKeys:  deps.APIKeys,
		presence: deps.Presence,
		clock:    deps.Clock,
	}
}

// Challenge generates a cryptographic nonce for the peer to sign.
func (s *RegistrationService) Challenge(ctx context.Context, req ChallengeRequest) (*ChallengeResponse, error) {
	if req.PeerID == "" {
		return nil, models.ErrInvalidInput
	}

	// Check IP block
	if err := s.checkIPBlocked(ctx, req.ClientIP); err != nil {
		return nil, err
	}

	// TD-075: Return existing valid nonce instead of replacing it.
	// Prevents DoS where attacker floods challenges to invalidate victim's nonce.
	now := s.clock.Now()
	existing, err := s.nonces.GetActiveByPeerID(ctx, req.PeerID)
	if err == nil && existing.ExpiresAt.After(now) {
		// Active, non-expired nonce exists — return it
		remainingSecs := int(existing.ExpiresAt.Sub(now).Seconds())
		if remainingSecs < 1 {
			remainingSecs = 1
		}
		return &ChallengeResponse{
			Nonce:     existing.Nonce,
			ExpiresIn: remainingSecs,
		}, nil
	}

	// No active nonce or expired — generate fresh one
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	nonceRecord := &models.RegistrationNonce{
		ID:        uuid.New().String(),
		PeerID:    req.PeerID,
		Nonce:     nonce,
		Consumed:  false,
		ExpiresAt: now.Add(time.Duration(NonceExpirySeconds) * time.Second),
		CreatedAt: now,
	}

	if err := s.nonces.Upsert(ctx, nonceRecord); err != nil {
		return nil, fmt.Errorf("store nonce: %w", err)
	}

	return &ChallengeResponse{
		Nonce:     nonce,
		ExpiresIn: NonceExpirySeconds,
	}, nil
}

// Register completes the challenge-response flow and creates or returns an account.
func (s *RegistrationService) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	if req.PeerID == "" || len(req.Ed25519Pubkey) == 0 || len(req.Signature) == 0 {
		return nil, models.ErrInvalidInput
	}

	// Check IP block
	if err := s.checkIPBlocked(ctx, req.ClientIP); err != nil {
		return nil, err
	}

	// Lookup unconsumed nonce for peer
	nonceRecord, err := s.nonces.GetActiveByPeerID(ctx, req.PeerID)
	if err != nil {
		return nil, err // ErrNotFound if no nonce exists
	}

	// Check nonce expiry
	if s.clock.Now().After(nonceRecord.ExpiresAt) {
		return nil, models.ErrNonceExpired
	}

	// Verify Ed25519 signature over domain-separated message
	message := []byte(SignDomainPrefix + base64.StdEncoding.EncodeToString(nonceRecord.Nonce))
	if len(req.Ed25519Pubkey) != ed25519.PublicKeySize {
		return nil, ErrInvalidSignature
	}
	if !ed25519.Verify(req.Ed25519Pubkey, message, req.Signature) {
		return nil, ErrInvalidSignature
	}

	// Mark nonce as consumed
	if err := s.nonces.MarkConsumed(ctx, nonceRecord.ID); err != nil {
		return nil, fmt.Errorf("consume nonce: %w", err)
	}

	// Upsert peer record (updates last_seen, multiaddrs)
	now := s.clock.Now()
	peer := &models.Peer{
		PeerID:     req.PeerID,
		PublicKey:  base64.StdEncoding.EncodeToString(req.Ed25519Pubkey),
		Multiaddrs: req.Multiaddrs,
		LastSeen:   now,
		Country:    req.Country,
		Region:     req.Region,
	}
	existing, _ := s.peers.FindByID(ctx, req.PeerID)
	if existing != nil {
		peer.FirstSeen = existing.FirstSeen
		peer.DisplayName = models.ResolveDisplayName(existing.DisplayName, req.DisplayName)
	} else {
		peer.FirstSeen = now
		peer.DisplayName = models.ResolveDisplayName("", req.DisplayName)
	}
	_ = s.peers.Upsert(ctx, peer)

	// Send heartbeat
	_ = s.presence.Heartbeat(ctx, req.PeerID, DefaultPresenceTTL)

	// Check if returning peer (account already exists)
	acct, err := s.accounts.GetByPeerID(ctx, req.PeerID)
	if err == nil {
		// Returning peer — return existing account + credentials
		apiKey, _ := s.apiKeys.GetByPeerID(ctx, req.PeerID)
		bal, _ := s.credits.GetBalance(ctx, acct.ID)

		credits := CreditSummary{}
		if bal != nil {
			credits.Free = bal.FreeBalance
			credits.Paid = bal.PaidBalance
		}

		return &RegisterResponse{
			APIKey:    apiKey,
			AccountID: acct.ID,
			Credits:   credits,
			IsNew:     false,
		}, nil
	}

	// New peer — create account
	accountID := uuid.New().String()
	newAccount := &models.Account{
		ID:        accountID,
		PeerID:    req.PeerID,
		Status:    models.AccountStatusActive,
		CreatedAt: now,
	}
	if err := s.accounts.Create(ctx, newAccount); err != nil {
		return nil, fmt.Errorf("create account: %w", err)
	}

	// Initialize credit balance
	expiresAt := now.Add(30 * 24 * time.Hour)
	balance := &models.CreditBalance{
		AccountID:            accountID,
		FreeBalance:          0,
		PaidBalance:          0,
		FreeCreditsExpiresAt: &expiresAt,
		UpdatedAt:            now,
	}
	if err := s.credits.CreateBalance(ctx, balance); err != nil {
		return nil, fmt.Errorf("create balance: %w", err)
	}

	// Grant initial free credits
	requestID := "reg:" + accountID
	if err := s.credits.CreditFree(ctx, accountID, InitialFreeCredits, "registration_grant", requestID, expiresAt); err != nil {
		return nil, fmt.Errorf("grant credits: %w", err)
	}

	// Seed detailed-mode trial calls so new users can taste gpt-5.4 before paying.
	trialExpiresAt := now.Add(DetailedTrialExpiryWindow)
	if err := s.credits.SetDetailedTrial(ctx, accountID, DetailedTrialInitial, trialExpiresAt); err != nil {
		return nil, fmt.Errorf("seed detailed trial: %w", err)
	}

	// Issue API key
	apiKey, err := s.apiKeys.Create(ctx, req.PeerID)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}

	return &RegisterResponse{
		APIKey:    apiKey,
		AccountID: accountID,
		Credits:   CreditSummary{Free: InitialFreeCredits, Paid: 0},
		IsNew:     true,
	}, nil
}

// checkIPBlocked checks all block types for the given IP.
func (s *RegistrationService) checkIPBlocked(ctx context.Context, ip string) error {
	if ip == "" {
		return nil
	}

	now := s.clock.Now()

	// Check IP block
	blocked, err := s.blocks.IsBlocked(ctx, models.BlockTypeIP, ip, now)
	if err != nil {
		return fmt.Errorf("check ip block: %w", err)
	}
	if blocked {
		return ErrIPBlocked
	}

	// Check /24 subnet block
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		subnet24 := strings.Join(parts[:3], ".")
		blocked, err = s.blocks.IsBlocked(ctx, models.BlockTypeSubnet24, subnet24, now)
		if err != nil {
			return fmt.Errorf("check subnet24 block: %w", err)
		}
		if blocked {
			return ErrIPBlocked
		}

		// Check /16 subnet block
		subnet16 := strings.Join(parts[:2], ".")
		blocked, err = s.blocks.IsBlocked(ctx, models.BlockTypeSubnet16, subnet16, now)
		if err != nil {
			return fmt.Errorf("check subnet16 block: %w", err)
		}
		if blocked {
			return ErrIPBlocked
		}
	}

	return nil
}

// CheckPresenceGrants grants 200 bonus credits to accounts with 15+ min continuous
// daemon presence that haven't already received the grant. Returns the number of grants issued.
func (s *RegistrationService) CheckPresenceGrants(ctx context.Context) (int, error) {
	now := s.clock.Now()
	onlinePeerIDs, err := s.presence.OnlinePeerIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list online peers: %w", err)
	}

	granted := 0
	for _, peerID := range onlinePeerIDs {
		peer, err := s.peers.FindByID(ctx, peerID)
		if err != nil {
			continue
		}
		// Check continuous session length
		if peer.CurrentSessionStart == nil || now.Sub(*peer.CurrentSessionStart) < PresenceGrantThreshold {
			continue
		}
		// Look up account
		acct, err := s.accounts.GetByPeerID(ctx, peerID)
		if err != nil {
			continue
		}
		// Check if presence grant already issued (idempotent via request_id)
		requestID := "presence:" + acct.ID
		_, err = s.credits.GetTransactionByRequestID(ctx, acct.ID, requestID)
		if err == nil {
			continue // Already granted
		}
		// Grant the credits
		expiresAt := now.Add(30 * 24 * time.Hour)
		if err := s.credits.CreditFree(ctx, acct.ID, PresenceGrantCredits, "presence_grant", requestID, expiresAt); err != nil {
			continue
		}
		granted++
	}
	return granted, nil
}
