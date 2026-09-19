// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-03 (Credit System)
// Purpose: In-memory implementation of CreditRepository for testing

package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryCreditRepository is a thread-safe in-memory CreditRepository.
type MemoryCreditRepository struct {
	mu           sync.RWMutex
	balances     map[string]*models.CreditBalance
	transactions []*models.CreditTransaction
	// requestIDs tracks (accountID, requestID) for idempotency.
	requestIDs map[string]int // key: "accountID:requestID" → index in transactions
	clock      clock.Clock
}

// NewMemoryCreditRepository creates a new in-memory credit repository.
func NewMemoryCreditRepository() *MemoryCreditRepository {
	return &MemoryCreditRepository{
		balances:   make(map[string]*models.CreditBalance),
		requestIDs: make(map[string]int),
		clock:      clock.RealClock{},
	}
}

// NewMemoryCreditRepositoryWithClock creates a new in-memory credit repository with a custom clock.
func NewMemoryCreditRepositoryWithClock(clk clock.Clock) *MemoryCreditRepository {
	return &MemoryCreditRepository{
		balances:   make(map[string]*models.CreditBalance),
		requestIDs: make(map[string]int),
		clock:      clk,
	}
}

func reqKey(accountID, requestID string) string {
	return accountID + ":" + requestID
}

func (r *MemoryCreditRepository) CreateBalance(_ context.Context, balance *models.CreditBalance) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.balances[balance.AccountID]; exists {
		return models.ErrAlreadyExists
	}

	stored := *balance
	r.balances[balance.AccountID] = &stored
	return nil
}

func (r *MemoryCreditRepository) GetBalance(_ context.Context, accountID string) (*models.CreditBalance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return nil, models.ErrNotFound
	}
	copy := *bal
	return &copy, nil
}

func (r *MemoryCreditRepository) CreditFree(_ context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}

	// Idempotency check
	if _, exists := r.requestIDs[reqKey(accountID, requestID)]; exists {
		return nil
	}

	bal.FreeBalance += amount
	bal.FreeCreditsExpiresAt = &expiresAt
	bal.UpdatedAt = time.Now()

	tx := &models.CreditTransaction{
		ID:          requestID, // simplified for in-memory
		AccountID:   accountID,
		Amount:      amount,
		BalanceType: models.BalanceTypeFree,
		Reason:      reason,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
	}
	r.transactions = append(r.transactions, tx)
	r.requestIDs[reqKey(accountID, requestID)] = len(r.transactions) - 1
	return nil
}

func (r *MemoryCreditRepository) CreditPaid(_ context.Context, accountID string, amount int, reason, requestID string) error {
	return r.creditPaid(accountID, amount, reason, requestID, true)
}

// CreditPaidNoPurchase adds paid credits without counting them as purchased.
func (r *MemoryCreditRepository) CreditPaidNoPurchase(_ context.Context, accountID string, amount int, reason, requestID string) error {
	return r.creditPaid(accountID, amount, reason, requestID, false)
}

func (r *MemoryCreditRepository) creditPaid(accountID string, amount int, reason, requestID string, purchased bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}

	if _, exists := r.requestIDs[reqKey(accountID, requestID)]; exists {
		return nil
	}

	bal.PaidBalance += amount
	if purchased {
		bal.LifetimePurchased += amount
	}
	bal.UpdatedAt = time.Now()

	tx := &models.CreditTransaction{
		ID:          requestID,
		AccountID:   accountID,
		Amount:      amount,
		BalanceType: models.BalanceTypePaid,
		Reason:      reason,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
	}
	r.transactions = append(r.transactions, tx)
	r.requestIDs[reqKey(accountID, requestID)] = len(r.transactions) - 1
	return nil
}

func (r *MemoryCreditRepository) Spend(_ context.Context, accountID string, amount int, reason, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}

	// Idempotency: if already processed, return success
	if _, exists := r.requestIDs[reqKey(accountID, requestID)]; exists {
		return nil
	}

	total := bal.FreeBalance + bal.PaidBalance
	if total < amount {
		return models.ErrInsufficientCredits
	}

	// Deduct free first, then paid
	freeDeduct := amount
	if freeDeduct > bal.FreeBalance {
		freeDeduct = bal.FreeBalance
	}
	paidDeduct := amount - freeDeduct

	bal.FreeBalance -= freeDeduct
	bal.PaidBalance -= paidDeduct

	// Reset expiry clock on spend
	now := r.clock.Now()
	newExpiry := now.Add(30 * 24 * time.Hour)
	bal.FreeCreditsExpiresAt = &newExpiry
	bal.UpdatedAt = now

	balType := models.BalanceTypeFree
	if paidDeduct > 0 && freeDeduct > 0 {
		balType = models.BalanceTypeMixed
	} else if paidDeduct > 0 {
		balType = models.BalanceTypePaid
	}

	tx := &models.CreditTransaction{
		ID:          requestID,
		AccountID:   accountID,
		Amount:      -amount,
		BalanceType: balType,
		Reason:      reason,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
	}
	r.transactions = append(r.transactions, tx)
	r.requestIDs[reqKey(accountID, requestID)] = len(r.transactions) - 1
	return nil
}

// SpendSplit deducts free first, then paid, and reports the split. Unlike Spend it mirrors the
// Postgres expiry rule: the free expiry is kept, and cleared when the free balance hits 0.
func (r *MemoryCreditRepository) SpendSplit(_ context.Context, accountID string, amount int, reason, requestID string) (int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return 0, 0, models.ErrNotFound
	}
	if _, exists := r.requestIDs[reqKey(accountID, requestID)]; exists {
		return 0, 0, nil
	}
	if bal.FreeBalance+bal.PaidBalance < amount {
		return 0, 0, models.ErrInsufficientCredits
	}
	fromFree := amount
	if fromFree > bal.FreeBalance {
		fromFree = bal.FreeBalance
	}
	fromPaid := amount - fromFree
	bal.FreeBalance -= fromFree
	bal.PaidBalance -= fromPaid
	if bal.FreeBalance <= 0 {
		bal.FreeCreditsExpiresAt = nil
	}
	bal.UpdatedAt = r.clock.Now()

	balType := models.BalanceTypeFree
	if fromPaid > 0 && fromFree > 0 {
		balType = models.BalanceTypeMixed
	} else if fromPaid > 0 {
		balType = models.BalanceTypePaid
	}
	tx := &models.CreditTransaction{
		ID:          requestID,
		AccountID:   accountID,
		Amount:      -amount,
		BalanceType: balType,
		Reason:      reason,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
	}
	r.transactions = append(r.transactions, tx)
	r.requestIDs[reqKey(accountID, requestID)] = len(r.transactions) - 1
	return fromFree, fromPaid, nil
}

// CreditFreeKeepExpiry adds free credits; an existing expiry on a positive free balance is kept,
// otherwise expiresAt applies.
func (r *MemoryCreditRepository) CreditFreeKeepExpiry(_ context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	if _, exists := r.requestIDs[reqKey(accountID, requestID)]; exists {
		return nil
	}
	if bal.FreeBalance <= 0 || bal.FreeCreditsExpiresAt == nil {
		exp := expiresAt
		bal.FreeCreditsExpiresAt = &exp
	}
	bal.FreeBalance += amount
	bal.UpdatedAt = time.Now()

	tx := &models.CreditTransaction{
		ID:          requestID,
		AccountID:   accountID,
		Amount:      amount,
		BalanceType: models.BalanceTypeFree,
		Reason:      reason,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
	}
	r.transactions = append(r.transactions, tx)
	r.requestIDs[reqKey(accountID, requestID)] = len(r.transactions) - 1
	return nil
}

func (r *MemoryCreditRepository) SpendPaidOnly(_ context.Context, accountID string, amount int, reason, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	if _, exists := r.requestIDs[reqKey(accountID, requestID)]; exists {
		return nil
	}
	if bal.PaidBalance < amount {
		return models.ErrInsufficientPaidCredits
	}
	bal.PaidBalance -= amount
	bal.UpdatedAt = r.clock.Now()

	tx := &models.CreditTransaction{
		ID:          requestID,
		AccountID:   accountID,
		Amount:      -amount,
		BalanceType: models.BalanceTypePaid,
		Reason:      reason,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
	}
	r.transactions = append(r.transactions, tx)
	r.requestIDs[reqKey(accountID, requestID)] = len(r.transactions) - 1
	return nil
}

func (r *MemoryCreditRepository) SetDetailedTrial(_ context.Context, accountID string, remaining int, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	bal.DetailedTrialRemaining = remaining
	bal.DetailedTrialExpiresAt = &expiresAt
	bal.UpdatedAt = r.clock.Now()
	return nil
}

func (r *MemoryCreditRepository) DecrementDetailedTrial(_ context.Context, accountID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	if bal.DetailedTrialRemaining <= 0 {
		return models.ErrTrialExhausted
	}
	if bal.DetailedTrialExpiresAt != nil && !bal.DetailedTrialExpiresAt.After(now) {
		return models.ErrTrialExhausted
	}
	bal.DetailedTrialRemaining--
	bal.UpdatedAt = r.clock.Now()
	return nil
}

func (r *MemoryCreditRepository) RestoreDetailedTrial(_ context.Context, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	bal.DetailedTrialRemaining++
	bal.UpdatedAt = r.clock.Now()
	return nil
}

func (r *MemoryCreditRepository) SetFreeBalance(_ context.Context, accountID string, balance int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	bal.FreeBalance = balance
	bal.UpdatedAt = time.Now()
	return nil
}

func (r *MemoryCreditRepository) SetPaidBalance(_ context.Context, accountID string, balance int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	bal, ok := r.balances[accountID]
	if !ok {
		return models.ErrNotFound
	}
	bal.PaidBalance = balance
	bal.UpdatedAt = time.Now()
	return nil
}

func (r *MemoryCreditRepository) GetTransactionByRequestID(_ context.Context, accountID, requestID string) (*models.CreditTransaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	idx, ok := r.requestIDs[reqKey(accountID, requestID)]
	if !ok {
		return nil, models.ErrNotFound
	}
	copy := *r.transactions[idx]
	return &copy, nil
}

// SumSpentByReasonsSince sums the account's debits with one of the reasons at or after since
// (returned as a positive number).
func (r *MemoryCreditRepository) SumSpentByReasonsSince(_ context.Context, accountID string, reasons []string, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		want[reason] = true
	}
	total := 0
	for _, tx := range r.transactions {
		if tx.AccountID == accountID && tx.Amount < 0 && want[tx.Reason] && !tx.CreatedAt.Before(since) {
			total -= tx.Amount
		}
	}
	return total, nil
}

func (r *MemoryCreditRepository) ListTransactions(_ context.Context, accountID string, limit, offset int) ([]*models.CreditTransaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var filtered []*models.CreditTransaction
	for _, tx := range r.transactions {
		if tx.AccountID == accountID {
			copy := *tx
			filtered = append(filtered, &copy)
		}
	}

	// Sort newest first
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if offset >= len(filtered) {
		return nil, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], nil
}

func (r *MemoryCreditRepository) ListExpirableBalances(_ context.Context, now time.Time) ([]*models.CreditBalance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*models.CreditBalance
	for _, bal := range r.balances {
		if bal.FreeBalance > 0 && bal.FreeCreditsExpiresAt != nil && bal.FreeCreditsExpiresAt.Before(now) {
			copy := *bal
			result = append(result, &copy)
		}
	}
	return result, nil
}
