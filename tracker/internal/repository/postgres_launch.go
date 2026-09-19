// Package: tracker/internal/repository
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: PostgreSQL LaunchRepository (table token_launches, migration 008)

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresLaunchRepository implements LaunchRepository with PostgreSQL.
type PostgresLaunchRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresLaunchRepository creates a new PostgreSQL launch repository.
func NewPostgresLaunchRepository(pool *pgxpool.Pool) *PostgresLaunchRepository {
	return &PostgresLaunchRepository{pool: pool}
}

const launchSelectCols = `mint, COALESCE(pool_id, ''), creator_wallet, quote_mint, name, symbol,
	COALESCE(image_url, ''), COALESCE(image_thumb_url, ''), COALESCE(metadata_uri, ''), launch_signature, fee_lamports, transfer_fee_bps,
	COALESCE(platform_id, ''), COALESCE(peer_id, ''), status, created_at, bound_at`

func scanLaunch(row pgx.Row) (*models.TokenLaunch, error) {
	var l models.TokenLaunch
	err := row.Scan(&l.Mint, &l.PoolID, &l.CreatorWallet, &l.QuoteMint, &l.Name, &l.Symbol,
		&l.ImageURL, &l.ImageThumbURL, &l.MetadataURI, &l.LaunchSignature, &l.FeeLamports, &l.TransferFeeBps,
		&l.PlatformID, &l.PeerID, &l.Status, &l.CreatedAt, &l.BoundAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// Create inserts a launch. Returns ErrAlreadyExists if the mint or signature is already recorded.
func (r *PostgresLaunchRepository) Create(ctx context.Context, l *models.TokenLaunch) error {
	status := l.Status
	if status == "" {
		status = models.LaunchStatusConfirmed
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO token_launches (
		mint, pool_id, creator_wallet, quote_mint, name, symbol, image_url, image_thumb_url, metadata_uri,
		launch_signature, fee_lamports, transfer_fee_bps, platform_id, status, created_at
	) VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12, NULLIF($13, ''), $14, $15)`,
		l.Mint, l.PoolID, l.CreatorWallet, l.QuoteMint, l.Name, l.Symbol, l.ImageURL, l.ImageThumbURL, l.MetadataURI,
		l.LaunchSignature, l.FeeLamports, l.TransferFeeBps, l.PlatformID, status, l.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// GetByMint returns the launch for a mint, or ErrNotFound.
func (r *PostgresLaunchRepository) GetByMint(ctx context.Context, mint string) (*models.TokenLaunch, error) {
	l, err := scanLaunch(r.pool.QueryRow(ctx, `SELECT `+launchSelectCols+` FROM token_launches WHERE mint = $1`, mint))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return l, nil
}

// GetBySignature returns the launch recorded from a launch transaction, or ErrNotFound.
func (r *PostgresLaunchRepository) GetBySignature(ctx context.Context, signature string) (*models.TokenLaunch, error) {
	l, err := scanLaunch(r.pool.QueryRow(ctx, `SELECT `+launchSelectCols+` FROM token_launches WHERE launch_signature = $1`, signature))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return l, nil
}

// List returns launches ordered by created_at DESC plus the total matching count.
func (r *PostgresLaunchRepository) List(ctx context.Context, opts ListLaunchesOptions) ([]*models.TokenLaunch, int, error) {
	var where []string
	var args []interface{}
	if opts.CreatorWallet != "" {
		args = append(args, opts.CreatorWallet)
		where = append(where, fmt.Sprintf("creator_wallet = $%d", len(args)))
	}
	if opts.UnboundOnly {
		where = append(where, "peer_id IS NULL")
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM token_launches`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	listArgs := append(append([]interface{}{}, args...), limit, opts.Offset)
	rows, err := r.pool.Query(ctx, `SELECT `+launchSelectCols+` FROM token_launches`+clause+
		fmt.Sprintf(` ORDER BY created_at DESC, mint LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2), listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	launches := []*models.TokenLaunch{}
	for rows.Next() {
		l, err := scanLaunch(rows)
		if err != nil {
			return nil, 0, err
		}
		launches = append(launches, l)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return launches, total, nil
}

// Bind sets peer_id, status='bound' and bound_at on an unbound launch (conditional update, race-safe).
func (r *PostgresLaunchRepository) Bind(ctx context.Context, mint, peerID string, boundAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `UPDATE token_launches
		SET peer_id = $1, status = $2, bound_at = $3
		WHERE mint = $4 AND peer_id IS NULL`, peerID, models.LaunchStatusBound, boundAt, mint)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM token_launches WHERE mint = $1)`, mint).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return models.ErrNotFound
		}
		return models.ErrAlreadyExists
	}
	return nil
}

// GetByMints returns the launches that exist among mints, keyed by mint.
func (r *PostgresLaunchRepository) GetByMints(ctx context.Context, mints []string) (map[string]*models.TokenLaunch, error) {
	out := make(map[string]*models.TokenLaunch, len(mints))
	if len(mints) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+launchSelectCols+` FROM token_launches WHERE mint = ANY($1)`, mints)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanLaunch(rows)
		if err != nil {
			return nil, err
		}
		out[l.Mint] = l
	}
	return out, rows.Err()
}

// ListByPeerID returns the launches bound to peerID, newest first.
func (r *PostgresLaunchRepository) ListByPeerID(ctx context.Context, peerID string) ([]*models.TokenLaunch, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+launchSelectCols+` FROM token_launches WHERE peer_id = $1 ORDER BY created_at DESC, mint`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.TokenLaunch{}
	for rows.Next() {
		l, err := scanLaunch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
