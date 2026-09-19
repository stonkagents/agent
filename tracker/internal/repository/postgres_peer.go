// Package repository: Postgres implementation of PeerRepository.
// Feature: F-007 (Centralized Tracker). Story: US-007-01 (PostgreSQL Schema and Migrations)

package repository

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// roundCoord rounds a coordinate to 2 decimal places (~1.1km precision) for privacy.
func roundCoord(v float64) float64 {
	return math.Round(v*100) / 100
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// peerSelectCols is the column list for peer queries; wallet comes from account_wallets via subquery.
// country, region and masked_peer_id are COALESCEd: they were nullable before migration 029
// and a NULL there made scanPeers fail for the whole batch (auto hide silently off whenever
// one reporter had such a row); every other scanned column is NOT NULL or a pointer.
const peerSelectCols = `p.peer_id, p.ed25519_pubkey, p.multiaddrs, p.first_seen, p.last_seen, p.total_uptime_seconds,
	COALESCE(p.country, ''), COALESCE(p.region, ''), COALESCE(p.masked_peer_id, ''), p.total_upload_bytes, p.total_download_bytes, p.average_speed_bytes_per_sec,
	COALESCE((SELECT aw.wallet_address FROM account_wallets aw JOIN accounts a ON aw.account_id = a.id WHERE a.peer_id = p.peer_id AND aw.chain = 'solana' LIMIT 1), ''),
	p.current_session_start, p.display_name, p.city, p.latitude, p.longitude`

// PostgresPeerRepository implements PeerRepository using PostgreSQL.
type PostgresPeerRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresPeerRepository creates a new Postgres peer repository.
func NewPostgresPeerRepository(pool *pgxpool.Pool) *PostgresPeerRepository {
	return &PostgresPeerRepository{pool: pool}
}

func (r *PostgresPeerRepository) Create(ctx context.Context, peer *models.Peer) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO peers (
		peer_id, ed25519_pubkey, multiaddrs, first_seen, last_seen, total_uptime_seconds,
		country, region, masked_peer_id, total_upload_bytes, total_download_bytes, average_speed_bytes_per_sec,
		display_name, city, latitude, longitude
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		peer.PeerID, peer.PublicKey, peer.Multiaddrs, peer.FirstSeen, peer.LastSeen, peer.TotalUptimeSeconds,
		peer.Country, peer.Region, peer.MaskedPeerID, peer.TotalUploadBytes, peer.TotalDownloadBytes, peer.AverageSpeedBytesPerSec,
		peer.DisplayName, peer.City, roundCoord(peer.Lat), roundCoord(peer.Lng),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (r *PostgresPeerRepository) Upsert(ctx context.Context, peer *models.Peer) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO peers (
		peer_id, ed25519_pubkey, multiaddrs, first_seen, last_seen, total_uptime_seconds,
		country, region, masked_peer_id, total_upload_bytes, total_download_bytes, average_speed_bytes_per_sec,
		display_name, city, latitude, longitude
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (peer_id) DO UPDATE SET
		ed25519_pubkey = EXCLUDED.ed25519_pubkey,
		multiaddrs = EXCLUDED.multiaddrs,
		last_seen = EXCLUDED.last_seen,
		total_uptime_seconds = EXCLUDED.total_uptime_seconds,
		country = COALESCE(EXCLUDED.country, peers.country),
		region = COALESCE(EXCLUDED.region, peers.region),
		masked_peer_id = COALESCE(EXCLUDED.masked_peer_id, peers.masked_peer_id),
		total_upload_bytes = COALESCE(peers.total_upload_bytes, 0),
		total_download_bytes = COALESCE(peers.total_download_bytes, 0),
		average_speed_bytes_per_sec = EXCLUDED.average_speed_bytes_per_sec,
		display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), peers.display_name),
		city = COALESCE(NULLIF(EXCLUDED.city, ''), peers.city),
		latitude = CASE WHEN EXCLUDED.latitude != 0 THEN EXCLUDED.latitude ELSE peers.latitude END,
		longitude = CASE WHEN EXCLUDED.longitude != 0 THEN EXCLUDED.longitude ELSE peers.longitude END`,
		peer.PeerID, peer.PublicKey, peer.Multiaddrs, peer.FirstSeen, peer.LastSeen, peer.TotalUptimeSeconds,
		peer.Country, peer.Region, peer.MaskedPeerID, peer.TotalUploadBytes, peer.TotalDownloadBytes, peer.AverageSpeedBytesPerSec,
		peer.DisplayName, peer.City, roundCoord(peer.Lat), roundCoord(peer.Lng),
	)
	return err
}

func (r *PostgresPeerRepository) FindByID(ctx context.Context, peerID string) (*models.Peer, error) {
	var p models.Peer
	err := r.pool.QueryRow(ctx, `SELECT `+peerSelectCols+` FROM peers p WHERE p.peer_id = $1`, peerID).Scan(
		&p.PeerID, &p.PublicKey, &p.Multiaddrs, &p.FirstSeen, &p.LastSeen, &p.TotalUptimeSeconds,
		&p.Country, &p.Region, &p.MaskedPeerID, &p.TotalUploadBytes, &p.TotalDownloadBytes, &p.AverageSpeedBytesPerSec, &p.WalletAddress, &p.CurrentSessionStart,
		&p.DisplayName, &p.City, &p.Lat, &p.Lng,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *PostgresPeerRepository) List(ctx context.Context, opts ListPeersOptions) ([]*models.Peer, error) {
	limit, offset := opts.Limit, opts.Offset
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p ORDER BY p.peer_id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

func (r *PostgresPeerRepository) FindByIDs(ctx context.Context, peerIDs []string) ([]*models.Peer, error) {
	if len(peerIDs) == 0 {
		return []*models.Peer{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p WHERE p.peer_id = ANY($1)`, peerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

func (r *PostgresPeerRepository) Delete(ctx context.Context, peerID string) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM peers WHERE peer_id = $1`, peerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresPeerRepository) UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64, avgSpeed *int64) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE peers SET total_upload_bytes = $1, total_download_bytes = $2, average_speed_bytes_per_sec = $3 WHERE peer_id = $4`,
		uploadBytes, downloadBytes, avgSpeed, peerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresPeerRepository) DisplayNamesByIDs(ctx context.Context, peerIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(peerIDs))
	if len(peerIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT peer_id, display_name FROM peers WHERE peer_id = ANY($1) AND display_name != ''`, peerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// PeerIDsByDisplayNames returns lowercased display name -> peer_id for the given names in one
// query (case-insensitive match; the smallest peer_id wins a duplicate name).
func (r *PostgresPeerRepository) PeerIDsByDisplayNames(ctx context.Context, lowerNames []string) (map[string]string, error) {
	out := make(map[string]string, len(lowerNames))
	if len(lowerNames) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT peer_id, lower(display_name) FROM peers
		WHERE display_name != '' AND lower(display_name) = ANY($1) ORDER BY peer_id`, lowerNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, lower string
		if err := rows.Scan(&id, &lower); err != nil {
			return nil, err
		}
		if _, dup := out[lower]; !dup {
			out[lower] = id
		}
	}
	return out, rows.Err()
}

// SearchDisplayNames returns peers whose display name starts with prefix (case-insensitive),
// shortest name first then alphabetical, at most limit.
func (r *PostgresPeerRepository) SearchDisplayNames(ctx context.Context, prefix string, limit int) ([]DisplayNameMatch, error) {
	if limit <= 0 {
		limit = 8
	}
	pattern := escapeLike(prefix) + "%"
	rows, err := r.pool.Query(ctx, `SELECT peer_id, display_name FROM peers
		WHERE display_name != '' AND display_name ILIKE $1
		ORDER BY length(display_name), lower(display_name), peer_id LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DisplayNameMatch{}
	for rows.Next() {
		var m DisplayNameMatch
		if err := rows.Scan(&m.PeerID, &m.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// escapeLike escapes the LIKE metacharacters in s so it matches literally.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

func (r *PostgresPeerRepository) UpdateDisplayName(ctx context.Context, peerID, displayName string) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE peers SET display_name = $1 WHERE peer_id = $2`, displayName, peerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (r *PostgresPeerRepository) ListPeersByUploadBytes(ctx context.Context, limit, offset int, hasAssets bool) ([]*models.Peer, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows pgx.Rows
	var err error
	if hasAssets {
		rows, err = r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p WHERE EXISTS (SELECT 1 FROM assets a WHERE a.peer_id = p.peer_id AND NOT a.quarantined)
			ORDER BY p.total_upload_bytes DESC NULLS LAST LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p ORDER BY p.total_upload_bytes DESC NULLS LAST LIMIT $1 OFFSET $2`, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

func (r *PostgresPeerRepository) ListPeersByDownloadBytes(ctx context.Context, limit, offset int, hasDownloads bool) ([]*models.Peer, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows pgx.Rows
	var err error
	if hasDownloads {
		rows, err = r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p WHERE p.total_download_bytes > 0 ORDER BY p.total_download_bytes DESC LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p ORDER BY p.total_download_bytes DESC NULLS LAST LIMIT $1 OFFSET $2`, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

func (r *PostgresPeerRepository) Count(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM peers`).Scan(&n)
	return n, err
}

// CountOnlineByCountry returns online peer counts by country (last_seen > since). Excludes empty/NULL country.
func (r *PostgresPeerRepository) CountOnlineByCountry(ctx context.Context, since time.Time) ([]CountryCount, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT country, COUNT(*) FROM peers WHERE last_seen > $1 AND country IS NOT NULL AND TRIM(country) != '' GROUP BY country ORDER BY count DESC`,
		since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CountryCount
	for rows.Next() {
		var c CountryCount
		if err := rows.Scan(&c.Country, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecentlyJoined returns peers ordered by first_seen DESC (newest first).
func (r *PostgresPeerRepository) RecentlyJoined(ctx context.Context, limit int) ([]*models.Peer, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `SELECT `+peerSelectCols+` FROM peers p ORDER BY p.first_seen DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

func scanPeers(rows pgx.Rows) ([]*models.Peer, error) {
	var out []*models.Peer
	for rows.Next() {
		var p models.Peer
		err := rows.Scan(&p.PeerID, &p.PublicKey, &p.Multiaddrs, &p.FirstSeen, &p.LastSeen, &p.TotalUptimeSeconds,
			&p.Country, &p.Region, &p.MaskedPeerID, &p.TotalUploadBytes, &p.TotalDownloadBytes, &p.AverageSpeedBytesPerSec, &p.WalletAddress, &p.CurrentSessionStart,
			&p.DisplayName, &p.City, &p.Lat, &p.Lng)
		if err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// PeerIDsSeenSince returns the peers with last_seen at or after since, most recent first.
func (r *PostgresPeerRepository) PeerIDsSeenSince(ctx context.Context, since time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `SELECT peer_id FROM peers WHERE last_seen >= $1 ORDER BY last_seen DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
