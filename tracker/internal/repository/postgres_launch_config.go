// Package: tracker/internal/repository
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: PostgreSQL implementations of LaunchSettingsRepository and LaunchQuoteRepository

package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresLaunchSettingsRepository implements LaunchSettingsRepository (single-row launch_settings).
type PostgresLaunchSettingsRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresLaunchSettingsRepository creates a new Postgres launch settings repository.
func NewPostgresLaunchSettingsRepository(pool *pgxpool.Pool) *PostgresLaunchSettingsRepository {
	return &PostgresLaunchSettingsRepository{pool: pool}
}

func (r *PostgresLaunchSettingsRepository) GetFee(ctx context.Context) (*models.LaunchFee, error) {
	var f models.LaunchFee
	err := r.pool.QueryRow(ctx,
		`SELECT fee_usd::float8, fee_lamports, sol_usd::float8, priced_at, source
		 FROM launch_settings WHERE id = 1`,
	).Scan(&f.FeeUSD, &f.FeeLamports, &f.SolUSD, &f.PricedAt, &f.Source)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

func (r *PostgresLaunchSettingsRepository) UpsertFee(ctx context.Context, fee *models.LaunchFee) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO launch_settings (id, fee_usd, fee_lamports, sol_usd, priced_at, source, updated_at)
		 VALUES (1, $1, $2, $3, $4, $5, NOW())
		 ON CONFLICT (id) DO UPDATE SET
		   fee_usd = EXCLUDED.fee_usd,
		   fee_lamports = EXCLUDED.fee_lamports,
		   sol_usd = EXCLUDED.sol_usd,
		   priced_at = EXCLUDED.priced_at,
		   source = EXCLUDED.source,
		   updated_at = NOW()`,
		fee.FeeUSD, fee.FeeLamports, fee.SolUSD, fee.PricedAt, fee.Source,
	)
	return err
}

// PostgresLaunchQuoteRepository implements LaunchQuoteRepository over launch_quotes, scoped to
// one Solana cluster: every read filters on launch_quotes.cluster so a tracker pointed at the
// devnet LaunchLab program never serves a mainnet GlobalConfig (migration 014).
type PostgresLaunchQuoteRepository struct {
	pool    *pgxpool.Pool
	cluster string
}

// NewPostgresLaunchQuoteRepository creates a Postgres launch quote repository for cluster
// ("mainnet" | "devnet"; empty = mainnet).
func NewPostgresLaunchQuoteRepository(pool *pgxpool.Pool, cluster string) *PostgresLaunchQuoteRepository {
	if cluster == "" {
		cluster = models.LaunchClusterMainnet
	}
	return &PostgresLaunchQuoteRepository{pool: pool, cluster: cluster}
}

// Cluster returns the cluster this repository reads.
func (r *PostgresLaunchQuoteRepository) Cluster() string { return r.cluster }

// min_fund_raising_raw is NUMERIC(40,0); select as text so values beyond int64 survive.
const launchQuoteColumns = `cluster, quote_mint, symbol, name, decimals, token_program, category, launchlab_config_id,
	min_fund_raising_raw::text, enabled, sort_order`

func scanLaunchQuote(row pgx.Row) (*models.LaunchQuote, error) {
	var q models.LaunchQuote
	if err := row.Scan(&q.Cluster, &q.QuoteMint, &q.Symbol, &q.Name, &q.Decimals, &q.TokenProgram, &q.Category,
		&q.LaunchLabConfigID, &q.MinFundRaisingRaw, &q.Enabled, &q.SortOrder); err != nil {
		return nil, err
	}
	return &q, nil
}

func (r *PostgresLaunchQuoteRepository) ListEnabled(ctx context.Context) ([]*models.LaunchQuote, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+launchQuoteColumns+` FROM launch_quotes WHERE cluster = $1 AND enabled ORDER BY sort_order ASC, quote_mint ASC`,
		r.cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.LaunchQuote
	for rows.Next() {
		q, err := scanLaunchQuote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *PostgresLaunchQuoteRepository) GetByMint(ctx context.Context, quoteMint string) (*models.LaunchQuote, error) {
	q, err := scanLaunchQuote(r.pool.QueryRow(ctx,
		`SELECT `+launchQuoteColumns+` FROM launch_quotes WHERE cluster = $1 AND quote_mint = $2`, r.cluster, quoteMint))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return q, nil
}
