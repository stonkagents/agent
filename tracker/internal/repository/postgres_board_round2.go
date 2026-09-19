// Package repository: Postgres implementations of the community board round 2 repositories:
// edit history, room controls (settings and mutes), feed visits and notification preferences
// (migration 030).
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// --- Edit history ---

// PostgresBoardEditHistoryRepository implements BoardEditHistoryRepository (table board_edit_history).
type PostgresBoardEditHistoryRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardEditHistoryRepository creates the repository.
func NewPostgresBoardEditHistoryRepository(pool *pgxpool.Pool) *PostgresBoardEditHistoryRepository {
	return &PostgresBoardEditHistoryRepository{pool: pool}
}

// Add inserts one row; a missing ID is generated.
func (r *PostgresBoardEditHistoryRepository) Add(ctx context.Context, e *models.BoardEdit) error {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO board_edit_history (id, target_type, target_id, editor_peer_id, previous_title, previous_body, edited_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.ID, e.TargetType, e.TargetID, e.EditorPeerID, e.PreviousTitle, e.PreviousBody, e.EditedAt)
	return err
}

// List returns the target's edits newest first.
func (r *PostgresBoardEditHistoryRepository) List(ctx context.Context, targetType, targetID string, limit int) ([]*models.BoardEdit, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT id, target_type, target_id, editor_peer_id, previous_title, previous_body, edited_at
		FROM board_edit_history WHERE target_type = $1 AND target_id = $2 ORDER BY edited_at DESC LIMIT $3`, targetType, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.BoardEdit{}
	for rows.Next() {
		var e models.BoardEdit
		if err := rows.Scan(&e.ID, &e.TargetType, &e.TargetID, &e.EditorPeerID, &e.PreviousTitle, &e.PreviousBody, &e.EditedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// --- Room controls ---

// PostgresBoardRoomRepository implements BoardRoomRepository (board_room_settings, board_room_mutes).
type PostgresBoardRoomRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardRoomRepository creates the repository.
func NewPostgresBoardRoomRepository(pool *pgxpool.Pool) *PostgresBoardRoomRepository {
	return &PostgresBoardRoomRepository{pool: pool}
}

// GetSettings returns the room's settings, the defaults when it has no row.
func (r *PostgresBoardRoomRepository) GetSettings(ctx context.Context, mint string) (*models.RoomSettings, error) {
	s := models.DefaultRoomSettings(mint)
	err := r.pool.QueryRow(ctx, `SELECT routing, min_hold_raw, updated_at FROM board_room_settings WHERE mint = $1`, mint).
		Scan(&s.Routing, &s.MinHoldRaw, &s.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return s, nil
}

// SetSettings upserts the room's settings.
func (r *PostgresBoardRoomRepository) SetSettings(ctx context.Context, s *models.RoomSettings) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO board_room_settings (mint, routing, min_hold_raw, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (mint) DO UPDATE SET routing = EXCLUDED.routing, min_hold_raw = EXCLUDED.min_hold_raw, updated_at = EXCLUDED.updated_at`,
		s.Mint, s.Routing, s.MinHoldRaw, s.UpdatedAt)
	return err
}

// SettingsByMints returns the stored rows among mints.
func (r *PostgresBoardRoomRepository) SettingsByMints(ctx context.Context, mints []string) (map[string]*models.RoomSettings, error) {
	out := map[string]*models.RoomSettings{}
	if len(mints) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT mint, routing, min_hold_raw, updated_at FROM board_room_settings WHERE mint = ANY($1)`, mints)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s models.RoomSettings
		if err := rows.Scan(&s.Mint, &s.Routing, &s.MinHoldRaw, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out[s.Mint] = &s
	}
	return out, rows.Err()
}

// Mute upserts a mute.
func (r *PostgresBoardRoomRepository) Mute(ctx context.Context, m *models.RoomMute) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO board_room_mutes (mint, peer_id, by_peer_id, reason, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (mint, peer_id) DO UPDATE SET by_peer_id = EXCLUDED.by_peer_id, reason = EXCLUDED.reason, created_at = EXCLUDED.created_at`,
		m.Mint, m.PeerID, m.ByPeerID, m.Reason, m.CreatedAt)
	return err
}

// Unmute removes a mute (ErrNotFound when there was none).
func (r *PostgresBoardRoomRepository) Unmute(ctx context.Context, mint, peerID string) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM board_room_mutes WHERE mint = $1 AND peer_id = $2`, mint, peerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// IsMuted reports whether the peer is muted in the room.
func (r *PostgresBoardRoomRepository) IsMuted(ctx context.Context, mint, peerID string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_room_mutes WHERE mint = $1 AND peer_id = $2`, mint, peerID).Scan(&n)
	return n > 0, err
}

// ListMutes returns the room's mutes newest first.
func (r *PostgresBoardRoomRepository) ListMutes(ctx context.Context, mint string) ([]*models.RoomMute, error) {
	rows, err := r.pool.Query(ctx, `SELECT mint, peer_id, by_peer_id, reason, created_at FROM board_room_mutes WHERE mint = $1 ORDER BY created_at DESC`, mint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.RoomMute{}
	for rows.Next() {
		var m models.RoomMute
		if err := rows.Scan(&m.Mint, &m.PeerID, &m.ByPeerID, &m.Reason, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// --- Visits ---

// PostgresBoardVisitRepository implements BoardVisitRepository (table board_visits).
type PostgresBoardVisitRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardVisitRepository creates the repository.
func NewPostgresBoardVisitRepository(pool *pgxpool.Pool) *PostgresBoardVisitRepository {
	return &PostgresBoardVisitRepository{pool: pool}
}

// Visit upserts the visit and returns the previous one in the same statement (nil for the first).
func (r *PostgresBoardVisitRepository) Visit(ctx context.Context, peerID, scope string, at time.Time) (*time.Time, error) {
	var prev *time.Time
	err := r.pool.QueryRow(ctx, `WITH prev AS (SELECT visited_at FROM board_visits WHERE peer_id = $1 AND scope = $2),
		up AS (INSERT INTO board_visits (peer_id, scope, visited_at) VALUES ($1, $2, $3)
		       ON CONFLICT (peer_id, scope) DO UPDATE SET visited_at = GREATEST(board_visits.visited_at, EXCLUDED.visited_at))
		SELECT visited_at FROM prev`, peerID, scope, at).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return prev, nil
}

// LastVisits returns scope -> last visit for the scopes the peer has visited.
func (r *PostgresBoardVisitRepository) LastVisits(ctx context.Context, peerID string, scopes []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(scopes) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT scope, visited_at FROM board_visits WHERE peer_id = $1 AND scope = ANY($2)`, peerID, scopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope string
		var at time.Time
		if err := rows.Scan(&scope, &at); err != nil {
			return nil, err
		}
		out[scope] = at
	}
	return out, rows.Err()
}

// --- Notification preferences ---

// PostgresBoardNotificationPrefRepository implements BoardNotificationPrefRepository.
type PostgresBoardNotificationPrefRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardNotificationPrefRepository creates the repository.
func NewPostgresBoardNotificationPrefRepository(pool *pgxpool.Pool) *PostgresBoardNotificationPrefRepository {
	return &PostgresBoardNotificationPrefRepository{pool: pool}
}

// Get returns the peer's preferences (nothing muted without a row).
func (r *PostgresBoardNotificationPrefRepository) Get(ctx context.Context, peerID string) (*models.NotificationPrefs, error) {
	p := &models.NotificationPrefs{PeerID: peerID, MutedKinds: []string{}}
	err := r.pool.QueryRow(ctx, `SELECT muted_kinds, updated_at FROM board_notification_prefs WHERE peer_id = $1`, peerID).Scan(&p.MutedKinds, &p.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if p.MutedKinds == nil {
		p.MutedKinds = []string{}
	}
	return p, nil
}

// Set upserts the peer's preferences.
func (r *PostgresBoardNotificationPrefRepository) Set(ctx context.Context, p *models.NotificationPrefs) error {
	kinds := p.MutedKinds
	if kinds == nil {
		kinds = []string{}
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO board_notification_prefs (peer_id, muted_kinds, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (peer_id) DO UPDATE SET muted_kinds = EXCLUDED.muted_kinds, updated_at = EXCLUDED.updated_at`, p.PeerID, kinds, p.UpdatedAt)
	return err
}
