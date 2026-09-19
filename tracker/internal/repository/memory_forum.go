// Package repository: In-memory implementation of ForumRepository.
package repository

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryForumRepository implements ForumRepository in memory.
type MemoryForumRepository struct {
	mu      sync.RWMutex
	posts   map[string]*models.ForumPost
	replies map[string][]*models.ForumReply // postID -> replies
	upvotes map[string]map[string]struct{}  // postID -> set of peerID
	// upvoteAt is when each upvote landed (postID -> peerID -> time), for the per-peer daily cap.
	upvoteAt map[string]map[string]time.Time
	// NowFunc stamps bounty_completed_at (AwardBounty); nil = time.Now (tests set a mock clock).
	NowFunc func() time.Time
	// PeerFirstSeen and PeerWallet stand in for the peers and account_wallets tables under a
	// BoardStatsGuard (tests set them); a peer absent from PeerFirstSeen is treated as brand new.
	PeerFirstSeen map[string]time.Time
	PeerWallet    map[string]string
}

// NewMemoryForumRepository creates a new in-memory forum repository.
func NewMemoryForumRepository() *MemoryForumRepository {
	return &MemoryForumRepository{
		posts:    make(map[string]*models.ForumPost),
		replies:  make(map[string][]*models.ForumReply),
		upvotes:  make(map[string]map[string]struct{}),
		upvoteAt: make(map[string]map[string]time.Time),
	}
}

// CreatePost inserts a new forum post (including rich fields: category, tags, bounty, tokenOffer, cid).
func (r *MemoryForumRepository) CreatePost(ctx context.Context, post *models.ForumPost) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if post.ID == "" {
		post.ID = uuid.New().String()
	}
	p := *post
	p.UpvoteCount = 0
	p.ReplyCount = 0
	if p.Category == "" {
		p.Category = "general"
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.RoomPinned && p.InRoom() {
		// One pinned announcement per room (idx_forum_posts_room_announcement).
		for _, other := range r.posts {
			if other.RoomPinned && other.InRoom() && *other.RoomMint == *p.RoomMint {
				return models.ErrAlreadyExists
			}
		}
	}
	r.posts[p.ID] = &p
	r.replies[p.ID] = nil
	r.upvotes[p.ID] = make(map[string]struct{})
	return nil
}

// GetPostByID returns a post by ID, or models.ErrNotFound.
func (r *MemoryForumRepository) GetPostByID(ctx context.Context, postID string) (*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.posts[postID]
	if !ok {
		return nil, models.ErrNotFound
	}
	out := *p
	out.ReplyCount = len(r.replies[postID])
	return &out, nil
}

// ListPosts returns posts with total count. sortNewest: true = created_at DESC, false = upvote_count DESC.
func (r *MemoryForumRepository) ListPosts(ctx context.Context, limit, offset int, sortNewest bool) ([]*models.ForumPost, int, error) {
	q := PostQuery{Limit: limit, Offset: offset, Sort: PostSortRecent}
	if !sortNewest {
		q.Sort = PostSortTop
	}
	return r.QueryPosts(ctx, q)
}

// bountyOpenAt reports whether the post's bounty is open and its expiry has not passed at now.
func bountyOpenAt(p *models.ForumPost, now time.Time) bool {
	if !p.HasBounty() || p.BountyStatus != "open" {
		return false
	}
	return p.BountyExpiresAt == nil || p.BountyExpiresAt.After(now)
}

// repliedIn reports whether peerID has a reply on postID (caller holds the lock).
func (r *MemoryForumRepository) repliedIn(peerID, postID string) bool {
	for _, rp := range r.replies[postID] {
		if rp.AuthorPeerID == peerID {
			return true
		}
	}
	return false
}

// participatedIn reports whether peerID has a visible reply on postID; hidden replies count
// only when showHidden is set or the viewer is peerID (caller holds the lock).
func (r *MemoryForumRepository) participatedIn(peerID, postID, viewer string, showHidden bool) bool {
	for _, rp := range r.replies[postID] {
		if rp.AuthorPeerID == peerID && (!rp.Hidden || showHidden || viewer == peerID) {
			return true
		}
	}
	return false
}

// QueryPosts filters (category, substring search, mine, author, participant) and sorts
// (recent, top, bounties) the feed.
func (r *MemoryForumRepository) QueryPosts(ctx context.Context, q PostQuery) ([]*models.ForumPost, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	list := make([]*models.ForumPost, 0, len(r.posts))
	for _, p := range r.posts {
		if p.IsDeleted() {
			continue
		}
		if p.Hidden && !q.ShowHidden && p.AuthorPeerID != q.Viewer {
			continue
		}
		if q.Category != "" && p.Category != q.Category {
			continue
		}
		// Rooms are separate spaces: a room feed is that room only; the main feed leaves room
		// posts out, except in a poster's Mine views and the per-agent views.
		if q.Room != "" {
			if !p.InRoom() || *p.RoomMint != q.Room {
				continue
			}
		} else if q.Mine == "" && q.Author == "" && q.Participant == "" && p.InRoom() {
			continue
		}
		if q.HideAuto && p.Auto {
			continue
		}
		if q.Author != "" && p.AuthorPeerID != q.Author {
			continue
		}
		if q.Participant != "" && !r.participatedIn(q.Participant, p.ID, q.Viewer, q.ShowHidden) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(p.Title), search) && !strings.Contains(strings.ToLower(p.Description), search) {
			continue
		}
		if q.Sort == PostSortBounties && !bountyOpenAt(p, now) {
			continue
		}
		switch q.Mine {
		case MinePosts:
			if p.AuthorPeerID != q.MinePeerID {
				continue
			}
		case MineReplies:
			if !r.repliedIn(q.MinePeerID, p.ID) {
				continue
			}
		case MineBounties:
			won := p.BountyClaimedBy != nil && *p.BountyClaimedBy == q.MinePeerID
			if !p.HasBounty() || (p.AuthorPeerID != q.MinePeerID && !won) {
				continue
			}
		}
		cp := *p
		cp.ReplyCount = len(r.replies[p.ID])
		list = append(list, &cp)
	}
	total := len(list)
	// Keyset page of the recent sort: strictly older than the cursor row (the total stays the
	// whole feed's).
	if q.Before != nil && q.Sort != PostSortTop && q.Sort != PostSortBounties {
		after := list[:0]
		for _, p := range list {
			if p.CreatedAt.After(q.Before.CreatedAt) || (p.CreatedAt.Equal(q.Before.CreatedAt) && p.ID >= q.Before.ID) {
				continue
			}
			after = append(after, p)
		}
		list = after
	}
	// The platform pin leads the main feed; the room pin (launch announcement) leads a room feed.
	pinned := func(p *models.ForumPost) bool {
		if q.Room != "" {
			return p.RoomPinned
		}
		return p.Pinned
	}
	switch q.Sort {
	case PostSortTop:
		sort.Slice(list, func(i, j int) bool {
			if pinned(list[i]) != pinned(list[j]) {
				return pinned(list[i])
			}
			if list[i].UpvoteCount != list[j].UpvoteCount {
				return list[i].UpvoteCount > list[j].UpvoteCount
			}
			return list[i].CreatedAt.After(list[j].CreatedAt)
		})
	case PostSortBounties:
		sort.Slice(list, func(i, j int) bool {
			a, b := *list[i].BountyAmount, *list[j].BountyAmount
			if a != b {
				return a > b
			}
			ea, eb := list[i].BountyExpiresAt, list[j].BountyExpiresAt
			switch {
			case ea != nil && eb != nil && !ea.Equal(*eb):
				return ea.Before(*eb)
			case ea != nil && eb == nil:
				return true
			case ea == nil && eb != nil:
				return false
			}
			return list[i].CreatedAt.After(list[j].CreatedAt)
		})
	default:
		sort.Slice(list, func(i, j int) bool {
			if q.Before == nil && pinned(list[i]) != pinned(list[j]) {
				return pinned(list[i])
			}
			if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
				return list[i].CreatedAt.After(list[j].CreatedAt)
			}
			return list[i].ID > list[j].ID
		})
	}
	if q.Offset > len(list) {
		return []*models.ForumPost{}, total, nil
	}
	list = list[q.Offset:]
	if q.Limit > 0 && len(list) > q.Limit {
		list = list[:q.Limit]
	}
	return list, total, nil
}

// GetPostsByIDs returns the posts that exist among ids, keyed by id.
func (r *MemoryForumRepository) GetPostsByIDs(ctx context.Context, ids []string) (map[string]*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*models.ForumPost, len(ids))
	for _, id := range ids {
		if p, ok := r.posts[id]; ok {
			cp := *p
			cp.ReplyCount = len(r.replies[id])
			out[id] = &cp
		}
	}
	return out, nil
}

// CountPosts returns the feed aggregates (all, per category, open bounties at now) for the
// main feed, or for one room when roomMint is set.
func (r *MemoryForumRepository) CountPosts(ctx context.Context, now time.Time, roomMint string) (*PostCounts, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := &PostCounts{ByCategory: map[string]int{}}
	for _, p := range r.posts {
		if p.Hidden || p.IsDeleted() {
			continue
		}
		if roomMint != "" {
			if !p.InRoom() || *p.RoomMint != roomMint {
				continue
			}
		} else if p.InRoom() {
			continue
		}
		c.All++
		c.ByCategory[p.Category]++
		if bountyOpenAt(p, now) {
			c.OpenBounties++
		}
	}
	return c, nil
}

// CreateReply inserts a new reply (level-1 only).
func (r *MemoryForumRepository) CreateReply(ctx context.Context, reply *models.ForumReply) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reply.ID == "" {
		reply.ID = uuid.New().String()
	}
	if r.replies[reply.PostID] == nil {
		r.replies[reply.PostID] = nil
	}
	r.replies[reply.PostID] = append(r.replies[reply.PostID], &models.ForumReply{
		ID: reply.ID, PostID: reply.PostID, AuthorPeerID: reply.AuthorPeerID, Body: reply.Body, CreatedAt: reply.CreatedAt, Auto: reply.Auto,
		MentionPeerIDs: append([]string(nil), reply.MentionPeerIDs...), Ask: reply.Ask,
		Relevance: copyFloat(reply.Relevance), RelevanceSignals: copySignals(reply.RelevanceSignals),
		BodyHash: reply.BodyHash,
	})
	return nil
}

func copyFloat(f *float64) *float64 {
	if f == nil {
		return nil
	}
	v := *f
	return &v
}

func copySignals(s *models.RelevanceSignals) *models.RelevanceSignals {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

// CountAutoAsksByPeerOnPost counts the peer's autopilot replies on the post that carry an ask.
func (r *MemoryForumRepository) CountAutoAsksByPeerOnPost(ctx context.Context, peerID, postID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, rp := range r.replies[postID] {
		if rp.Auto && rp.HasAsk() && rp.AuthorPeerID == peerID {
			n++
		}
	}
	return n, nil
}

// AskerPeerIDs returns the distinct authors of the post's replies that carry an ask, sorted.
func (r *MemoryForumRepository) AskerPeerIDs(ctx context.Context, postID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, rp := range r.replies[postID] {
		if rp.HasAsk() && !seen[rp.AuthorPeerID] {
			seen[rp.AuthorPeerID] = true
			out = append(out, rp.AuthorPeerID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ListRepliesByPostID returns replies for a post, ordered by created_at ASC.
func (r *MemoryForumRepository) ListRepliesByPostID(ctx context.Context, postID string, limit, offset int) ([]*models.ForumReply, int, error) {
	return r.listReplies(postID, limit, offset, false)
}

// ListManualRepliesByPostID returns the post's replies without the autopilot ones.
func (r *MemoryForumRepository) ListManualRepliesByPostID(ctx context.Context, postID string, limit, offset int) ([]*models.ForumReply, int, error) {
	return r.listReplies(postID, limit, offset, true)
}

// CountAutoRepliesByPeerOnPost returns how many autopilot replies peerID has on postID.
func (r *MemoryForumRepository) CountAutoRepliesByPeerOnPost(ctx context.Context, peerID, postID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, rp := range r.replies[postID] {
		if rp.Auto && rp.AuthorPeerID == peerID {
			n++
		}
	}
	return n, nil
}

// CountAutoRepliesByPeerSince returns how many autopilot replies peerID created at or after since.
func (r *MemoryForumRepository) CountAutoRepliesByPeerSince(ctx context.Context, peerID string, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, repl := range r.replies {
		for _, rp := range repl {
			if rp.Auto && rp.AuthorPeerID == peerID && !rp.CreatedAt.Before(since) {
				n++
			}
		}
	}
	return n, nil
}

// ListAutoReplyOutcomes joins the peer's autopilot replies since `since` with their post's
// upvotes and bounty state, newest reply first.
func (r *MemoryForumRepository) ListAutoReplyOutcomes(ctx context.Context, peerID string, since time.Time) ([]*models.AutoReplyOutcome, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.AutoReplyOutcome
	for postID, repl := range r.replies {
		p := r.posts[postID]
		for _, rp := range repl {
			if !rp.Auto || rp.AuthorPeerID != peerID || rp.CreatedAt.Before(since) || p == nil {
				continue
			}
			o := &models.AutoReplyOutcome{ReplyID: rp.ID, PostID: postID, Category: p.Category, PostedAt: rp.CreatedAt, Upvotes: p.UpvoteCount, Hidden: rp.Hidden,
				Relevance: copyFloat(rp.Relevance), RelevanceSignals: copySignals(rp.RelevanceSignals)}
			if p.BountyAmount != nil {
				amt := *p.BountyAmount
				o.BountyAmount = &amt
			}
			o.Accepted = p.AcceptedReplyID != nil && *p.AcceptedReplyID == rp.ID
			o.Awarded = p.BountyStatus == "completed" && p.BountyClaimedBy != nil && *p.BountyClaimedBy == peerID
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PostedAt.After(out[j].PostedAt) })
	return out, nil
}

func (r *MemoryForumRepository) listReplies(postID string, limit, offset int, hideAuto bool) ([]*models.ForumReply, int, error) {
	return r.ListRepliesFiltered(context.Background(), postID, ReplyQuery{Limit: limit, Offset: offset, HideAuto: hideAuto, ShowHidden: true})
}

// ListRepliesFiltered returns the post's replies oldest first under the ReplyQuery visibility
// rules: hidden replies only for their author or with ShowHidden; HideAuto drops autopilot replies.
func (r *MemoryForumRepository) ListRepliesFiltered(ctx context.Context, postID string, q ReplyQuery) ([]*models.ForumReply, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	limit, offset := q.Limit, q.Offset
	repl := r.replies[postID]
	if repl == nil {
		return []*models.ForumReply{}, 0, nil
	}
	sorted := make([]*models.ForumReply, 0, len(repl))
	for _, rp := range repl {
		if q.HideAuto && rp.Auto {
			continue
		}
		if rp.Hidden && !q.ShowHidden && rp.AuthorPeerID != q.Viewer {
			continue
		}
		cp := *rp
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) })
	total := len(sorted)
	if offset > len(sorted) {
		return []*models.ForumReply{}, total, nil
	}
	sorted = sorted[offset:]
	if limit > 0 && len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted, total, nil
}

// AddUpvote adds an upvote; idempotent.
func (r *MemoryForumRepository) AddUpvote(ctx context.Context, peerID, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if r.upvotes[postID] == nil {
		r.upvotes[postID] = make(map[string]struct{})
	}
	if _, exists := r.upvotes[postID][peerID]; !exists {
		r.upvotes[postID][peerID] = struct{}{}
		if r.upvoteAt[postID] == nil {
			r.upvoteAt[postID] = make(map[string]time.Time)
		}
		r.upvoteAt[postID][peerID] = r.now()
		p.UpvoteCount++
	}
	return nil
}

// now is the repository clock (NowFunc, else the wall clock).
func (r *MemoryForumRepository) now() time.Time {
	if r.NowFunc != nil {
		return r.NowFunc()
	}
	return time.Now()
}

// RemoveUpvote removes an upvote; no-op if not present.
func (r *MemoryForumRepository) RemoveUpvote(ctx context.Context, peerID, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return nil
	}
	if _, exists := r.upvotes[postID][peerID]; exists {
		delete(r.upvotes[postID], peerID)
		if p.UpvoteCount > 0 {
			p.UpvoteCount--
		}
	}
	return nil
}

// GetUpvoteCount returns the upvote count for a post.
func (r *MemoryForumRepository) GetUpvoteCount(ctx context.Context, postID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.posts[postID]
	if !ok {
		return 0, models.ErrNotFound
	}
	return p.UpvoteCount, nil
}

// HasUpvoted returns true if the peer has upvoted the post.
func (r *MemoryForumRepository) HasUpvoted(ctx context.Context, peerID, postID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.upvotes[postID][peerID]
	return ok, nil
}

// BatchHasUpvoted returns the set of postIDs (from the given slice) that peerID has upvoted.
func (r *MemoryForumRepository) BatchHasUpvoted(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]bool, len(postIDs))
	for _, postID := range postIDs {
		if _, ok := r.upvotes[postID][peerID]; ok {
			result[postID] = true
		}
	}
	return result, nil
}

// CountRepliesByPostID returns the number of replies for a post.
func (r *MemoryForumRepository) CountRepliesByPostID(ctx context.Context, postID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.replies[postID]), nil
}

// IncrementViewCount atomically increments and returns the view count for a post.
func (r *MemoryForumRepository) IncrementViewCount(ctx context.Context, postID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return 0, models.ErrNotFound
	}
	p.ViewCount++
	return p.ViewCount, nil
}

// AwardBounty marks a bounty completed and records the winner peer.
func (r *MemoryForumRepository) AwardBounty(ctx context.Context, postID, winnerPeerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if !p.HasBounty() || p.BountyStatus != "open" {
		return models.ErrInvalidInput
	}
	now := time.Now()
	if r.NowFunc != nil {
		now = r.NowFunc()
	}
	p.BountyStatus = "completed"
	p.BountyClaimedBy = &winnerPeerID
	p.BountyCompletedAt = &now
	return nil
}

// RevertBountyAward reopens a completed bounty whose winner could not be credited.
func (r *MemoryForumRepository) RevertBountyAward(ctx context.Context, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if p.BountyStatus == "completed" {
		p.BountyStatus, p.BountyClaimedBy, p.BountyCompletedAt = "open", nil, nil
	}
	return nil
}

// SetBountyEscrowRequestID stores the idempotency key from the escrow Spend.
func (r *MemoryForumRepository) SetBountyEscrowRequestID(ctx context.Context, postID, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	p.BountyEscrowRequestID = &requestID
	return nil
}

// ListExpiredOpenBounties returns posts whose bounty has expired and is still open.
func (r *MemoryForumRepository) ListExpiredOpenBounties(ctx context.Context, now time.Time) ([]*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.ForumPost
	for _, p := range r.posts {
		if p.BountyAmount == nil {
			continue
		}
		pastDeadline := p.BountyStatus == "open" && p.BountyExpiresAt != nil && p.BountyExpiresAt.Before(now)
		unrefunded := p.BountyStatus == "expired" && p.BountyEscrowRequestID != nil && *p.BountyEscrowRequestID != "" && p.BountyRefundedAt == nil
		if pastDeadline || unrefunded {
			cp := *p
			out = append(out, &cp)
		}
	}
	return out, nil
}

// ExpireBounty flips an open bounty to expired; ErrInvalidInput when it is not open.
func (r *MemoryForumRepository) ExpireBounty(ctx context.Context, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if !p.HasBounty() || p.BountyStatus != "open" {
		return models.ErrInvalidInput
	}
	p.BountyStatus = "expired"
	return nil
}

// SetBountyRefundedAt records when the escrow went back to the poster.
func (r *MemoryForumRepository) SetBountyRefundedAt(ctx context.Context, postID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	p.BountyRefundedAt = &at
	return nil
}

// ExtendBounty moves the expiry and marks the post extended; ErrInvalidInput unless open and not yet extended.
func (r *MemoryForumRepository) ExtendBounty(ctx context.Context, postID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if !p.HasBounty() || p.BountyStatus != "open" || p.BountyExtended {
		return models.ErrInvalidInput
	}
	p.BountyExpiresAt = &expiresAt
	p.BountyExtended = true
	return nil
}

// RaiseBounty claims the raise under the lock: the post must have no bounty or an open bounty
// below amount (ErrInvalidInput otherwise, nothing written). Returns the previous amount (0
// for none). The escrow request id and deadline only land on a post that had none.
func (r *MemoryForumRepository) RaiseBounty(ctx context.Context, postID string, amount int, escrowRequestID string, expiresAt time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return 0, models.ErrNotFound
	}
	prev := 0
	if p.HasBounty() {
		if p.BountyStatus != "open" || *p.BountyAmount >= amount {
			return 0, models.ErrInvalidInput
		}
		prev = *p.BountyAmount
	}
	amt := amount
	p.BountyAmount = &amt
	p.BountyStatus = "open"
	if p.BountyCurrency == nil {
		currency := "credits"
		p.BountyCurrency = &currency
	}
	if p.BountyEscrowRequestID == nil {
		id := escrowRequestID
		p.BountyEscrowRequestID = &id
	}
	if p.BountyExpiresAt == nil {
		exp := expiresAt
		p.BountyExpiresAt = &exp
	}
	p.UpdatedAt = time.Now()
	if r.NowFunc != nil {
		p.UpdatedAt = r.NowFunc()
	}
	return prev, nil
}

// SetBountyEscrowPaid records the paid part of the escrow after a successful raise.
func (r *MemoryForumRepository) SetBountyEscrowPaid(ctx context.Context, postID string, escrowPaid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	p.BountyEscrowPaid = escrowPaid
	return nil
}

// RevertBountyRaise undoes RaiseBounty when the escrow failed: back to prevAmount, or no
// bounty at all when prevAmount is 0.
func (r *MemoryForumRepository) RevertBountyRaise(ctx context.Context, postID string, prevAmount int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if prevAmount <= 0 {
		p.BountyAmount, p.BountyStatus, p.BountyCurrency, p.BountyEscrowRequestID, p.BountyExpiresAt, p.BountyEscrowPaid = nil, "", nil, nil, nil, 0
		return nil
	}
	amt := prevAmount
	p.BountyAmount = &amt
	return nil
}

// ListBountiesExpiringBetween returns open bounties with from < bounty_expires_at <= to.
func (r *MemoryForumRepository) ListBountiesExpiringBetween(ctx context.Context, from, to time.Time) ([]*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.ForumPost
	for _, p := range r.posts {
		if !p.HasBounty() || p.BountyStatus != "open" || p.BountyExpiresAt == nil {
			continue
		}
		if p.BountyExpiresAt.After(from) && !p.BountyExpiresAt.After(to) {
			cp := *p
			out = append(out, &cp)
		}
	}
	return out, nil
}
