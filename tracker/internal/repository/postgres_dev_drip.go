// Package: tracker/internal/repository
// Feature: StonkAgents devnet drip
// Purpose: PostgreSQL DevDripRepository (table dev_drips, migration 020)

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresDevDripRepository implements DevDripRepository with PostgreSQL.
type PostgresDevDripRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresDevDripRepository creates a new PostgreSQL dev drip repository.
func NewPostgresDevDripRepository(pool *pgxpool.Pool) *PostgresDevDripRepository {
	return &PostgresDevDripRepository{pool: pool}
}

// Get returns the wallet's row or ErrNotFound.
func (r *PostgresDevDripRepository) Get(ctx context.Context, wallet string) (*models.DevDrip, error) {
	var d models.DevDrip
	var sol, stonk int64
	err := r.pool.QueryRow(ctx, `SELECT wallet, ip, signature, amount_sol, amount_stonk, dripped_at
		FROM dev_drips WHERE wallet = $1`, wallet).Scan(&d.Wallet, &d.IPHash, &d.Signature, &sol, &stonk, &d.DrippedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.AmountSol, d.AmountStonk = uint64(sol), uint64(stonk)
	return &d, nil
}

// Upsert inserts or replaces the wallet's row.
func (r *PostgresDevDripRepository) Upsert(ctx context.Context, drip *models.DevDrip) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO dev_drips (wallet, ip, signature, amount_sol, amount_stonk, dripped_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (wallet) DO UPDATE SET ip = EXCLUDED.ip, signature = EXCLUDED.signature,
			amount_sol = EXCLUDED.amount_sol, amount_stonk = EXCLUDED.amount_stonk, dripped_at = EXCLUDED.dripped_at`,
		drip.Wallet, drip.IPHash, drip.Signature, int64(drip.AmountSol), int64(drip.AmountStonk), drip.DrippedAt)
	return err
}

// ListByIPSince returns dripped_at times for ipHash at or after since, oldest first.
func (r *PostgresDevDripRepository) ListByIPSince(ctx context.Context, ipHash string, since time.Time) ([]time.Time, error) {
	rows, err := r.pool.Query(ctx, `SELECT dripped_at FROM dev_drips WHERE ip = $1 AND dripped_at >= $2 ORDER BY dripped_at ASC`, ipHash, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []time.Time{}
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
