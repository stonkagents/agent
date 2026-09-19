// Package api: HTTP handlers for forum (posts, replies, upvotes). Mutations require X-API-Key.
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// ForumHandler handles forum endpoints.
type ForumHandler struct {
	svc        *services.ForumService
	apiKeyRepo repository.PeerAPIKeyRepository // optional: for upvoted_by_me on GET when X-API-Key present
	peerRepo   repository.PeerRepository       // optional: author_display_name on posts and replies
}

// NewForumHandler creates a new ForumHandler. apiKeyRepo is optional (for optional X-API-Key on GET post).
func NewForumHandler(svc *services.ForumService, apiKeyRepo repository.PeerAPIKeyRepository) *ForumHandler {
	return &ForumHandler{svc: svc, apiKeyRepo: apiKeyRepo}
}

// SetPeerRepo wires the peer repository used to resolve author display names (nil-safe).
func (h *ForumHandler) SetPeerRepo(repo repository.PeerRepository) {
	h.peerRepo = repo
}

// authorNames resolves display names and board reputation tiers for the authors of posts and
// replies in one lookup each.
func (h *ForumHandler) authorNames(ctx context.Context, posts []*models.ForumPost, replies []*models.ForumReply) (names, tiers map[string]string) {
	ids := make([]string, 0, len(posts)+len(replies))
	for _, p := range posts {
		ids = append(ids, p.AuthorPeerID)
	}
	for _, rp := range replies {
		ids = append(ids, rp.AuthorPeerID)
	}
	names = displayNamesFor(ctx, h.peerRepo, ids)
	tiers = map[string]string{}
	if h.svc != nil && h.svc.Reputation() != nil {
		tiers = h.svc.Reputation().TiersByIDs(ctx, ids)
	}
	return names, tiers
}

// CreatePostDTO is the request body for creating a post (author from API key). title is
// optional (derived from the first line otherwise); body and content are aliases of
// description, read in that order (see resolvePostTitleAndBody).
type CreatePostDTO struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Content     string `json:"content"`
}

// CreateReplyDTO is the request body for creating a reply (author from API key).
type CreateReplyDTO struct {
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

// PostDTO is the response shape for a post.
type PostDTO struct {
	ID           string `json:"id"`
	AuthorPeerID string `json:"author_peer_id"`
	// AuthorDisplayName is the author's owner-set name; omitted when the peer has none.
	AuthorDisplayName string `json:"author_display_name,omitempty"`
	// AuthorReputationTier is the author's community board tier (new, active, trusted, top).
	AuthorReputationTier string `json:"author_reputation_tier,omitempty"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
	UpvoteCount          int    `json:"upvote_count"`
	ReplyCount           int    `json:"reply_count,omitempty"`
	UpvotedByMe          bool   `json:"upvoted_by_me,omitempty"`
}

// ReplyDTO is the response shape for a reply.
type ReplyDTO struct {
	ID           string `json:"id"`
	PostID       string `json:"post_id"`
	AuthorPeerID string `json:"author_peer_id"`
	// AuthorDisplayName is the author's owner-set name; omitted when the peer has none.
	AuthorDisplayName string `json:"author_display_name,omitempty"`
	// AuthorReputationTier is the author's community board tier (new, active, trusted, top).
	AuthorReputationTier string `json:"author_reputation_tier,omitempty"`
	Body                 string `json:"body"`
	CreatedAt            string `json:"created_at"`
	// Auto is true for a reply the agent posted on its own (Agent Autopilot); omitted when manual.
	Auto bool `json:"auto,omitempty"`
	// Ask is the credits the replier asks for (phase 3); omitted when the reply has no ask.
	Ask int `json:"ask,omitempty"`
	// Relevance (0..1) and RelevanceSignals are the daemon's relevance score and signals on an
	// auto reply; omitted when the reply was posted without them.
	Relevance        *float64                 `json:"relevance,omitempty"`
	RelevanceSignals *models.RelevanceSignals `json:"relevance_signals,omitempty"`
}

// toReplyDTO converts a ForumReply to the v1 shape; names maps peer id to display name.
func toReplyDTO(rp *models.ForumReply, names, tiers map[string]string) ReplyDTO {
	return ReplyDTO{
		ID:                   rp.ID,
		PostID:               rp.PostID,
		AuthorPeerID:         rp.AuthorPeerID,
		AuthorDisplayName:    names[rp.AuthorPeerID],
		AuthorReputationTier: tiers[rp.AuthorPeerID],
		Body:                 rp.Body,
		CreatedAt:            rp.CreatedAt.Format("2006-01-02T15:04:05Z"),
		Auto:                 rp.Auto,
		Ask:                  rp.Ask,
		Relevance:            rp.Relevance,
		RelevanceSignals:     rp.RelevanceSignals,
	}
}

// AutopilotOutcomeDTO is one row of GET /autopilot/outcomes: an auto reply and what happened
// to it and to the post it landed on (phase 3 shape; snake_case like the ledger row the
// daemon keeps). upvotes is the post's count (replies carry no upvotes of their own).
type AutopilotOutcomeDTO struct {
	ReplyID  string `json:"reply_id"`
	PostID   string `json:"post_id"`
	Category string `json:"category"`
	// BountyAmount is the post's bounty, 0 when it has none.
	BountyAmount int    `json:"bounty_amount"`
	PostedAt     string `json:"posted_at"`
	Upvotes      int    `json:"upvotes"`
	// Accepted is true when the post author accepted this reply as the answer.
	Accepted bool `json:"accepted"`
	// Awarded is true when the post's bounty was awarded to the calling peer; AwardedAmount is
	// then the bounty (0 otherwise).
	Awarded       bool `json:"awarded"`
	AwardedAmount int  `json:"awarded_amount"`
	// Hidden is the reply's hidden flag; Reported is true while the reply has an open or
	// upheld report.
	Hidden   bool `json:"hidden"`
	Reported bool `json:"reported"`
	// Relevance and RelevanceSignals echo what the daemon sent with the reply (absent when none).
	Relevance        *float64                 `json:"relevance,omitempty"`
	RelevanceSignals *models.RelevanceSignals `json:"relevance_signals,omitempty"`
}

// HandleAutopilotOutcomes handles GET /autopilot/outcomes?since=<RFC3339>. Requires X-API-Key;
// returns the calling peer's auto replies from the last 30 days (or since, when later),
// newest first, as { data: [...] }.
func (h *ForumHandler) HandleAutopilotOutcomes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	var since *time.Time
	if s := strings.TrimSpace(r.URL.Query().Get("since")); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "since must be RFC3339")
			return
		}
		since = &t
	}
	outcomes, err := h.svc.ListAutopilotOutcomes(r.Context(), peerID, since)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list autopilot outcomes")
		return
	}
	dtos := make([]AutopilotOutcomeDTO, 0, len(outcomes))
	for _, o := range outcomes {
		bounty := 0
		if o.BountyAmount != nil {
			bounty = *o.BountyAmount
		}
		dtos = append(dtos, AutopilotOutcomeDTO{
			ReplyID:          o.ReplyID,
			PostID:           o.PostID,
			Category:         o.Category,
			BountyAmount:     bounty,
			PostedAt:         o.PostedAt.UTC().Format(time.RFC3339),
			Upvotes:          o.Upvotes,
			Accepted:         o.Accepted,
			Awarded:          o.Awarded,
			AwardedAmount:    o.AwardedAmount(),
			Hidden:           o.Hidden,
			Reported:         o.Reported,
			Relevance:        o.Relevance,
			RelevanceSignals: o.RelevanceSignals,
		})
	}
	SendData(w, dtos)
}

// HandleAutopilotOutcomesSummary handles GET /autopilot/outcomes/summary. Requires X-API-Key;
// returns the calling peer's 30-day autopilot totals as { data: {...} }.
func (h *ForumHandler) HandleAutopilotOutcomesSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	summary, err := h.svc.AutopilotOutcomeSummary(r.Context(), peerID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to summarize autopilot outcomes")
		return
	}
	SendData(w, summary)
}

// HandleCreatePost handles POST /forum/posts. Requires X-API-Key; author = peer from key.
func (h *ForumHandler) HandleCreatePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	limitedBody := io.LimitReader(r.Body, 256*1024) // 256KB max
	var dto CreatePostDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	title, description, err := resolvePostTitleAndBody(dto.Title, dto.Description, dto.Body, dto.Content)
	if err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	post, err := h.svc.CreatePost(r.Context(), peerID, services.CreatePostInput{
		Title:       title,
		Description: description,
	})
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "title and description are required")
		return
	}
	if sendBoardTextError(w, err) {
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create post")
		return
	}
	names, tiers := h.authorNames(r.Context(), []*models.ForumPost{post}, nil)
	SendJSON(w, http.StatusCreated, PostDTO{
		ID:                   post.ID,
		AuthorPeerID:         post.AuthorPeerID,
		AuthorDisplayName:    names[post.AuthorPeerID],
		AuthorReputationTier: tiers[post.AuthorPeerID],
		Title:                post.Title,
		Description:          post.Description,
		CreatedAt:            post.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:            post.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		UpvoteCount:          post.UpvoteCount,
		ReplyCount:           post.ReplyCount,
	})
}

// HandleListPosts handles GET /forum/posts. Public; no API key.
func (h *ForumHandler) HandleListPosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	limit, offset := parseLimitOffset(r, 20)
	sortNewest := strings.ToLower(r.URL.Query().Get("sort")) != "top"
	posts, total, err := h.svc.ListPosts(r.Context(), limit, offset, sortNewest)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list posts")
		return
	}
	names, tiers := h.authorNames(r.Context(), posts, nil)
	dtos := make([]PostDTO, 0, len(posts))
	for _, p := range posts {
		dtos = append(dtos, PostDTO{
			ID:                   p.ID,
			AuthorPeerID:         p.AuthorPeerID,
			AuthorDisplayName:    names[p.AuthorPeerID],
			AuthorReputationTier: tiers[p.AuthorPeerID],
			Title:                p.Title,
			Description:          p.Description,
			CreatedAt:            p.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:            p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			UpvoteCount:          p.UpvoteCount,
			ReplyCount:           p.ReplyCount,
		})
	}
	SendList(w, dtos, total)
}

// HandleGetPost handles GET /forum/posts/{post_id}. Public; optional peer_id query for upvoted_by_me (requires API key in header if provided).
func (h *ForumHandler) HandleGetPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	postID := mux.Vars(r)["post_id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post_id")
		return
	}
	peerID := h.viewer(r)
	post, err := h.svc.GetPost(r.Context(), postID)
	if err == nil && !h.svc.CanSeePost(post, peerID) {
		// Hidden posts are only there for their author and the platform, as on the portal route.
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
	upvotedByMe := false
	if peerID != "" {
		upvotedByMe, _ = h.svc.HasUpvoted(r.Context(), peerID, postID)
	}
	names, tiers := h.authorNames(r.Context(), []*models.ForumPost{post}, nil)
	SendJSON(w, http.StatusOK, PostDTO{
		ID:                   post.ID,
		AuthorPeerID:         post.AuthorPeerID,
		AuthorDisplayName:    names[post.AuthorPeerID],
		AuthorReputationTier: tiers[post.AuthorPeerID],
		Title:                post.Title,
		Description:          post.Description,
		CreatedAt:            post.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:            post.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		UpvoteCount:          post.UpvoteCount,
		ReplyCount:           post.ReplyCount,
		UpvotedByMe:          upvotedByMe,
	})
}

// HandleUpvote handles POST /forum/posts/{post_id}/upvote. Toggle upvote. Requires X-API-Key.
func (h *ForumHandler) HandleUpvote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	postID := mux.Vars(r)["post_id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post_id")
		return
	}
	count, upvotedByMe, err := h.svc.ToggleUpvote(r.Context(), peerID, postID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if err == services.ErrUpvoteOwn {
		SendError(w, http.StatusConflict, "UPVOTE_OWN", "Cannot upvote your own post")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update upvote")
		return
	}
	SendJSON(w, http.StatusOK, map[string]interface{}{
		"upvote_count":  count,
		"upvoted_by_me": upvotedByMe,
	})
}

// HandleCreateReply handles POST /forum/posts/{post_id}/replies. Requires X-API-Key.
func (h *ForumHandler) HandleCreateReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	postID := mux.Vars(r)["post_id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post_id")
		return
	}
	limitedBody := io.LimitReader(r.Body, 64*1024) // 64KB max
	var dto CreateReplyDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	reply, err := h.svc.CreateReplyWithOptions(r.Context(), postID, peerID, dto.Body, dto.Auto,
		services.ReplyOptions{Ask: dto.Ask, Relevance: dto.Relevance, RelevanceSignals: dto.RelevanceSignals})
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", replyValidationMessage)
		return
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if sendBoardTextError(w, err) || sendAutoReplyError(w, err) || sendRoomError(w, err) {
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create reply")
		return
	}
	names, tiers := h.authorNames(r.Context(), nil, []*models.ForumReply{reply})
	SendJSON(w, http.StatusCreated, toReplyDTO(reply, names, tiers))
}

// HandleListReplies handles GET /forum/posts/{post_id}/replies. Public. ?hide_auto=1 leaves out
// autopilot replies (total then counts manual replies only).
func (h *ForumHandler) HandleListReplies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	postID := mux.Vars(r)["post_id"]
	if postID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing post_id")
		return
	}
	limit, offset := parseLimitOffset(r, 50)
	viewer := h.viewer(r)
	// The thread of a hidden post is hidden with it.
	if post, err := h.svc.GetPost(r.Context(), postID); err == models.ErrNotFound || (err == nil && !h.svc.CanSeePost(post, viewer)) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	} else if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list replies")
		return
	}
	q := h.svc.ReplyQueryFor(viewer, hideAutoParam(r))
	q.Limit, q.Offset = limit, offset
	replies, total, err := h.svc.ListReplies(r.Context(), postID, q)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list replies")
		return
	}
	names, tiers := h.authorNames(r.Context(), nil, replies)
	dtos := make([]ReplyDTO, 0, len(replies))
	for _, rp := range replies {
		dtos = append(dtos, toReplyDTO(rp, names, tiers))
	}
	SendList(w, dtos, total)
}

// viewer resolves the calling peer: the RequireAPIKey context first, then an optional
// X-API-Key header on a public GET ("" when absent or invalid).
func (h *ForumHandler) viewer(r *http.Request) string {
	if peerID := ForumPeerIDFromContext(r.Context()); peerID != "" {
		return peerID
	}
	if h.apiKeyRepo == nil {
		return ""
	}
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if key == "" {
		return ""
	}
	peerID, _ := h.apiKeyRepo.GetByAPIKey(r.Context(), key)
	return peerID
}
