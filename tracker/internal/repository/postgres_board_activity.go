// Package repository: Postgres implementation of BoardActivityRepository (table board_activity, migration 022).
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresBoardActivityRepository implements BoardActivityRepository using PostgreSQL.
type PostgresBoardActivityRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresBoardActivityRepository creates a new Postgres board activity repository.
func NewPostgresBoardActivityRepository(pool *pgxpool.Pool) *PostgresBoardActivityRepository {
	return &PostgresBoardActivityRepository{pool: pool}
}

const boardActivityCols = `id, peer_id, kind, post_id, reply_id, actor_peer_id, amount, symbol, created_at, read_at`

// Create inserts one row; a missing ID is generated.
func (r *PostgresBoardActivityRepository) Create(ctx context.Context, a *models.BoardActivity) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO board_activity (`+boardActivityCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		a.ID, a.PeerID, a.Kind, a.PostID, a.ReplyID, a.ActorPeerID, a.Amount, a.Symbol, a.CreatedAt, a.ReadAt)
	return err
}

// Exists reports whether peerID already has a row of kind for postID from actorPeerID.
func (r *PostgresBoardActivityRepository) Exists(ctx context.Context, peerID, kind, postID, actorPeerID string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_activity
		WHERE peer_id = $1 AND kind = $2 AND post_id = $3 AND actor_peer_id = $4`,
		peerID, kind, postID, actorPeerID).Scan(&n)
	return n > 0, err
}

// ExistsAfter is Exists restricted to rows created at or after the given time.
func (r *PostgresBoardActivityRepository) ExistsAfter(ctx context.Context, peerID, kind, postID, actorPeerID string, after time.Time) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_activity
		WHERE peer_id = $1 AND kind = $2 AND post_id = $3 AND actor_peer_id = $4 AND created_at >= $5`,
		peerID, kind, postID, actorPeerID, after).Scan(&n)
	return n > 0, err
}

// List returns peerID's rows newest first under the ActivityQuery filters.
func (r *PostgresBoardActivityRepository) List(ctx context.Context, peerID string, q ActivityQuery) ([]*models.BoardActivity, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	var kinds []string
	if len(q.Kinds) > 0 {
		kinds = q.Kinds
	}
	rows, err := r.pool.Query(ctx, `SELECT `+boardActivityCols+` FROM board_activity
		WHERE peer_id = $1 AND ($2::timestamptz IS NULL OR created_at > $2)
		  AND ($4::text[] IS NULL OR kind = ANY($4))
		  AND (NOT $5::boolean OR read_at IS NULL)
		  AND ($6::text[] IS NULL OR NOT (kind = ANY($6)))
		ORDER BY created_at DESC LIMIT $3`, peerID, q.Since, limit, kinds, q.Unread, nilIfEmpty(q.ExcludeKinds))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.BoardActivity{}
	for rows.Next() {
		var a models.BoardActivity
		if err := rows.Scan(&a.ID, &a.PeerID, &a.Kind, &a.PostID, &a.ReplyID, &a.ActorPeerID, &a.Amount, &a.Symbol, &a.CreatedAt, &a.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// CountUnread returns how many of peerID's rows have no read_at.
func (r *PostgresBoardActivityRepository) CountUnread(ctx context.Context, peerID string) (int, error) {
	return r.CountUnreadExcluding(ctx, peerID, nil)
}

// CountUnreadExcluding is CountUnread without rows of the given kinds.
func (r *PostgresBoardActivityRepository) CountUnreadExcluding(ctx context.Context, peerID string, kinds []string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_activity WHERE peer_id = $1 AND read_at IS NULL
		AND ($2::text[] IS NULL OR NOT (kind = ANY($2)))`, peerID, nilIfEmpty(kinds)).Scan(&n)
	return n, err
}

// nilIfEmpty turns an empty slice into a NULL array parameter.
func nilIfEmpty(kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	return kinds
}

// MarkRead sets read_at on peerID's unread rows with the given ids.
func (r *PostgresBoardActivityRepository) MarkRead(ctx context.Context, peerID string, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `UPDATE board_activity SET read_at = $3
		WHERE peer_id = $1 AND id = ANY($2) AND read_at IS NULL`, peerID, ids, at)
	return err
}

// MarkAllRead sets read_at on all of peerID's unread rows.
func (r *PostgresBoardActivityRepository) MarkAllRead(ctx context.Context, peerID string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE board_activity SET read_at = $2
		WHERE peer_id = $1 AND read_at IS NULL`, peerID, at)
	return err
}
