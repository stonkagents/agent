// Package: tracker/internal/repository
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Peer Activity & Reputation)
// Purpose: Postgres implementation of ReputationSnapshotRepository (TD-062)

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// PostgresReputationSnapshotRepository implements ReputationSnapshotRepository with Postgres.
type PostgresReputationSnapshotRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresReputationSnapshotRepository creates a new Postgres reputation snapshot repository.
func NewPostgresReputationSnapshotRepository(pool *pgxpool.Pool) *PostgresReputationSnapshotRepository {
	return &PostgresReputationSnapshotRepository{pool: pool}
}

// Upsert inserts or updates a snapshot for a peer (one snapshot per peer per day).
func (r *PostgresReputationSnapshotRepository) Upsert(ctx context.Context, snapshot *reputation.ReputationSnapshot) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO reputation_snapshots (peer_id, composite_score, snapped_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (peer_id, snapped_at) DO UPDATE SET
		 composite_score = EXCLUDED.composite_score`,
		snapshot.PeerID, snapshot.CompositeScore, snapshot.SnappedAt,
	)
	return err
}

// FindPrevious returns the most recent snapshot before the given time, or nil if none.
func (r *PostgresReputationSnapshotRepository) FindPrevious(ctx context.Context, peerID string, before time.Time) (*reputation.ReputationSnapshot, error) {
	var s reputation.ReputationSnapshot
	err := r.pool.QueryRow(ctx,
		`SELECT peer_id, composite_score, snapped_at
		 FROM reputation_snapshots
		 WHERE peer_id = $1 AND snapped_at < $2
		 ORDER BY snapped_at DESC
		 LIMIT 1`,
		peerID, before,
	).Scan(&s.PeerID, &s.CompositeScore, &s.SnappedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}
