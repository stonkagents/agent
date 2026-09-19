// Package: tracker/internal/repository
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: PostgreSQL LaunchTradeRepository / LaunchBurnRepository (tables launch_trades,
//          launch_index_cursor, launch_burns — migration 018)

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresLaunchTradeRepository implements LaunchTradeRepository with PostgreSQL.
type PostgresLaunchTradeRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresLaunchTradeRepository creates a new PostgreSQL trade repository.
func NewPostgresLaunchTradeRepository(pool *pgxpool.Pool) *PostgresLaunchTradeRepository {
	return &PostgresLaunchTradeRepository{pool: pool}
}

const tradeSelectCols = `mint, pool_id, signature, slot, block_time, side, trader,
	base_amount, quote_amount, price_quote, quote_symbol`

func scanTrade(row pgx.Row) (*models.LaunchTrade, error) {
	var t models.LaunchTrade
	if err := row.Scan(&t.Mint, &t.PoolID, &t.Signature, &t.Slot, &t.BlockTime, &t.Side, &t.Trader,
		&t.BaseAmount, &t.QuoteAmount, &t.PriceQuote, &t.QuoteSymbol); err != nil {
		return nil, err
	}
	return &t, nil
}

func collectTrades(rows pgx.Rows) ([]*models.LaunchTrade, error) {
	defer rows.Close()
	out := []*models.LaunchTrade{}
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertTrades stores trades idempotently on signature (ON CONFLICT DO NOTHING); returns how many were new.
func (r *PostgresLaunchTradeRepository) InsertTrades(ctx context.Context, trades []*models.LaunchTrade) (int, error) {
	if len(trades) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, t := range trades {
		batch.Queue(`INSERT INTO launch_trades (
			mint, pool_id, signature, slot, block_time, side, trader, base_amount, quote_amount, price_quote, quote_symbol
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (signature) DO NOTHING`,
			t.Mint, t.PoolID, t.Signature, t.Slot, t.BlockTime.UTC(), t.Side, t.Trader,
			t.BaseAmount, t.QuoteAmount, t.PriceQuote, t.QuoteSymbol)
	}
	res := r.pool.SendBatch(ctx, batch)
	defer res.Close()
	n := 0
	for range trades {
		tag, err := res.Exec()
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// ListTrades returns a mint's trades newest first, after the cursor (nil = newest).
func (r *PostgresLaunchTradeRepository) ListTrades(ctx context.Context, mint string, limit int, after *TradeCursor) ([]*models.LaunchTrade, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if after == nil {
		rows, err = r.pool.Query(ctx, `SELECT `+tradeSelectCols+` FROM launch_trades WHERE mint = $1
			ORDER BY block_time DESC, slot DESC, signature DESC LIMIT $2`, mint, limit)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+tradeSelectCols+` FROM launch_trades WHERE mint = $1
			AND (block_time, slot, signature) < ($2::timestamptz, $3::bigint, $4::text)
			ORDER BY block_time DESC, slot DESC, signature DESC LIMIT $5`,
			mint, after.BlockTime.UTC(), after.Slot, after.Signature, limit)
	}
	if err != nil {
		return nil, err
	}
	return collectTrades(rows)
}

// ListTradesAsc returns a mint's trades with block_time >= since, oldest first.
func (r *PostgresLaunchTradeRepository) ListTradesAsc(ctx context.Context, mint string, since time.Time, limit int) ([]*models.LaunchTrade, error) {
	if limit <= 0 {
		limit = 10000
	}
	rows, err := r.pool.Query(ctx, `SELECT `+tradeSelectCols+` FROM launch_trades
		WHERE mint = $1 AND block_time >= $2
		ORDER BY block_time ASC, slot ASC, signature ASC LIMIT $3`, mint, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	return collectTrades(rows)
}

// AggregateWindow returns per-mint volume, count and boundary prices for (from, to] in
// three set-based queries, whatever the number of mints.
func (r *PostgresLaunchTradeRepository) AggregateWindow(ctx context.Context, mints []string, from, to time.Time) (map[string]*models.LaunchTradeWindow, error) {
	out := make(map[string]*models.LaunchTradeWindow)
	if len(mints) == 0 {
		return out, nil
	}
	get := func(mint string) *models.LaunchTradeWindow {
		w, ok := out[mint]
		if !ok {
			w = &models.LaunchTradeWindow{Mint: mint}
			out[mint] = w
		}
		return w
	}

	rows, err := r.pool.Query(ctx, `SELECT mint, COALESCE(SUM(quote_amount), 0), COUNT(*)
		FROM launch_trades WHERE mint = ANY($1) AND block_time > $2 AND block_time <= $3 GROUP BY mint`,
		mints, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var mint string
		var vol float64
		var n int
		if err := rows.Scan(&mint, &vol, &n); err != nil {
			rows.Close()
			return nil, err
		}
		w := get(mint)
		w.VolumeQuote, w.Trades = vol, n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Last trade at or before a boundary, per mint.
	boundary := func(at time.Time, set func(w *models.LaunchTradeWindow, price float64, symbol string)) error {
		rows, err := r.pool.Query(ctx, `SELECT DISTINCT ON (mint) mint, price_quote, quote_symbol
			FROM launch_trades WHERE mint = ANY($1) AND block_time <= $2
			ORDER BY mint, block_time DESC, slot DESC, signature DESC`, mints, at.UTC())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var mint, symbol string
			var price float64
			if err := rows.Scan(&mint, &price, &symbol); err != nil {
				return err
			}
			set(get(mint), price, symbol)
		}
		return rows.Err()
	}
	if err := boundary(to, func(w *models.LaunchTradeWindow, p float64, sym string) {
		w.PriceAtTo = &p
		w.QuoteSymbol = sym
	}); err != nil {
		return nil, err
	}
	if err := boundary(from, func(w *models.LaunchTradeWindow, p float64, sym string) {
		w.PriceAtFrom = &p
		if w.QuoteSymbol == "" {
			w.QuoteSymbol = sym
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCursor returns the index cursor for key, or ErrNotFound.
func (r *PostgresLaunchTradeRepository) GetCursor(ctx context.Context, key string) (*models.LaunchIndexCursor, error) {
	var c models.LaunchIndexCursor
	err := r.pool.QueryRow(ctx, `SELECT pool_id, last_signature, last_block_time, updated_at
		FROM launch_index_cursor WHERE pool_id = $1`, key).Scan(&c.Key, &c.LastSignature, &c.LastBlockTime, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// UpsertCursor stores the index cursor for cursor.Key.
func (r *PostgresLaunchTradeRepository) UpsertCursor(ctx context.Context, cursor *models.LaunchIndexCursor) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO launch_index_cursor (pool_id, last_signature, last_block_time, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (pool_id) DO UPDATE SET last_signature = EXCLUDED.last_signature,
			last_block_time = EXCLUDED.last_block_time, updated_at = NOW()`,
		cursor.Key, cursor.LastSignature, cursor.LastBlockTime)
	return err
}

// PostgresLaunchBurnRepository implements LaunchBurnRepository with PostgreSQL.
type PostgresLaunchBurnRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresLaunchBurnRepository creates a new PostgreSQL burn ledger repository.
func NewPostgresLaunchBurnRepository(pool *pgxpool.Pool) *PostgresLaunchBurnRepository {
	return &PostgresLaunchBurnRepository{pool: pool}
}

// InsertBurns stores burns idempotently on signature; returns how many were new.
func (r *PostgresLaunchBurnRepository) InsertBurns(ctx context.Context, burns []*models.LaunchBurn) (int, error) {
	if len(burns) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, b := range burns {
		batch.Queue(`INSERT INTO launch_burns (mint, signature, slot, block_time, amount, burner)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (signature) DO NOTHING`,
			b.Mint, b.Signature, b.Slot, b.BlockTime.UTC(), b.Amount, b.Burner)
	}
	res := r.pool.SendBatch(ctx, batch)
	defer res.Close()
	n := 0
	for range burns {
		tag, err := res.Exec()
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// Summary returns the total burned and the burn count for mint.
func (r *PostgresLaunchBurnRepository) Summary(ctx context.Context, mint string) (*models.LaunchBurnSummary, error) {
	var s models.LaunchBurnSummary
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0), COUNT(*) FROM launch_burns WHERE mint = $1`, mint).
		Scan(&s.Burned, &s.Burns)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Recent returns the newest burns first, at most limit.
func (r *PostgresLaunchBurnRepository) Recent(ctx context.Context, mint string, limit int) ([]*models.LaunchBurn, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.pool.Query(ctx, `SELECT mint, signature, slot, block_time, amount, burner FROM launch_burns
		WHERE mint = $1 ORDER BY block_time DESC, slot DESC, signature DESC LIMIT $2`, mint, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.LaunchBurn{}
	for rows.Next() {
		var b models.LaunchBurn
		if err := rows.Scan(&b.Mint, &b.Signature, &b.Slot, &b.BlockTime, &b.Amount, &b.Burner); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}
