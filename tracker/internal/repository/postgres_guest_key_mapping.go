// Package repository: Postgres implementation of GuestKeyMappingRepository.
// Maps guest API keys to account IDs for unified credit ledger (Agent completions auth).

package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresGuestKeyMappingRepository implements GuestKeyMappingRepository.
type PostgresGuestKeyMappingRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresGuestKeyMappingRepository creates a new Postgres guest key mapping repository.
func NewPostgresGuestKeyMappingRepository(pool *pgxpool.Pool) *PostgresGuestKeyMappingRepository {
	return &PostgresGuestKeyMappingRepository{pool: pool}
}

// Create stores api_key -> account_id.
func (r *PostgresGuestKeyMappingRepository) Create(ctx context.Context, apiKey, accountID string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO guest_api_keys (api_key, account_id) VALUES ($1, $2)`,
		apiKey, accountID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// GetAccountIDByAPIKey returns the account_id for the guest key, or ErrNotFound.
func (r *PostgresGuestKeyMappingRepository) GetAccountIDByAPIKey(ctx context.Context, apiKey string) (accountID string, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT account_id FROM guest_api_keys WHERE api_key = $1`, apiKey,
	).Scan(&accountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", models.ErrNotFound
		}
		return "", err
	}
	return accountID, nil
}
