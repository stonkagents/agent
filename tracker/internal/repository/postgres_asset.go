// Package repository: Postgres implementation of AssetRepository.
// Feature: F-007 (Centralized Tracker). Story: US-007-01 (PostgreSQL Schema and Migrations)

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

// PostgresAssetRepository implements AssetRepository using PostgreSQL.
type PostgresAssetRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresAssetRepository creates a new Postgres asset repository.
func NewPostgresAssetRepository(pool *pgxpool.Pool) *PostgresAssetRepository {
	return &PostgresAssetRepository{pool: pool}
}

func (r *PostgresAssetRepository) Create(ctx context.Context, asset *models.Asset) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO assets (
		cid, filename, mime_type, size, peer_id, announced_at, manifest_type, manifest_data, quarantined, download_count
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		asset.CID, asset.Filename, asset.MimeType, asset.Size, asset.PeerID, asset.AnnouncedAt,
		asset.ManifestType, asset.ManifestData, asset.Quarantined, asset.DownloadCount,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (r *PostgresAssetRepository) FindByCID(ctx context.Context, cid string) (*models.Asset, error) {
	var a models.Asset
	err := r.pool.QueryRow(ctx, `SELECT cid, filename, mime_type, size, peer_id, announced_at, manifest_type, manifest_data, quarantined, download_count
		FROM assets WHERE cid = $1`, cid).Scan(
		&a.CID, &a.Filename, &a.MimeType, &a.Size, &a.PeerID, &a.AnnouncedAt,
		&a.ManifestType, &a.ManifestData, &a.Quarantined, &a.DownloadCount,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

func (r *PostgresAssetRepository) Search(ctx context.Context, opts SearchAssetsOptions) ([]*models.Asset, int, error) {
	// Build query: exclude quarantined; filter by query (filename ILIKE), mime_type, manifest_type, peer_ids; limit/offset
	args := []interface{}{}
	argNum := 1
	where := " quarantined = FALSE"
	if opts.Query != "" {
		where += " AND filename ILIKE '%' || $" + fmt.Sprint(argNum) + " || '%'"
		args = append(args, opts.Query)
		argNum++
	}
	if opts.MimeType != "" {
		where += " AND mime_type = $" + fmt.Sprint(argNum)
		args = append(args, opts.MimeType)
		argNum++
	}
	if opts.ManifestType != "" {
		where += " AND manifest_type = $" + fmt.Sprint(argNum)
		args = append(args, opts.ManifestType)
		argNum++
	}
	if len(opts.PeerIDs) > 0 {
		where += " AND peer_id = ANY($" + fmt.Sprint(argNum) + ")"
		args = append(args, opts.PeerIDs)
		argNum++
	}
	limit, offset := opts.Limit, opts.Offset
	if limit <= 0 {
		limit = 50
	}

	// Count total
	var total int
	countQ := "SELECT COUNT(*) FROM assets WHERE" + where
	err := r.pool.QueryRow(ctx, countQ, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Select page
	args = append(args, limit, offset)
	selQ := "SELECT cid, filename, mime_type, size, peer_id, announced_at, manifest_type, manifest_data, quarantined, download_count FROM assets WHERE" + where + " ORDER BY announced_at DESC LIMIT $" + fmt.Sprint(argNum) + " OFFSET $" + fmt.Sprint(argNum+1)
	rows, err := r.pool.Query(ctx, selQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := scanAssets(rows)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *PostgresAssetRepository) Delete(ctx context.Context, cid string) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM assets WHERE cid = $1`, cid)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresAssetRepository) SetQuarantined(ctx context.Context, cid string, quarantined bool) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE assets SET quarantined = $1 WHERE cid = $2`, quarantined, cid)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresAssetRepository) IncrementDownloadCount(ctx context.Context, cid string) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE assets SET download_count = download_count + 1 WHERE cid = $1`, cid)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresAssetRepository) RecordDownloadEvent(ctx context.Context, cid string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO asset_download_events (cid, completed_at) VALUES ($1, NOW())`, cid)
	return err
}

func (r *PostgresAssetRepository) ListTrendingSince(ctx context.Context, since time.Time, limit, offset int) ([]TrendingRow, int, error) {
	if limit <= 0 {
		limit = 10
	}
	// Total: distinct non-quarantined assets that have at least one event in the window
	var total int
	err := r.pool.QueryRow(ctx, `
		WITH window_cids AS (
			SELECT DISTINCT cid FROM asset_download_events WHERE completed_at >= $1
		)
		SELECT COUNT(*) FROM assets a
		INNER JOIN window_cids w ON a.cid = w.cid
		WHERE a.quarantined = FALSE
	`, since).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx, `
		WITH counts AS (
			SELECT cid, COUNT(*) AS cnt
			FROM asset_download_events
			WHERE completed_at >= $1
			GROUP BY cid
		)
		SELECT a.cid, a.filename, a.mime_type, a.size, a.peer_id, a.announced_at, a.manifest_type, a.manifest_data, a.quarantined, a.download_count, COALESCE(c.cnt, 0)::int AS count_in_window
		FROM assets a
		INNER JOIN counts c ON a.cid = c.cid
		WHERE a.quarantined = FALSE
		ORDER BY c.cnt DESC, a.announced_at DESC
		LIMIT $2 OFFSET $3
	`, since, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []TrendingRow
	for rows.Next() {
		var a models.Asset
		var countInWindow int
		err := rows.Scan(&a.CID, &a.Filename, &a.MimeType, &a.Size, &a.PeerID, &a.AnnouncedAt,
			&a.ManifestType, &a.ManifestData, &a.Quarantined, &a.DownloadCount, &countInWindow)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, TrendingRow{Asset: &a, CountInWindow: countInWindow})
	}
	return list, total, rows.Err()
}

func (r *PostgresAssetRepository) ListTrending(ctx context.Context, limit, offset int) ([]*models.Asset, int, error) {
	if limit <= 0 {
		limit = 10
	}
	var total int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM assets WHERE quarantined = FALSE`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx, `SELECT cid, filename, mime_type, size, peer_id, announced_at, manifest_type, manifest_data, quarantined, download_count
		FROM assets WHERE quarantined = FALSE ORDER BY download_count DESC, announced_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := scanAssets(rows)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *PostgresAssetRepository) Count(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM assets WHERE quarantined = FALSE`).Scan(&n)
	return n, err
}

func (r *PostgresAssetRepository) CountByPeerID(ctx context.Context, peerID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM assets WHERE peer_id = $1 AND quarantined = FALSE`, peerID).Scan(&n)
	return n, err
}

// CountByPeerIDs returns non-quarantined asset counts for each peer ID. Peers with 0 assets are omitted.
// F-032, US-032-01: batch query for enriched peer list.
func (r *PostgresAssetRepository) CountByPeerIDs(ctx context.Context, peerIDs []string) (map[string]int, error) {
	if len(peerIDs) == 0 {
		return map[string]int{}, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT peer_id, COUNT(*) FROM assets WHERE peer_id = ANY($1) AND quarantined = FALSE GROUP BY peer_id`, peerIDs)
	if err != nil {
		return nil, fmt.Errorf("count by peer ids: %w", err)
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		result[id] = count
	}
	return result, rows.Err()
}

// PeerIDsWithAssets returns peer IDs that have at least one non-quarantined asset.
func (r *PostgresAssetRepository) PeerIDsWithAssets(ctx context.Context, peerIDs []string) ([]string, error) {
	if len(peerIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT peer_id FROM assets WHERE peer_id = ANY($1) AND quarantined = FALSE`, peerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RecentAnnouncements returns assets ordered by announced_at DESC.
func (r *PostgresAssetRepository) RecentAnnouncements(ctx context.Context, limit int) ([]*models.Asset, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx,
		`SELECT cid, filename, mime_type, size, peer_id, announced_at, manifest_type, manifest_data, quarantined, download_count
		FROM assets WHERE quarantined = FALSE ORDER BY announced_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// RecentDownloadEvents returns recent download events with asset filename.
func (r *PostgresAssetRepository) RecentDownloadEvents(ctx context.Context, limit int) ([]DownloadEventRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx,
		`SELECT e.cid, a.filename, e.completed_at
		FROM asset_download_events e
		JOIN assets a ON e.cid = a.cid
		WHERE a.quarantined = FALSE
		ORDER BY e.completed_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadEventRecord
	for rows.Next() {
		var d DownloadEventRecord
		if err := rows.Scan(&d.CID, &d.Filename, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanAssets(rows pgx.Rows) ([]*models.Asset, error) {
	var out []*models.Asset
	for rows.Next() {
		var a models.Asset
		err := rows.Scan(&a.CID, &a.Filename, &a.MimeType, &a.Size, &a.PeerID, &a.AnnouncedAt,
			&a.ManifestType, &a.ManifestData, &a.Quarantined, &a.DownloadCount)
		if err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// TopByDownloads returns the top N non-quarantined assets for a peer, ordered
// by download_count DESC. Deterministic tie-breaking: announced_at DESC, cid ASC.
//
// Security note (Gate 0 review): Uses NULL AS manifest_data to avoid transferring
// potentially large JSONB blobs from Postgres. The caller (buildTopDrops) only
// needs filename, file_type, download_count, size_bytes. scanAssets still works
// because it scans the same 10 positional columns — manifest_data simply gets nil.
func (r *PostgresAssetRepository) TopByDownloads(ctx context.Context, peerID string, limit int) ([]*models.Asset, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT cid, filename, mime_type, size, peer_id, announced_at, manifest_type, NULL AS manifest_data, quarantined, download_count
		 FROM assets WHERE peer_id = $1 AND quarantined = false
		 ORDER BY download_count DESC, announced_at DESC, cid ASC
		 LIMIT $2`, peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("top by downloads: %w", err)
	}
	defer rows.Close()
	return scanAssets(rows)
}
