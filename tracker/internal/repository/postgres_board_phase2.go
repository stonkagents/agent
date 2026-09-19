// Package repository: Postgres community board phase 2 repositories: the autopilot categories
// each daemon reports (peer_autopilot) and the room holder snapshots behind the digest
// (room_holder_snapshots). Migration 025.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// --- PeerAutopilotRepository ---

// PostgresPeerAutopilotRepository implements PeerAutopilotRepository on peer_autopilot.
type PostgresPeerAutopilotRepository struct{ pool *pgxpool.Pool }

// NewPostgresPeerAutopilotRepository creates the repository.
func NewPostgresPeerAutopilotRepository(pool *pgxpool.Pool) *PostgresPeerAutopilotRepository {
	return &PostgresPeerAutopilotRepository{pool: pool}
}

// Set replaces the peer's categories; the row is only written when they changed (heartbeats
// are frequent).
func (r *PostgresPeerAutopilotRepository) Set(ctx context.Context, peerID string, categories []string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO peer_autopilot (peer_id, categories, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (peer_id) DO UPDATE SET categories = EXCLUDED.categories, updated_at = EXCLUDED.updated_at
		WHERE peer_autopilot.categories IS DISTINCT FROM EXCLUDED.categories`, peerID, mentionArray(categories), at)
	return err
}

// Clear removes the peer's row.
func (r *PostgresPeerAutopilotRepository) Clear(ctx context.Context, peerID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM peer_autopilot WHERE peer_id = $1`, peerID)
	return err
}

// CategoriesByIDs returns peer id -> categories for the peers that have a row.
func (r *PostgresPeerAutopilotRepository) CategoriesByIDs(ctx context.Context, peerIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(peerIDs))
	if len(peerIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT peer_id, categories FROM peer_autopilot WHERE peer_id = ANY($1)`, peerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var cats []string
		if err := rows.Scan(&id, &cats); err != nil {
			return nil, err
		}
		out[id] = cats
	}
	return out, rows.Err()
}

// --- RoomHolderSnapshotRepository ---

// PostgresRoomHolderSnapshotRepository implements RoomHolderSnapshotRepository on room_holder_snapshots.
type PostgresRoomHolderSnapshotRepository struct{ pool *pgxpool.Pool }

// NewPostgresRoomHolderSnapshotRepository creates the repository.
func NewPostgresRoomHolderSnapshotRepository(pool *pgxpool.Pool) *PostgresRoomHolderSnapshotRepository {
	return &PostgresRoomHolderSnapshotRepository{pool: pool}
}

// utcDay truncates t to its UTC calendar day.
func utcDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// Record upserts the room's holder count for the UTC day of takenOn (last write wins).
func (r *PostgresRoomHolderSnapshotRepository) Record(ctx context.Context, mint string, takenOn time.Time, holders int) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO room_holder_snapshots (mint, taken_on, holders) VALUES ($1, $2, $3)
		ON CONFLICT (mint, taken_on) DO UPDATE SET holders = EXCLUDED.holders`, mint, utcDay(takenOn), holders)
	return err
}

// LatestAtOrBefore returns the newest snapshot taken on or before the UTC day of at.
func (r *PostgresRoomHolderSnapshotRepository) LatestAtOrBefore(ctx context.Context, mint string, at time.Time) (*models.RoomHolderSnapshot, error) {
	var s models.RoomHolderSnapshot
	err := r.pool.QueryRow(ctx, `SELECT mint, taken_on, holders FROM room_holder_snapshots
		WHERE mint = $1 AND taken_on <= $2 ORDER BY taken_on DESC LIMIT 1`, mint, utcDay(at)).Scan(&s.Mint, &s.TakenOn, &s.Holders)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}
