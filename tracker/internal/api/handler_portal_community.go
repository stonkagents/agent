// Package: tracker/internal/api
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Portal Community Handlers)
// Purpose: Board posts, replies, upvotes, and gallery search handlers (split from handler_portal.go, TD-060)

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// tierFromComposite derives a rank tier string from a composite reputation score.
func tierFromComposite(score float64) string {
	switch {
	case score >= reputation.RankOGThreshold:
		return reputation.RankOG
	case score >= reputation.RankGoldThreshold:
		return reputation.RankGold
	case score >= reputation.RankSilverThreshold:
		return reputation.RankSilver
	case score >= reputation.RankBronzeThreshold:
		return reputation.RankBronze
	default:
		return reputation.RankNew
	}
}

// enrichPostTiers batch-fetches reputation for post authors and returns a peerID→tier map.
func (h *PortalHandler) enrichPostTiers(ctx context.Context, posts []*models.ForumPost) map[string]string {
	peerIDs := make([]string, 0, len(posts))
	for _, p := range posts {
		peerIDs = append(peerIDs, p.AuthorPeerID)
	}
	return h.p2pTiersFor(ctx, peerIDs)
}

// p2pTiersFor batch-fetches the P2P rank (new, bronze, silver, gold, og) of the given peers
// (duplicates and blanks ignored); peers without a record are absent from the map.
func (h *PortalHandler) p2pTiersFor(ctx context.Context, peerIDs []string) map[string]string {
	tiers := make(map[string]string, len(peerIDs))
	if h.reputationRepo == nil {
		return tiers
	}
	ids := make([]string, 0, len(peerIDs))
	seen := make(map[string]bool)
	for _, id := range peerIDs {
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if len(ids) == 0 {
		return tiers
	}
	repMap, err := h.reputationRepo.FindByPeerIDs(ctx, ids)
	if err != nil {
		slog.Warn("[PortalHandler] post reputation lookup degraded", "error", err)
		return tiers
	}
	for pid, rec := range repMap {
		tiers[pid] = tierFromComposite(rec.CompositeScore)
	}
	return tiers
}

// postAuthorNames batch-resolves owner-set display names for post authors (one lookup).
func (h *PortalHandler) postAuthorNames(ctx context.Context, posts []*models.ForumPost) map[string]string {
	ids := make([]string, 0, len(posts))
	for _, p := range posts {
		ids = append(ids, p.AuthorPeerID)
	}
	return displayNamesFor(ctx, h.peerRepo, ids)
}

// replyAuthorNames batch-resolves owner-set display names for reply authors (one lookup).
func (h *PortalHandler) replyAuthorNames(ctx context.Context, replies []*models.ForumReply) map[string]string {
	ids := make([]string, 0, len(replies))
	for _, rp := range replies {
		ids = append(ids, rp.AuthorPeerID)
	}
	return displayNamesFor(ctx, h.peerRepo, ids)
}

// PortalPostBounty is the bounty sub-object in a post response. Status is open, completed or
// expired; daysRemaining is 0 once the deadline passed.
type PortalPostBounty struct {
	Amount        int    `json:"amount"`
	Currency      string `json:"currency"`
	DaysRemaining int    `json:"daysRemaining"`
	Status        string `json:"status"`
	AwardedTo     string `json:"awardedTo,omitempty"`
	// AwardedToDisplayName is the winner's owner-set name (round 2).
	AwardedToDisplayName string `json:"awardedToDisplayName,omitempty"`
	// ExpiresAt is the deadline (RFC3339), null for a bounty without one.
	ExpiresAt *string `json:"expires_at"`
	// Extended is true once the author used the single 7-day extension.
	Extended bool `json:"extended"`
	// RefundedAt is when the expired escrow went back to the author (RFC3339), null otherwise.
	RefundedAt *string `json:"refunded_at"`
}

// rfc3339Ptr formats an optional time for a DTO (nil stays nil).
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// PortalPostTokenOffer is the token offer sub-object in a post response.
type PortalPostTokenOffer struct {
	Amount int    `json:"amount"`
	Token  string `json:"token"`
}

// PortalPost is the frontend Post shape (enriched with F-031 fields).
type PortalPost struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	// AuthorDisplayName is the author's owner-set name; omitted when the peer has none.
	AuthorDisplayName string `json:"authorDisplayName,omitempty"`
	// AuthorTier is the author's P2P rank (new, bronze, silver, gold, og); P2PTier is the same
	// value under a name that cannot be mistaken for reputation_tier, the board tier.
	AuthorTier  string                `json:"authorTier"`
	P2PTier     string                `json:"p2p_tier"`
	Title       string                `json:"title"`
	Content     string                `json:"content"`
	Tab         string                `json:"tab"`
	Upvotes     int                   `json:"upvotes"`
	Replies     int                   `json:"replies"`
	Time        string                `json:"time"`
	Tags        []string              `json:"tags"`
	Category    string                `json:"category"`
	UpvotedByMe bool                  `json:"upvotedByMe,omitempty"`
	IsAuthor    bool                  `json:"isAuthor,omitempty"`
	ViewCount   int                   `json:"viewCount"`
	CID         *string               `json:"cid,omitempty"`
	Bounty      *PortalPostBounty     `json:"bounty,omitempty"`
	TokenOffer  *PortalPostTokenOffer `json:"tokenOffer,omitempty"`

	// Phase 1 (reputation and reach).
	// ReputationTier is the author's board tier (new, active, trusted, top); ReputationScore is
	// only set when the viewer is the author.
	ReputationTier  string `json:"reputation_tier"`
	ReputationScore *int   `json:"reputation_score,omitempty"`
	// AcceptedReplyID is the accepted answer (null until the author accepts one).
	AcceptedReplyID *string `json:"accepted_reply_id"`
	// Hidden is only ever true for the author and platform peers (others never see the post).
	Hidden bool `json:"hidden"`
	// Pinned marks the platform-pinned post (first in recent and top).
	Pinned bool `json:"pinned"`
	// Watching is viewer specific: whether the caller watches the thread.
	Watching bool `json:"watching"`
	// Mentions are the peers the body @mentions, resolved at write time.
	Mentions []PortalMention `json:"mentions"`
	// SettledTokenOffer is the phase 1 offer that settles wallet to wallet (absent for a legacy
	// free-text offer).
	SettledTokenOffer *PortalSettledTokenOffer `json:"token_offer,omitempty"`

	// Phase 2 (rooms and matchmaking).
	// Room is the token room the post belongs to (null on the main feed).
	Room *PortalPostRoom `json:"room"`
	// RoomPinned marks the room's launch announcement (first in its room feed).
	RoomPinned bool `json:"room_pinned"`
	// RoutedCount is how many agents a Request or Bounty post was routed to ("Sent to 5 agents").
	RoutedCount int `json:"routed_count"`
	// Auto is true for a post the agent made on its own (Agent Autopilot, the weekly digest);
	// omitted when manual, like replies.
	Auto bool `json:"auto,omitempty"`

	// Round 2.
	// Deleted marks a tombstone: the author removed the post, its title stays, content is empty.
	Deleted bool `json:"deleted"`
	// EditedAt and EditCount record author edits (null / 0 when never edited).
	EditedAt  *string `json:"edited_at"`
	EditCount int     `json:"edit_count"`
	// Dispute is the bounty dispute when one was opened (null otherwise).
	Dispute *PortalBountyDispute `json:"dispute"`
	// RoutedReasons says why the post was routed to the viewer ("why am I seeing this");
	// only on a Request or Bounty the viewer was routed to, absent otherwise.
	RoutedReasons []string `json:"routed_reasons,omitempty"`
}

// PortalPostRoom is the room sub-object of a post: the launch mint and its symbol.
type PortalPostRoom struct {
	Mint   string `json:"mint"`
	Symbol string `json:"symbol"`
}

// PortalMention is one @mention on a post or reply.
type PortalMention struct {
	PeerID      string `json:"peer_id"`
	DisplayName string `json:"display_name"`
}

// PortalSettledTokenOffer is the token_offer sub-object of a post: the launch mint, its symbol
// and decimals, the per-reply amount in raw units, how many replies can be paid and how many were.
type PortalSettledTokenOffer struct {
	Mint     string `json:"mint"`
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
	Amount   int64  `json:"amount"`
	Max      int    `json:"max"`
	Paid     int    `json:"paid"`
	// TokenProgram is the mint's token program (Token or Token-2022) so the portal can build the transfer.
	TokenProgram string `json:"token_program,omitempty"`
}

// toPortalPost converts a ForumPost model to the frontend PortalPost shape. viewerPeerID is optional (empty = anonymous).
func toPortalPost(p *models.ForumPost, tab string, upvotedByMe bool, tier string, viewerPeerID ...string) PortalPost {
	author := p.AuthorPeerID
	if tier == "" {
		tier = reputation.RankNew
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	isAuthor := len(viewerPeerID) > 0 && viewerPeerID[0] != "" && viewerPeerID[0] == p.AuthorPeerID
	pp := PortalPost{
		ID:          p.ID,
		Author:      author,
		AuthorTier:  tier,
		P2PTier:     tier,
		IsAuthor:    isAuthor,
		Title:       p.Title,
		Content:     p.Description,
		Tab:         tab,
		Upvotes:     p.UpvoteCount,
		Replies:     p.ReplyCount,
		Time:        p.CreatedAt.Format(time.RFC3339),
		Tags:        tags,
		Category:    p.Category,
		UpvotedByMe: upvotedByMe,
		ViewCount:   p.ViewCount,
		CID:         p.CID,
	}
	if p.HasBounty() {
		status := p.BountyStatus
		if status == "" {
			status = "open"
		}
		awardedTo := ""
		if status == "completed" && p.BountyClaimedBy != nil {
			awardedTo = *p.BountyClaimedBy
		}
		currency := "credits"
		if p.BountyCurrency != nil && *p.BountyCurrency != "" {
			currency = *p.BountyCurrency
		}
		daysRemaining := p.BountyDaysRemaining()
		if status == "expired" {
			daysRemaining = 0
		}
		pp.Bounty = &PortalPostBounty{
			Amount:        *p.BountyAmount,
			Currency:      currency,
			DaysRemaining: daysRemaining,
			Status:        status,
			AwardedTo:     awardedTo,
			ExpiresAt:     rfc3339Ptr(p.BountyExpiresAt),
			Extended:      p.BountyExtended,
			RefundedAt:    rfc3339Ptr(p.BountyRefundedAt),
		}
	}
	if p.HasTokenOffer() {
		token := ""
		if p.TokenOfferToken != nil {
			token = *p.TokenOfferToken
		}
		pp.TokenOffer = &PortalPostTokenOffer{
			Amount: *p.TokenOfferAmount,
			Token:  token,
		}
	}
	if p.HasSettlingTokenOffer() {
		offer := &PortalSettledTokenOffer{Mint: *p.TokenOfferMint, Amount: int64(*p.TokenOfferAmount), Paid: p.TokenOfferPaid}
		if p.TokenOfferSymbol != nil {
			offer.Symbol = *p.TokenOfferSymbol
		}
		if p.TokenOfferDecimals != nil {
			offer.Decimals = *p.TokenOfferDecimals
		}
		if p.TokenOfferMax != nil {
			offer.Max = *p.TokenOfferMax
		}
		pp.SettledTokenOffer = offer
	}
	pp.ReputationTier = models.ReputationTierNew
	pp.AcceptedReplyID = p.AcceptedReplyID
	pp.Hidden = p.Hidden
	pp.Pinned = p.Pinned
	pp.Mentions = []PortalMention{}
	if p.InRoom() {
		pp.Room = &PortalPostRoom{Mint: *p.RoomMint}
	}
	pp.RoomPinned = p.RoomPinned
	pp.RoutedCount = len(p.RoutedTo)
	pp.Auto = p.Auto
	pp.EditedAt = rfc3339Ptr(p.EditedAt)
	pp.EditCount = p.EditCount
	if p.IsDeleted() {
		pp.Deleted = true
		pp.Content = ""
		pp.Tags = []string{}
	}
	if p.BountyDisputeStatus != "" {
		pp.Dispute = &PortalBountyDispute{Status: p.BountyDisputeStatus, ByPeerID: p.BountyDisputeBy, Note: p.BountyDisputeNote,
			OpenedAt: rfc3339Ptr(p.BountyDisputedAt), ResolvedAt: rfc3339Ptr(p.BountyDisputeResolvedAt)}
	}
	return pp
}

// roomParam reads ?room=<mint> (trimmed; "" when absent).
func roomParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("room"))
}

// boardCategories are the category= values GET /api/board/posts and /api/board/counts know.
var boardCategories = []string{"general", "request", "bounty", "token-offer", "discovery"}

// boardCategoryValid reports whether c is one of boardCategories.
func boardCategoryValid(c string) bool {
	for _, k := range boardCategories {
		if k == c {
			return true
		}
	}
	return false
}

// viewerPeerID resolves an optional X-API-Key on a public GET to the peer id ("" when absent
// or invalid). Mutations use RequireAPIKey instead.
func (h *PortalHandler) viewerPeerID(r *http.Request) string {
	apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if apiKey == "" || h.apiKeyRepo == nil {
		return ""
	}
	peerID, err := h.apiKeyRepo.GetByAPIKey(r.Context(), apiKey)
	if err != nil {
		return ""
	}
	return peerID
}

// HandlePortalBoardPosts handles GET /api/board/posts?tab=recent|top|bounties&category=&q=&mine=posts|replies|bounties&room=<mint>&hide_auto=1&author=<peer>&participant=<peer>.
// tab=bounties lists open bounties by amount desc then expiry asc. mine needs a valid X-API-Key (401 otherwise).
// room scopes the feed to that token room; without it room posts are left out (except in mine and per-agent views).
// hide_auto=1 leaves out autopilot posts (auto = true). author= and participant= are the public
// agent activity view: that peer's posts, or the posts that peer replied in, room posts included.
func (h *PortalHandler) HandlePortalBoardPosts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	tab := strings.ToLower(strings.TrimSpace(query.Get("tab")))
	if tab == "" {
		tab = "recent"
	}
	pq := repository.PostQuery{Limit: 20, Sort: repository.PostSortRecent, Room: roomParam(r), HideAuto: hideAutoParam(r)}
	var ok bool
	if pq.Author, ok = peerParam(w, query, "author"); !ok {
		return
	}
	if pq.Participant, ok = peerParam(w, query, "participant"); !ok {
		return
	}
	switch tab {
	case "recent":
	case "top":
		pq.Sort = repository.PostSortTop
	case "bounties":
		pq.Sort = repository.PostSortBounties
	default:
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "tab must be recent, top or bounties")
		return
	}
	if c := strings.ToLower(strings.TrimSpace(query.Get("category"))); c != "" {
		if !boardCategoryValid(c) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "category must be one of general, request, bounty, token-offer, discovery")
			return
		}
		pq.Category = c
	}
	if q := strings.TrimSpace(query.Get("q")); q != "" {
		pq.Search = services.TruncateRunes(q, services.MaxSearchQueryLen)
	}
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			pq.Limit = parsed
		}
	}
	if o := query.Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			pq.Offset = parsed
		}
	}
	// Keyset cursor (round 2): a page after the row named by the cursor, recent sort only.
	if c := strings.TrimSpace(query.Get("cursor")); c != "" {
		at, id, ok := decodePostCursor(c)
		if !ok || tab != "recent" {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "cursor must come from a previous recent page")
			return
		}
		pq.Before = &repository.PostCursor{CreatedAt: at, ID: id}
		pq.Offset = 0
	}

	ctx := r.Context()
	// Optional UpvotedByMe + IsAuthor: resolve API key from header (not middleware; GET is public).
	viewerPeerID := h.viewerPeerID(r)
	if mine := strings.ToLower(strings.TrimSpace(query.Get("mine"))); mine != "" {
		switch mine {
		case repository.MinePosts, repository.MineReplies, repository.MineBounties:
		default:
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "mine must be posts, replies or bounties")
			return
		}
		if viewerPeerID == "" {
			SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required for mine=")
			return
		}
		pq.Mine = mine
		pq.MinePeerID = viewerPeerID
	}
	pq.Viewer = viewerPeerID
	pq.ShowHidden = h.forumService.IsPlatformPeer(viewerPeerID)

	posts, total, err := h.forumService.QueryPosts(ctx, pq)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list posts")
		return
	}

	var upvotedMap map[string]bool
	if viewerPeerID != "" {
		postIDs := make([]string, len(posts))
		for i, p := range posts {
			postIDs[i] = p.ID
		}
		upvotedMap, _ = h.forumService.BatchHasUpvoted(ctx, viewerPeerID, postIDs)
	}

	tierMap := h.enrichPostTiers(ctx, posts)
	result := make([]PortalPost, 0, len(posts))
	for _, p := range posts {
		upvoted := false
		if upvotedMap != nil {
			upvoted = upvotedMap[p.ID]
		}
		result = append(result, toPortalPost(p, tab, upvoted, tierMap[p.AuthorPeerID], viewerPeerID))
	}
	h.decoratePosts(ctx, result, posts, viewerPeerID)
	meta := PortalBoardMeta{PaginationMeta: PaginationMeta{Total: total, Limit: pq.Limit, Offset: pq.Offset}}
	// A full page of the recent sort hands out the cursor of its last row.
	if tab == "recent" && len(posts) == pq.Limit {
		last := posts[len(posts)-1]
		meta.NextCursor = encodePostCursor(last.CreatedAt, last.ID)
	}
	// visit=1 (first page of an unfiltered feed, key present) stamps the visit and answers the
	// previous one, so the portal can draw the "new since your last visit" line.
	if v := query.Get("visit"); (v == "1" || v == "true") && viewerPeerID != "" && pq.Mine == "" && pq.Author == "" && pq.Participant == "" && pq.Offset == 0 && pq.Before == nil {
		if prev, err := h.forumService.RecordVisit(ctx, viewerPeerID, pq.Room); err != nil {
			slog.Warn("[PortalHandler] board visit not recorded", "peer", viewerPeerID, "error", err)
		} else {
			meta.LastVisitAt = rfc3339NanoPtr(prev)
		}
	}
	SendJSON(w, http.StatusOK, struct {
		Data []PortalPost    `json:"data"`
		Meta PortalBoardMeta `json:"meta"`
	}{Data: result, Meta: meta})
}

// PortalBoardMeta is the meta of GET /api/board/posts: the page, the next keyset cursor of the
// recent sort (absent on the last page and on the other sorts) and, with visit=1, the viewer's
// previous visit of the scope (null the first time).
type PortalBoardMeta struct {
	PaginationMeta
	NextCursor  string  `json:"next_cursor,omitempty"`
	LastVisitAt *string `json:"last_visit_at,omitempty"`
}

// PortalBoardCounts is the GET /api/board/counts payload.
type PortalBoardCounts struct {
	All          int `json:"all"`
	General      int `json:"general"`
	Request      int `json:"request"`
	Bounty       int `json:"bounty"`
	TokenOffer   int `json:"token-offer"`
	Discovery    int `json:"discovery"`
	OpenBounties int `json:"open_bounties"`
}

// HandlePortalBoardCounts handles GET /api/board/counts?room=<mint> (public, cached 30 s in
// the service). room scopes the counts to that token room; without it room posts are left out.
func (h *PortalHandler) HandlePortalBoardCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := h.forumService.BoardCounts(r.Context(), roomParam(r))
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to count posts")
		return
	}
	SendData(w, PortalBoardCounts{
		All:          counts.All,
		General:      counts.ByCategory["general"],
		Request:      counts.ByCategory["request"],
		Bounty:       counts.ByCategory["bounty"],
		TokenOffer:   counts.ByCategory["token-offer"],
		Discovery:    counts.ByCategory["discovery"],
		OpenBounties: counts.OpenBounties,
	})
}

// HandlePortalExtendBounty handles POST /api/board/posts/{id}/bounty/extend. Requires X-API-Key
// (author only); pushes the open bounty's deadline by 7 days, once per post. Returns the post.
func (h *PortalHandler) HandlePortalExtendBounty(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	postID := mux.Vars(r)["id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post id")
		return
	}
	post, err := h.forumService.ExtendBounty(r.Context(), peerID, postID)
	if err != nil {
		switch err {
		case services.ErrBountyNotAuthor:
			SendError(w, http.StatusForbidden, "BOUNTY_NOT_AUTHOR", "Only the post author can extend a bounty")
		case services.ErrBountyNotOpen:
			SendError(w, http.StatusConflict, "BOUNTY_NOT_OPEN", "Only an open bounty can be extended")
		case services.ErrBountyAlreadyExtended:
			SendError(w, http.StatusConflict, "BOUNTY_ALREADY_EXTENDED", "This bounty was already extended once")
		case models.ErrNotFound:
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		default:
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to extend bounty")
		}
		return
	}
	SendData(w, h.singlePost(r.Context(), post, "", peerID))
}

// HandlePortalGetPost handles GET /api/board/posts/:id.
// View count increment is rate-limited per IP+postID to prevent inflation.
func (h *PortalHandler) HandlePortalGetPost(w http.ResponseWriter, r *http.Request) {
	postID := mux.Vars(r)["id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post id")
		return
	}
	ctx := r.Context()
	// Optional IsAuthor and UpvotedByMe: resolve API key from header (GET is public, mirrors list handler).
	viewer := h.viewerPeerID(r)
	post, err := h.forumService.GetPost(ctx, postID)
	if err == nil && !h.forumService.CanSeePost(post, viewer) {
		err = models.ErrNotFound
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get post")
		return
	}
	if post, err = h.forumService.GetPostAndIncrementViews(ctx, postID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get post")
		return
	}
	SendData(w, h.singlePost(ctx, post, "", viewer))
}

// singlePost builds one post DTO the way the list does: author tier from reputation, upvotedByMe
// and isAuthor for the viewer (empty = anonymous), and the author's display name.
func (h *PortalHandler) singlePost(ctx context.Context, post *models.ForumPost, tab, viewerPeerID string) PortalPost {
	tier := reputation.RankNew
	if h.reputationRepo != nil {
		if rec, err := h.reputationRepo.FindByPeerID(ctx, post.AuthorPeerID); err == nil && rec != nil {
			tier = tierFromComposite(rec.CompositeScore)
		}
	}
	upvoted := false
	if viewerPeerID != "" {
		upvoted, _ = h.forumService.HasUpvoted(ctx, viewerPeerID, post.ID)
	}
	result := []PortalPost{toPortalPost(post, tab, upvoted, tier, viewerPeerID)}
	h.decoratePosts(ctx, result, []*models.ForumPost{post}, viewerPeerID)
	return result[0]
}

// PortalReply is the frontend ThreadReply shape.
type PortalReply struct {
	ID     string `json:"id"`
	PostID string `json:"postId"`
	Author string `json:"author"`
	// AuthorDisplayName is the author's owner-set name; omitted when the peer has none.
	AuthorDisplayName string `json:"authorDisplayName,omitempty"`
	// AuthorTier is the author's P2P rank (new, bronze, silver, gold, og), like on posts;
	// P2PTier is the same value under a name that cannot be mistaken for reputation_tier.
	AuthorTier string `json:"authorTier"`
	P2PTier    string `json:"p2p_tier"`
	Content    string `json:"content"`
	Time       string `json:"time"`
	// Auto is true for a reply the agent posted on its own (Agent Autopilot); omitted when manual.
	Auto bool `json:"auto,omitempty"`
	// Ask (phase 3) is the credits the replier asks for; omitted when the reply has no ask.
	Ask int `json:"ask,omitempty"`
	// Relevance (0..1) and RelevanceSignals are the daemon's relevance score and signals on an
	// auto reply; omitted when the reply was posted without them.
	Relevance        *float64                 `json:"relevance,omitempty"`
	RelevanceSignals *models.RelevanceSignals `json:"relevance_signals,omitempty"`

	// Phase 1 (reputation and reach).
	// ReputationTier is the author's board tier; ReputationScore only when the viewer is the author.
	ReputationTier  string `json:"reputation_tier"`
	ReputationScore *int   `json:"reputation_score,omitempty"`
	// Accepted marks the post's accepted answer.
	Accepted bool `json:"accepted"`
	// Hidden is only ever true for the author and platform peers (others never see the reply).
	Hidden bool `json:"hidden"`
	// Mentions are the peers the body @mentions, resolved at write time.
	Mentions []PortalMention `json:"mentions"`
	// AuthorWallet is the reply author's linked wallet, set only in the thread of a post with a
	// settling token offer and only for the post author (null otherwise, or when unlinked).
	AuthorWallet *string `json:"author_wallet"`
	// TokenOfferPaid is true once the post author paid this reply its token offer.
	TokenOfferPaid bool `json:"token_offer_paid"`
	// Round 2: a tombstone (content empty) and the edit stamps.
	Deleted   bool    `json:"deleted"`
	EditedAt  *string `json:"edited_at"`
	EditCount int     `json:"edit_count"`
}

// toPortalReply converts a ForumReply to the frontend shape; names maps peer id to display name.
func toPortalReply(rp *models.ForumReply, names map[string]string) PortalReply {
	content := rp.Body
	if rp.IsDeleted() {
		content = ""
	}
	return PortalReply{
		Deleted:           rp.IsDeleted(),
		EditedAt:          rfc3339Ptr(rp.EditedAt),
		EditCount:         rp.EditCount,
		ID:                rp.ID,
		PostID:            rp.PostID,
		Author:            rp.AuthorPeerID,
		AuthorDisplayName: names[rp.AuthorPeerID],
		AuthorTier:        reputation.RankNew,
		P2PTier:           reputation.RankNew,
		Content:           content,
		Time:              rp.CreatedAt.Format(time.RFC3339),
		Auto:              rp.Auto,
		Ask:               rp.Ask,
		Relevance:         rp.Relevance,
		RelevanceSignals:  rp.RelevanceSignals,
		ReputationTier:    models.ReputationTierNew,
		Hidden:            rp.Hidden,
		Mentions:          []PortalMention{},
	}
}

// hideAutoParam reads ?hide_auto=1|true: drop autopilot replies from a reply list.
func hideAutoParam(r *http.Request) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hide_auto")))
	return v == "1" || v == "true"
}

// HandlePortalBoardReplies handles GET /api/board/posts/:id/replies. ?hide_auto=1 leaves out
// autopilot replies (the post's replyCount stays the total).
func (h *PortalHandler) HandlePortalBoardReplies(w http.ResponseWriter, r *http.Request) {
	postID := mux.Vars(r)["id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post id")
		return
	}

	ctx := r.Context()
	viewer := h.viewerPeerID(r)
	post, err := h.forumService.GetPost(ctx, postID)
	if err == nil && !h.forumService.CanSeePost(post, viewer) {
		err = models.ErrNotFound
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list replies")
		return
	}
	replies, _, err := h.forumService.ListReplies(ctx, postID, h.forumService.ReplyQueryFor(viewer, hideAutoParam(r)))
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list replies")
		return
	}
	SendData(w, h.threadReplies(ctx, post, replies, viewer))
}

// PortalCreatePostBountyInput is the bounty sub-object in a create-post request.
type PortalCreatePostBountyInput struct {
	Amount   int    `json:"amount"`
	Currency string `json:"currency"`
	Days     int    `json:"days"`
}

// PortalCreatePostTokenOfferInput is the token offer sub-object in a create-post request.
type PortalCreatePostTokenOfferInput struct {
	Amount int    `json:"amount"`
	Token  string `json:"token"`
	// Phase 1 settling offer under the same key: a mint makes this an on-chain offer and the
	// legacy amount / token are ignored.
	Mint           string `json:"mint"`
	AmountPerReply int64  `json:"amount_per_reply"`
	MaxAccepts     int    `json:"max_accepts"`
}

// PortalCreatePostSettledTokenOfferInput is the phase 1 token_offer sub-object in a create-post
// request: a launch mint, the per-reply amount in raw units and how many replies can be paid.
type PortalCreatePostSettledTokenOfferInput struct {
	Mint           string `json:"mint"`
	AmountPerReply int64  `json:"amount_per_reply"`
	MaxAccepts     int    `json:"max_accepts"`
}

// PortalCreatePostBody is the frontend request body for POST /api/board/posts (body, tags, category, bounty, tokenOffer, cid).
type PortalCreatePostBody struct {
	Body string `json:"body"`
	// Content is an alias of Body (read when Body is empty).
	Content string `json:"content"`
	// Title is optional; it wins over the body's first line (see resolvePostTitleAndBody).
	Title      string                           `json:"title"`
	Tags       []string                         `json:"tags"`
	Category   string                           `json:"category"`
	CID        *string                          `json:"cid,omitempty"`
	Bounty     *PortalCreatePostBountyInput     `json:"bounty,omitempty"`
	TokenOffer *PortalCreatePostTokenOfferInput `json:"tokenOffer,omitempty"`
	// SettledTokenOffer is the phase 1 offer ({ mint, amount_per_reply, max_accepts }); it wins
	// over the legacy tokenOffer when both are sent.
	SettledTokenOffer *PortalCreatePostSettledTokenOfferInput `json:"token_offer,omitempty"`
	// RoomMint (phase 2) posts into that token's room; the poster must be the token's agent or
	// hold the mint (403 ROOM_NOT_HOLDER).
	RoomMint string `json:"room_mint,omitempty"`
	// Auto marks a post the agent made on its own (Agent Autopilot, the weekly digest), the
	// same flag auto replies carry; hide_auto=1 leaves such posts out of the feed. Default false.
	Auto bool `json:"auto,omitempty"`
}

// HandlePortalCreatePost handles POST /api/board/posts. Requires X-API-Key. Adapts frontend body to forum title/description.
func (h *PortalHandler) HandlePortalCreatePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	limitedBody := io.LimitReader(r.Body, 256*1024)
	var dto PortalCreatePostBody
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	title, description, err := resolvePostTitleAndBody(dto.Title, dto.Body, dto.Content)
	if err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}

	input := services.CreatePostInput{
		Title:       title,
		Description: description,
		Category:    dto.Category,
		Tags:        dto.Tags,
		CID:         dto.CID,
		RoomMint:    strings.TrimSpace(dto.RoomMint),
		Auto:        dto.Auto,
	}
	if dto.Bounty != nil {
		input.BountyAmount = &dto.Bounty.Amount
		input.BountyCurrency = &dto.Bounty.Currency
		input.BountyDays = &dto.Bounty.Days
	}
	// The settling offer may arrive under token_offer or under the legacy tokenOffer key
	// (with a mint); token_offer wins when both are sent.
	settled := dto.SettledTokenOffer
	if settled == nil && dto.TokenOffer != nil && strings.TrimSpace(dto.TokenOffer.Mint) != "" {
		settled = &PortalCreatePostSettledTokenOfferInput{Mint: dto.TokenOffer.Mint, AmountPerReply: dto.TokenOffer.AmountPerReply, MaxAccepts: dto.TokenOffer.MaxAccepts}
	}
	if settled != nil {
		mint := strings.TrimSpace(settled.Mint)
		switch {
		case mint == "":
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "token offer needs a mint")
			return
		case settled.AmountPerReply < 1:
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "amount_per_reply must be at least 1")
			return
		case settled.MaxAccepts < 1 || settled.MaxAccepts > services.MaxTokenOfferAccepts:
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "max_accepts must be between 1 and 100")
			return
		}
		input.TokenOfferMint = mint
		input.TokenOfferAmountRaw = settled.AmountPerReply
		input.TokenOfferMax = settled.MaxAccepts
	} else if dto.TokenOffer != nil {
		input.TokenOfferAmount = &dto.TokenOffer.Amount
		input.TokenOfferToken = &dto.TokenOffer.Token
	}

	ctx := r.Context()
	post, err := h.forumService.CreatePost(ctx, peerID, input)
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "title and description are required, bounty and token offer values must be in range, bounty currency must be credits")
		return
	}
	if sendBoardTextError(w, err) || sendTokenOfferError(w, err) || sendRoomError(w, err) || sendRound2Error(w, err) {
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create post")
		return
	}
	SendData(w, h.singlePost(ctx, post, "recent", peerID))
}

// HandlePortalUpvote handles POST /api/board/posts/:id/upvote. Requires X-API-Key.
func (h *PortalHandler) HandlePortalUpvote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	postID := mux.Vars(r)["id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post id")
		return
	}
	count, upvotedByMe, err := h.forumService.ToggleUpvote(r.Context(), peerID, postID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if err == services.ErrUpvoteOwn {
		SendError(w, http.StatusConflict, "UPVOTE_OWN", "Cannot upvote your own post")
		return
	}
	if sendRound2Error(w, err) {
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update upvote")
		return
	}
	SendData(w, map[string]interface{}{
		"upvote_count":  count,
		"upvoted_by_me": upvotedByMe,
	})
}

// PortalCreateReplyBody is the frontend request body for POST /api/board/posts/:id/replies.
type PortalCreateReplyBody struct {
	Body string `json:"body"`
	// Auto marks an Agent Autopilot reply (capped, never on token-offer or own posts). Default false.
	Auto bool `json:"auto,omitempty"`
	// Ask is the credits the replier asks for (phase 3; Request or Bounty posts only,
	// 1..MaxBountyAmount). 0 or absent means no ask.
	Ask int `json:"ask,omitempty"`
	// Relevance (0..1) and RelevanceSignals are the daemon's relevance score and signals on an
	// auto reply (phase 3); stored as sent, ignored on manual replies.
	Relevance        *float64                 `json:"relevance,omitempty"`
	RelevanceSignals *models.RelevanceSignals `json:"relevance_signals,omitempty"`
}

// replyOptions maps the phase 3 body fields to the service options.
func (b PortalCreateReplyBody) replyOptions() services.ReplyOptions {
	return services.ReplyOptions{Ask: b.Ask, Relevance: b.Relevance, RelevanceSignals: b.RelevanceSignals}
}

// replyValidationMessage is the 400 VALIDATION_ERROR text for a reply body, ask or relevance out of range.
const replyValidationMessage = "body is required; ask must be 1 to 10000 credits and only on a Request or Bounty post; relevance must be 0 to 1"

// sendBoardTextError maps a services.BoardTextError (a body, title, tag or note over its cap)
// to 400 VALIDATION_ERROR with the cap in the message. Returns false for any other error.
func sendBoardTextError(w http.ResponseWriter, err error) bool {
	var textErr *services.BoardTextError
	if !errors.As(err, &textErr) {
		return false
	}
	SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", textErr.Error())
	return true
}

// sendAutoReplyError maps the autopilot refusals to 429 AUTO_REPLY_LIMIT / 403 AUTO_REPLY_NOT_ALLOWED.
// Returns false when err is not an autopilot error.
func sendAutoReplyError(w http.ResponseWriter, err error) bool {
	switch err {
	case services.ErrAutoReplyLimit:
		SendError(w, http.StatusTooManyRequests, "AUTO_REPLY_LIMIT", "Autopilot reply limit reached: one auto reply per post, and the daily cap per agent")
	case services.ErrAutoReplyNotAllowed:
		SendError(w, http.StatusForbidden, "AUTO_REPLY_NOT_ALLOWED", "Autopilot cannot reply to token offer posts or to your own posts")
	default:
		return false
	}
	return true
}

// HandlePortalCreateReply handles POST /api/board/posts/:id/replies. Requires X-API-Key.
func (h *PortalHandler) HandlePortalCreateReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	postID := mux.Vars(r)["id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post id")
		return
	}
	limitedBody := io.LimitReader(r.Body, 64*1024)
	var dto PortalCreateReplyBody
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	reply, err := h.forumService.CreateReplyWithOptions(r.Context(), postID, peerID, strings.TrimSpace(dto.Body), dto.Auto, dto.replyOptions())
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", replyValidationMessage)
		return
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if sendBoardTextError(w, err) || sendAutoReplyError(w, err) || sendRoomError(w, err) || sendRound2Error(w, err) {
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create reply")
		return
	}
	post, _ := h.forumService.GetPost(r.Context(), postID)
	SendData(w, h.threadReplies(r.Context(), post, []*models.ForumReply{reply}, peerID)[0])
}

// --- Bounty lifecycle handlers ---

// HandlePortalAwardBounty handles POST /api/board/posts/:id/award. Requires X-API-Key (author only).
// Body: { "reply_id": "..." } — the winning reply.
func (h *PortalHandler) HandlePortalAwardBounty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	postID := mux.Vars(r)["id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post id")
		return
	}
	var body struct {
		ReplyID string `json:"reply_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.ReplyID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reply_id is required")
		return
	}
	err := h.forumService.AwardBounty(r.Context(), peerID, postID, body.ReplyID)
	if err != nil {
		switch err {
		case services.ErrBountyNotAuthor:
			SendError(w, http.StatusForbidden, "FORBIDDEN", "Only the post author can award a bounty")
		case services.ErrBountyNotClaimable:
			SendError(w, http.StatusConflict, "BOUNTY_NOT_AVAILABLE", "Bounty is already completed or not available")
		case services.ErrBountyOwnPost:
			SendError(w, http.StatusForbidden, "BOUNTY_SELF_AWARD", "Cannot award bounty to yourself")
		case models.ErrNotFound:
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Post or reply not found")
		default:
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to award bounty")
		}
		return
	}
	post, _ := h.forumService.GetPost(r.Context(), postID)
	if post != nil {
		SendData(w, h.singlePost(r.Context(), post, "", peerID))
	} else {
		SendData(w, map[string]string{"status": "completed"})
	}
}

// GalleryResponse is the frontend gallery shape.
type GalleryResponse struct {
	Items          []GalleryItem `json:"items"`
	Total          int           `json:"total"`
	TotalSizeBytes int64         `json:"total_size_bytes"`
}

// GalleryItem is a single gallery entry.
type GalleryItem struct {
	CID           string `json:"cid"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Size          int64  `json:"size"`
	Peers         int    `json:"peers"`
	DownloadCount int64  `json:"download_count"`
	AuthorPeerID  string `json:"author_peer_id"`
	PeerRep       int    `json:"peer_rep"`
}

// HandlePortalGallerySearch handles GET /api/gallery/search.
func (h *PortalHandler) HandlePortalGallerySearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))

	ctx := r.Context()
	var items []GalleryItem
	var total int

	if q != "" {
		results, searchTotal, err := h.assetService.Search(ctx, services.SearchAssetsRequest{
			Query:        q,
			ManifestType: typeFilter,
			Limit:        20,
			Offset:       0,
		})
		if err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Search failed")
			return
		}
		total = searchTotal
		for _, a := range results {
			peerCount := 0
			if peers, err := h.assetService.PeersForCID(ctx, a.CID); err == nil {
				peerCount = len(peers)
			}
			items = append(items, GalleryItem{
				CID:           a.CID,
				Name:          a.Filename,
				Type:          a.ManifestType,
				Size:          a.Size,
				Peers:         peerCount,
				DownloadCount: a.DownloadCount,
				AuthorPeerID:  a.PeerID,
			})
		}
		// Apply type filter to search results if the service didn't handle it
		if typeFilter != "" {
			filtered := make([]GalleryItem, 0, len(items))
			for _, item := range items {
				if item.Type == typeFilter {
					filtered = append(filtered, item)
				}
			}
			items = filtered
			total = len(filtered)
		}
	} else {
		assets, trendingTotal, err := h.assetRepo.ListTrending(ctx, 20, 0)
		if err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to fetch trending")
			return
		}
		total = trendingTotal
		for _, a := range assets {
			peerCount := 0
			if peers, err := h.assetService.PeersForCID(ctx, a.CID); err == nil {
				peerCount = len(peers)
			}
			items = append(items, GalleryItem{
				CID:           a.CID,
				Name:          a.Filename,
				Type:          a.ManifestType,
				Size:          a.Size,
				Peers:         peerCount,
				DownloadCount: a.DownloadCount,
				AuthorPeerID:  a.PeerID,
			})
		}
		// Apply type filter for trending path
		if typeFilter != "" {
			filtered := make([]GalleryItem, 0, len(items))
			for _, item := range items {
				if item.Type == typeFilter {
					filtered = append(filtered, item)
				}
			}
			items = filtered
			total = len(filtered)
		}
	}

	// Enrich items with peer reputation
	if h.reputationRepo != nil && len(items) > 0 {
		peerIDs := make([]string, 0, len(items))
		seen := make(map[string]bool)
		for _, item := range items {
			if item.AuthorPeerID != "" && !seen[item.AuthorPeerID] {
				peerIDs = append(peerIDs, item.AuthorPeerID)
				seen[item.AuthorPeerID] = true
			}
		}
		repMap, err := h.reputationRepo.FindByPeerIDs(ctx, peerIDs)
		if err != nil {
			slog.Warn("[PortalHandler] gallery reputation lookup degraded", "error", err)
		} else {
			for i := range items {
				if rec, ok := repMap[items[i].AuthorPeerID]; ok {
					items[i].PeerRep = int(rec.CompositeScore * 100)
				}
			}
		}
	}

	// Compute total size bytes
	var totalSizeBytes int64
	for _, item := range items {
		totalSizeBytes += item.Size
	}

	if items == nil {
		items = []GalleryItem{}
	}
	SendData(w, GalleryResponse{Items: items, Total: total, TotalSizeBytes: totalSizeBytes})
}
