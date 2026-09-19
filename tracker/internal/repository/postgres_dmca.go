// Package repository: Postgres implementation of DMCARepository.
// Feature: F-007 (Centralized Tracker). Story: US-007-06 (DMCA Takedown Endpoint)

package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresDMCARepository implements DMCARepository using PostgreSQL.
type PostgresDMCARepository struct {
	pool *pgxpool.Pool
}

// NewPostgresDMCARepository creates a new Postgres DMCA repository.
func NewPostgresDMCARepository(pool *pgxpool.Pool) *PostgresDMCARepository {
	return &PostgresDMCARepository{pool: pool}
}

func (r *PostgresDMCARepository) Create(ctx context.Context, notice *models.DMCANotice) error {
	id, err := uuid.Parse(notice.ID)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO dmca_notices (id, cid, reporter_email, complaint_text, quarantined_at, status)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, notice.CID, notice.ReporterEmail, notice.ComplaintText, notice.QuarantinedAt, notice.Status,
	)
	return err
}

func (r *PostgresDMCARepository) FindByID(ctx context.Context, idStr string) (*models.DMCANotice, error) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, models.ErrNotFound
	}
	var n models.DMCANotice
	var idBytes [16]byte
	err = r.pool.QueryRow(ctx, `SELECT id, cid, reporter_email, complaint_text, quarantined_at, status
		FROM dmca_notices WHERE id = $1`, id).Scan(&idBytes, &n.CID, &n.ReporterEmail, &n.ComplaintText, &n.QuarantinedAt, &n.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	n.ID = uuid.UUID(idBytes).String()
	return &n, nil
}

func (r *PostgresDMCARepository) FindByCID(ctx context.Context, cid string) ([]*models.DMCANotice, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, cid, reporter_email, complaint_text, quarantined_at, status
		FROM dmca_notices WHERE cid = $1 ORDER BY quarantined_at DESC`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDMCANotices(rows)
}

func (r *PostgresDMCARepository) List(ctx context.Context) ([]*models.DMCANotice, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, cid, reporter_email, complaint_text, quarantined_at, status
		FROM dmca_notices ORDER BY quarantined_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDMCANotices(rows)
}

func (r *PostgresDMCARepository) UpdateStatus(ctx context.Context, idStr string, status string) error {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE dmca_notices SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func scanDMCANotices(rows pgx.Rows) ([]*models.DMCANotice, error) {
	var out []*models.DMCANotice
	for rows.Next() {
		var n models.DMCANotice
		var idBytes [16]byte
		err := rows.Scan(&idBytes, &n.CID, &n.ReporterEmail, &n.ComplaintText, &n.QuarantinedAt, &n.Status)
		if err != nil {
			return nil, err
		}
		n.ID = uuid.UUID(idBytes).String()
		out = append(out, &n)
	}
	return out, rows.Err()
}
