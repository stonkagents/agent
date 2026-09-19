package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresGuestKeyRepository implements GuestKeyRepository using agent_guest_keys table.
type PostgresGuestKeyRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresGuestKeyRepository creates a new Postgres guest key repository.
func NewPostgresGuestKeyRepository(pool *pgxpool.Pool) *PostgresGuestKeyRepository {
	return &PostgresGuestKeyRepository{pool: pool}
}

// CreateGuestKey creates a new guest API key with the given default credit and returns it.
func (r *PostgresGuestKeyRepository) CreateGuestKey(ctx context.Context, defaultCredits int) (apiKey string, err error) {
	key, err := GenerateAPIKey()
	if err != nil {
		return "", err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO agent_guest_keys (api_key, credits_remaining) VALUES ($1, $2)`, key, defaultCredits)
	if err != nil {
		return "", err
	}
	return key, nil
}
