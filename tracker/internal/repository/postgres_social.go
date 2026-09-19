// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: PostgreSQL implementation of SocialRepository

package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresSocialRepository implements SocialRepository using PostgreSQL.
type PostgresSocialRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresSocialRepository creates a new Postgres social repository.
func NewPostgresSocialRepository(pool *pgxpool.Pool) *PostgresSocialRepository {
	return &PostgresSocialRepository{pool: pool}
}

func (r *PostgresSocialRepository) Insert(ctx context.Context, conn *models.SocialConnection) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO social_connections (id, account_id, platform, platform_user_id, verified_at, bonus_granted)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		conn.ID, conn.AccountID, conn.Platform, conn.PlatformUserID, conn.VerifiedAt, conn.BonusGranted,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (r *PostgresSocialRepository) GetByAccountID(ctx context.Context, accountID string) ([]*models.SocialConnection, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, account_id, platform, platform_user_id, verified_at, bonus_granted
		 FROM social_connections WHERE account_id = $1
		 ORDER BY verified_at`, accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.SocialConnection
	for rows.Next() {
		var c models.SocialConnection
		if err := rows.Scan(&c.ID, &c.AccountID, &c.Platform, &c.PlatformUserID, &c.VerifiedAt, &c.BonusGranted); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (r *PostgresSocialRepository) GetByAccountAndPlatform(ctx context.Context, accountID, platform string) (*models.SocialConnection, error) {
	var c models.SocialConnection
	err := r.pool.QueryRow(ctx,
		`SELECT id, account_id, platform, platform_user_id, verified_at, bonus_granted
		 FROM social_connections WHERE account_id = $1 AND platform = $2`,
		accountID, platform,
	).Scan(&c.ID, &c.AccountID, &c.Platform, &c.PlatformUserID, &c.VerifiedAt, &c.BonusGranted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (r *PostgresSocialRepository) Delete(ctx context.Context, accountID, platform string) error {
	cmd, err := r.pool.Exec(ctx,
		`DELETE FROM social_connections WHERE account_id = $1 AND platform = $2`,
		accountID, platform,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}
