// Package: tracker/internal/repository
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: PostgreSQL RevenueRepository (table platform_revenue, migration 009)

package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresRevenueRepository implements RevenueRepository with PostgreSQL.
type PostgresRevenueRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRevenueRepository creates a new PostgreSQL revenue repository.
func NewPostgresRevenueRepository(pool *pgxpool.Pool) *PostgresRevenueRepository {
	return &PostgresRevenueRepository{pool: pool}
}

// Insert appends a ledger entry. Duplicate signatures return ErrAlreadyExists (UNIQUE(signature)).
func (r *PostgresRevenueRepository) Insert(ctx context.Context, e *models.PlatformRevenue) error {
	var meta []byte
	if len(e.Meta) > 0 {
		b, err := json.Marshal(e.Meta)
		if err != nil {
			return err
		}
		meta = b
	}
	occurred := e.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	err := r.pool.QueryRow(ctx, `INSERT INTO platform_revenue (
		kind, quote_mint, amount_raw, amount_usd, signature, mint, occurred_at, meta
	) VALUES ($1, NULLIF($2, ''), $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8)
	RETURNING id`,
		e.Kind, e.QuoteMint, e.AmountRaw, e.AmountUSD, e.Signature, e.Mint, occurred, meta,
	).Scan(&e.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// TotalsByKind returns count and sums per kind across the whole ledger.
func (r *PostgresRevenueRepository) TotalsByKind(ctx context.Context) ([]*models.RevenueKindTotal, error) {
	rows, err := r.pool.Query(ctx, `SELECT kind, COUNT(*),
		COALESCE(SUM(amount_raw), 0)::float8, COALESCE(SUM(amount_usd), 0)::float8
		FROM platform_revenue GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.RevenueKindTotal{}
	for rows.Next() {
		var t models.RevenueKindTotal
		if err := rows.Scan(&t.Kind, &t.Count, &t.AmountRaw, &t.AmountUSD); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// DailyByKind returns per-day (UTC), per-kind aggregates for entries at or after since.
func (r *PostgresRevenueRepository) DailyByKind(ctx context.Context, since time.Time) ([]*models.RevenueDailyRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT to_char((occurred_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD') AS day, kind, COUNT(*),
		COALESCE(SUM(amount_raw), 0)::float8, COALESCE(SUM(amount_usd), 0)::float8
		FROM platform_revenue WHERE occurred_at >= $1
		GROUP BY day, kind ORDER BY day, kind`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.RevenueDailyRow{}
	for rows.Next() {
		var d models.RevenueDailyRow
		if err := rows.Scan(&d.Date, &d.Kind, &d.Count, &d.AmountRaw, &d.AmountUSD); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// Recent returns the newest ledger entries first, at most limit of them.
func (r *PostgresRevenueRepository) Recent(ctx context.Context, limit int) ([]*models.PlatformRevenue, error) {
	if limit <= 0 {
		return []*models.PlatformRevenue{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT id, kind, COALESCE(quote_mint, ''), amount_raw, amount_usd,
		COALESCE(signature, ''), COALESCE(mint, ''), occurred_at, meta
		FROM platform_revenue ORDER BY occurred_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.PlatformRevenue{}
	for rows.Next() {
		var e models.PlatformRevenue
		var meta []byte
		if err := rows.Scan(&e.ID, &e.Kind, &e.QuoteMint, &e.AmountRaw, &e.AmountUSD,
			&e.Signature, &e.Mint, &e.OccurredAt, &meta); err != nil {
			return nil, err
		}
		if len(meta) > 0 {
			if err := json.Unmarshal(meta, &e.Meta); err != nil {
				e.Meta = nil
			}
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
