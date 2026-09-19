// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: PostgreSQL implementation of NonceRepository for challenge-response registration

package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresNonceRepository implements NonceRepository using PostgreSQL.
type PostgresNonceRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresNonceRepository creates a new Postgres nonce repository.
func NewPostgresNonceRepository(pool *pgxpool.Pool) *PostgresNonceRepository {
	return &PostgresNonceRepository{pool: pool}
}

func (r *PostgresNonceRepository) Upsert(ctx context.Context, nonce *models.RegistrationNonce) error {
	// The unique partial index ux_nonce_active_per_peer enforces one unconsumed nonce per peer.
	// ON CONFLICT replaces the existing active nonce with the new one.
	_, err := r.pool.Exec(ctx,
		`INSERT INTO registration_nonces (id, peer_id, nonce, consumed, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (peer_id) WHERE consumed = FALSE
		 DO UPDATE SET id = $1, nonce = $3, expires_at = $5, created_at = $6`,
		nonce.ID, nonce.PeerID, nonce.Nonce, nonce.Consumed, nonce.ExpiresAt, nonce.CreatedAt,
	)
	return err
}

func (r *PostgresNonceRepository) GetActiveByPeerID(ctx context.Context, peerID string) (*models.RegistrationNonce, error) {
	var n models.RegistrationNonce
	err := r.pool.QueryRow(ctx,
		`SELECT id, peer_id, nonce, consumed, expires_at, created_at
		 FROM registration_nonces
		 WHERE peer_id = $1 AND consumed = FALSE
		 ORDER BY created_at DESC LIMIT 1`, peerID,
	).Scan(&n.ID, &n.PeerID, &n.Nonce, &n.Consumed, &n.ExpiresAt, &n.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

func (r *PostgresNonceRepository) MarkConsumed(ctx context.Context, id string) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE registration_nonces SET consumed = TRUE WHERE id = $1 AND consumed = FALSE`, id,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}
