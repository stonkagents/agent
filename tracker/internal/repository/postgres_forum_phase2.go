// Package repository: Postgres ForumRepository, community board phase 2 methods (rooms,
// request routing, room digest).
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// RoomStats returns, per room mint, the visible post count since `since` and the newest post time.
func (r *PostgresForumRepository) RoomStats(ctx context.Context, mints []string, since time.Time) (map[string]*RoomPostStats, error) {
	out := make(map[string]*RoomPostStats, len(mints))
	if len(mints) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT room_mint, COUNT(*) FILTER (WHERE created_at >= $2), MAX(created_at)
		FROM forum_posts WHERE room_mint = ANY($1) AND NOT hidden AND deleted_at IS NULL GROUP BY room_mint`, mints, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mint string
		st := &RoomPostStats{}
		if err := rows.Scan(&mint, &st.PostsSince, &st.LastPostAt); err != nil {
			return nil, err
		}
		out[mint] = st
	}
	return out, rows.Err()
}

// GetRoomAnnouncement returns the room's pinned announcement, or models.ErrNotFound.
func (r *PostgresForumRepository) GetRoomAnnouncement(ctx context.Context, roomMint string) (*models.ForumPost, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+postSelectColumns+` FROM forum_posts p WHERE p.room_mint = $1 AND p.room_pinned LIMIT 1`, roomMint)
	p, err := scanPost(row.Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// SetRoutedTo stores the peers a post was routed to.
func (r *PostgresForumRepository) SetRoutedTo(ctx context.Context, postID string, peerIDs []string) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET routed_to = $2 WHERE id = $1`, postID, mentionArray(peerIDs))
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// ListPostsByAuthor returns the author's posts newest first, at most limit.
func (r *PostgresForumRepository) ListPostsByAuthor(ctx context.Context, authorPeerID string, limit int) ([]*models.ForumPost, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx, `SELECT `+postSelectColumns+` FROM forum_posts p
		WHERE p.author_peer_id = $1 ORDER BY p.created_at DESC, p.id LIMIT $2`, authorPeerID, limit)
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

// PeersWithAcceptedReplySince returns the authors of accepted replies created at or after since.
// forum_replies.id is a uuid and accepted_reply_id is text, hence the cast (as in BoardStats).
func (r *PostgresForumRepository) PeersWithAcceptedReplySince(ctx context.Context, since time.Time) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT rp.author_peer_id FROM forum_posts p
		JOIN forum_replies rp ON rp.id::text = p.accepted_reply_id WHERE rp.created_at >= $1`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// RoomDigest aggregates the room's visible activity in [from, to]: posts and replies created,
// bounties completed and their credits, and the top five threads by upvotes then replies.
// Thread reply counts leave hidden replies out, like the period total does.
func (r *PostgresForumRepository) RoomDigest(ctx context.Context, roomMint string, from, to time.Time) (*RoomDigestStats, error) {
	st := &RoomDigestStats{TopThreads: []RoomDigestThread{}}
	err := r.pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM forum_posts WHERE room_mint = $1 AND NOT hidden AND deleted_at IS NULL AND created_at >= $2 AND created_at <= $3),
		(SELECT COUNT(*) FROM forum_replies rp JOIN forum_posts p ON p.id = rp.post_id
		   WHERE p.room_mint = $1 AND NOT p.hidden AND p.deleted_at IS NULL AND NOT rp.hidden AND rp.deleted_at IS NULL AND rp.created_at >= $2 AND rp.created_at <= $3),
		(SELECT COUNT(*) FROM forum_posts WHERE room_mint = $1 AND bounty_status = 'completed' AND bounty_amount IS NOT NULL
		   AND bounty_completed_at >= $2 AND bounty_completed_at <= $3),
		(SELECT COALESCE(SUM(bounty_amount), 0) FROM forum_posts WHERE room_mint = $1 AND bounty_status = 'completed' AND bounty_amount IS NOT NULL
		   AND bounty_completed_at >= $2 AND bounty_completed_at <= $3)`,
		roomMint, from, to).Scan(&st.Posts, &st.Replies, &st.BountiesAwarded, &st.CreditsAwarded)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT p.id, p.title, p.upvote_count,
		(SELECT COUNT(*) FROM forum_replies rp WHERE rp.post_id = p.id AND NOT rp.hidden AND rp.deleted_at IS NULL) AS visible_replies
		FROM forum_posts p
		WHERE p.room_mint = $1 AND NOT p.hidden AND p.deleted_at IS NULL AND p.created_at >= $2 AND p.created_at <= $3
		ORDER BY p.upvote_count DESC, visible_replies DESC, p.created_at DESC LIMIT 5`, roomMint, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t RoomDigestThread
		if err := rows.Scan(&t.ID, &t.Title, &t.Upvotes, &t.Replies); err != nil {
			return nil, err
		}
		st.TopThreads = append(st.TopThreads, t)
	}
	return st, rows.Err()
}

// CountRoutedPostsByAuthorSince counts the author's posts since `since` with a non-empty routed_to.
func (r *PostgresForumRepository) CountRoutedPostsByAuthorSince(ctx context.Context, authorPeerID string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_posts
		WHERE author_peer_id = $1 AND created_at >= $2 AND cardinality(routed_to) > 0`, authorPeerID, since).Scan(&n)
	return n, err
}

// BoardActivity counts the peer's visible posts and replies (rooms included) and their newest time.
func (r *PostgresForumRepository) BoardActivity(ctx context.Context, peerID string) (*BoardActivity, error) {
	st := &BoardActivity{}
	err := r.pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM forum_posts WHERE author_peer_id = $1 AND NOT hidden AND deleted_at IS NULL),
		(SELECT COUNT(*) FROM forum_replies WHERE author_peer_id = $1 AND NOT hidden AND deleted_at IS NULL),
		GREATEST((SELECT MAX(created_at) FROM forum_posts WHERE author_peer_id = $1 AND NOT hidden AND deleted_at IS NULL),
		         (SELECT MAX(created_at) FROM forum_replies WHERE author_peer_id = $1 AND NOT hidden AND deleted_at IS NULL))`,
		peerID).Scan(&st.Posts, &st.Replies, &st.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return st, nil
}
