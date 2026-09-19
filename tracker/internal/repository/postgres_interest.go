// Package: tracker/internal/repository
// Feature: StonkAgents roadmap interest
// Purpose: PostgreSQL InterestRepository (table agent_interest, migration 017)

package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresInterestRepository implements InterestRepository with PostgreSQL.
type PostgresInterestRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresInterestRepository creates a new PostgreSQL interest repository.
func NewPostgresInterestRepository(pool *pgxpool.Pool) *PostgresInterestRepository {
	return &PostgresInterestRepository{pool: pool}
}

const interestSelectCols = `id, capabilities, description, priority, contact, contact_via, wallet_address, user_agent, ip_hash, path, created_at`

// Create inserts a submission and fills ID and CreatedAt from the database.
func (r *PostgresInterestRepository) Create(ctx context.Context, it *models.AgentInterest) error {
	return r.pool.QueryRow(ctx, `INSERT INTO agent_interest
		(capabilities, description, priority, contact, contact_via, wallet_address, user_agent, ip_hash, path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		it.Capabilities, it.Description, it.Priority, it.Contact, it.ContactVia, it.WalletAddress, it.UserAgent, it.IPHash, it.Path,
	).Scan(&it.ID, &it.CreatedAt)
}

// List returns submissions ordered by id DESC, optionally strictly before a cursor id.
func (r *PostgresInterestRepository) List(ctx context.Context, opts ListInterestOptions) ([]*models.AgentInterest, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+interestSelectCols+` FROM agent_interest
		WHERE ($1::bigint = 0 OR id < $1) ORDER BY id DESC LIMIT $2`, opts.BeforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.AgentInterest{}
	for rows.Next() {
		var it models.AgentInterest
		if err := rows.Scan(&it.ID, &it.Capabilities, &it.Description, &it.Priority, &it.Contact, &it.ContactVia,
			&it.WalletAddress, &it.UserAgent, &it.IPHash, &it.Path, &it.CreatedAt); err != nil {
			return nil, err
		}
		if it.Capabilities == nil {
			it.Capabilities = []string{}
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

// CountByCapability counts rows whose capabilities contain each requested key (absent keys count 0).
func (r *PostgresInterestRepository) CountByCapability(ctx context.Context, keys []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, k := range keys {
		out[k] = 0
	}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT c, COUNT(*) FROM agent_interest, unnest(capabilities) AS c
		WHERE c = ANY($1) GROUP BY c`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int64
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, rows.Err()
}

// Summary rolls up every row by capability and priority (every allowed key present, 0 when absent).
func (r *PostgresInterestRepository) Summary(ctx context.Context) (*models.InterestSummary, error) {
	s := models.NewInterestSummary()
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_interest`).Scan(&s.Total); err != nil {
		return nil, err
	}
	caps, err := r.pool.Query(ctx, `SELECT c, COUNT(*) FROM agent_interest, unnest(capabilities) AS c GROUP BY c`)
	if err != nil {
		return nil, err
	}
	defer caps.Close()
	for caps.Next() {
		var key string
		var n int64
		if err := caps.Scan(&key, &n); err != nil {
			return nil, err
		}
		s.Capabilities[key] = n
	}
	if err := caps.Err(); err != nil {
		return nil, err
	}
	pris, err := r.pool.Query(ctx, `SELECT priority, COUNT(*) FROM agent_interest GROUP BY priority`)
	if err != nil {
		return nil, err
	}
	defer pris.Close()
	for pris.Next() {
		var key string
		var n int64
		if err := pris.Scan(&key, &n); err != nil {
			return nil, err
		}
		s.Priorities[key] = n
	}
	return s, pris.Err()
}
