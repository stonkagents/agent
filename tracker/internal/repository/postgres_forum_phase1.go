// Package repository: Postgres ForumRepository, community board phase 1 methods (accepted
// answer, hide and pin, token offer paid count, reputation counters).
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// GetReplyByID returns one reply, or models.ErrNotFound.
func (r *PostgresForumRepository) GetReplyByID(ctx context.Context, replyID string) (*models.ForumReply, error) {
	if !isUUID(replyID) {
		return nil, models.ErrNotFound
	}
	row := r.pool.QueryRow(ctx, `SELECT `+replySelectColumns+` FROM forum_replies WHERE id = $1`, replyID)
	rp, err := scanReply(row.Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return rp, nil
}

// SetAcceptedReply records the accepted answer (nil clears it).
func (r *PostgresForumRepository) SetAcceptedReply(ctx context.Context, postID string, replyID *string) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET accepted_reply_id = $2 WHERE id = $1`, postID, replyID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// SetPostHidden flips a post's hidden flag.
func (r *PostgresForumRepository) SetPostHidden(ctx context.Context, postID string, hidden bool) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET hidden = $2 WHERE id = $1`, postID, hidden)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// SetReplyHidden flips a reply's hidden flag.
func (r *PostgresForumRepository) SetReplyHidden(ctx context.Context, replyID string, hidden bool) error {
	if !isUUID(replyID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_replies SET hidden = $2 WHERE id = $1`, replyID, hidden)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// PinPost pins postID and unpins every other post in one transaction.
func (r *PostgresForumRepository) PinPost(ctx context.Context, postID string) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE forum_posts SET pinned = FALSE WHERE pinned AND id <> $1`, postID); err != nil {
		return err
	}
	cmd, err := tx.Exec(ctx, `UPDATE forum_posts SET pinned = TRUE WHERE id = $1`, postID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return tx.Commit(ctx)
}

// UnpinPost clears the pinned flag.
func (r *PostgresForumRepository) UnpinPost(ctx context.Context, postID string) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE forum_posts SET pinned = FALSE WHERE id = $1`, postID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// IncrementTokenOfferPaid bumps token_offer_paid in one conditional UPDATE (the row lock makes
// concurrent payments serialise), so paid never exceeds token_offer_max: models.ErrInvalidInput
// when the offer is exhausted, ErrNotFound when the post is gone.
func (r *PostgresForumRepository) IncrementTokenOfferPaid(ctx context.Context, postID string) (int, error) {
	if !isUUID(postID) {
		return 0, models.ErrNotFound
	}
	var n int
	err := r.pool.QueryRow(ctx, `UPDATE forum_posts SET token_offer_paid = token_offer_paid + 1
		WHERE id = $1 AND (token_offer_max IS NULL OR token_offer_paid < token_offer_max)
		RETURNING token_offer_paid`, postID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists int
			if e := r.pool.QueryRow(ctx, `SELECT 1 FROM forum_posts WHERE id = $1`, postID).Scan(&exists); e != nil {
				return 0, models.ErrNotFound
			}
			return 0, models.ErrInvalidInput
		}
		return 0, err
	}
	return n, nil
}

// DecrementTokenOfferPaid gives a reserved slot back (never below zero).
func (r *PostgresForumRepository) DecrementTokenOfferPaid(ctx context.Context, postID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE forum_posts SET token_offer_paid = GREATEST(0, token_offer_paid - 1) WHERE id = $1`, postID)
	return err
}

// BoardStats derives the reputation counters for peerID from the forum tables. Under a guard (round 2), an upvote or an accepted answer only counts
// when the voter or accepting author is older than guard.VoterSince, has posted or replied at
// least once, and shares no linked wallet with the beneficiary.
func (r *PostgresForumRepository) BoardStats(ctx context.Context, peerID string, guard BoardStatsGuard) (*BoardStats, error) {
	st := &BoardStats{}
	// qualified(voter) is true when the guard is off or the voter passes it: old enough, active
	// (a post or a reply), and not linked to the same wallet as the beneficiary ($1).
	const qualified = `($2::timestamptz IS NULL OR (
		EXISTS (SELECT 1 FROM peers pr WHERE pr.peer_id = VOTER AND pr.first_seen < $2)
		AND (EXISTS (SELECT 1 FROM forum_posts vp WHERE vp.author_peer_id = VOTER) OR EXISTS (SELECT 1 FROM forum_replies vr WHERE vr.author_peer_id = VOTER))
		AND NOT EXISTS (SELECT 1 FROM accounts a1 JOIN account_wallets w1 ON w1.account_id = a1.id
		                JOIN accounts a2 ON a2.peer_id = $1 JOIN account_wallets w2 ON w2.account_id = a2.id AND w2.wallet_address = w1.wallet_address
		                WHERE a1.peer_id = VOTER)))`
	upvoteOK := strings.ReplaceAll(qualified, "VOTER", "u.peer_id")
	acceptOK := strings.ReplaceAll(qualified, "VOTER", "p.author_peer_id")
	var voterSince *time.Time
	if !guard.VoterSince.IsZero() {
		t := guard.VoterSince
		voterSince = &t
	}
	err := r.pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM forum_posts WHERE bounty_status = 'completed' AND bounty_claimed_by = $1 AND bounty_amount IS NOT NULL),
		(SELECT COALESCE(SUM(bounty_amount), 0) FROM forum_posts WHERE bounty_status = 'completed' AND bounty_claimed_by = $1 AND bounty_amount IS NOT NULL),
		(SELECT COUNT(*) FROM forum_posts p JOIN forum_replies rp ON rp.id::text = p.accepted_reply_id WHERE rp.author_peer_id = $1 AND `+acceptOK+`),
		(SELECT COUNT(*) FROM forum_upvotes u JOIN forum_posts p ON p.id = u.post_id WHERE p.author_peer_id = $1 AND `+upvoteOK+`),
		(SELECT COUNT(*) FROM forum_posts p
		   JOIN LATERAL (SELECT author_peer_id, created_at FROM forum_replies WHERE post_id = p.id AND NOT auto ORDER BY created_at ASC, id ASC LIMIT 1) fr ON TRUE
		 WHERE p.author_peer_id <> $1 AND fr.author_peer_id = $1 AND fr.created_at <= p.created_at + INTERVAL '1 hour')`,
		peerID, voterSince).Scan(&st.BountiesWon, &st.CreditsWon, &st.AnswersAccepted, &st.UpvotesReceived, &st.FirstReplies1h)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// BoardPeerIDs returns every peer that authored a post or a reply.
func (r *PostgresForumRepository) BoardPeerIDs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT author_peer_id FROM forum_posts UNION SELECT author_peer_id FROM forum_replies ORDER BY 1`)
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
		if id != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}
