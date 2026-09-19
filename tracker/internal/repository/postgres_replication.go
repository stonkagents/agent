package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

type PostgresReplicationRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresReplicationRepository(pool *pgxpool.Pool) *PostgresReplicationRepository {
	return &PostgresReplicationRepository{pool: pool}
}

func (r *PostgresReplicationRepository) UpsertAssetReplication(ctx context.Context, replication *models.AssetReplication) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO asset_replications (
			cid, s3_key, status, size_bytes, etag, replicated_at, last_error, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (cid) DO UPDATE SET
			s3_key = EXCLUDED.s3_key,
			status = EXCLUDED.status,
			size_bytes = EXCLUDED.size_bytes,
			etag = EXCLUDED.etag,
			replicated_at = EXCLUDED.replicated_at,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`, replication.CID, replication.S3Key, replication.Status, replication.SizeBytes, replication.ETag, replication.ReplicatedAt, replication.LastError)
	return err
}

func (r *PostgresReplicationRepository) GetAssetReplication(ctx context.Context, cid string) (*models.AssetReplication, error) {
	var out models.AssetReplication
	err := r.pool.QueryRow(ctx, `
		SELECT cid, s3_key, status, size_bytes, etag, replicated_at, last_error, updated_at
		FROM asset_replications WHERE cid = $1
	`, cid).Scan(&out.CID, &out.S3Key, &out.Status, &out.SizeBytes, &out.ETag, &out.ReplicatedAt, &out.LastError, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (r *PostgresReplicationRepository) CreateReplicationJob(ctx context.Context, job *models.ReplicationJob) error {
	var finishedAt interface{}
	if job.FinishedAt != nil {
		finishedAt = *job.FinishedAt
	}
	id := job.ID
	if id == "" {
		id = fmt.Sprintf("%s-%d", job.CID, time.Now().UnixNano())
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO replication_jobs (
			id, cid, assigned_worker, attempt, status, started_at, finished_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id, job.CID, job.AssignedWorker, job.Attempt, job.Status, job.StartedAt, finishedAt)
	return err
}
