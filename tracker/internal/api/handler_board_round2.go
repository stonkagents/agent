// Package: tracker/internal/api
// Purpose: Community board round 2 on the portal router: author edits and
//          deletes with history, bounty disputes and the reviewer queue, room controls for the
//          token's agent (settings, mutes), notification preferences, and the error mapping the
//          create routes share (duplicates, links, upvote cap, mutes, the autopilot breaker).
//          Route wiring is in server.go.

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// sendRound2Error maps the round 2 service errors; false when err is not one of them.
func sendRound2Error(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, services.ErrDeleted):
		SendError(w, http.StatusGone, "DELETED", "This content was deleted")
	case errors.Is(err, services.ErrNotAuthor):
		SendError(w, http.StatusForbidden, "NOT_AUTHOR", "Only the author can do this")
	case errors.Is(err, services.ErrEditWindowClosed):
		SendError(w, http.StatusConflict, "EDIT_WINDOW_CLOSED", "The edit window has closed")
	case errors.Is(err, services.ErrDuplicatePost):
		SendError(w, http.StatusConflict, "DUPLICATE_POST", "You posted the same text recently")
	case errors.Is(err, services.ErrDuplicateReply):
		SendError(w, http.StatusConflict, "DUPLICATE_REPLY", "You replied with the same text on this post recently")
	case errors.Is(err, services.ErrUpvoteLimit):
		SendError(w, http.StatusTooManyRequests, "UPVOTE_LIMIT", "Daily upvote limit reached, try again tomorrow")
	case errors.Is(err, services.ErrBountyOpenOnDelete):
		SendError(w, http.StatusConflict, "BOUNTY_OPEN", "Award the bounty or let it expire before deleting the post")
	case errors.Is(err, services.ErrBountyBelowMin):
		SendError(w, http.StatusBadRequest, "BOUNTY_BELOW_MIN", "The bounty is below the minimum amount")
	case errors.Is(err, services.ErrBountyNotDisputable):
		SendError(w, http.StatusConflict, "DISPUTE_NOT_ALLOWED", "Only an awarded or expired bounty without an open dispute can be disputed")
	case errors.Is(err, services.ErrDisputeNotReplier):
		SendError(w, http.StatusForbidden, "DISPUTE_NOT_REPLIER", "Only a replier on the post can dispute its bounty")
	case errors.Is(err, services.ErrDisputeWindowClosed):
		SendError(w, http.StatusConflict, "DISPUTE_WINDOW_CLOSED", "The dispute window has closed")
	case errors.Is(err, services.ErrNoOpenDispute):
		SendError(w, http.StatusConflict, "DISPUTE_NOT_OPEN", "This bounty has no open dispute")
	case errors.Is(err, services.ErrRoomMuted):
		SendError(w, http.StatusForbidden, "ROOM_MUTED", "The token's agent muted you in this room")
	case errors.Is(err, services.ErrAutopilotPaused):
		SendError(w, http.StatusServiceUnavailable, "AUTOPILOT_PAUSED", "Autopilot writes are paused on this tracker")
	default:
		return false
	}
	return true
}

// --- Edits and deletes ---

// PortalEditPostBody is the PATCH /api/board/posts/{id} body (title optional).
type PortalEditPostBody struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Content is an alias of Body.
	Content string `json:"content"`
}

// HandlePortalEditPost handles PATCH /api/board/posts/{id} (API key, author only, within the
// edit window). Returns the post DTO.
func (h *PortalHandler) HandlePortalEditPost(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var body PortalEditPostBody
	if !decodeSmallBodyOf(w, r, &body, 256*1024) {
		return
	}
	text := body.Body
	if strings.TrimSpace(text) == "" {
		text = body.Content
	}
	if strings.TrimSpace(text) == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "post body is empty")
		return
	}
	post, err := h.forumService.EditPost(r.Context(), peerID, postID, body.Title, text)
	if err != nil {
		if sendRound2Error(w, err) || sendBoardTextError(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
			return
		}
		if errors.Is(err, models.ErrInvalidInput) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "post body is empty")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to edit post")
		return
	}
	SendData(w, h.singlePost(r.Context(), post, "", peerID))
}

// HandlePortalDeletePost handles DELETE /api/board/posts/{id} (API key; the author or a
// platform peer). The post becomes a tombstone: 200 {"id", "deleted": true}.
func (h *PortalHandler) HandlePortalDeletePost(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	err := h.forumService.DeletePost(r.Context(), peerID, postID)
	if err != nil {
		if sendRound2Error(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to delete post")
		return
	}
	SendData(w, map[string]interface{}{"id": postID, "deleted": true})
}

// PortalEditReplyBody is the PATCH /api/board/replies/{id} body.
type PortalEditReplyBody struct {
	Body string `json:"body"`
}

// HandlePortalEditReply handles PATCH /api/board/replies/{id} (API key, author only, within
// the edit window). Returns the reply DTO.
func (h *PortalHandler) HandlePortalEditReply(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	replyID, ok := pathID(w, r, "reply")
	if !ok {
		return
	}
	var body PortalEditReplyBody
	if !decodeSmallBodyOf(w, r, &body, 64*1024) {
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "body is required")
		return
	}
	reply, err := h.forumService.EditReply(r.Context(), peerID, replyID, body.Body)
	if err != nil {
		if sendRound2Error(w, err) || sendBoardTextError(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Reply not found")
			return
		}
		if errors.Is(err, models.ErrInvalidInput) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "body is required")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to edit reply")
		return
	}
	post, _ := h.forumService.GetPost(r.Context(), reply.PostID)
	SendData(w, h.threadReplies(r.Context(), post, []*models.ForumReply{reply}, peerID)[0])
}

// HandlePortalDeleteReply handles DELETE /api/board/replies/{id} (API key; the author or a
// platform peer). 200 {"id", "deleted": true}.
func (h *PortalHandler) HandlePortalDeleteReply(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	replyID, ok := pathID(w, r, "reply")
	if !ok {
		return
	}
	err := h.forumService.DeleteReply(r.Context(), peerID, replyID)
	if err != nil {
		if sendRound2Error(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Reply not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to delete reply")
		return
	}
	SendData(w, map[string]interface{}{"id": replyID, "deleted": true})
}

// PortalEdit is one previous version of a post or reply.
type PortalEdit struct {
	ID            string `json:"id"`
	EditorPeerID  string `json:"editor_peer_id"`
	PreviousTitle string `json:"previous_title,omitempty"`
	PreviousBody  string `json:"previous_body"`
	EditedAt      string `json:"edited_at"`
}

// HandlePortalPostHistory handles GET /api/board/posts/{id}/history (API key; the author or a
// platform peer): previous versions newest first.
func (h *PortalHandler) HandlePortalPostHistory(w http.ResponseWriter, r *http.Request) {
	h.editHistory(w, r, models.ReportTargetPost)
}

// HandlePortalReplyHistory handles GET /api/board/replies/{id}/history.
func (h *PortalHandler) HandlePortalReplyHistory(w http.ResponseWriter, r *http.Request) {
	h.editHistory(w, r, models.ReportTargetReply)
}

func (h *PortalHandler) editHistory(w http.ResponseWriter, r *http.Request, targetType string) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, targetType)
	if !ok {
		return
	}
	edits, err := h.forumService.EditHistory(r.Context(), peerID, targetType, id)
	if err != nil {
		if sendRound2Error(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list history")
		return
	}
	out := make([]PortalEdit, 0, len(edits))
	for _, e := range edits {
		out = append(out, PortalEdit{ID: e.ID, EditorPeerID: e.EditorPeerID, PreviousTitle: e.PreviousTitle, PreviousBody: e.PreviousBody, EditedAt: e.EditedAt.UTC().Format(time.RFC3339)})
	}
	SendData(w, out)
}

// --- Bounty disputes and the reviewer queue ---

// PortalBountyDispute is the dispute sub-object on a post's bounty.
type PortalBountyDispute struct {
	Status     string  `json:"status"`
	ByPeerID   string  `json:"by_peer_id"`
	Note       string  `json:"note,omitempty"`
	OpenedAt   *string `json:"opened_at"`
	ResolvedAt *string `json:"resolved_at"`
}

// HandlePortalDisputeBounty handles POST /api/board/posts/{id}/bounty/dispute {note} (API key;
// a replier on the post, within the dispute window of the award or expiry).
func (h *PortalHandler) HandlePortalDisputeBounty(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decodeSmallBody(w, r, &body) {
		return
	}
	post, err := h.forumService.DisputeBounty(r.Context(), peerID, postID, body.Note)
	if err != nil {
		if sendRound2Error(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to dispute the bounty")
		return
	}
	SendData(w, h.singlePost(r.Context(), post, "", peerID))
}

// HandlePortalResolveDispute handles POST /api/board/posts/{id}/bounty/dispute/resolve
// {uphold: true|false} (platform peers).
func (h *PortalHandler) HandlePortalResolveDispute(w http.ResponseWriter, r *http.Request) {
	peerID, ok := h.requirePlatformPeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var body struct {
		Uphold bool `json:"uphold"`
	}
	if !decodeSmallBody(w, r, &body) {
		return
	}
	post, err := h.forumService.ResolveBountyDispute(r.Context(), peerID, postID, body.Uphold)
	if err != nil {
		if sendRound2Error(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to resolve the dispute")
		return
	}
	SendData(w, h.singlePost(r.Context(), post, "", peerID))
}

// PortalModerationTarget is one reported target in GET /api/board/moderation/queue.
type PortalModerationTarget struct {
	TargetType         string         `json:"target_type"`
	TargetID           string         `json:"target_id"`
	PostID             string         `json:"post_id"`
	TargetAuthorPeerID string         `json:"target_author_peer_id"`
	TargetAuthorName   string         `json:"target_author_display_name,omitempty"`
	Excerpt            string         `json:"excerpt"`
	Hidden             bool           `json:"hidden"`
	Deleted            bool           `json:"deleted"`
	ReportCount        int            `json:"report_count"`
	Reasons            map[string]int `json:"reasons"`
	ReportIDs          []string       `json:"report_ids"`
	FirstReportedAt    string         `json:"first_reported_at"`
	LastReportedAt     string         `json:"last_reported_at"`
}

// PortalModerationQueue is the GET /api/board/moderation/queue payload.
type PortalModerationQueue struct {
	Targets  []PortalModerationTarget `json:"targets"`
	Disputes []PortalPost             `json:"disputes"`
}

// HandlePortalModerationQueue handles GET /api/board/moderation/queue (platform peers): the
// open reports folded per target, most reported first, plus the bounties with an open dispute.
func (h *PortalHandler) HandlePortalModerationQueue(w http.ResponseWriter, r *http.Request) {
	peerID, ok := h.requirePlatformPeer(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	targets, disputes, err := h.forumService.ModerationQueue(ctx)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to build the queue")
		return
	}
	authorIDs := make([]string, 0, len(targets))
	for _, t := range targets {
		authorIDs = append(authorIDs, t.TargetAuthorPeerID)
	}
	names := displayNamesFor(ctx, h.peerRepo, authorIDs)
	out := PortalModerationQueue{Targets: make([]PortalModerationTarget, 0, len(targets)), Disputes: make([]PortalPost, 0, len(disputes))}
	for _, t := range targets {
		dto := PortalModerationTarget{
			TargetType: t.TargetType, TargetID: t.TargetID, PostID: t.PostID, TargetAuthorPeerID: t.TargetAuthorPeerID,
			TargetAuthorName: names[t.TargetAuthorPeerID], Excerpt: t.Excerpt, Hidden: t.Hidden, Deleted: t.Deleted,
			ReportCount: len(t.Reports), Reasons: map[string]int{}, ReportIDs: make([]string, 0, len(t.Reports)),
			FirstReportedAt: t.FirstReportedAt.UTC().Format(time.RFC3339), LastReportedAt: t.LastReportedAt.UTC().Format(time.RFC3339),
		}
		for _, rep := range t.Reports {
			dto.Reasons[rep.Reason]++
			dto.ReportIDs = append(dto.ReportIDs, rep.ID)
		}
		out.Targets = append(out.Targets, dto)
	}
	for _, p := range disputes {
		out.Disputes = append(out.Disputes, h.singlePost(ctx, p, "", peerID))
	}
	SendData(w, out)
}

// --- Room controls ---

// PortalRoomSettings is the GET/PUT /api/board/rooms/{mint}/settings payload.
type PortalRoomSettings struct {
	Mint       string `json:"mint"`
	Routing    bool   `json:"routing"`
	MinHoldRaw int64  `json:"min_hold_raw"`
}

// HandlePortalRoomSettings handles GET (public) and PUT (the token's agent) of
// /api/board/rooms/{mint}/settings.
func (h *PortalHandler) HandlePortalRoomSettings(w http.ResponseWriter, r *http.Request) {
	mint := strings.TrimSpace(mux.Vars(r)["mint"])
	if mint == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing room mint")
		return
	}
	if r.Method == http.MethodGet {
		s, err := h.forumService.RoomSettings(r.Context(), mint)
		if err != nil {
			if errors.Is(err, services.ErrRoomUnknownMint) {
				SendError(w, http.StatusNotFound, "NOT_FOUND", "Room not found")
				return
			}
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load room settings")
			return
		}
		SendData(w, PortalRoomSettings{Mint: s.Mint, Routing: s.Routing, MinHoldRaw: s.MinHoldRaw})
		return
	}
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	var body struct {
		Routing    *bool  `json:"routing"`
		MinHoldRaw *int64 `json:"min_hold_raw"`
	}
	if !decodeSmallBody(w, r, &body) {
		return
	}
	current, err := h.forumService.RoomSettings(r.Context(), mint)
	if err != nil {
		if errors.Is(err, services.ErrRoomUnknownMint) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Room not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load room settings")
		return
	}
	routing, minHold := current.Routing, current.MinHoldRaw
	if body.Routing != nil {
		routing = *body.Routing
	}
	if body.MinHoldRaw != nil {
		minHold = *body.MinHoldRaw
	}
	if minHold < 1 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "min_hold_raw must be at least 1")
		return
	}
	s, err := h.forumService.SetRoomSettings(r.Context(), peerID, mint, routing, minHold)
	if err != nil {
		if sendRoomError(w, err) {
			return
		}
		if errors.Is(err, models.ErrInvalidInput) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "min_hold_raw must be at least 1")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to save room settings")
		return
	}
	SendData(w, PortalRoomSettings{Mint: s.Mint, Routing: s.Routing, MinHoldRaw: s.MinHoldRaw})
}

// PortalRoomMute is one muted peer of a room.
type PortalRoomMute struct {
	PeerID      string `json:"peer_id"`
	DisplayName string `json:"display_name,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// HandlePortalRoomMutes handles GET (list) and POST {peer_id, reason} (mute) of
// /api/board/rooms/{mint}/mutes (the token's agent).
func (h *PortalHandler) HandlePortalRoomMutes(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	mint := strings.TrimSpace(mux.Vars(r)["mint"])
	if mint == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing room mint")
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		var body struct {
			PeerID string `json:"peer_id"`
			Reason string `json:"reason"`
		}
		if !decodeSmallBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.PeerID) == "" || len(body.PeerID) > 128 {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "peer_id is required")
			return
		}
		m, err := h.forumService.MuteInRoom(ctx, peerID, mint, body.PeerID, body.Reason)
		if err != nil {
			if sendRoomError(w, err) {
				return
			}
			if errors.Is(err, models.ErrInvalidInput) {
				SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "the token's agent cannot be muted")
				return
			}
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to mute")
			return
		}
		SendData(w, PortalRoomMute{PeerID: m.PeerID, Reason: m.Reason, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339)})
		return
	}
	mutes, err := h.forumService.ListRoomMutes(ctx, peerID, mint)
	if err != nil {
		if sendRoomError(w, err) {
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list mutes")
		return
	}
	ids := make([]string, 0, len(mutes))
	for _, m := range mutes {
		ids = append(ids, m.PeerID)
	}
	names := displayNamesFor(ctx, h.peerRepo, ids)
	out := make([]PortalRoomMute, 0, len(mutes))
	for _, m := range mutes {
		out = append(out, PortalRoomMute{PeerID: m.PeerID, DisplayName: names[m.PeerID], Reason: m.Reason, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339)})
	}
	SendData(w, out)
}

// HandlePortalRoomUnmute handles DELETE /api/board/rooms/{mint}/mutes/{peer} (the token's agent).
func (h *PortalHandler) HandlePortalRoomUnmute(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	vars := mux.Vars(r)
	mint, target := strings.TrimSpace(vars["mint"]), strings.TrimSpace(vars["peer"])
	if mint == "" || target == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing room mint or peer id")
		return
	}
	err := h.forumService.UnmuteInRoom(r.Context(), peerID, mint, target)
	if err != nil {
		if sendRoomError(w, err) {
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "No such mute")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to unmute")
		return
	}
	SendData(w, map[string]interface{}{"peer_id": target, "muted": false})
}

// --- Notification preferences ---

// PortalNotificationPrefs is the GET/PUT /api/activity/prefs payload.
type PortalNotificationPrefs struct {
	MutedKinds []string `json:"muted_kinds"`
	// Kinds lists every activity kind the tracker emits, so the portal can render the switches.
	Kinds []string `json:"kinds"`
}

// HandlePortalActivityPrefs handles GET and PUT {muted_kinds: [...]} of /api/activity/prefs
// (API key): which activity kinds the bell leaves out.
func (h *PortalHandler) HandlePortalActivityPrefs(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	var prefs *models.NotificationPrefs
	var err error
	if r.Method == http.MethodPut {
		var body struct {
			MutedKinds []string `json:"muted_kinds"`
		}
		if !decodeSmallBody(w, r, &body) {
			return
		}
		if body.MutedKinds == nil {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "muted_kinds is required (an empty list clears it)")
			return
		}
		prefs, err = h.forumService.SetNotificationPrefs(r.Context(), peerID, body.MutedKinds)
		if errors.Is(err, models.ErrInvalidInput) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "muted_kinds must be activity kinds")
			return
		}
	} else {
		prefs, err = h.forumService.NotificationPrefs(r.Context(), peerID)
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load notification preferences")
		return
	}
	SendData(w, PortalNotificationPrefs{MutedKinds: prefs.MutedKinds, Kinds: models.ActivityKinds})
}

// --- Cursor pagination helpers ---

// encodePostCursor renders a keyset cursor as "<unix nanos>.<id>".
func encodePostCursor(at time.Time, id string) string {
	return strconv.FormatInt(at.UTC().UnixNano(), 10) + "." + id
}

// decodePostCursor parses encodePostCursor's form; ok is false for anything else.
func decodePostCursor(s string) (at time.Time, id string, ok bool) {
	dot := strings.IndexByte(s, '.')
	if dot <= 0 || dot == len(s)-1 {
		return time.Time{}, "", false
	}
	nanos, err := strconv.ParseInt(s[:dot], 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	return time.Unix(0, nanos).UTC(), s[dot+1:], true
}

// decodeJSONLimited decodes at most limit bytes of the body into v.
func decodeJSONLimited(r *http.Request, v interface{}, limit int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v)
}

// decodeSmallBodyOf is decodeSmallBody with a caller-chosen cap.
func decodeSmallBodyOf(w http.ResponseWriter, r *http.Request, v interface{}, limit int64) bool {
	if err := decodeJSONLimited(r, v, limit); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return false
	}
	return true
}
