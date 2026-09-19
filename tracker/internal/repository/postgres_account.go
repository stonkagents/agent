// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: PostgreSQL implementation of AccountRepository

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresAccountRepository implements AccountRepository using PostgreSQL.
type PostgresAccountRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresAccountRepository creates a new Postgres account repository.
func NewPostgresAccountRepository(pool *pgxpool.Pool) *PostgresAccountRepository {
	return &PostgresAccountRepository{pool: pool}
}

func (r *PostgresAccountRepository) Create(ctx context.Context, account *models.Account) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO accounts (id, peer_id, status, created_at) VALUES ($1, $2, $3, $4)`,
		account.ID, account.PeerID, account.Status, account.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (r *PostgresAccountRepository) GetByID(ctx context.Context, id string) (*models.Account, error) {
	var a models.Account
	err := r.pool.QueryRow(ctx,
		`SELECT id, peer_id, status, created_at FROM accounts WHERE id = $1`, id,
	).Scan(&a.ID, &a.PeerID, &a.Status, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *PostgresAccountRepository) GetByPeerID(ctx context.Context, peerID string) (*models.Account, error) {
	var a models.Account
	err := r.pool.QueryRow(ctx,
		`SELECT id, peer_id, status, created_at FROM accounts WHERE peer_id = $1`, peerID,
	).Scan(&a.ID, &a.PeerID, &a.Status, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

// GetOrCreateForPeer returns the account for the peer, creating it if it does not exist.
// The second return is true if the account was newly created.
// Uses atomic INSERT ... ON CONFLICT to eliminate race conditions.
func (r *PostgresAccountRepository) GetOrCreateForPeer(ctx context.Context, peerID string) (*models.Account, bool, error) {
	var acc models.Account
	acc.ID = uuid.New().String()
	acc.PeerID = peerID
	acc.Status = models.AccountStatusActive
	acc.CreatedAt = time.Now().UTC()

	// Try to insert; if conflict, account already exists
	err := r.pool.QueryRow(ctx,
		`INSERT INTO accounts (id, peer_id, status, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (peer_id) DO NOTHING
		 RETURNING id, peer_id, status, created_at`,
		acc.ID, acc.PeerID, acc.Status, acc.CreatedAt,
	).Scan(&acc.ID, &acc.PeerID, &acc.Status, &acc.CreatedAt)

	if err == nil {
		// Row inserted - newly created
		return &acc, true, nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		// Conflict occurred - account already exists, fetch it
		existing, err := r.GetByPeerID(ctx, peerID)
		if err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}

	// Other error (e.g., database error)
	return nil, false, err
}

func (r *PostgresAccountRepository) UpdateStatus(ctx context.Context, id, status string) error {
	cmd, err := r.pool.Exec(ctx,
		`UPDATE accounts SET status = $1 WHERE id = $2`, status, id,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}
