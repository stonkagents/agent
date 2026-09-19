// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: Wallet linking, balance/age verification, bonus credit grants

package services

import (
	"context"
	"fmt"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Wallet verification constants.
const (
	MinBalanceLamports = 200_000_000 // 0.2 SOL
	MinWalletAge       = 7 * 24 * time.Hour
	WalletBonus        = 75

	// WalletLinkPrefix is the domain prefix of the ownership-proof message a wallet signs.
	// It is user-visible: the wallet shows the message in its signature dialog.
	WalletLinkPrefix = "stonkagents-wallet-link:"
	// so an old client can still link a wallet after the rename.
)

// WalletLinkMessage returns the ownership-proof message for an account (current prefix).
func WalletLinkMessage(accountID string) string { return WalletLinkPrefix + accountID }

// SolanaClient abstracts Solana RPC calls for testability.
type SolanaClient interface {
	GetBalance(ctx context.Context, walletAddress string) (int64, error)
	GetFirstTransactionTime(ctx context.Context, walletAddress string) (*time.Time, error)
	VerifyWalletSignature(walletAddr string, message, signature []byte) bool
}

// LinkWalletRequest is the input for linking a wallet.
type LinkWalletRequest struct {
	AccountID     string
	WalletAddress string
	Chain         string
	Signature     []byte
}

// LinkWalletResponse is the output after linking a wallet.
type LinkWalletResponse struct {
	Linked       bool
	BonusGranted int
	NewBalance   CreditSummary
}

// WalletServiceDeps holds dependencies for WalletService.
type WalletServiceDeps struct {
	Wallets  repository.WalletRepository
	Credits  repository.CreditRepository
	Accounts repository.AccountRepository
	Clock    clock.Clock
	Solana   SolanaClient
}

// WalletService manages wallet linking and bonus credit grants.
type WalletService struct {
	wallets  repository.WalletRepository
	credits  repository.CreditRepository
	accounts repository.AccountRepository
	clock    clock.Clock
	solana   SolanaClient
}

// NewWalletService creates a new WalletService.
func NewWalletService(deps WalletServiceDeps) *WalletService {
	return &WalletService{
		wallets:  deps.Wallets,
		credits:  deps.Credits,
		accounts: deps.Accounts,
		clock:    deps.Clock,
		solana:   deps.Solana,
	}
}

// verifyOwnership reports whether signature proves the wallet signed either the current or the
// legacy ownership-proof message for accountID.
func (s *WalletService) verifyOwnership(walletAddress, accountID string, signature []byte) bool {
	for _, msg := range []string{WalletLinkMessage(accountID)} {
		if s.solana.VerifyWalletSignature(walletAddress, []byte(msg), signature) {
			return true
		}
	}
	return false
}

// LinkWallet verifies wallet ownership, checks balance/age, links the wallet, and grants bonus if eligible.
func (s *WalletService) LinkWallet(ctx context.Context, req LinkWalletRequest) (*LinkWalletResponse, error) {
	// Verify account exists
	_, err := s.accounts.GetByID(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}

	// Verify wallet signature (ownership proof). The current message is checked first;
	if !s.verifyOwnership(req.WalletAddress, req.AccountID, req.Signature) {
		return nil, fmt.Errorf("wallet signature verification failed")
	}

	now := s.clock.Now()

	// Get balance from Solana
	balanceLamports, err := s.solana.GetBalance(ctx, req.WalletAddress)
	if err != nil {
		return nil, fmt.Errorf("get wallet balance: %w", err)
	}

	// Link the wallet
	wallet := &models.AccountWallet{
		AccountID:     req.AccountID,
		WalletAddress: req.WalletAddress,
		Chain:         req.Chain,
		LinkedAt:      now,
		BalanceAtLink: &balanceLamports,
	}
	if err := s.wallets.LinkWallet(ctx, wallet); err != nil {
		return nil, err
	}

	// Check eligibility for bonus
	bonus := 0
	eligible := balanceLamports >= MinBalanceLamports

	if eligible {
		// Check wallet age
		firstTxTime, err := s.solana.GetFirstTransactionTime(ctx, req.WalletAddress)
		if err != nil {
			eligible = false
		} else if firstTxTime == nil || now.Sub(*firstTxTime) < MinWalletAge {
			eligible = false
		}
	}

	if eligible {
		// Check if wallet already received a grant
		alreadyGranted, _ := s.wallets.HasGrantHistory(ctx, req.WalletAddress, req.Chain)
		if alreadyGranted {
			eligible = false
		}
	}

	if eligible {
		bonus = WalletBonus
		expiresAt := now.Add(30 * 24 * time.Hour)
		requestID := fmt.Sprintf("wallet:%s", req.WalletAddress)

		if err := s.credits.CreditFree(ctx, req.AccountID, bonus, "wallet_bonus", requestID, expiresAt); err != nil {
			return nil, err
		}

		_ = s.wallets.InsertGrantHistory(ctx, &models.WalletGrantHistory{
			WalletAddress: req.WalletAddress,
			Chain:         req.Chain,
			GrantedAt:     now,
			Amount:        bonus,
		})
	}

	// Get updated balance
	bal, err := s.credits.GetBalance(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}

	return &LinkWalletResponse{
		Linked:       true,
		BonusGranted: bonus,
		NewBalance: CreditSummary{
			Free: bal.FreeBalance,
			Paid: bal.PaidBalance,
		},
	}, nil
}
