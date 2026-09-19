// Package repository: Postgres implementation of AvailabilityRepository.
// Feature: F-007 (Centralized Tracker). Purpose: Per-peer chunk availability in PostgreSQL.

package repository

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresAvailabilityRepository implements AvailabilityRepository using PostgreSQL.
type PostgresAvailabilityRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresAvailabilityRepository creates a new Postgres availability repository.
func NewPostgresAvailabilityRepository(pool *pgxpool.Pool) *PostgresAvailabilityRepository {
	return &PostgresAvailabilityRepository{pool: pool}
}

func (r *PostgresAvailabilityRepository) UpsertPeerChunks(ctx context.Context, cid string, peerID string, chunks []int) error {
	// Filter to non-negative and dedupe
	seen := make(map[int]bool)
	var clean []int
	for _, c := range chunks {
		if c >= 0 && !seen[c] {
			seen[c] = true
			clean = append(clean, c)
		}
	}
	sort.Ints(clean)
	_, err := r.pool.Exec(ctx, `INSERT INTO peer_chunk_availability (cid, peer_id, chunks)
		VALUES ($1, $2, $3)
		ON CONFLICT (cid, peer_id) DO UPDATE SET chunks = EXCLUDED.chunks`,
		cid, peerID, clean,
	)
	return err
}

func (r *PostgresAvailabilityRepository) GetPeerChunks(ctx context.Context, cid string) ([]PeerChunkAvailability, error) {
	rows, err := r.pool.Query(ctx, `SELECT peer_id, chunks FROM peer_chunk_availability WHERE cid = $1`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerChunkAvailability
	for rows.Next() {
		var pca PeerChunkAvailability
		err := rows.Scan(&pca.PeerID, &pca.Chunks)
		if err != nil {
			return nil, err
		}
		out = append(out, pca)
	}
	return out, rows.Err()
}
