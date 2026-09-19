// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-08 (Account Recovery)
// Purpose: Account recovery — verify proof, transfer paid credits, deactivate old account

package services

import (
	"context"
	"fmt"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// MinSocialProofCount is the minimum number of social verifications required for recovery.
const MinSocialProofCount = 2

// RecoverRequest is the input for account recovery.
type RecoverRequest struct {
	NewAccountID        string
	OldPeerID           string
	SocialVerifications []string // platforms to verify (must match old account's connections)
	WalletSignature     []byte   // optional: alternative proof via wallet
}

// RecoverResponse is the output after successful recovery.
type RecoverResponse struct {
	Recovered              bool
	PaidCreditsTransferred int
}

// RecoveryServiceDeps holds dependencies for RecoveryService.
type RecoveryServiceDeps struct {
	Accounts repository.AccountRepository
	Credits  repository.CreditRepository
	Social   repository.SocialRepository
	Wallets  repository.WalletRepository
	APIKeys  repository.PeerAPIKeyRepository
	Clock    clock.Clock
}

// RecoveryService manages account recovery.
type RecoveryService struct {
	accounts repository.AccountRepository
	credits  repository.CreditRepository
	social   repository.SocialRepository
	wallets  repository.WalletRepository
	apiKeys  repository.PeerAPIKeyRepository
	clock    clock.Clock
}

// NewRecoveryService creates a new RecoveryService.
func NewRecoveryService(deps RecoveryServiceDeps) *RecoveryService {
	return &RecoveryService{
		accounts: deps.Accounts,
		credits:  deps.Credits,
		social:   deps.Social,
		wallets:  deps.Wallets,
		apiKeys:  deps.APIKeys,
		clock:    deps.Clock,
	}
}

// Recover performs account recovery by transferring paid credits from old account to new.
func (s *RecoveryService) Recover(ctx context.Context, req RecoverRequest) (*RecoverResponse, error) {
	// Look up old account
	oldAccount, err := s.accounts.GetByPeerID(ctx, req.OldPeerID)
	if err != nil {
		return nil, err
	}

	// Look up new account
	_, err = s.accounts.GetByID(ctx, req.NewAccountID)
	if err != nil {
		return nil, err
	}

	// Verify social proof: check that the claimed platforms match old account's connections
	if len(req.SocialVerifications) < MinSocialProofCount {
		return nil, fmt.Errorf("insufficient social proof: need %d verifications, got %d", MinSocialProofCount, len(req.SocialVerifications))
	}

	verified := 0
	for _, platform := range req.SocialVerifications {
		_, err := s.social.GetByAccountAndPlatform(ctx, oldAccount.ID, platform)
		if err == nil {
			verified++
		}
	}
	if verified < MinSocialProofCount {
		return nil, fmt.Errorf("could not verify %d social connections on old account", MinSocialProofCount)
	}

	// Get old account's balance
	oldBal, err := s.credits.GetBalance(ctx, oldAccount.ID)
	if err != nil {
		return nil, err
	}

	paidToTransfer := oldBal.PaidBalance

	// Transfer paid credits if any
	if paidToTransfer > 0 {
		requestID := fmt.Sprintf("recovery:%s:%s", oldAccount.ID, req.NewAccountID)

		// Zero out old account's paid balance (don't use Spend — it follows free-first policy)
		if err := s.credits.SetPaidBalance(ctx, oldAccount.ID, 0); err != nil {
			return nil, fmt.Errorf("deduct from old account: %w", err)
		}

		// Credit to new account
		if err := s.credits.CreditPaid(ctx, req.NewAccountID, paidToTransfer, "recovery_in", requestID+":in"); err != nil {
			return nil, fmt.Errorf("credit to new account: %w", err)
		}
	}

	// Deactivate old account
	if err := s.accounts.UpdateStatus(ctx, oldAccount.ID, models.AccountStatusRecovered); err != nil {
		return nil, fmt.Errorf("deactivate old account: %w", err)
	}

	return &RecoverResponse{
		Recovered:              true,
		PaidCreditsTransferred: paidToTransfer,
	}, nil
}
