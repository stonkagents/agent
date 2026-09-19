// Package: tracker/internal/services
// Feature: F-013 (Credits & Identity)
// Story: US-013-03 (Credit Lifecycle)
// Purpose: Credit balance management, atomic spend, JWT spend tokens, expiry

package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Credit system constants.
const (
	SpendTokenTTL    = 5 * time.Minute
	FreeExpiryWindow = 30 * 24 * time.Hour
	// DetailedTrialInitial is the number of free detailed-mode (gpt-5.4) calls a
	// new account starts with. Lets users taste the premium model before paying.
	DetailedTrialInitial = 3
	// DetailedTrialExpiryWindow is how long the trial calls remain redeemable.
	DetailedTrialExpiryWindow = 30 * 24 * time.Hour
)

// SpendSource indicates where the credits for a chat call came from.
// Used by handler refunds (Stop button) to credit the right bucket back.
type SpendSource string

const (
	SpendSourceFree  SpendSource = "free"
	SpendSourcePaid  SpendSource = "paid"
	SpendSourceMixed SpendSource = "mixed"
	SpendSourceTrial SpendSource = "trial"
)

// SpendResult is returned from SpendForModel and tells the caller how many
// credits were actually deducted (0 for trial calls) and which bucket they came
// from (so refunds can target the same bucket).
type SpendResult struct {
	CreditsDeducted int
	Source          SpendSource
}

// modelIsDetailed returns true if the model identifier corresponds to the
// detailed (paid-only) tier. Currently any model name without "-mini" suffix
// is treated as detailed.
func modelIsDetailed(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	// Conservative: only the full gpt-5.4 model (and its variants without -mini)
	// is detailed. Mini variants always require fewer credits and use free balance.
	return !strings.Contains(m, "-mini")
}

// BalanceResponse is the full balance snapshot returned to callers.
type BalanceResponse struct {
	FreeBalance           int
	PaidBalance           int
	Total                 int
	FreeCreditsExpiresAt  *time.Time
	LifetimePurchased     int
	LifetimeSocialGranted int
	// Trial counter for detailed (gpt-5.4) mode. When >0 and not expired, free
	// users can taste detailed mode without paid credits.
	DetailedTrialRemaining int
	DetailedTrialExpiresAt *time.Time
}

// SpendTokenResponse contains the JWT spend token and its expiry.
type SpendTokenResponse struct {
	Token     string
	ExpiresAt time.Time
}

// SpendTokenClaims are the parsed claims from a validated spend token.
type SpendTokenClaims struct {
	AccountID string
	RequestID string
	Amount    int
	Purpose   string
}

// CreditServiceDeps holds all dependencies for CreditService.
type CreditServiceDeps struct {
	Credits           repository.CreditRepository
	Accounts          repository.AccountRepository
	Clock             clock.Clock
	JWTSecret         string
	JWTPreviousSecret string // TD-080: previous key for rotation window
}

// CreditService manages credit balances, spending, and JWT tokens.
type CreditService struct {
	credits           repository.CreditRepository
	accounts          repository.AccountRepository
	clock             clock.Clock
	jwtSecret         []byte
	jwtPreviousSecret []byte // TD-080: previous key for rotation window
}

// NewCreditService creates a new CreditService.
func NewCreditService(deps CreditServiceDeps) *CreditService {
	var prevSecret []byte
	if deps.JWTPreviousSecret != "" {
		prevSecret = []byte(deps.JWTPreviousSecret)
	}
	return &CreditService{
		credits:           deps.Credits,
		accounts:          deps.Accounts,
		clock:             deps.Clock,
		jwtSecret:         []byte(deps.JWTSecret),
		jwtPreviousSecret: prevSecret,
	}
}

// GetBalance returns the current credit balance for an account.
func (s *CreditService) GetBalance(ctx context.Context, accountID string) (*BalanceResponse, error) {
	bal, err := s.credits.GetBalance(ctx, accountID)
	if err != nil {
		return nil, err
	}

	return &BalanceResponse{
		FreeBalance:            bal.FreeBalance,
		PaidBalance:            bal.PaidBalance,
		Total:                  bal.FreeBalance + bal.PaidBalance,
		FreeCreditsExpiresAt:   bal.FreeCreditsExpiresAt,
		LifetimePurchased:      bal.LifetimePurchased,
		LifetimeSocialGranted:  bal.LifetimeSocialGranted,
		DetailedTrialRemaining: bal.DetailedTrialRemaining,
		DetailedTrialExpiresAt: bal.DetailedTrialExpiresAt,
	}, nil
}

// Spend deducts credits atomically (free first, then paid). Idempotent via requestID.
func (s *CreditService) Spend(ctx context.Context, accountID string, amount int, reason, requestID string) error {
	// Check idempotency — if already processed, return success
	_, err := s.credits.GetTransactionByRequestID(ctx, accountID, requestID)
	if err == nil {
		return nil // Already processed
	}

	// Delegate to repository's atomic spend (handles free-first deduction + expiry clock reset)
	return s.credits.Spend(ctx, accountID, amount, reason, requestID)
}

// Refund returns credits to an account when an operation is cancelled before completion
// (e.g. user clicks Stop on agent chat before the LLM call finishes). Idempotent via originalRequestID
// — calling Refund twice for the same spend is a no-op. Refunded credits go back to the
// bucket they came from (via the source param). For trial calls the trial counter is restored
// instead of granting credits.
func (s *CreditService) Refund(ctx context.Context, accountID string, amount int, reason, originalRequestID string, source SpendSource) error {
	refundRequestID := "refund:" + originalRequestID

	// Trial refund — restore the counter, no credits move.
	if source == SpendSourceTrial {
		// Idempotency: if we've already refunded this spend, no-op.
		if _, err := s.credits.GetTransactionByRequestID(ctx, accountID, refundRequestID); err == nil {
			return nil
		}
		// Use a CreditFree(0) marker just to record the refund as a transaction for idempotency,
		// then restore the trial counter. Amount is 0 so balance doesn't change.
		expiresAt := s.clock.Now().Add(FreeExpiryWindow)
		_ = s.credits.CreditFree(ctx, accountID, 0, reason, refundRequestID, expiresAt)
		return s.credits.RestoreDetailedTrial(ctx, accountID)
	}

	if amount <= 0 {
		return nil
	}
	// Idempotency: if we've already refunded this spend, no-op.
	if _, err := s.credits.GetTransactionByRequestID(ctx, accountID, refundRequestID); err == nil {
		return nil
	}
	if source == SpendSourcePaid {
		// Refund directly to paid bucket so user keeps purchased credits as paid; the credits were
		// already counted at purchase time, so this must not bump lifetime_purchased again.
		return s.credits.CreditPaidNoPurchase(ctx, accountID, amount, reason, refundRequestID)
	}
	// Default (free / mixed): refund as free credits with fresh expiry.
	expiresAt := s.clock.Now().Add(FreeExpiryWindow)
	return s.credits.CreditFree(ctx, accountID, amount, reason, refundRequestID, expiresAt)
}

// RefundEscrow returns an escrow (a SpendSplit) to the buckets it came from: fromPaid goes
// back as paid (not counted as a purchase), fromFree as free without moving the account's
// existing free expiry (a fresh FreeExpiryWindow applies only when the free balance was empty).
// Idempotent per bucket via "refund:<originalRequestID>:paid" and ":free" request ids.
func (s *CreditService) RefundEscrow(ctx context.Context, accountID string, fromFree, fromPaid int, reason, originalRequestID string) error {
	base := "refund:" + originalRequestID
	if fromPaid > 0 {
		id := base + ":paid"
		if _, err := s.credits.GetTransactionByRequestID(ctx, accountID, id); err != nil {
			if err := s.credits.CreditPaidNoPurchase(ctx, accountID, fromPaid, reason, id); err != nil {
				return err
			}
		}
	}
	if fromFree > 0 {
		id := base + ":free"
		if _, err := s.credits.GetTransactionByRequestID(ctx, accountID, id); err != nil {
			expiresAt := s.clock.Now().Add(FreeExpiryWindow)
			if err := s.credits.CreditFreeKeepExpiry(ctx, accountID, fromFree, reason, id, expiresAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// SpendForModel deducts credits for a chat call based on the model. Mini mode
// spends from free first then paid (existing behaviour). Detailed mode is
// strictly paid-only; if paid balance is insufficient, falls back to the trial
// counter; if that's also exhausted, returns ErrPaidCreditsRequired.
//
// Returns the actual credits deducted (0 for trial calls) and the source bucket
// so the caller can route refunds correctly on cancellation.
func (s *CreditService) SpendForModel(ctx context.Context, accountID, model string, cost int, requestID string) (SpendResult, error) {
	return s.SpendForModelAs(ctx, accountID, model, cost, requestID, false)
}

// Completion spend reasons. The autopilot variants tag a draft the agent requested on its
// own (the request carried the autopilot marker), so GET /autopilot/outcomes/summary can
// report what autopilot cost without counting the owner's own chat.
const (
	CompletionReason             = "agent_completion"
	CompletionReasonDetailed     = "agent_completion_detailed"
	CompletionReasonAuto         = "agent_completion_auto"
	CompletionReasonDetailedAuto = "agent_completion_detailed_auto"
)

// AutopilotCompletionReasons are the spend reasons that count as autopilot draft cost.
var AutopilotCompletionReasons = []string{CompletionReasonAuto, CompletionReasonDetailedAuto}

// SpendForModelAs is SpendForModel with the spend reason chosen by who asked: autopilot
// drafts are recorded under the autopilot reasons, everything else as before.
func (s *CreditService) SpendForModelAs(ctx context.Context, accountID, model string, cost int, requestID string, autopilot bool) (SpendResult, error) {
	reason, detailedReason := CompletionReason, CompletionReasonDetailed
	if autopilot {
		reason, detailedReason = CompletionReasonAuto, CompletionReasonDetailedAuto
	}
	if !modelIsDetailed(model) {
		// Mini path — existing behaviour. Source is approximated as "free" since the
		// repository deducts free first; refund-to-free is acceptable for v1.
		if err := s.Spend(ctx, accountID, cost, reason, requestID); err != nil {
			return SpendResult{}, err
		}
		return SpendResult{CreditsDeducted: cost, Source: SpendSourceFree}, nil
	}

	// Detailed path — paid first, trial fallback.
	bal, err := s.credits.GetBalance(ctx, accountID)
	if err != nil {
		return SpendResult{}, err
	}

	if bal.PaidBalance >= cost {
		if err := s.credits.SpendPaidOnly(ctx, accountID, cost, detailedReason, requestID); err != nil {
			return SpendResult{}, err
		}
		return SpendResult{CreditsDeducted: cost, Source: SpendSourcePaid}, nil
	}

	// Trial fallback — only if user has zero paid balance. If they have some
	// paid but not enough, return InsufficientPaidCredits so they top up rather
	// than burn a trial when they could just buy more.
	if bal.PaidBalance > 0 {
		return SpendResult{}, models.ErrInsufficientPaidCredits
	}

	now := s.clock.Now()
	if bal.DetailedTrialRemaining > 0 && (bal.DetailedTrialExpiresAt == nil || bal.DetailedTrialExpiresAt.After(now)) {
		if err := s.credits.DecrementDetailedTrial(ctx, accountID, now); err != nil {
			if err == models.ErrTrialExhausted {
				return SpendResult{}, models.ErrPaidCreditsRequired
			}
			return SpendResult{}, err
		}
		return SpendResult{CreditsDeducted: 0, Source: SpendSourceTrial}, nil
	}

	return SpendResult{}, models.ErrPaidCreditsRequired
}

// IssueSpendToken creates a JWT spend token that can be redeemed by the AI gateway.
func (s *CreditService) IssueSpendToken(ctx context.Context, accountID string, amount int, purpose string) (*SpendTokenResponse, error) {
	// Check balance first
	bal, err := s.credits.GetBalance(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if bal.FreeBalance+bal.PaidBalance < amount {
		return nil, models.ErrInsufficientCredits
	}

	now := s.clock.Now()
	expiresAt := now.Add(SpendTokenTTL)
	requestID := uuid.New().String()

	claims := jwt.MapClaims{
		"sub":     accountID,
		"jti":     requestID,
		"amount":  amount,
		"purpose": purpose,
		"exp":     expiresAt.Unix(),
		"iat":     now.Unix(),
		"iss":     "stonkagents-tracker",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("sign jwt: %w", err)
	}

	return &SpendTokenResponse{
		Token:     tokenString,
		ExpiresAt: expiresAt,
	}, nil
}

// ValidateSpendToken parses and validates a JWT spend token.
// TD-080: Supports key rotation — tries current key first, then previous key if set.
func (s *CreditService) ValidateSpendToken(tokenString string) (*SpendTokenClaims, error) {
	claims, err := s.parseSpendToken(tokenString, s.jwtSecret)
	if err != nil && s.jwtPreviousSecret != nil {
		// Try previous key during rotation window
		claims, err = s.parseSpendToken(tokenString, s.jwtPreviousSecret)
	}
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// parseSpendToken attempts to parse and validate a JWT with the given key.
func (s *CreditService) parseSpendToken(tokenString string, key []byte) (*SpendTokenClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return key, nil
	}, jwt.WithTimeFunc(s.clock.Now))

	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	accountID, _ := claims["sub"].(string)
	requestID, _ := claims["jti"].(string)
	purpose, _ := claims["purpose"].(string)

	// Amount comes as float64 from JSON
	amountFloat, _ := claims["amount"].(float64)
	amount := int(amountFloat)

	return &SpendTokenClaims{
		AccountID: accountID,
		RequestID: requestID,
		Amount:    amount,
		Purpose:   purpose,
	}, nil
}

// ExpireStaleCredits zeroes free balances that have expired. Returns count of affected accounts.
// In production, this maps to a single SQL UPDATE; the service iterates via ListExpirableBalances.
func (s *CreditService) ExpireStaleCredits(ctx context.Context) (int, error) {
	now := s.clock.Now()

	balances, err := s.credits.ListExpirableBalances(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("list expirable balances: %w", err)
	}

	expired := 0
	for _, bal := range balances {
		requestID := fmt.Sprintf("expiry:%s:%s", bal.AccountID, now.Format("2006-01-02"))
		// Idempotency: skip if already processed today
		if _, err := s.credits.GetTransactionByRequestID(ctx, bal.AccountID, requestID); err == nil {
			continue
		}

		if err := s.credits.SetFreeBalance(ctx, bal.AccountID, 0); err != nil {
			continue
		}
		expired++
	}

	return expired, nil
}
