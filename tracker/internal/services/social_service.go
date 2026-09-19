// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: Social connection management, bonus credit grants with per-platform caps

package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Bonus amounts per social platform. Calibrated for the 80%-margin credit
// economy (1 credit ≈ $0.00172): wallet is the strongest signal so it pays
// most, light-touch socials pay less.
var socialBonusAmounts = map[string]int{
	models.PlatformWallet:   100,
	models.PlatformGitHub:   50,
	models.PlatformTwitter:  25,
	models.PlatformEmail:    25,
	models.PlatformDiscord:  25,
	models.PlatformTelegram: 25,
	models.PlatformCalendar: 50,
}

// SocialBonusCap is the maximum total social bonus credits an account can receive.
const SocialBonusCap = 400

// ConfirmConnectionRequest is the input for confirming a social connection.
type ConfirmConnectionRequest struct {
	AccountID      string
	Platform       string
	PlatformUserID string
}

// ConfirmConnectionResponse is the output after confirming a social connection.
type ConfirmConnectionResponse struct {
	BonusGranted int
	NewBalance   CreditSummary
}

// SocialServiceDeps holds dependencies for SocialService.
type SocialServiceDeps struct {
	Social   repository.SocialRepository
	Credits  repository.CreditRepository
	Accounts repository.AccountRepository
	Clock    clock.Clock
}

// SocialService manages social connections and bonus credit grants.
type SocialService struct {
	social   repository.SocialRepository
	credits  repository.CreditRepository
	accounts repository.AccountRepository
	clock    clock.Clock
}

// NewSocialService creates a new SocialService.
func NewSocialService(deps SocialServiceDeps) *SocialService {
	return &SocialService{
		social:   deps.Social,
		credits:  deps.Credits,
		accounts: deps.Accounts,
		clock:    deps.Clock,
	}
}

// ConfirmConnection verifies a social platform connection and grants bonus credits.
func (s *SocialService) ConfirmConnection(ctx context.Context, req ConfirmConnectionRequest) (*ConfirmConnectionResponse, error) {
	// Verify account exists
	_, err := s.accounts.GetByID(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}

	// Check if platform is already connected
	_, err = s.social.GetByAccountAndPlatform(ctx, req.AccountID, req.Platform)
	if err == nil {
		return nil, models.ErrAlreadyExists
	}

	// Determine bonus amount
	bonus, ok := socialBonusAmounts[req.Platform]
	if !ok {
		return nil, fmt.Errorf("unknown platform: %s", req.Platform)
	}

	// Check lifetime social cap
	bal, err := s.credits.GetBalance(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}
	if bal.LifetimeSocialGranted+bonus > SocialBonusCap {
		bonus = SocialBonusCap - bal.LifetimeSocialGranted
		if bonus <= 0 {
			return nil, fmt.Errorf("social bonus cap reached")
		}
	}

	now := s.clock.Now()
	expiresAt := now.Add(30 * 24 * time.Hour)

	// Insert social connection
	conn := &models.SocialConnection{
		ID:             uuid.New().String(),
		AccountID:      req.AccountID,
		Platform:       req.Platform,
		PlatformUserID: req.PlatformUserID,
		VerifiedAt:     now,
		BonusGranted:   bonus,
	}
	if err := s.social.Insert(ctx, conn); err != nil {
		return nil, err
	}

	// Grant bonus credits
	requestID := fmt.Sprintf("social:%s:%s", req.AccountID, req.Platform)
	if err := s.credits.CreditFree(ctx, req.AccountID, bonus, "social_bonus_"+req.Platform, requestID, expiresAt); err != nil {
		return nil, err
	}

	// Get updated balance
	updatedBal, err := s.credits.GetBalance(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}

	return &ConfirmConnectionResponse{
		BonusGranted: bonus,
		NewBalance: CreditSummary{
			Free: updatedBal.FreeBalance,
			Paid: updatedBal.PaidBalance,
		},
	}, nil
}

// ListConnections returns all social connections for an account.
func (s *SocialService) ListConnections(ctx context.Context, accountID string) ([]*models.SocialConnection, error) {
	return s.social.GetByAccountID(ctx, accountID)
}

// Disconnect removes a social connection. Does NOT revoke bonus credits.
func (s *SocialService) Disconnect(ctx context.Context, accountID, platform string) error {
	return s.social.Delete(ctx, accountID, platform)
}
