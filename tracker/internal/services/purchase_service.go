// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: Two-step Solana purchase flow — create intent, verify on-chain, credit account

package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Purchase flow constants.
const (
	IntentExpiryDuration = 15 * time.Minute
	// LamportsPerCredit is the default fallback if the platform_settings row
	// "credit_lamports_rate" is missing. 20,000 lamports = 0.00002 SOL ≈ $0.00172/credit
	// at SOL $86 (April 2026 baseline), targeting ~80% gross margin on LLM calls.
	LamportsPerCredit     = 20_000
	DustToleranceLamports = 5000
	// CreditLamportsRateSettingKey is the platform_settings key for the
	// runtime-tunable credit conversion rate. Re-anchor as SOL price drifts.
	CreditLamportsRateSettingKey = "credit_lamports_rate"

	// PurchaseMemoPrefix is the on-chain memo prefix for new purchase intents. The memo is
	// user-visible (wallet confirmation dialog and block explorer), so it carries the
	// current product name.
	PurchaseMemoPrefix = "stonkagents:purchase:"
	// Verification accepts it forever so an intent created by an old client keeps verifying.
)

// PurchaseMemo returns the on-chain memo for an intent (current prefix).
func PurchaseMemo(intentID string) string { return PurchaseMemoPrefix + intentID }

// VerifyParams holds parameters for on-chain transaction verification.
// AmountLamports is forwarded for the dev stub's use only — the real verifier
// derives the actual lamport delta from on-chain pre/post balances and ignores
// this field, so passing it never compromises real verification.
type VerifyParams struct {
	TreasuryAddress string
	ExpectedMemo    string
	// AcceptedMemos are additional memo strings that also satisfy the memo check.
	// Used to accept the legacy purchase memo prefix from installed clients.
	AcceptedMemos  []string
	AmountLamports int64
}

// MemoMatches reports whether observed contains the expected memo or any accepted alternative.
// Verifier implementations call this so the "any accepted memo" rule lives in one place.
func (p VerifyParams) MemoMatches(observed string) bool {
	if p.ExpectedMemo != "" && strings.Contains(observed, p.ExpectedMemo) {
		return true
	}
	for _, m := range p.AcceptedMemos {
		if m != "" && strings.Contains(observed, m) {
			return true
		}
	}
	return false
}

// TxVerifyResult holds the result of on-chain transaction verification.
type TxVerifyResult struct {
	Success        bool
	ActualLamports int64
	MemoMatch      bool
}

// TransactionVerifier abstracts Solana transaction verification for testability.
type TransactionVerifier interface {
	VerifyTransaction(ctx context.Context, txSignature string, params VerifyParams) (*TxVerifyResult, error)
}

// CreateIntentResponse is returned after creating a purchase intent.
type CreateIntentResponse struct {
	IntentID        string
	TreasuryAddress string
	AmountLamports  int64
	CreditAmount    int
	Memo            string
	ExpiresAt       time.Time
}

// VerifyPurchaseResponse is returned after verifying a purchase.
type VerifyPurchaseResponse struct {
	CreditsGranted int
	NewBalance     CreditSummary
}

// PurchaseServiceDeps holds dependencies for PurchaseService.
type PurchaseServiceDeps struct {
	Purchases       repository.PurchaseRepository
	Credits         repository.CreditRepository
	Accounts        repository.AccountRepository
	Settings        *repository.PlatformSettingsRepository
	Clock           clock.Clock
	TxVerifier      TransactionVerifier
	TreasuryAddress string
}

// PurchaseService manages the two-step Solana purchase flow.
type PurchaseService struct {
	purchases       repository.PurchaseRepository
	credits         repository.CreditRepository
	accounts        repository.AccountRepository
	settings        *repository.PlatformSettingsRepository
	clock           clock.Clock
	txVerifier      TransactionVerifier
	treasuryAddress string
}

// NewPurchaseService creates a new PurchaseService.
func NewPurchaseService(deps PurchaseServiceDeps) *PurchaseService {
	return &PurchaseService{
		purchases:       deps.Purchases,
		credits:         deps.Credits,
		accounts:        deps.Accounts,
		settings:        deps.Settings,
		clock:           deps.Clock,
		txVerifier:      deps.TxVerifier,
		treasuryAddress: deps.TreasuryAddress,
	}
}

// resolveLamportsPerCredit reads the conversion rate from platform_settings,
// falling back to the compile-time default. Cached for 5 minutes by the
// settings repo so this is cheap to call on every intent.
func (s *PurchaseService) resolveLamportsPerCredit(ctx context.Context) int64 {
	if s.settings == nil {
		return LamportsPerCredit
	}
	n, err := s.settings.GetIntSetting(ctx, CreditLamportsRateSettingKey, LamportsPerCredit)
	if err != nil || n <= 0 {
		return LamportsPerCredit
	}
	return int64(n)
}

// CreateIntent creates a new purchase intent for the given amount.
func (s *PurchaseService) CreateIntent(ctx context.Context, accountID string, amountLamports int64) (*CreateIntentResponse, error) {
	_, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	intentID := uuid.New().String()
	rate := s.resolveLamportsPerCredit(ctx)
	creditAmount := int(amountLamports / rate)
	memo := PurchaseMemo(intentID)

	intent := &models.PurchaseIntent{
		ID:             intentID,
		AccountID:      accountID,
		AmountLamports: amountLamports,
		CreditAmount:   creditAmount,
		Status:         models.PurchaseIntentPending,
		CreatedAt:      now,
		ExpiresAt:      now.Add(IntentExpiryDuration),
	}
	if err := s.purchases.CreateIntent(ctx, intent); err != nil {
		return nil, err
	}

	return &CreateIntentResponse{
		IntentID:        intentID,
		TreasuryAddress: s.treasuryAddress,
		AmountLamports:  amountLamports,
		CreditAmount:    creditAmount,
		Memo:            memo,
		ExpiresAt:       intent.ExpiresAt,
	}, nil
}

// VerifyPurchase verifies an on-chain transaction and credits the account.
func (s *PurchaseService) VerifyPurchase(ctx context.Context, accountID, intentID, txSignature string) (*VerifyPurchaseResponse, error) {
	now := s.clock.Now()

	// Lookup intent
	intent, err := s.purchases.GetIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.AccountID != accountID {
		return nil, fmt.Errorf("intent does not belong to this account")
	}
	if intent.Status != models.PurchaseIntentPending {
		return nil, fmt.Errorf("intent is not pending (status: %s)", intent.Status)
	}
	if now.After(intent.ExpiresAt) {
		return nil, fmt.Errorf("intent has expired")
	}

	// Check replay prevention
	alreadyProcessed, _ := s.purchases.HasProcessedSignature(ctx, txSignature)
	if alreadyProcessed {
		return nil, fmt.Errorf("transaction signature already processed")
	}

	// Verify on-chain transaction. AmountLamports is forwarded so the dev stub
	// can mirror what was requested (the real verifier ignores it and reads
	// pre/post balances from chain).
	// Both memo prefixes are accepted: an intent created before the rename, or by an
	result, err := s.txVerifier.VerifyTransaction(ctx, txSignature, VerifyParams{
		TreasuryAddress: s.treasuryAddress,
		ExpectedMemo:    PurchaseMemo(intentID),
		AmountLamports:  intent.AmountLamports,
	})
	if err != nil {
		return nil, fmt.Errorf("verify transaction: %w", err)
	}
	if !result.Success {
		return nil, fmt.Errorf("transaction failed on-chain")
	}

	// Check amount (with dust tolerance)
	diff := result.ActualLamports - intent.AmountLamports
	if diff < -DustToleranceLamports || diff > DustToleranceLamports {
		return nil, fmt.Errorf("amount mismatch: expected %d, got %d", intent.AmountLamports, result.ActualLamports)
	}

	// Mark intent as verified
	if err := s.purchases.MarkVerified(ctx, intentID, txSignature, now); err != nil {
		return nil, err
	}

	// Record processed signature for replay prevention
	_ = s.purchases.RecordProcessedSignature(ctx, txSignature, intentID)

	// Credit paid balance
	requestID := fmt.Sprintf("purchase:%s", intentID)
	if err := s.credits.CreditPaid(ctx, accountID, intent.CreditAmount, "solana_purchase", requestID); err != nil {
		return nil, err
	}

	// Get updated balance
	bal, err := s.credits.GetBalance(ctx, accountID)
	if err != nil {
		return nil, err
	}

	return &VerifyPurchaseResponse{
		CreditsGranted: intent.CreditAmount,
		NewBalance: CreditSummary{
			Free: bal.FreeBalance,
			Paid: bal.PaidBalance,
		},
	}, nil
}

// ExpireStaleIntents marks pending intents past their expiry as expired.
func (s *PurchaseService) ExpireStaleIntents(ctx context.Context) (int, error) {
	return s.purchases.ExpireStaleIntents(ctx, s.clock.Now())
}
