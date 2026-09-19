// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: PostgreSQL implementation of PurchaseRepository with replay prevention

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresPurchaseRepository implements PurchaseRepository using PostgreSQL.
type PostgresPurchaseRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresPurchaseRepository creates a new Postgres purchase repository.
func NewPostgresPurchaseRepository(pool *pgxpool.Pool) *PostgresPurchaseRepository {
	return &PostgresPurchaseRepository{pool: pool}
}

func (r *PostgresPurchaseRepository) CreateIntent(ctx context.Context, intent *models.PurchaseIntent) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO purchase_intents (id, account_id, amount_lamports, credit_amount, status, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		intent.ID, intent.AccountID, intent.AmountLamports, intent.CreditAmount,
		intent.Status, intent.CreatedAt, intent.ExpiresAt,
	)
	return err
}

func (r *PostgresPurchaseRepository) GetIntent(ctx context.Context, id string) (*models.PurchaseIntent, error) {
	var i models.PurchaseIntent
	err := r.pool.QueryRow(ctx,
		`SELECT id, account_id, amount_lamports, credit_amount, status, created_at, expires_at,
		        COALESCE(tx_signature, ''), verified_at
		 FROM purchase_intents WHERE id = $1`, id,
	).Scan(&i.ID, &i.AccountID, &i.AmountLamports, &i.CreditAmount,
		&i.Status, &i.CreatedAt, &i.ExpiresAt, &i.TxSignature, &i.VerifiedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &i, nil
}

func (r *PostgresPurchaseRepository) MarkVerified(ctx context.Context, id, txSignature string, verifiedAt time.Time) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE purchase_intents SET status = 'verified', tx_signature = $1, verified_at = $2
		 WHERE id = $3 AND status = 'pending'`,
		txSignature, verifiedAt, id,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresPurchaseRepository) ExpireStaleIntents(ctx context.Context, now time.Time) (int, error) {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE purchase_intents SET status = 'expired'
		 WHERE status = 'pending' AND expires_at < $1`, now,
	)
	if err != nil {
		return 0, err
	}
	return int(cmd.RowsAffected()), nil
}

func (r *PostgresPurchaseRepository) HasProcessedSignature(ctx context.Context, txSignature string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_signatures WHERE tx_signature = $1)`, txSignature,
	).Scan(&exists)
	return exists, err
}

func (r *PostgresPurchaseRepository) RecordProcessedSignature(ctx context.Context, txSignature, intentID string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO processed_signatures (tx_signature, intent_id) VALUES ($1, $2)
		 ON CONFLICT (tx_signature) DO NOTHING`,
		txSignature, intentID,
	)
	return err
}
