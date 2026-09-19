// Package: tracker/internal/repository
// Feature: F-031 (Token Data Persistence)
// Story: US-031-01 (Backend Token Persistence)
// Purpose: PostgreSQL implementation of TokenRepository

package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresTokenRepository implements TokenRepository with PostgreSQL.
type PostgresTokenRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresTokenRepository creates a new PostgreSQL token repository.
func NewPostgresTokenRepository(pool *pgxpool.Pool) *PostgresTokenRepository {
	return &PostgresTokenRepository{pool: pool}
}

// Create persists a new peer token. Returns ErrAlreadyExists if peer_id or contract address already exists.
func (r *PostgresTokenRepository) Create(ctx context.Context, token *models.PeerToken) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO peer_tokens (
		peer_id, token_contract_address, token_ticker, token_name, token_image_url, launched_at
	) VALUES ($1, $2, $3, $4, $5, $6)`,
		token.PeerID, token.TokenContractAddress, token.TokenTicker,
		token.TokenName, token.TokenImageURL, token.LaunchedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// GetByPeerID returns the token for a peer, or ErrNotFound.
func (r *PostgresTokenRepository) GetByPeerID(ctx context.Context, peerID string) (*models.PeerToken, error) {
	var t models.PeerToken
	err := r.pool.QueryRow(ctx, `SELECT peer_id, token_contract_address, token_ticker, token_name,
		COALESCE(token_image_url, ''), launched_at
		FROM peer_tokens WHERE peer_id = $1`, peerID).Scan(
		&t.PeerID, &t.TokenContractAddress, &t.TokenTicker,
		&t.TokenName, &t.TokenImageURL, &t.LaunchedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// GetByContractAddress returns the token with this contract address, or ErrNotFound.
func (r *PostgresTokenRepository) GetByContractAddress(ctx context.Context, contractAddr string) (*models.PeerToken, error) {
	var t models.PeerToken
	err := r.pool.QueryRow(ctx, `SELECT peer_id, token_contract_address, token_ticker, token_name,
		COALESCE(token_image_url, ''), launched_at
		FROM peer_tokens WHERE token_contract_address = $1`, contractAddr).Scan(
		&t.PeerID, &t.TokenContractAddress, &t.TokenTicker,
		&t.TokenName, &t.TokenImageURL, &t.LaunchedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// List returns peer tokens ordered by launched_at DESC with pagination.
func (r *PostgresTokenRepository) List(ctx context.Context, limit, offset int) ([]*models.PeerToken, int, error) {
	// Get total count
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM peer_tokens`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx, `SELECT peer_id, token_contract_address, token_ticker, token_name,
		COALESCE(token_image_url, ''), launched_at
		FROM peer_tokens ORDER BY launched_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var tokens []*models.PeerToken
	for rows.Next() {
		var t models.PeerToken
		if err := rows.Scan(&t.PeerID, &t.TokenContractAddress, &t.TokenTicker,
			&t.TokenName, &t.TokenImageURL, &t.LaunchedAt); err != nil {
			return nil, 0, err
		}
		tokens = append(tokens, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if tokens == nil {
		tokens = []*models.PeerToken{}
	}
	return tokens, total, nil
}

// Upsert inserts the token or replaces the peer's existing row (ON CONFLICT (peer_id)).
// Returns ErrAlreadyExists if the contract address is already held by a different peer
// (unique index idx_peer_tokens_contract_address).
func (r *PostgresTokenRepository) Upsert(ctx context.Context, token *models.PeerToken) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO peer_tokens (
		peer_id, token_contract_address, token_ticker, token_name, token_image_url, launched_at
	) VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)
	ON CONFLICT (peer_id) DO UPDATE SET
		token_contract_address = EXCLUDED.token_contract_address,
		token_ticker           = EXCLUDED.token_ticker,
		token_name             = EXCLUDED.token_name,
		token_image_url        = COALESCE(EXCLUDED.token_image_url, peer_tokens.token_image_url),
		launched_at            = EXCLUDED.launched_at`,
		token.PeerID, token.TokenContractAddress, token.TokenTicker,
		token.TokenName, token.TokenImageURL, token.LaunchedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// UpdateImageURL sets the token_image_url for a peer's token (write-once: only when currently NULL).
func (r *PostgresTokenRepository) UpdateImageURL(ctx context.Context, peerID, imageURL string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE peer_tokens SET token_image_url = $1 WHERE peer_id = $2 AND token_image_url IS NULL`, imageURL, peerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}
