// Package repository: Postgres implementation of ReputationRepository.
// Feature: F-007 (Centralized Tracker). Story: US-007-04 (EigenTrust Reputation System)

package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// PostgresReputationRepository implements ReputationRepository using PostgreSQL.
type PostgresReputationRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresReputationRepository creates a new Postgres reputation repository.
func NewPostgresReputationRepository(pool *pgxpool.Pool) *PostgresReputationRepository {
	return &PostgresReputationRepository{pool: pool}
}

func (r *PostgresReputationRepository) Upsert(ctx context.Context, record *reputation.ReputationRecord) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO reputation_scores (
		peer_id, bandwidth_score, quality_score, security_score, citizenship_score, composite_score, updated_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (peer_id) DO UPDATE SET
		bandwidth_score = EXCLUDED.bandwidth_score,
		quality_score = EXCLUDED.quality_score,
		security_score = EXCLUDED.security_score,
		citizenship_score = EXCLUDED.citizenship_score,
		composite_score = EXCLUDED.composite_score,
		updated_at = EXCLUDED.updated_at`,
		record.PeerID, record.BandwidthScore, record.QualityScore, record.SecurityScore,
		record.CitizenshipScore, record.CompositeScore, record.UpdatedAt,
	)
	return err
}

func (r *PostgresReputationRepository) FindByPeerID(ctx context.Context, peerID string) (*reputation.ReputationRecord, error) {
	var rec reputation.ReputationRecord
	err := r.pool.QueryRow(ctx, `SELECT peer_id, bandwidth_score, quality_score, security_score, citizenship_score, composite_score, updated_at
		FROM reputation_scores WHERE peer_id = $1`, peerID).Scan(
		&rec.PeerID, &rec.BandwidthScore, &rec.QualityScore, &rec.SecurityScore,
		&rec.CitizenshipScore, &rec.CompositeScore, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

// FindByPeerIDs returns reputation records for the given peer IDs as a map. Missing peers are omitted.
// F-032, US-032-01: batch query for enriched peer list.
func (r *PostgresReputationRepository) FindByPeerIDs(ctx context.Context, peerIDs []string) (map[string]*reputation.ReputationRecord, error) {
	if len(peerIDs) == 0 {
		return map[string]*reputation.ReputationRecord{}, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT peer_id, bandwidth_score, quality_score, security_score, citizenship_score, composite_score, updated_at
		 FROM reputation_scores WHERE peer_id = ANY($1)`, peerIDs)
	if err != nil {
		return nil, fmt.Errorf("find by peer ids: %w", err)
	}
	defer rows.Close()
	records, err := scanReputationRecords(rows)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*reputation.ReputationRecord, len(records))
	for _, rec := range records {
		result[rec.PeerID] = rec
	}
	return result, nil
}

func (r *PostgresReputationRepository) ListByCompositeScore(ctx context.Context, limit, offset int) ([]*reputation.ReputationRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT peer_id, bandwidth_score, quality_score, security_score, citizenship_score, composite_score, updated_at
		FROM reputation_scores ORDER BY composite_score DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReputationRecords(rows)
}

func (r *PostgresReputationRepository) ListAll(ctx context.Context) ([]*reputation.ReputationRecord, error) {
	rows, err := r.pool.Query(ctx, `SELECT peer_id, bandwidth_score, quality_score, security_score, citizenship_score, composite_score, updated_at
		FROM reputation_scores ORDER BY composite_score DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReputationRecords(rows)
}

func scanReputationRecords(rows pgx.Rows) ([]*reputation.ReputationRecord, error) {
	var out []*reputation.ReputationRecord
	for rows.Next() {
		var rec reputation.ReputationRecord
		err := rows.Scan(&rec.PeerID, &rec.BandwidthScore, &rec.QualityScore, &rec.SecurityScore,
			&rec.CitizenshipScore, &rec.CompositeScore, &rec.UpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// AverageCompositeScore returns the mean composite score across all peers. Returns 0.0 if no records.
func (r *PostgresReputationRepository) AverageCompositeScore(ctx context.Context) (float64, error) {
	var avg *float64
	err := r.pool.QueryRow(ctx, `SELECT AVG(composite_score) FROM reputation_scores`).Scan(&avg)
	if err != nil {
		return 0.0, fmt.Errorf("average composite score: %w", err)
	}
	if avg == nil {
		return 0.0, nil
	}
	return *avg, nil
}

// CountAboveScore returns how many peers have a composite_score strictly above
// the given score, and the total number of scored peers. Used for top_percent.
func (r *PostgresReputationRepository) CountAboveScore(ctx context.Context, score float64) (above int, total int, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT COUNT(*) AS total,
		        COUNT(*) FILTER (WHERE composite_score > $1) AS above
		 FROM reputation_scores`, score,
	).Scan(&total, &above)
	if err != nil {
		return 0, 0, fmt.Errorf("count above score: %w", err)
	}
	return above, total, nil
}
