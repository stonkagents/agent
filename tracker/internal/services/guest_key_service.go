// Package services: Guest key creation with unified credit ledger.
// Creates account + peer (guest) + credit balance + guest_api_keys mapping.

package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// GuestKeyServiceDeps holds dependencies for GuestKeyService.
type GuestKeyServiceDeps struct {
	Peers     repository.PeerRepository
	Accounts  repository.AccountRepository
	Credits   repository.CreditRepository
	GuestKeys repository.GuestKeyMappingRepository
	Clock     clock.Clock
}

// GuestKeyService creates guest API keys backed by accounts and the unified credit ledger.
type GuestKeyService struct {
	peers     repository.PeerRepository
	accounts  repository.AccountRepository
	credits   repository.CreditRepository
	guestKeys repository.GuestKeyMappingRepository
	clock     clock.Clock
}

// NewGuestKeyService creates a new GuestKeyService.
func NewGuestKeyService(deps GuestKeyServiceDeps) *GuestKeyService {
	return &GuestKeyService{
		peers:     deps.Peers,
		accounts:  deps.Accounts,
		credits:   deps.Credits,
		guestKeys: deps.GuestKeys,
		clock:     deps.Clock,
	}
}

// CreateGuestKey creates a new guest API key with an account and default credits.
// Implements repository.GuestKeyRepository for drop-in use in the portal handler.
func (s *GuestKeyService) CreateGuestKey(ctx context.Context, defaultCredits int) (apiKey string, err error) {
	apiKey, err = repository.GenerateAPIKey()
	if err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}

	accountID := uuid.New().String()
	peerID := "guest:" + accountID

	now := s.clock.Now().UTC()

	// Placeholder peer so accounts FK is satisfied (peer_id REFERENCES peers(peer_id)).
	placeholderPubkey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	peer := &models.Peer{
		PeerID:     peerID,
		PublicKey:  placeholderPubkey,
		Multiaddrs: []string{},
		FirstSeen:  now,
		LastSeen:   now,
	}
	if err := s.peers.Create(ctx, peer); err != nil {
		return "", fmt.Errorf("create guest peer: %w", err)
	}

	account := &models.Account{
		ID:        accountID,
		PeerID:    peerID,
		Status:    models.AccountStatusActive,
		CreatedAt: now,
	}
	if err := s.accounts.Create(ctx, account); err != nil {
		return "", fmt.Errorf("create account: %w", err)
	}

	expiresAt := now.Add(30 * 24 * time.Hour)
	balance := &models.CreditBalance{
		AccountID:            accountID,
		FreeBalance:          0,
		PaidBalance:          0,
		FreeCreditsExpiresAt: &expiresAt,
		UpdatedAt:            now,
	}
	if err := s.credits.CreateBalance(ctx, balance); err != nil {
		return "", fmt.Errorf("create balance: %w", err)
	}

	requestID := "guest:" + accountID
	if err := s.credits.CreditFree(ctx, accountID, defaultCredits, "guest_grant", requestID, expiresAt); err != nil {
		return "", fmt.Errorf("grant credits: %w", err)
	}

	if err := s.guestKeys.Create(ctx, apiKey, accountID); err != nil {
		return "", fmt.Errorf("create guest key mapping: %w", err)
	}

	return apiKey, nil
}
