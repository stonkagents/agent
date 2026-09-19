// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-02 (Credit Balance), US-013-03 (Credit Spending)
// Purpose: PostgreSQL implementation of CreditRepository with atomic spend via CTE

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresCreditRepository implements CreditRepository using PostgreSQL.
type PostgresCreditRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresCreditRepository creates a new Postgres credit repository.
func NewPostgresCreditRepository(pool *pgxpool.Pool) *PostgresCreditRepository {
	return &PostgresCreditRepository{pool: pool}
}

func (r *PostgresCreditRepository) CreateBalance(ctx context.Context, balance *models.CreditBalance) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO credit_balances (account_id, free_balance, paid_balance, lifetime_purchased, lifetime_social_granted, free_credits_expires_at, detailed_trial_remaining, detailed_trial_expires_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		balance.AccountID, balance.FreeBalance, balance.PaidBalance,
		balance.LifetimePurchased, balance.LifetimeSocialGranted,
		balance.FreeCreditsExpiresAt,
		balance.DetailedTrialRemaining, balance.DetailedTrialExpiresAt,
		balance.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (r *PostgresCreditRepository) GetBalance(ctx context.Context, accountID string) (*models.CreditBalance, error) {
	var b models.CreditBalance
	err := r.pool.QueryRow(ctx,
		`SELECT account_id, free_balance, paid_balance, lifetime_purchased, lifetime_social_granted, free_credits_expires_at, detailed_trial_remaining, detailed_trial_expires_at, updated_at
		 FROM credit_balances WHERE account_id = $1`, accountID,
	).Scan(&b.AccountID, &b.FreeBalance, &b.PaidBalance,
		&b.LifetimePurchased, &b.LifetimeSocialGranted,
		&b.FreeCreditsExpiresAt,
		&b.DetailedTrialRemaining, &b.DetailedTrialExpiresAt,
		&b.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

func (r *PostgresCreditRepository) CreditFree(ctx context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`WITH ins AS (
			INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
			VALUES ($1, $2, 'free', $3, $4)
			ON CONFLICT (account_id, request_id) DO NOTHING
			RETURNING id
		)
		UPDATE credit_balances SET
			free_balance = free_balance + $2,
			free_credits_expires_at = $5,
			updated_at = NOW()
		WHERE account_id = $1 AND EXISTS (SELECT 1 FROM ins)`,
		accountID, amount, reason, requestID, expiresAt,
	)
	return err
}

func (r *PostgresCreditRepository) CreditPaid(ctx context.Context, accountID string, amount int, reason, requestID string) error {
	_, err := r.pool.Exec(ctx,
		`WITH ins AS (
			INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
			VALUES ($1, $2, 'paid', $3, $4)
			ON CONFLICT (account_id, request_id) DO NOTHING
			RETURNING id
		)
		UPDATE credit_balances SET
			paid_balance = paid_balance + $2,
			lifetime_purchased = lifetime_purchased + $2,
			updated_at = NOW()
		WHERE account_id = $1 AND EXISTS (SELECT 1 FROM ins)`,
		accountID, amount, reason, requestID,
	)
	return err
}

// CreditPaidNoPurchase adds paid credits that were not bought (refund, bounty reward):
// paid_balance only, lifetime_purchased untouched.
func (r *PostgresCreditRepository) CreditPaidNoPurchase(ctx context.Context, accountID string, amount int, reason, requestID string) error {
	_, err := r.pool.Exec(ctx,
		`WITH ins AS (
			INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
			VALUES ($1, $2, 'paid', $3, $4)
			ON CONFLICT (account_id, request_id) DO NOTHING
			RETURNING id
		)
		UPDATE credit_balances SET
			paid_balance = paid_balance + $2,
			updated_at = NOW()
		WHERE account_id = $1 AND EXISTS (SELECT 1 FROM ins)`,
		accountID, amount, reason, requestID,
	)
	return err
}

// Spend atomically deducts credits (free first, then paid) using a CTE with FOR UPDATE lock.
// Returns ErrInsufficientCredits if total balance < amount.
// Uses request_id idempotency via ON CONFLICT DO NOTHING to prevent double-spend on retry.
func (r *PostgresCreditRepository) Spend(ctx context.Context, accountID string, amount int, reason, requestID string) error {
	// Atomic CTE: lock row, compute deduction split, update, and insert transaction in one statement.
	// LEAST(free_balance, amount) goes to free; remainder to paid.
	cmd, err := r.pool.Exec(ctx,
		`WITH locked AS (
			SELECT account_id, free_balance, paid_balance
			FROM credit_balances
			WHERE account_id = $1
			FOR UPDATE
		),
		deduction AS (
			SELECT
				account_id,
				LEAST(free_balance, $2) AS from_free,
				$2 - LEAST(free_balance, $2) AS from_paid
			FROM locked
			WHERE free_balance + paid_balance >= $2
		),
		do_update AS (
			UPDATE credit_balances cb SET
				free_balance = cb.free_balance - d.from_free,
				paid_balance = cb.paid_balance - d.from_paid,
				free_credits_expires_at = CASE
					WHEN cb.free_balance - d.from_free <= 0 THEN NULL
					ELSE cb.free_credits_expires_at
				END,
				updated_at = NOW()
			FROM deduction d
			WHERE cb.account_id = d.account_id
			RETURNING cb.account_id, d.from_free, d.from_paid
		)
		INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
		SELECT
			account_id,
			-$2,
			CASE
				WHEN from_free > 0 AND from_paid > 0 THEN 'mixed'
				WHEN from_paid > 0 THEN 'paid'
				ELSE 'free'
			END,
			$3,
			$4
		FROM do_update
		ON CONFLICT (account_id, request_id) DO NOTHING`,
		accountID, amount, reason, requestID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		// 0 rows: either account not found, insufficient balance, or duplicate request_id.
		// Re-query to distinguish.
		var exists bool
		_ = r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM credit_balances WHERE account_id = $1)`, accountID,
		).Scan(&exists)
		if !exists {
			return models.ErrNotFound
		}
		// Check for idempotent replay
		var txExists bool
		_ = r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM credit_transactions WHERE account_id = $1 AND request_id = $2)`,
			accountID, requestID,
		).Scan(&txExists)
		if txExists {
			return nil // Idempotent replay — already processed
		}
		return models.ErrInsufficientCredits
	}
	return nil
}

// SpendSplit deducts free first, then paid, inside one transaction (row lock), and reports the
// split so the caller can return the credits to the buckets they left. Same balance and expiry
// rules as Spend; an idempotent replay reports (0, 0, nil).
func (r *PostgresCreditRepository) SpendSplit(ctx context.Context, accountID string, amount int, reason, requestID string) (int, int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)

	var free, paid int
	err = tx.QueryRow(ctx,
		`SELECT free_balance, paid_balance FROM credit_balances WHERE account_id = $1 FOR UPDATE`,
		accountID).Scan(&free, &paid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, models.ErrNotFound
		}
		return 0, 0, err
	}
	var replay bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM credit_transactions WHERE account_id = $1 AND request_id = $2)`,
		accountID, requestID).Scan(&replay); err != nil {
		return 0, 0, err
	}
	if replay {
		return 0, 0, nil
	}
	if free+paid < amount {
		return 0, 0, models.ErrInsufficientCredits
	}
	fromFree := amount
	if fromFree > free {
		fromFree = free
	}
	fromPaid := amount - fromFree
	balType := "free"
	switch {
	case fromFree > 0 && fromPaid > 0:
		balType = "mixed"
	case fromPaid > 0:
		balType = "paid"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE credit_balances SET
			free_balance = free_balance - $2,
			paid_balance = paid_balance - $3,
			free_credits_expires_at = CASE WHEN free_balance - $2 <= 0 THEN NULL ELSE free_credits_expires_at END,
			updated_at = NOW()
		 WHERE account_id = $1`,
		accountID, fromFree, fromPaid); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
		 VALUES ($1, $2, $3, $4, $5)`,
		accountID, -amount, balType, reason, requestID); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return fromFree, fromPaid, nil
}

// CreditFreeKeepExpiry adds free credits; an existing expiry on a positive free balance is
// kept (free_balance in the CASE is the pre-update value), otherwise expiresAt applies.
func (r *PostgresCreditRepository) CreditFreeKeepExpiry(ctx context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`WITH ins AS (
			INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
			VALUES ($1, $2, 'free', $3, $4)
			ON CONFLICT (account_id, request_id) DO NOTHING
			RETURNING id
		)
		UPDATE credit_balances SET
			free_balance = free_balance + $2,
			free_credits_expires_at = CASE
				WHEN free_balance > 0 AND free_credits_expires_at IS NOT NULL THEN free_credits_expires_at
				ELSE $5
			END,
			updated_at = NOW()
		WHERE account_id = $1 AND EXISTS (SELECT 1 FROM ins)`,
		accountID, amount, reason, requestID, expiresAt,
	)
	return err
}

// SpendPaidOnly atomically deducts credits strictly from the paid balance.
// Returns ErrInsufficientPaidCredits if paid_balance < amount.
// Used for detailed mode where free credits are not eligible.
func (r *PostgresCreditRepository) SpendPaidOnly(ctx context.Context, accountID string, amount int, reason, requestID string) error {
	cmd, err := r.pool.Exec(ctx,
		`WITH locked AS (
			SELECT account_id, paid_balance
			FROM credit_balances
			WHERE account_id = $1
			FOR UPDATE
		),
		do_update AS (
			UPDATE credit_balances cb SET
				paid_balance = cb.paid_balance - $2,
				updated_at = NOW()
			FROM locked l
			WHERE cb.account_id = l.account_id AND l.paid_balance >= $2
			RETURNING cb.account_id
		)
		INSERT INTO credit_transactions (account_id, amount, balance_type, reason, request_id)
		SELECT account_id, -$2, 'paid', $3, $4 FROM do_update
		ON CONFLICT (account_id, request_id) DO NOTHING`,
		accountID, amount, reason, requestID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		var exists bool
		_ = r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM credit_balances WHERE account_id = $1)`, accountID,
		).Scan(&exists)
		if !exists {
			return models.ErrNotFound
		}
		var txExists bool
		_ = r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM credit_transactions WHERE account_id = $1 AND request_id = $2)`,
			accountID, requestID,
		).Scan(&txExists)
		if txExists {
			return nil
		}
		return models.ErrInsufficientPaidCredits
	}
	return nil
}

// SetDetailedTrial sets the trial counter and expiry. Used by registration to
// seed new accounts.
func (r *PostgresCreditRepository) SetDetailedTrial(ctx context.Context, accountID string, remaining int, expiresAt time.Time) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE credit_balances
		 SET detailed_trial_remaining = $2, detailed_trial_expires_at = $3, updated_at = NOW()
		 WHERE account_id = $1`,
		accountID, remaining, expiresAt,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// DecrementDetailedTrial atomically decrements the counter if it's > 0 and not expired.
// Returns ErrTrialExhausted if no trial calls remain.
func (r *PostgresCreditRepository) DecrementDetailedTrial(ctx context.Context, accountID string, now time.Time) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE credit_balances
		 SET detailed_trial_remaining = detailed_trial_remaining - 1, updated_at = NOW()
		 WHERE account_id = $1
		   AND detailed_trial_remaining > 0
		   AND (detailed_trial_expires_at IS NULL OR detailed_trial_expires_at > $2)`,
		accountID, now,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		var exists bool
		_ = r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM credit_balances WHERE account_id = $1)`, accountID,
		).Scan(&exists)
		if !exists {
			return models.ErrNotFound
		}
		return models.ErrTrialExhausted
	}
	return nil
}

// RestoreDetailedTrial increments the trial counter (used to refund a trial
// call when the user aborts mid-flight).
func (r *PostgresCreditRepository) RestoreDetailedTrial(ctx context.Context, accountID string) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE credit_balances
		 SET detailed_trial_remaining = detailed_trial_remaining + 1, updated_at = NOW()
		 WHERE account_id = $1`,
		accountID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresCreditRepository) SetFreeBalance(ctx context.Context, accountID string, balance int) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE credit_balances SET free_balance = $1, updated_at = NOW() WHERE account_id = $2`,
		balance, accountID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresCreditRepository) SetPaidBalance(ctx context.Context, accountID string, balance int) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE credit_balances SET paid_balance = $1, updated_at = NOW() WHERE account_id = $2`,
		balance, accountID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// SumSpentByReasonsSince sums the account's debits with one of the reasons at or after since
// (returned as a positive number).
func (r *PostgresCreditRepository) SumSpentByReasonsSince(ctx context.Context, accountID string, reasons []string, since time.Time) (int, error) {
	if len(reasons) == 0 {
		return 0, nil
	}
	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(-SUM(amount), 0) FROM credit_transactions
		 WHERE account_id = $1 AND amount < 0 AND reason = ANY($2) AND created_at >= $3`,
		accountID, reasons, since).Scan(&total)
	return total, err
}

func (r *PostgresCreditRepository) GetTransactionByRequestID(ctx context.Context, accountID, requestID string) (*models.CreditTransaction, error) {
	var t models.CreditTransaction
	err := r.pool.QueryRow(ctx,
		`SELECT id, account_id, amount, balance_type, reason, request_id, created_at
		 FROM credit_transactions WHERE account_id = $1 AND request_id = $2`,
		accountID, requestID,
	).Scan(&t.ID, &t.AccountID, &t.Amount, &t.BalanceType, &t.Reason, &t.RequestID, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

func (r *PostgresCreditRepository) ListTransactions(ctx context.Context, accountID string, limit, offset int) ([]*models.CreditTransaction, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, account_id, amount, balance_type, reason, request_id, created_at
		 FROM credit_transactions WHERE account_id = $1
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		accountID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.CreditTransaction
	for rows.Next() {
		var t models.CreditTransaction
		if err := rows.Scan(&t.ID, &t.AccountID, &t.Amount, &t.BalanceType, &t.Reason, &t.RequestID, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (r *PostgresCreditRepository) ListExpirableBalances(ctx context.Context, now time.Time) ([]*models.CreditBalance, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT account_id, free_balance, paid_balance, lifetime_purchased, lifetime_social_granted, free_credits_expires_at, detailed_trial_remaining, detailed_trial_expires_at, updated_at
		 FROM credit_balances
		 WHERE free_balance > 0 AND free_credits_expires_at IS NOT NULL AND free_credits_expires_at < $1`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.CreditBalance
	for rows.Next() {
		var b models.CreditBalance
		if err := rows.Scan(&b.AccountID, &b.FreeBalance, &b.PaidBalance,
			&b.LifetimePurchased, &b.LifetimeSocialGranted,
			&b.FreeCreditsExpiresAt,
			&b.DetailedTrialRemaining, &b.DetailedTrialExpiresAt,
			&b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}
