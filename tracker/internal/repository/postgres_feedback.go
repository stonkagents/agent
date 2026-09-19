// Package: tracker/internal/repository
// Feature: StonkAgents portal feedback
// Purpose: PostgreSQL FeedbackRepository (table feedback, migration 016)

package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresFeedbackRepository implements FeedbackRepository with PostgreSQL.
type PostgresFeedbackRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresFeedbackRepository creates a new PostgreSQL feedback repository.
func NewPostgresFeedbackRepository(pool *pgxpool.Pool) *PostgresFeedbackRepository {
	return &PostgresFeedbackRepository{pool: pool}
}

const feedbackSelectCols = `id, kind, message, path, contact, contact_via, wallet_address, user_agent, ip_hash, created_at`

// Create inserts a submission and fills ID and CreatedAt from the database.
func (r *PostgresFeedbackRepository) Create(ctx context.Context, fb *models.Feedback) error {
	return r.pool.QueryRow(ctx, `INSERT INTO feedback
		(kind, message, path, contact, contact_via, wallet_address, user_agent, ip_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		fb.Kind, fb.Message, fb.Path, fb.Contact, fb.ContactVia, fb.WalletAddress, fb.UserAgent, fb.IPHash,
	).Scan(&fb.ID, &fb.CreatedAt)
}

// List returns submissions ordered by id DESC, optionally strictly before a cursor id.
func (r *PostgresFeedbackRepository) List(ctx context.Context, opts ListFeedbackOptions) ([]*models.Feedback, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+feedbackSelectCols+` FROM feedback
		WHERE ($1::bigint = 0 OR id < $1) ORDER BY id DESC LIMIT $2`, opts.BeforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.Feedback{}
	for rows.Next() {
		var fb models.Feedback
		if err := rows.Scan(&fb.ID, &fb.Kind, &fb.Message, &fb.Path, &fb.Contact, &fb.ContactVia,
			&fb.WalletAddress, &fb.UserAgent, &fb.IPHash, &fb.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &fb)
	}
	return out, rows.Err()
}
