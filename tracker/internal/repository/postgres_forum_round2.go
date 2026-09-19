// Package repository: Postgres ForumRepository, community board round 2 methods: author edits
// and soft deletes, duplicate detection by body hash, the upvote and per-room autopilot caps,
// routing reasons, bounty disputes and the per-scope post counts behind unread rooms.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// UpdatePostBody replaces the title and body (search_tsv follows, it is generated) and stamps
// the edit; ErrNotFound when the post is missing or deleted.
func (r *PostgresForumRepository) UpdatePostBody(ctx context.Context, postID, title, body, bodyHash string, at time.Time) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET title = $2, description = $3, body_hash = $4, edited_at = $5,
		edit_count = edit_count + 1, updated_at = $5 WHERE id = $1 AND deleted_at IS NULL`, postID, title, body, bodyHash, at)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// UpdateReplyBody replaces a reply's body and stamps the edit; ErrNotFound when missing or deleted.
func (r *PostgresForumRepository) UpdateReplyBody(ctx context.Context, replyID, body, bodyHash string, at time.Time) error {
	if !isUUID(replyID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_replies SET body = $2, body_hash = $3, edited_at = $4, edit_count = edit_count + 1
		WHERE id = $1 AND deleted_at IS NULL`, replyID, body, bodyHash, at)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// SoftDeletePost sets deleted_at once: ErrNotFound when missing, ErrInvalidInput when already deleted.
func (r *PostgresForumRepository) SoftDeletePost(ctx context.Context, postID string, at time.Time) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	return r.softDelete(ctx, `forum_posts`, postID, at)
}

// SoftDeleteReply sets deleted_at once on a reply and keeps the post's reply_count (the
// tombstone still counts as a row of the thread).
func (r *PostgresForumRepository) SoftDeleteReply(ctx context.Context, replyID string, at time.Time) error {
	if !isUUID(replyID) {
		return models.ErrNotFound
	}
	return r.softDelete(ctx, `forum_replies`, replyID, at)
}

func (r *PostgresForumRepository) softDelete(ctx context.Context, table, id string, at time.Time) error {
	var deleted *time.Time
	err := r.pool.QueryRow(ctx, `SELECT deleted_at FROM `+table+` WHERE id = $1`, id).Scan(&deleted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.ErrNotFound
		}
		return err
	}
	if deleted != nil {
		return models.ErrInvalidInput
	}
	_, err = r.pool.Exec(ctx, `UPDATE `+table+` SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`, id, at)
	return err
}

// FindRecentPostByBodyHash returns the author's newest live post with that body hash created at
// or after since, or ErrNotFound.
func (r *PostgresForumRepository) FindRecentPostByBodyHash(ctx context.Context, authorPeerID, bodyHash string, since time.Time) (string, error) {
	if bodyHash == "" {
		return "", models.ErrNotFound
	}
	var id string
	err := r.pool.QueryRow(ctx, `SELECT id FROM forum_posts WHERE author_peer_id = $1 AND body_hash = $2 AND created_at >= $3 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, authorPeerID, bodyHash, since).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", models.ErrNotFound
		}
		return "", err
	}
	return id, nil
}

// FindRecentReplyByBodyHash returns the author's newest live reply on the post with that body
// hash created at or after since, or ErrNotFound.
func (r *PostgresForumRepository) FindRecentReplyByBodyHash(ctx context.Context, postID, authorPeerID, bodyHash string, since time.Time) (string, error) {
	if bodyHash == "" || !isUUID(postID) {
		return "", models.ErrNotFound
	}
	var id string
	err := r.pool.QueryRow(ctx, `SELECT id FROM forum_replies WHERE post_id = $1 AND author_peer_id = $2 AND body_hash = $3 AND created_at >= $4 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, postID, authorPeerID, bodyHash, since).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", models.ErrNotFound
		}
		return "", err
	}
	return id, nil
}

// CountUpvotesByPeerSince counts the upvotes the peer gave at or after since.
func (r *PostgresForumRepository) CountUpvotesByPeerSince(ctx context.Context, peerID string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_upvotes WHERE peer_id = $1 AND created_at >= $2`, peerID, since).Scan(&n)
	return n, err
}

// CountAutoRepliesByPeerInRoomSince counts the peer's auto replies on the room's posts created
// at or after since.
func (r *PostgresForumRepository) CountAutoRepliesByPeerInRoomSince(ctx context.Context, peerID, roomMint string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_replies rp JOIN forum_posts p ON p.id = rp.post_id
		WHERE rp.author_peer_id = $1 AND rp.auto AND rp.created_at >= $2 AND p.room_mint = $3`, peerID, since, roomMint).Scan(&n)
	return n, err
}

// SetRoutedReasons stores why each routed peer was picked (JSONB).
func (r *PostgresForumRepository) SetRoutedReasons(ctx context.Context, postID string, reasons map[string][]string) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	b, err := json.Marshal(reasons)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `UPDATE forum_posts SET routed_reasons = $2 WHERE id = $1`, postID, b)
	return err
}

// OpenBountyDispute records a dispute on a completed or expired bounty with no open dispute
// (one conditional update; ErrInvalidInput when the row does not qualify).
func (r *PostgresForumRepository) OpenBountyDispute(ctx context.Context, postID, byPeerID, note string, at time.Time) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET bounty_dispute_status = $5, bounty_dispute_by = $2, bounty_dispute_note = $3,
		bounty_disputed_at = $4, bounty_dispute_resolved_at = NULL
		WHERE id = $1 AND bounty_amount IS NOT NULL AND bounty_status IN ('completed', 'expired') AND bounty_dispute_status <> $5`,
		postID, byPeerID, note, at, models.BountyDisputeOpen)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrInvalidInput
	}
	return nil
}

// ResolveBountyDispute closes the open dispute (ErrInvalidInput when there is none).
func (r *PostgresForumRepository) ResolveBountyDispute(ctx context.Context, postID, status string, at time.Time) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET bounty_dispute_status = $2, bounty_dispute_resolved_at = $3
		WHERE id = $1 AND bounty_dispute_status = $4`, postID, status, at, models.BountyDisputeOpen)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrInvalidInput
	}
	return nil
}

// ListDisputedBounties returns the posts with an open dispute, oldest dispute first.
func (r *PostgresForumRepository) ListDisputedBounties(ctx context.Context, limit int) ([]*models.ForumPost, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+postSelectColumns+` FROM forum_posts p WHERE p.bounty_dispute_status = $1
		ORDER BY p.bounty_disputed_at ASC LIMIT $2`, models.BountyDisputeOpen, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.ForumPost
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPostsSince counts the visible posts of the scope created after since (exclusive).
func (r *PostgresForumRepository) CountPostsSince(ctx context.Context, scope string, since time.Time) (int, error) {
	var n int
	var err error
	if scope == "" {
		err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_posts WHERE room_mint IS NULL AND NOT hidden AND deleted_at IS NULL AND created_at > $1`, since).Scan(&n)
	} else {
		err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_posts WHERE room_mint = $2 AND NOT hidden AND deleted_at IS NULL AND created_at > $1`, since, scope).Scan(&n)
	}
	return n, err
}
