// Package repository: Postgres implementation of ForumRepository.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresForumRepository implements ForumRepository using PostgreSQL.
type PostgresForumRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresForumRepository creates a new Postgres forum repository.
func NewPostgresForumRepository(pool *pgxpool.Pool) *PostgresForumRepository {
	return &PostgresForumRepository{pool: pool}
}

// isUUID reports whether id can be a forum_posts / forum_replies primary key. Every method
// keyed by such an id checks it first, so a malformed id from a request path reads as "no such
// row" instead of a Postgres 22P02 error (and a 500) on the uuid column.
func isUUID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

// uuidsOnly drops the ids that cannot be primary keys (an ANY($1) on the uuid column would
// otherwise fail on the first bad one).
func uuidsOnly(ids []string) []string {
	out := ids[:0:0]
	for _, id := range ids {
		if isUUID(id) {
			out = append(out, id)
		}
	}
	return out
}

// CreatePost inserts a new forum post (including rich fields from migration 009).
func (r *PostgresForumRepository) CreatePost(ctx context.Context, post *models.ForumPost) error {
	if post.ID == "" {
		post.ID = uuid.New().String()
	}
	bountyStatus := post.BountyStatus
	if bountyStatus == "" && post.HasBounty() {
		bountyStatus = "open"
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO forum_posts
		(id, author_peer_id, title, description, created_at, updated_at, upvote_count, reply_count,
		 category, tags, bounty_amount, bounty_currency, bounty_expires_at,
		 token_offer_amount, token_offer_token, cid, view_count, bounty_status,
		 bounty_escrow_request_id, bounty_escrow_paid,
		 token_offer_mint, token_offer_symbol, token_offer_decimals, token_offer_max, token_offer_paid, mention_peer_ids,
		 room_mint, room_pinned, routed_to, auto, body_hash)
		VALUES ($1, $2, $3, $4, $5, $6, 0, 0,
		        $7, $8, $9, $10, $11, $12, $13, $14, 0, $15, $16, $17,
		        $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)`,
		post.ID, post.AuthorPeerID, post.Title, post.Description, post.CreatedAt, post.UpdatedAt,
		post.Category, post.Tags, post.BountyAmount, post.BountyCurrency, post.BountyExpiresAt,
		post.TokenOfferAmount, post.TokenOfferToken, post.CID, bountyStatus,
		post.BountyEscrowRequestID, post.BountyEscrowPaid,
		post.TokenOfferMint, post.TokenOfferSymbol, post.TokenOfferDecimals, post.TokenOfferMax, post.TokenOfferPaid, mentionArray(post.MentionPeerIDs),
		post.RoomMint, post.RoomPinned, mentionArray(post.RoutedTo), post.Auto, post.BodyHash)
	if err != nil && isUniqueViolation(err) {
		// One pinned announcement per room (idx_forum_posts_room_announcement).
		return models.ErrAlreadyExists
	}
	return err
}

// postSelectColumns is the shared column list for GetPostByID and ListPosts (migration 009 rich fields + 006 bounty lifecycle).
const postSelectColumns = `p.id, p.author_peer_id, p.title, p.description, p.created_at, p.updated_at, p.upvote_count,
	p.reply_count, p.category, p.tags, p.bounty_amount, p.bounty_currency, p.bounty_expires_at,
	p.token_offer_amount, p.token_offer_token, p.cid, p.view_count,
	p.bounty_status, p.bounty_claimed_by, p.bounty_claimed_at, p.bounty_completed_at, p.bounty_escrow_request_id,
	p.bounty_extended, p.bounty_refunded_at, p.bounty_escrow_paid,
	p.accepted_reply_id, p.hidden, p.pinned, p.token_offer_mint, p.token_offer_symbol, p.token_offer_decimals,
	p.token_offer_max, p.token_offer_paid, p.mention_peer_ids, p.room_mint, p.room_pinned, p.routed_to, p.auto,
	p.deleted_at, p.edited_at, p.edit_count, p.body_hash, p.bounty_dispute_status, p.bounty_dispute_by, p.bounty_dispute_note,
	p.bounty_disputed_at, p.bounty_dispute_resolved_at, p.routed_reasons`

// scanPost scans a row into a ForumPost with all rich columns.
func scanPost(scan func(dest ...interface{}) error) (*models.ForumPost, error) {
	var p models.ForumPost
	var routedReasons []byte
	err := scan(
		&p.ID, &p.AuthorPeerID, &p.Title, &p.Description, &p.CreatedAt, &p.UpdatedAt, &p.UpvoteCount,
		&p.ReplyCount, &p.Category, &p.Tags, &p.BountyAmount, &p.BountyCurrency, &p.BountyExpiresAt,
		&p.TokenOfferAmount, &p.TokenOfferToken, &p.CID, &p.ViewCount,
		&p.BountyStatus, &p.BountyClaimedBy, &p.BountyClaimedAt, &p.BountyCompletedAt, &p.BountyEscrowRequestID,
		&p.BountyExtended, &p.BountyRefundedAt, &p.BountyEscrowPaid,
		&p.AcceptedReplyID, &p.Hidden, &p.Pinned, &p.TokenOfferMint, &p.TokenOfferSymbol, &p.TokenOfferDecimals,
		&p.TokenOfferMax, &p.TokenOfferPaid, &p.MentionPeerIDs, &p.RoomMint, &p.RoomPinned, &p.RoutedTo, &p.Auto,
		&p.DeletedAt, &p.EditedAt, &p.EditCount, &p.BodyHash, &p.BountyDisputeStatus, &p.BountyDisputeBy, &p.BountyDisputeNote,
		&p.BountyDisputedAt, &p.BountyDisputeResolvedAt, &routedReasons,
	)
	if err != nil {
		return nil, err
	}
	if len(routedReasons) > 0 {
		_ = json.Unmarshal(routedReasons, &p.RoutedReasons)
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if len(p.MentionPeerIDs) == 0 {
		p.MentionPeerIDs = nil
	}
	if len(p.RoutedTo) == 0 {
		p.RoutedTo = nil
	}
	if p.RoomMint != nil && *p.RoomMint == "" {
		p.RoomMint = nil
	}
	return &p, nil
}

// GetPostByID returns a post by ID, or models.ErrNotFound.
func (r *PostgresForumRepository) GetPostByID(ctx context.Context, postID string) (*models.ForumPost, error) {
	if !isUUID(postID) {
		return nil, models.ErrNotFound
	}
	row := r.pool.QueryRow(ctx, `SELECT `+postSelectColumns+` FROM forum_posts p WHERE p.id = $1`, postID)
	p, err := scanPost(row.Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// ListPosts returns posts with total count. sortNewest: true = created_at DESC, false = upvote_count DESC.
func (r *PostgresForumRepository) ListPosts(ctx context.Context, limit, offset int, sortNewest bool) ([]*models.ForumPost, int, error) {
	q := PostQuery{Limit: limit, Offset: offset, Sort: PostSortRecent}
	if !sortNewest {
		q.Sort = PostSortTop
	}
	return r.QueryPosts(ctx, q)
}

// openBountySQL is the "open bounty at $now" predicate; now is the positional arg named by the caller.
func openBountySQL(nowArg string) string {
	return `p.bounty_amount IS NOT NULL AND p.bounty_status = 'open' AND (p.bounty_expires_at IS NULL OR p.bounty_expires_at > ` + nowArg + `)`
}

// QueryPosts filters (category, full-text search, mine) and sorts (recent, top, bounties) the feed.
func (r *PostgresForumRepository) QueryPosts(ctx context.Context, q PostQuery) ([]*models.ForumPost, int, error) {
	if q.Limit <= 0 {
		q.Limit = 20
	}
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	var where []string
	var args []interface{}
	arg := func(v interface{}) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where = append(where, `p.deleted_at IS NULL`)
	if !q.ShowHidden {
		if q.Viewer != "" {
			where = append(where, `(NOT p.hidden OR p.author_peer_id = `+arg(q.Viewer)+`)`)
		} else {
			where = append(where, `NOT p.hidden`)
		}
	}
	if q.Category != "" {
		where = append(where, `p.category = `+arg(q.Category))
	}
	// Rooms are separate spaces: a room feed is that room only; the main feed leaves room
	// posts out, except in a poster's Mine views and the per-agent views.
	if q.Room != "" {
		where = append(where, `p.room_mint = `+arg(q.Room))
	} else if q.Mine == "" && q.Author == "" && q.Participant == "" {
		where = append(where, `p.room_mint IS NULL`)
	}
	if q.HideAuto {
		where = append(where, `NOT p.auto`)
	}
	if q.Author != "" {
		where = append(where, `p.author_peer_id = `+arg(q.Author))
	}
	if q.Participant != "" {
		// Hidden replies only count for the participant themselves or a platform viewer.
		replyVisible := ` AND NOT rp.hidden`
		if q.ShowHidden || (q.Viewer != "" && q.Viewer == q.Participant) {
			replyVisible = ``
		}
		where = append(where, `EXISTS (SELECT 1 FROM forum_replies rp WHERE rp.post_id = p.id AND rp.author_peer_id = `+arg(q.Participant)+replyVisible+`)`)
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		where = append(where, `p.search_tsv @@ plainto_tsquery('english', `+arg(s)+`)`)
	}
	if q.Sort == PostSortBounties {
		where = append(where, openBountySQL(arg(now)))
	}
	switch q.Mine {
	case MinePosts:
		where = append(where, `p.author_peer_id = `+arg(q.MinePeerID))
	case MineReplies:
		where = append(where, `EXISTS (SELECT 1 FROM forum_replies rp WHERE rp.post_id = p.id AND rp.author_peer_id = `+arg(q.MinePeerID)+`)`)
	case MineBounties:
		me := arg(q.MinePeerID)
		where = append(where, `p.bounty_amount IS NOT NULL AND (p.author_peer_id = `+me+` OR p.bounty_claimed_by = `+me+`)`)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	// The platform pin leads the main feed; the room pin (launch announcement) leads a room feed.
	pinCol := "p.pinned"
	if q.Room != "" {
		pinCol = "p.room_pinned"
	}
	orderBy := "ORDER BY " + pinCol + " DESC, p.created_at DESC, p.id DESC"
	if q.Before != nil {
		// A cursor page is plain time order: the pin already led the first page.
		orderBy = "ORDER BY p.created_at DESC, p.id DESC"
	}
	switch q.Sort {
	case PostSortTop:
		orderBy = "ORDER BY " + pinCol + " DESC, p.upvote_count DESC, p.created_at DESC"
	case PostSortBounties:
		orderBy = "ORDER BY p.bounty_amount DESC, p.bounty_expires_at ASC NULLS LAST, p.created_at DESC"
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_posts p`+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Keyset page of the recent sort (round 2): strictly older than the cursor row. The total
	// above is the whole feed's, so the cursor only narrows the page.
	if q.Before != nil && q.Sort != PostSortTop && q.Sort != PostSortBounties {
		where = append(where, `(p.created_at, p.id::text) < (`+arg(q.Before.CreatedAt)+`, `+arg(q.Before.ID)+`)`)
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	pageArgs := append(append([]interface{}{}, args...), q.Limit, q.Offset)
	rows, err := r.pool.Query(ctx, `SELECT `+postSelectColumns+` FROM forum_posts p`+whereSQL+` `+orderBy+
		` LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2), pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*models.ForumPost
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, p)
	}
	return list, total, rows.Err()
}

// GetPostsByIDs returns the posts that exist among ids, keyed by id.
func (r *PostgresForumRepository) GetPostsByIDs(ctx context.Context, ids []string) (map[string]*models.ForumPost, error) {
	out := make(map[string]*models.ForumPost, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+postSelectColumns+` FROM forum_posts p WHERE p.id::text = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

// CountPosts returns the feed aggregates in one scan: all, per category, open bounties at now.
// roomMint scopes the scan to that room; empty counts the main feed (room posts left out).
func (r *PostgresForumRepository) CountPosts(ctx context.Context, now time.Time, roomMint string) (*PostCounts, error) {
	c := &PostCounts{ByCategory: map[string]int{}}
	scope := `p.room_mint IS NULL`
	args := []interface{}{now}
	if roomMint != "" {
		scope = `p.room_mint = $2`
		args = append(args, roomMint)
	}
	rows, err := r.pool.Query(ctx, `SELECT COALESCE(p.category, 'general'), COUNT(*),
		COUNT(*) FILTER (WHERE `+openBountySQL("$1")+`) FROM forum_posts p WHERE NOT p.hidden AND p.deleted_at IS NULL AND `+scope+` GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var n, open int
		if err := rows.Scan(&cat, &n, &open); err != nil {
			return nil, err
		}
		c.ByCategory[cat] = n
		c.All += n
		c.OpenBounties += open
	}
	return c, rows.Err()
}

// CreateReply inserts a new reply (level-1 only).
func (r *PostgresForumRepository) CreateReply(ctx context.Context, reply *models.ForumReply) error {
	if reply.ID == "" {
		reply.ID = uuid.New().String()
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO forum_replies (id, post_id, author_peer_id, body, created_at, auto, mention_peer_ids, ask, relevance, relevance_signals, body_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		reply.ID, reply.PostID, reply.AuthorPeerID, reply.Body, reply.CreatedAt, reply.Auto, mentionArray(reply.MentionPeerIDs), reply.Ask,
		reply.Relevance, relevanceSignalsJSON(reply.RelevanceSignals), reply.BodyHash)
	return err
}

// relevanceSignalsJSON encodes the signals for the JSONB column (nil stays NULL).
func relevanceSignalsJSON(s *models.RelevanceSignals) []byte {
	if s == nil {
		return nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

// scanRelevanceSignals decodes the JSONB column (NULL or unreadable stays nil).
func scanRelevanceSignals(raw []byte) *models.RelevanceSignals {
	if len(raw) == 0 {
		return nil
	}
	var s models.RelevanceSignals
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

// mentionArray returns a non-nil slice for the TEXT[] NOT NULL columns.
func mentionArray(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// replySelectColumns is the shared column list for reply reads.
const replySelectColumns = `id, post_id, author_peer_id, body, created_at, auto, hidden, mention_peer_ids, ask, relevance, relevance_signals, deleted_at, edited_at, edit_count, body_hash`

// scanReply scans one reply row.
func scanReply(scan func(dest ...interface{}) error) (*models.ForumReply, error) {
	var rp models.ForumReply
	var signals []byte
	if err := scan(&rp.ID, &rp.PostID, &rp.AuthorPeerID, &rp.Body, &rp.CreatedAt, &rp.Auto, &rp.Hidden, &rp.MentionPeerIDs, &rp.Ask, &rp.Relevance, &signals,
		&rp.DeletedAt, &rp.EditedAt, &rp.EditCount, &rp.BodyHash); err != nil {
		return nil, err
	}
	rp.RelevanceSignals = scanRelevanceSignals(signals)
	if len(rp.MentionPeerIDs) == 0 {
		rp.MentionPeerIDs = nil
	}
	return &rp, nil
}

// ListRepliesByPostID returns replies for a post, ordered by created_at ASC.
func (r *PostgresForumRepository) ListRepliesByPostID(ctx context.Context, postID string, limit, offset int) ([]*models.ForumReply, int, error) {
	return r.listReplies(ctx, postID, limit, offset, false)
}

// ListManualRepliesByPostID returns the post's replies without the autopilot ones.
func (r *PostgresForumRepository) ListManualRepliesByPostID(ctx context.Context, postID string, limit, offset int) ([]*models.ForumReply, int, error) {
	return r.listReplies(ctx, postID, limit, offset, true)
}

func (r *PostgresForumRepository) listReplies(ctx context.Context, postID string, limit, offset int, hideAuto bool) ([]*models.ForumReply, int, error) {
	return r.ListRepliesFiltered(ctx, postID, ReplyQuery{Limit: limit, Offset: offset, HideAuto: hideAuto, ShowHidden: true})
}

// ListRepliesFiltered returns the post's replies oldest first under the ReplyQuery visibility
// rules: hidden replies only for their author or with ShowHidden; HideAuto drops autopilot replies.
func (r *PostgresForumRepository) ListRepliesFiltered(ctx context.Context, postID string, q ReplyQuery) ([]*models.ForumReply, int, error) {
	if !isUUID(postID) {
		return nil, 0, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	where := `WHERE post_id = $1`
	args := []interface{}{postID}
	if q.HideAuto {
		where += ` AND NOT auto`
	}
	if !q.ShowHidden {
		if q.Viewer != "" {
			args = append(args, q.Viewer)
			where += ` AND (NOT hidden OR author_peer_id = $2)`
		} else {
			where += ` AND NOT hidden`
		}
	}
	var total int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_replies `+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]interface{}{}, args...), limit, q.Offset)
	rows, err := r.pool.Query(ctx, `SELECT `+replySelectColumns+` FROM forum_replies `+where+
		` ORDER BY created_at ASC LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2), pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []*models.ForumReply
	for rows.Next() {
		rp, err := scanReply(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, rp)
	}
	return list, total, rows.Err()
}

// CountAutoRepliesByPeerOnPost returns how many autopilot replies peerID has on postID.
func (r *PostgresForumRepository) CountAutoRepliesByPeerOnPost(ctx context.Context, peerID, postID string) (int, error) {
	if !isUUID(postID) {
		return 0, nil
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_replies WHERE author_peer_id = $1 AND post_id = $2 AND auto`, peerID, postID).Scan(&n)
	return n, err
}

// CountAutoRepliesByPeerSince returns how many autopilot replies peerID created at or after since.
func (r *PostgresForumRepository) CountAutoRepliesByPeerSince(ctx context.Context, peerID string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_replies WHERE author_peer_id = $1 AND auto AND created_at >= $2`, peerID, since).Scan(&n)
	return n, err
}

// ListAutoReplyOutcomes joins the peer's autopilot replies since `since` with their post's
// category, upvotes, accepted answer and bounty state and the reply's hidden flag, newest
// reply first. forum_replies.id is a uuid while accepted_reply_id is text, hence the cast.
func (r *PostgresForumRepository) ListAutoReplyOutcomes(ctx context.Context, peerID string, since time.Time) ([]*models.AutoReplyOutcome, error) {
	rows, err := r.pool.Query(ctx, `SELECT rp.id, rp.post_id, p.category, rp.created_at, p.upvote_count, p.bounty_amount,
		(p.accepted_reply_id = rp.id::text),
		(p.bounty_status = 'completed' AND p.bounty_claimed_by = rp.author_peer_id),
		rp.hidden, rp.relevance, rp.relevance_signals
		FROM forum_replies rp JOIN forum_posts p ON p.id = rp.post_id
		WHERE rp.author_peer_id = $1 AND rp.auto AND rp.created_at >= $2
		ORDER BY rp.created_at DESC`, peerID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.AutoReplyOutcome
	for rows.Next() {
		var o models.AutoReplyOutcome
		var accepted, awarded *bool
		var signals []byte
		if err := rows.Scan(&o.ReplyID, &o.PostID, &o.Category, &o.PostedAt, &o.Upvotes, &o.BountyAmount, &accepted, &awarded, &o.Hidden, &o.Relevance, &signals); err != nil {
			return nil, err
		}
		o.RelevanceSignals = scanRelevanceSignals(signals)
		o.Accepted = accepted != nil && *accepted
		o.Awarded = awarded != nil && *awarded
		out = append(out, &o)
	}
	return out, rows.Err()
}

// CountAutoAsksByPeerOnPost counts the peer's autopilot replies on the post that carry an ask.
func (r *PostgresForumRepository) CountAutoAsksByPeerOnPost(ctx context.Context, peerID, postID string) (int, error) {
	if !isUUID(postID) {
		return 0, nil
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM forum_replies WHERE author_peer_id = $1 AND post_id = $2 AND auto AND ask > 0`, peerID, postID).Scan(&n)
	return n, err
}

// AskerPeerIDs returns the distinct authors of the post's replies that carry an ask.
func (r *PostgresForumRepository) AskerPeerIDs(ctx context.Context, postID string) ([]string, error) {
	if !isUUID(postID) {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT author_peer_id FROM forum_replies WHERE post_id = $1 AND ask > 0 ORDER BY 1`, postID)
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

// AddUpvote adds an upvote; idempotent (ignore unique violation). Updates post upvote_count only when a new row was inserted.
func (r *PostgresForumRepository) AddUpvote(ctx context.Context, peerID, postID string) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `INSERT INTO forum_upvotes (peer_id, post_id) VALUES ($1, $2) ON CONFLICT (peer_id, post_id) DO NOTHING`, peerID, postID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() > 0 {
		_, err = r.pool.Exec(ctx, `UPDATE forum_posts SET upvote_count = upvote_count + 1 WHERE id = $1`, postID)
		if err != nil {
			return err
		}
	}
	return nil
}

// RemoveUpvote removes an upvote; no-op if not present. Updates post upvote_count only when a row was deleted.
func (r *PostgresForumRepository) RemoveUpvote(ctx context.Context, peerID, postID string) error {
	if !isUUID(postID) {
		return models.ErrNotFound
	}
	cmd, err := r.pool.Exec(ctx, `DELETE FROM forum_upvotes WHERE peer_id = $1 AND post_id = $2`, peerID, postID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() > 0 {
		_, err = r.pool.Exec(ctx, `UPDATE forum_posts SET upvote_count = GREATEST(0, upvote_count - 1) WHERE id = $1`, postID)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetUpvoteCount returns the upvote count for a post (from forum_posts.upvote_count).
func (r *PostgresForumRepository) GetUpvoteCount(ctx context.Context, postID string) (int, error) {
	if !isUUID(postID) {
		return 0, models.ErrNotFound
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT upvote_count FROM forum_posts WHERE id = $1`, postID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, models.ErrNotFound
		}
		return 0, err
	}
	return n, nil
}

// HasUpvoted returns true if the peer has upvoted the post.
func (r *PostgresForumRepository) HasUpvoted(ctx context.Context, peerID, postID string) (bool, error) {
	if !isUUID(postID) {
		return false, nil
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT 1 FROM forum_upvotes WHERE peer_id = $1 AND post_id = $2`, peerID, postID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BatchHasUpvoted returns the set of postIDs (from the given slice) that peerID has upvoted.
func (r *PostgresForumRepository) BatchHasUpvoted(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(postIDs))
	if len(postIDs) == 0 || peerID == "" {
		return result, nil
	}
	postIDs = uuidsOnly(postIDs)
	if len(postIDs) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT post_id FROM forum_upvotes WHERE peer_id = $1 AND post_id::text = ANY($2)`,
		peerID, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var postID string
		if err := rows.Scan(&postID); err != nil {
			return nil, err
		}
		result[postID] = true
	}
	return result, rows.Err()
}

// CountRepliesByPostID returns the number of replies for a post (from forum_posts.reply_count).
func (r *PostgresForumRepository) CountRepliesByPostID(ctx context.Context, postID string) (int, error) {
	if !isUUID(postID) {
		return 0, models.ErrNotFound
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT reply_count FROM forum_posts WHERE id = $1`, postID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil // non-existent post: 0 replies (matches legacy COUNT behavior)
		}
		return 0, err
	}
	return n, nil
}

// IncrementViewCount atomically increments and returns the view count for a post.
func (r *PostgresForumRepository) IncrementViewCount(ctx context.Context, postID string) (int, error) {
	if !isUUID(postID) {
		return 0, models.ErrNotFound
	}
	var n int
	err := r.pool.QueryRow(ctx,
		`UPDATE forum_posts SET view_count = view_count + 1 WHERE id = $1 RETURNING view_count`,
		postID).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, models.ErrNotFound
		}
		return 0, err
	}
	return n, nil
}

// --- Bounty lifecycle ---

// AwardBounty claims the award in one conditional update: only an open bounty flips to
// completed, so two concurrent awards serialize on the row and the second finds nothing to
// update (ErrInvalidInput), and an expired (refunded) bounty can never be awarded on top of
// its refund.
func (r *PostgresForumRepository) AwardBounty(ctx context.Context, postID, winnerPeerID string) error {
	if !isUUID(postID) {
		return models.ErrInvalidInput
	}
	cmd, err := r.pool.Exec(ctx,
		`UPDATE forum_posts SET bounty_status = 'completed', bounty_claimed_by = $2, bounty_completed_at = NOW()
		 WHERE id = $1 AND bounty_amount IS NOT NULL AND bounty_status = 'open'`,
		postID, winnerPeerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrInvalidInput
	}
	return nil
}

// RevertBountyAward reopens a bounty whose winner could not be credited after the claim.
func (r *PostgresForumRepository) RevertBountyAward(ctx context.Context, postID string) error {
	if !isUUID(postID) {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE forum_posts SET bounty_status = 'open', bounty_claimed_by = NULL, bounty_completed_at = NULL
		 WHERE id = $1 AND bounty_status = 'completed'`, postID)
	return err
}

// SetBountyEscrowRequestID stores the credit escrow idempotency key on a bounty post.
func (r *PostgresForumRepository) SetBountyEscrowRequestID(ctx context.Context, postID, requestID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE forum_posts SET bounty_escrow_request_id = $2 WHERE id = $1`,
		postID, requestID)
	return err
}

// ListExpiredOpenBounties returns open bounty posts whose expiry has passed, plus expired
// escrowed bounties whose refund has not been recorded yet (a run that stopped between the
// status flip and the refund retries them; the refund is idempotent).
func (r *PostgresForumRepository) ListExpiredOpenBounties(ctx context.Context, now time.Time) ([]*models.ForumPost, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+postSelectColumns+` FROM forum_posts p
		 WHERE bounty_amount IS NOT NULL AND (
		   (bounty_status = 'open' AND bounty_expires_at < $1)
		   OR (bounty_status = 'expired' AND bounty_escrow_request_id IS NOT NULL AND bounty_refunded_at IS NULL))`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []*models.ForumPost
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// ExpireBounty flips an open bounty to "expired"; ErrInvalidInput when the bounty is not open
// (awarded meanwhile, or already expired), so an award and the expiry never both pay out.
func (r *PostgresForumRepository) ExpireBounty(ctx context.Context, postID string) error {
	if !isUUID(postID) {
		return models.ErrInvalidInput
	}
	cmd, err := r.pool.Exec(ctx,
		`UPDATE forum_posts SET bounty_status = 'expired' WHERE id = $1 AND bounty_amount IS NOT NULL AND bounty_status = 'open'`, postID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrInvalidInput
	}
	return nil
}

// SetBountyRefundedAt records when the escrow went back to the poster.
func (r *PostgresForumRepository) SetBountyRefundedAt(ctx context.Context, postID string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE forum_posts SET bounty_refunded_at = $2 WHERE id = $1`, postID, at)
	return err
}

// ExtendBounty moves the expiry and marks the post extended; ErrInvalidInput unless open and not yet extended.
func (r *PostgresForumRepository) ExtendBounty(ctx context.Context, postID string, expiresAt time.Time) error {
	if !isUUID(postID) {
		return models.ErrInvalidInput
	}
	cmd, err := r.pool.Exec(ctx,
		`UPDATE forum_posts SET bounty_expires_at = $2, bounty_extended = TRUE
		 WHERE id = $1 AND bounty_amount IS NOT NULL AND bounty_status = 'open' AND NOT bounty_extended`,
		postID, expiresAt)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrInvalidInput
	}
	return nil
}

// RaiseBounty claims the raise in one conditional update: the row must have no bounty or an
// open bounty below amount, so two concurrent raises to the same amount serialize on the row
// lock and the second finds nothing to update. Returns the previous amount (0 for none). The
// escrow request id and deadline only land on a post that had none. ErrInvalidInput when no
// row qualified.
func (r *PostgresForumRepository) RaiseBounty(ctx context.Context, postID string, amount int, escrowRequestID string, expiresAt time.Time) (int, error) {
	if !isUUID(postID) {
		return 0, models.ErrInvalidInput
	}
	var prev int
	err := r.pool.QueryRow(ctx,
		`WITH prev AS (
		   SELECT id, COALESCE(bounty_amount, 0) AS amount FROM forum_posts WHERE id = $1 FOR UPDATE
		 )
		 UPDATE forum_posts p SET bounty_amount = $2, bounty_status = 'open',
		   bounty_currency = COALESCE(p.bounty_currency, 'credits'),
		   bounty_escrow_request_id = COALESCE(p.bounty_escrow_request_id, $3),
		   bounty_expires_at = COALESCE(p.bounty_expires_at, $4),
		   updated_at = NOW()
		 FROM prev
		 WHERE p.id = prev.id AND (p.bounty_amount IS NULL OR (p.bounty_status = 'open' AND p.bounty_amount < $2))
		 RETURNING prev.amount`,
		postID, amount, escrowRequestID, expiresAt).Scan(&prev)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, models.ErrInvalidInput
		}
		return 0, err
	}
	return prev, nil
}

// SetBountyEscrowPaid records the paid part of the escrow after a successful raise.
func (r *PostgresForumRepository) SetBountyEscrowPaid(ctx context.Context, postID string, escrowPaid int) error {
	_, err := r.pool.Exec(ctx, `UPDATE forum_posts SET bounty_escrow_paid = $2 WHERE id = $1`, postID, escrowPaid)
	return err
}

// RevertBountyRaise undoes RaiseBounty when the escrow failed: back to prevAmount, or no
// bounty at all when prevAmount is 0.
func (r *PostgresForumRepository) RevertBountyRaise(ctx context.Context, postID string, prevAmount int) error {
	if prevAmount <= 0 {
		_, err := r.pool.Exec(ctx,
			`UPDATE forum_posts SET bounty_amount = NULL, bounty_status = '', bounty_currency = NULL,
			   bounty_escrow_request_id = NULL, bounty_expires_at = NULL, bounty_escrow_paid = 0, updated_at = NOW()
			 WHERE id = $1`, postID)
		return err
	}
	_, err := r.pool.Exec(ctx, `UPDATE forum_posts SET bounty_amount = $2, updated_at = NOW() WHERE id = $1`, postID, prevAmount)
	return err
}

// ListBountiesExpiringBetween returns open bounties with from < bounty_expires_at <= to.
func (r *PostgresForumRepository) ListBountiesExpiringBetween(ctx context.Context, from, to time.Time) ([]*models.ForumPost, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+postSelectColumns+` FROM forum_posts p
		 WHERE p.bounty_amount IS NOT NULL AND p.bounty_status = 'open'
		   AND p.bounty_expires_at > $1 AND p.bounty_expires_at <= $2`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []*models.ForumPost
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}
