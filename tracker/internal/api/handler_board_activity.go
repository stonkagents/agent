// Package: tracker/internal/api
// Purpose: Community board activity feed (per-peer notifications): GET /api/activity and
//          POST /api/activity/read. Both need X-API-Key (RequireAPIKey); the feed is the
//          caller's own rows only. Actor names come from the batched display-name lookup at
//          read time, never stored with the row.

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// MaxActivityLimit caps GET /api/activity?limit= (larger values are clamped to it).
const MaxActivityLimit = 100

// PortalActivityItem is one row of GET /api/activity.
type PortalActivityItem struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	PostID           string  `json:"post_id"`
	PostTitle        string  `json:"post_title"`
	ReplyID          *string `json:"reply_id"`
	ActorPeerID      string  `json:"actor_peer_id"`
	ActorDisplayName string  `json:"actor_display_name"`
	// ActorReputationTier is the actor's board tier (new for system rows).
	ActorReputationTier string `json:"actor_reputation_tier"`
	Amount              *int   `json:"amount"`
	// Symbol and Decimals describe the token on token_offer_paid rows (amount is then in raw
	// units); absent on every other kind.
	Symbol   string `json:"symbol,omitempty"`
	Decimals *int   `json:"decimals,omitempty"`
	// RoomMint is the post's token room when it is in one (absent on main feed posts).
	RoomMint  string  `json:"room_mint,omitempty"`
	CreatedAt string  `json:"created_at"`
	ReadAt    *string `json:"read_at"`
	// Reasons says why a request_routed row reached this peer (round 2, "why am I seeing
	// this"): category, tier:<tier>, accepted, online, holder. Absent on other kinds.
	Reasons []string `json:"reasons,omitempty"`
}

// PortalActivityResponse is the GET /api/activity payload.
type PortalActivityResponse struct {
	Items  []PortalActivityItem `json:"items"`
	Unread int                  `json:"unread"`
}

// HandlePortalActivity handles GET /api/activity?since=<RFC3339>&limit=<=100&kinds=<comma list>&unread=1&mark=0
// (API key). Newest first. Reading with unread=1 marks the returned rows read unless mark=0
// is given (the Autopilot watcher reads its request_routed queue with mark=0 and marks each
// item through POST /api/activity/read once handled).
func (h *PortalHandler) HandlePortalActivity(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	query := r.URL.Query()
	limit := 50
	if l := query.Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be a number of at least 1")
			return
		}
		limit = min(parsed, MaxActivityLimit)
	}
	var since *time.Time
	if s := strings.TrimSpace(query.Get("since")); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "since must be RFC3339")
			return
		}
		since = &t
	}
	aq := repository.ActivityQuery{Since: since, Limit: limit}
	if k := strings.TrimSpace(query.Get("kinds")); k != "" {
		for _, kind := range strings.Split(k, ",") {
			kind = strings.ToLower(strings.TrimSpace(kind))
			if kind == "" {
				continue
			}
			if !models.IsValidActivityKind(kind) {
				SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "kinds must be a comma separated list of activity kinds")
				return
			}
			aq.Kinds = append(aq.Kinds, kind)
		}
	}
	if u := strings.ToLower(strings.TrimSpace(query.Get("unread"))); u == "1" || u == "true" {
		aq.Unread = true
	}
	mark := true
	if m := strings.ToLower(strings.TrimSpace(query.Get("mark"))); m == "0" || m == "false" {
		mark = false
	}

	ctx := r.Context()
	rows, unread, err := h.forumService.ListActivity(ctx, peerID, aq)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list activity")
		return
	}
	if aq.Unread && mark && len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for _, a := range rows {
			ids = append(ids, a.ID)
		}
		if err := h.forumService.MarkActivityRead(ctx, peerID, ids, false); err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to mark activity read")
			return
		}
		if n, err := h.forumService.CountUnreadActivity(ctx, peerID); err == nil {
			unread = n
		}
	}
	actorIDs := make([]string, 0, len(rows))
	postIDs := make([]string, 0, len(rows))
	for _, a := range rows {
		actorIDs = append(actorIDs, a.ActorPeerID)
		postIDs = append(postIDs, a.PostID)
	}
	names := displayNamesFor(ctx, h.peerRepo, actorIDs)
	tiers := h.reputationTiersFor(ctx, actorIDs)
	posts, err := h.forumService.PostsByIDs(ctx, postIDs)
	if err != nil {
		posts = map[string]*models.ForumPost{}
	}
	// A hidden post's title is not shown to anyone but its author and the platform: the
	// row stays (the reply or mention happened), the title reads as "post gone".
	titles := make(map[string]string, len(posts))
	for id, p := range posts {
		if h.forumService.CanSeePost(p, peerID) {
			titles[id] = p.Title
		}
	}
	items := make([]PortalActivityItem, 0, len(rows))
	for _, a := range rows {
		item := toPortalActivityItem(a, names, titles)
		item.ActorReputationTier = tiers[a.ActorPeerID]
		if a.Kind == models.ActivityTokenOfferPaid {
			if p, ok := posts[a.PostID]; ok && p.TokenOfferDecimals != nil {
				d := *p.TokenOfferDecimals
				item.Decimals = &d
			}
		}
		if item.ActorReputationTier == "" {
			item.ActorReputationTier = models.ReputationTierNew
		}
		if p, ok := posts[a.PostID]; ok && p.InRoom() {
			item.RoomMint = *p.RoomMint
		}
		if a.Kind == models.ActivityRequestRouted {
			if p, ok := posts[a.PostID]; ok && p.RoutedReasons != nil {
				item.Reasons = p.RoutedReasons[peerID]
			}
		}
		items = append(items, item)
	}
	SendData(w, PortalActivityResponse{Items: items, Unread: unread})
}

// toPortalActivityItem builds the DTO; names and titles are the batched lookups.
func toPortalActivityItem(a *models.BoardActivity, names, titles map[string]string) PortalActivityItem {
	return PortalActivityItem{
		ID:               a.ID,
		Kind:             a.Kind,
		PostID:           a.PostID,
		PostTitle:        titles[a.PostID],
		ReplyID:          a.ReplyID,
		ActorPeerID:      a.ActorPeerID,
		ActorDisplayName: names[a.ActorPeerID],
		Amount:           a.Amount,
		Symbol:           a.Symbol,
		CreatedAt:        a.CreatedAt.UTC().Format(time.RFC3339Nano),
		ReadAt:           rfc3339NanoPtr(a.ReadAt),
	}
}

// rfc3339NanoPtr formats an optional time with sub-second precision (nil stays nil). The feed
// uses it so a client can echo the newest created_at as since= and get nothing back.
func rfc3339NanoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

// PortalActivityReadBody is the POST /api/activity/read body: {"ids":[...]} or {"all":true}.
type PortalActivityReadBody struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

// HandlePortalActivityRead handles POST /api/activity/read (API key). Marks the caller's rows read.
func (h *PortalHandler) HandlePortalActivityRead(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	var body PortalActivityReadBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	if !body.All && len(body.IDs) == 0 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "ids or all is required")
		return
	}
	if len(body.IDs) > 1000 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "at most 1000 ids per call")
		return
	}
	if err := h.forumService.MarkActivityRead(r.Context(), peerID, body.IDs, body.All); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to mark activity read")
		return
	}
	unread, err := h.forumService.CountUnreadActivity(r.Context(), peerID)
	if err != nil {
		unread = 0
	}
	SendData(w, map[string]interface{}{"ok": true, "unread": unread})
}
