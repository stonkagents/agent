// Package presence: Postgres-backed PresenceStore using peers.last_seen.
// Feature: F-007 (Centralized Tracker). Story: US-007-02 (Peer Registry with PostgreSQL Presence)

package presence

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel timestamp for "offline": peers with last_seen <= this are considered offline.
var offlineSentinel = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

// PostgresPresenceStore implements PresenceStore by updating and querying peers.last_seen.
type PostgresPresenceStore struct {
	pool      *pgxpool.Pool
	onlineTTL time.Duration // peers with last_seen within this window are "online"
}

// NewPostgresPresenceStore creates a new Postgres presence store.
// onlineTTL is the window (e.g. 5*time.Minute) within which last_seen counts as online.
func NewPostgresPresenceStore(pool *pgxpool.Pool, onlineTTL time.Duration) *PostgresPresenceStore {
	if onlineTTL <= 0 {
		onlineTTL = 5 * time.Minute
	}
	return &PostgresPresenceStore{pool: pool, onlineTTL: onlineTTL}
}

// Heartbeat updates peers.last_seen = NOW() for the given peer_id.
// Also manages current_session_start: sets it to NOW() on first heartbeat after
// an offline gap (last_seen older than the online TTL window), preserving it otherwise.
func (s *PostgresPresenceStore) Heartbeat(ctx context.Context, peerID string, _ time.Duration) error {
	ttlSeconds := int(s.onlineTTL.Seconds())
	_, err := s.pool.Exec(ctx,
		`UPDATE peers SET
			last_seen = NOW(),
			current_session_start = CASE
				WHEN last_seen < NOW() - make_interval(secs => $1) OR current_session_start IS NULL
				THEN NOW()
				ELSE current_session_start
			END
		WHERE peer_id = $2`,
		ttlSeconds, peerID)
	return err
}

// IsOnline returns true if the peer's last_seen is within the online TTL window.
// Single SQL expression avoids fetching last_seen and computing in Go.
func (s *PostgresPresenceStore) IsOnline(ctx context.Context, peerID string) (bool, error) {
	cutoff := time.Now().Add(-s.onlineTTL)
	var online bool
	err := s.pool.QueryRow(ctx,
		`SELECT (last_seen > $1 AND last_seen > $2) FROM peers WHERE peer_id = $3`,
		cutoff, offlineSentinel, peerID).Scan(&online)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return online, nil
}

// OnlinePeerIDs returns peer_ids where last_seen is within the online TTL window.
// Explicitly excludes peers marked offline via Remove (last_seen = offlineSentinel).
func (s *PostgresPresenceStore) OnlinePeerIDs(ctx context.Context) ([]string, error) {
	cutoff := time.Now().Add(-s.onlineTTL)
	rows, err := s.pool.Query(ctx, `SELECT peer_id FROM peers WHERE last_seen > $1 AND last_seen > $2`, cutoff, offlineSentinel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Remove marks the peer as offline by setting last_seen to a sentinel (epoch).
func (s *PostgresPresenceStore) Remove(ctx context.Context, peerID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE peers SET last_seen = $1 WHERE peer_id = $2`, offlineSentinel, peerID)
	return err
}
