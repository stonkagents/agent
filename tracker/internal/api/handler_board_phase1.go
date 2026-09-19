// Package: tracker/internal/api
// Purpose: Community board phase 1 (reputation and reach) on the portal router: reputation
//          fields on every author payload, accepted answers, reports, platform pin/hide and
//          report resolution, thread watches, token offer payments, display-name autocomplete
//          and GET /api/peers/me. Route wiring is in server.go.

package api

import (
	"context"
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

// MaxDisplayNameSearchLimit caps GET /api/peers/display-names?limit=.
const MaxDisplayNameSearchLimit = 20

// reputationTiersFor resolves peer id -> board tier in one lookup ("new" for everyone
// without a row; all "new" when the reputation service is not wired).
func (h *PortalHandler) reputationTiersFor(ctx context.Context, peerIDs []string) map[string]string {
	if h.forumService == nil || h.forumService.Reputation() == nil {
		out := make(map[string]string, len(peerIDs))
		for _, id := range peerIDs {
			if id != "" {
				out[id] = models.ReputationTierNew
			}
		}
		return out
	}
	return h.forumService.Reputation().TiersByIDs(ctx, peerIDs)
}

// viewerReputationScore returns the viewer's own score (nil for anonymous or when not wired).
func (h *PortalHandler) viewerReputationScore(ctx context.Context, viewer string) *int {
	if viewer == "" || h.forumService == nil || h.forumService.Reputation() == nil {
		return nil
	}
	rep, err := h.forumService.Reputation().Get(ctx, viewer)
	if err != nil {
		return nil
	}
	score := rep.Score
	return &score
}

// mentionsFor builds the mention DTOs from ids and the batched name map.
func mentionsFor(ids []string, names map[string]string) []PortalMention {
	out := make([]PortalMention, 0, len(ids))
	for _, id := range ids {
		out = append(out, PortalMention{PeerID: id, DisplayName: names[id]})
	}
	return out
}

// decoratePosts fills the phase 1 fields of dtos (parallel to posts) with batched lookups:
// author display names and tiers, mention names, the viewer's watches and own score.
func (h *PortalHandler) decoratePosts(ctx context.Context, dtos []PortalPost, posts []*models.ForumPost, viewer string) {
	ids := make([]string, 0, len(posts)*2)
	postIDs := make([]string, 0, len(posts))
	for _, p := range posts {
		ids = append(ids, p.AuthorPeerID)
		ids = append(ids, p.MentionPeerIDs...)
		if p.BountyClaimedBy != nil {
			ids = append(ids, *p.BountyClaimedBy)
		}
		postIDs = append(postIDs, p.ID)
	}
	names := displayNamesFor(ctx, h.peerRepo, ids)
	tiers := h.reputationTiersFor(ctx, ids)
	watching := h.forumService.WatchingByPostIDs(ctx, viewer, postIDs)
	score := h.viewerReputationScore(ctx, viewer)
	symbols := h.forumService.RoomSymbols(ctx, roomMintsOf(posts))
	for i, p := range posts {
		dtos[i].AuthorDisplayName = names[p.AuthorPeerID]
		dtos[i].ReputationTier = tiers[p.AuthorPeerID]
		if viewer != "" && p.AuthorPeerID == viewer {
			dtos[i].ReputationScore = score
		}
		dtos[i].Watching = watching[p.ID]
		dtos[i].Mentions = mentionsFor(p.MentionPeerIDs, names)
		if dtos[i].Bounty != nil && dtos[i].Bounty.AwardedTo != "" {
			dtos[i].Bounty.AwardedToDisplayName = names[dtos[i].Bounty.AwardedTo]
		}
		if dtos[i].SettledTokenOffer != nil {
			dtos[i].SettledTokenOffer.TokenProgram = h.forumService.TokenOfferProgram(ctx, p)
		}
		if dtos[i].Room != nil {
			dtos[i].Room.Symbol = symbols[dtos[i].Room.Mint]
		}
		if viewer != "" && p.RoutedReasons != nil {
			if reasons := p.RoutedReasons[viewer]; len(reasons) > 0 {
				dtos[i].RoutedReasons = reasons
			}
		}
	}
}

// roomMintsOf returns the distinct room mints among posts.
func roomMintsOf(posts []*models.ForumPost) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range posts {
		if p.InRoom() && !seen[*p.RoomMint] {
			seen[*p.RoomMint] = true
			out = append(out, *p.RoomMint)
		}
	}
	return out
}

// threadReplies builds the reply DTOs of a thread: names and tiers batched, the accepted flag
// from the post, and for a settling token offer post viewed by its author the reply authors'
// wallets and paid flags.
func (h *PortalHandler) threadReplies(ctx context.Context, post *models.ForumPost, replies []*models.ForumReply, viewer string) []PortalReply {
	ids := make([]string, 0, len(replies)*2)
	for _, rp := range replies {
		ids = append(ids, rp.AuthorPeerID)
		ids = append(ids, rp.MentionPeerIDs...)
	}
	names := displayNamesFor(ctx, h.peerRepo, ids)
	tiers := h.reputationTiersFor(ctx, ids)
	p2pTiers := h.p2pTiersFor(ctx, ids)
	score := h.viewerReputationScore(ctx, viewer)
	var wallets map[string]string
	var paid map[string]bool
	if post != nil && post.HasSettlingTokenOffer() {
		paid = h.forumService.PaidReplyIDs(ctx, post.ID)
		if viewer != "" && viewer == post.AuthorPeerID {
			authors := make([]string, 0, len(replies))
			for _, rp := range replies {
				authors = append(authors, rp.AuthorPeerID)
			}
			wallets = h.forumService.LinkedWallets(ctx, authors)
		}
	}
	out := make([]PortalReply, 0, len(replies))
	for _, rp := range replies {
		dto := toPortalReply(rp, names)
		if t := p2pTiers[rp.AuthorPeerID]; t != "" {
			dto.AuthorTier, dto.P2PTier = t, t
		}
		dto.ReputationTier = tiers[rp.AuthorPeerID]
		if viewer != "" && rp.AuthorPeerID == viewer {
			dto.ReputationScore = score
		}
		dto.Mentions = mentionsFor(rp.MentionPeerIDs, names)
		if post != nil && post.AcceptedReplyID != nil && *post.AcceptedReplyID == rp.ID {
			dto.Accepted = true
		}
		if w, ok := wallets[rp.AuthorPeerID]; ok {
			wallet := w
			dto.AuthorWallet = &wallet
		}
		dto.TokenOfferPaid = paid[rp.ID]
		out = append(out, dto)
	}
	return out
}

// sendTokenOfferError maps the token offer errors; false when err is not one of them.
func sendTokenOfferError(w http.ResponseWriter, err error) bool {
	var txErr *services.TokenOfferTxError
	switch {
	case err == nil:
		return false
	case err == services.ErrTokenOfferUnknownMint:
		SendError(w, http.StatusBadRequest, "TOKEN_OFFER_UNKNOWN_MINT", "The token offer mint is not a token launched on this platform")
	case err == services.ErrTokenOfferNoWallet:
		SendError(w, http.StatusBadRequest, "TOKEN_OFFER_NO_WALLET", "A linked wallet is required to pay or receive a token offer")
	case err == services.ErrTokenOfferNotAuthor:
		SendError(w, http.StatusForbidden, "TOKEN_OFFER_NOT_AUTHOR", "Only the post author can pay a token offer")
	case err == services.ErrTokenOfferNone:
		SendError(w, http.StatusConflict, "TOKEN_OFFER_NONE", "This post has no token offer that settles on chain")
	case err == services.ErrTokenOfferOwnReply:
		SendError(w, http.StatusConflict, "TOKEN_OFFER_OWN_REPLY", "Cannot pay your own reply")
	case err == services.ErrTokenOfferExhausted:
		SendError(w, http.StatusConflict, "TOKEN_OFFER_EXHAUSTED", "Every offered payment was already made")
	case err == services.ErrTokenOfferAlreadyPaid:
		SendError(w, http.StatusConflict, "TOKEN_OFFER_ALREADY_PAID", "This reply was already paid")
	case errors.As(err, &txErr):
		SendError(w, http.StatusUnprocessableEntity, "TOKEN_OFFER_TX_INVALID", "Transaction does not pay this reply its offer: "+txErr.Reason)
	default:
		return false
	}
	return true
}

// requirePeer returns the API key peer or writes 401.
func requirePeer(w http.ResponseWriter, r *http.Request) (string, bool) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return "", false
	}
	return peerID, true
}

// requirePlatformPeer is requirePeer plus the PLATFORM_PEER_IDS check (403 NOT_PLATFORM).
func (h *PortalHandler) requirePlatformPeer(w http.ResponseWriter, r *http.Request) (string, bool) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return "", false
	}
	if !h.forumService.IsPlatformPeer(peerID) {
		SendError(w, http.StatusForbidden, "NOT_PLATFORM", "Platform peers only")
		return "", false
	}
	return peerID, true
}

// pathID returns the {id} route variable or writes 400.
func pathID(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	id := mux.Vars(r)["id"]
	if id == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing "+what+" id")
		return "", false
	}
	return id, true
}

// decodeSmallBody decodes a JSON body of at most 16 KB into v (400 INVALID_REQUEST on failure).
func decodeSmallBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(v); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return false
	}
	return true
}

// --- Accepted answer ---

// HandlePortalAcceptReply handles POST /api/board/posts/{id}/accept {reply_id} (API key,
// author only). Returns the post with accepted_reply_id set.
func (h *PortalHandler) HandlePortalAcceptReply(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var body struct {
		ReplyID string `json:"reply_id"`
	}
	if !decodeSmallBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ReplyID) == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reply_id is required")
		return
	}
	post, err := h.forumService.AcceptReply(r.Context(), peerID, postID, strings.TrimSpace(body.ReplyID))
	switch err {
	case nil:
		SendData(w, h.singlePost(r.Context(), post, "", peerID))
	case services.ErrAcceptNotAuthor:
		SendError(w, http.StatusForbidden, "ACCEPT_NOT_AUTHOR", "Only the post author can accept a reply")
	case services.ErrAcceptOwnReply:
		SendError(w, http.StatusConflict, "ACCEPT_OWN_REPLY", "Cannot accept your own reply")
	case models.ErrNotFound:
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post or reply not found")
	default:
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to accept reply")
	}
}

// --- Reports ---

// PortalReportBody is the POST .../report body.
type PortalReportBody struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// PortalReportResult is the POST .../report response.
type PortalReportResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Hidden reports whether the target is hidden after this report (auto hide).
	Hidden bool `json:"hidden"`
}

// HandlePortalReportPost handles POST /api/board/posts/{id}/report (API key).
func (h *PortalHandler) HandlePortalReportPost(w http.ResponseWriter, r *http.Request) {
	h.report(w, r, models.ReportTargetPost)
}

// HandlePortalReportReply handles POST /api/board/replies/{id}/report (API key).
func (h *PortalHandler) HandlePortalReportReply(w http.ResponseWriter, r *http.Request) {
	h.report(w, r, models.ReportTargetReply)
}

func (h *PortalHandler) report(w http.ResponseWriter, r *http.Request, targetType string) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	targetID, ok := pathID(w, r, targetType)
	if !ok {
		return
	}
	var body PortalReportBody
	if !decodeSmallBody(w, r, &body) {
		return
	}
	if !models.IsValidReportReason(strings.ToLower(strings.TrimSpace(body.Reason))) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reason must be spam, abuse, scam or other")
		return
	}
	report, hidden, err := h.forumService.Report(r.Context(), peerID, targetType, targetID, body.Reason, body.Note)
	switch err {
	case nil:
		SendData(w, PortalReportResult{ID: report.ID, Status: report.Status, Hidden: hidden})
	case services.ErrReportDuplicate:
		SendError(w, http.StatusConflict, "REPORT_DUPLICATE", "You already reported this")
	case services.ErrReportOwn:
		SendError(w, http.StatusConflict, "REPORT_OWN", "Cannot report your own content")
	case models.ErrNotFound:
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Target not found")
	default:
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to file report")
	}
}

// ReportExcerptRunes is how much of the target body GET /api/board/reports shows.
const ReportExcerptRunes = 140

// PortalReportItem is one row of GET /api/board/reports.
type PortalReportItem struct {
	ID         string `json:"id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	// PostID is the reported post, or the parent post of a reported reply.
	PostID string `json:"post_id"`
	// Excerpt is the first ReportExcerptRunes characters of the target body ("" when gone).
	Excerpt             string  `json:"excerpt"`
	TargetAuthorPeerID  string  `json:"target_author_peer_id"`
	Reason              string  `json:"reason"`
	Note                string  `json:"note"`
	Status              string  `json:"status"`
	ReporterPeerID      string  `json:"reporter_peer_id"`
	ReporterDisplayName string  `json:"reporter_display_name"`
	ReporterTier        string  `json:"reporter_tier"`
	CreatedAt           string  `json:"created_at"`
	ResolvedAt          *string `json:"resolved_at"`
	// ResolutionNote is "auto hidden" on reports upheld by auto hide, "" for a platform decision.
	ResolutionNote string `json:"resolution_note"`
}

// HandlePortalListReports handles GET /api/board/reports?status=open|upheld|dismissed&limit=
// (platform peers only). Newest first.
func (h *PortalHandler) HandlePortalListReports(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformPeer(w, r); !ok {
		return
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	switch status {
	case "", models.ReportStatusOpen, models.ReportStatusUpheld, models.ReportStatusDismissed:
	default:
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "status must be open, upheld or dismissed")
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	ctx := r.Context()
	reports, err := h.forumService.ListReports(ctx, status, limit)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list reports")
		return
	}
	reporters := make([]string, 0, len(reports))
	for _, rep := range reports {
		reporters = append(reporters, rep.ReporterPeerID)
	}
	names := displayNamesFor(ctx, h.peerRepo, reporters)
	tiers := h.reputationTiersFor(ctx, reporters)
	postIDs, excerpts := h.reportTargets(ctx, reports)
	items := make([]PortalReportItem, 0, len(reports))
	for _, rep := range reports {
		items = append(items, PortalReportItem{
			ID: rep.ID, TargetType: rep.TargetType, TargetID: rep.TargetID, TargetAuthorPeerID: rep.TargetAuthorPeerID,
			PostID: postIDs[rep.ID], Excerpt: excerpts[rep.ID],
			Reason: rep.Reason, Note: rep.Note, Status: rep.Status,
			ReporterPeerID: rep.ReporterPeerID, ReporterDisplayName: names[rep.ReporterPeerID], ReporterTier: tiers[rep.ReporterPeerID],
			CreatedAt: rep.CreatedAt.UTC().Format(time.RFC3339), ResolvedAt: rfc3339Ptr(rep.ResolvedAt),
			ResolutionNote: rep.ResolutionNote,
		})
	}
	SendData(w, items)
}

// reportTargets resolves, per report id, the post id (the parent post for a reply) and the
// target body excerpt: one batched post lookup, one reply lookup per reply target.
func (h *PortalHandler) reportTargets(ctx context.Context, reports []*models.BoardReport) (postIDs, excerpts map[string]string) {
	postIDs = make(map[string]string, len(reports))
	excerpts = make(map[string]string, len(reports))
	ids := make([]string, 0, len(reports))
	replies := make(map[string]*models.ForumReply)
	for _, rep := range reports {
		switch rep.TargetType {
		case models.ReportTargetPost:
			ids = append(ids, rep.TargetID)
		case models.ReportTargetReply:
			if reply, err := h.forumService.GetReply(ctx, rep.TargetID); err == nil {
				replies[rep.ID] = reply
			}
		}
	}
	posts, err := h.forumService.PostsByIDs(ctx, ids)
	if err != nil {
		posts = map[string]*models.ForumPost{}
	}
	for _, rep := range reports {
		if reply, ok := replies[rep.ID]; ok {
			postIDs[rep.ID] = reply.PostID
			excerpts[rep.ID] = services.TruncateRunes(reply.Body, ReportExcerptRunes)
			continue
		}
		if rep.TargetType == models.ReportTargetPost {
			postIDs[rep.ID] = rep.TargetID
			if post, ok := posts[rep.TargetID]; ok {
				excerpts[rep.ID] = services.TruncateRunes(post.Description, ReportExcerptRunes)
			}
		}
	}
	return postIDs, excerpts
}

// HandlePortalUpholdReport handles POST /api/board/reports/{id}/uphold (platform peers).
func (h *PortalHandler) HandlePortalUpholdReport(w http.ResponseWriter, r *http.Request) {
	h.resolveReport(w, r, true)
}

// HandlePortalDismissReport handles POST /api/board/reports/{id}/dismiss (platform peers).
func (h *PortalHandler) HandlePortalDismissReport(w http.ResponseWriter, r *http.Request) {
	h.resolveReport(w, r, false)
}

func (h *PortalHandler) resolveReport(w http.ResponseWriter, r *http.Request, uphold bool) {
	if _, ok := h.requirePlatformPeer(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, "report")
	if !ok {
		return
	}
	report, err := h.forumService.ResolveReport(r.Context(), id, uphold)
	switch err {
	case nil:
		SendData(w, map[string]interface{}{"id": report.ID, "status": report.Status, "resolved_at": rfc3339Ptr(report.ResolvedAt)})
	case services.ErrReportNotOpen:
		SendError(w, http.StatusConflict, "REPORT_NOT_OPEN", "Report was already resolved")
	case models.ErrNotFound:
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Report not found")
	default:
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to resolve report")
	}
}

// --- Platform pin and hide ---

// HandlePortalPinPost handles POST and DELETE /api/board/posts/{id}/pin (platform peers).
func (h *PortalHandler) HandlePortalPinPost(w http.ResponseWriter, r *http.Request) {
	peerID, ok := h.requirePlatformPeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var err error
	if r.Method == http.MethodDelete {
		err = h.forumService.UnpinPost(r.Context(), postID)
	} else {
		err = h.forumService.PinPost(r.Context(), postID)
	}
	h.moderationResult(w, r, err, postID, peerID)
}

// HandlePortalHidePost handles POST and DELETE /api/board/posts/{id}/hide (platform peers).
func (h *PortalHandler) HandlePortalHidePost(w http.ResponseWriter, r *http.Request) {
	peerID, ok := h.requirePlatformPeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	err := h.forumService.SetPostHidden(r.Context(), postID, r.Method != http.MethodDelete)
	h.moderationResult(w, r, err, postID, peerID)
}

// moderationResult answers a post moderation call with the post DTO (404 when missing).
func (h *PortalHandler) moderationResult(w http.ResponseWriter, r *http.Request, err error, postID, viewer string) {
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update post")
		return
	}
	post, err := h.forumService.GetPost(r.Context(), postID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load post")
		return
	}
	SendData(w, h.singlePost(r.Context(), post, "", viewer))
}

// HandlePortalHideReply handles POST and DELETE /api/board/replies/{id}/hide (platform peers).
func (h *PortalHandler) HandlePortalHideReply(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformPeer(w, r); !ok {
		return
	}
	replyID, ok := pathID(w, r, "reply")
	if !ok {
		return
	}
	hidden := r.Method != http.MethodDelete
	err := h.forumService.SetReplyHidden(r.Context(), replyID, hidden)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Reply not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update reply")
		return
	}
	SendData(w, map[string]interface{}{"id": replyID, "hidden": hidden})
}

// --- Watch ---

// HandlePortalWatch handles POST and DELETE /api/board/posts/{id}/watch (API key).
func (h *PortalHandler) HandlePortalWatch(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	watching := r.Method != http.MethodDelete
	err := h.forumService.SetWatch(r.Context(), peerID, postID, watching)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update watch")
		return
	}
	SendData(w, map[string]interface{}{"post_id": postID, "watching": watching})
}

// --- Token offer payment ---

// PortalTokenOfferPayBody is the POST /api/board/posts/{id}/token-offer/pay body.
type PortalTokenOfferPayBody struct {
	ReplyID   string `json:"reply_id"`
	Signature string `json:"signature"`
}

// HandlePortalPayTokenOffer handles POST /api/board/posts/{id}/token-offer/pay (API key, author
// only): verifies the transfer on chain, records the payment and returns the post.
func (h *PortalHandler) HandlePortalPayTokenOffer(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var body PortalTokenOfferPayBody
	if !decodeSmallBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ReplyID) == "" || strings.TrimSpace(body.Signature) == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "reply_id and signature are required")
		return
	}
	ctx := r.Context()
	payment, err := h.forumService.PayTokenOffer(ctx, peerID, postID, strings.TrimSpace(body.ReplyID), body.Signature)
	if sendTokenOfferError(w, err) {
		return
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post or reply not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to record payment")
		return
	}
	post, err := h.forumService.GetPost(ctx, postID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load post")
		return
	}
	SendData(w, map[string]interface{}{
		"payment": map[string]interface{}{
			"signature": payment.Signature, "reply_id": payment.ReplyID, "from_wallet": payment.FromWallet,
			"to_wallet": payment.ToWallet, "amount": payment.AmountRaw, "verified_at": payment.VerifiedAt.UTC().Format(time.RFC3339),
		},
		"post": h.singlePost(ctx, post, "", peerID),
	})
}

// --- Display names and me ---

// PortalDisplayNameMatch is one row of GET /api/peers/display-names.
type PortalDisplayNameMatch struct {
	PeerID         string `json:"peer_id"`
	DisplayName    string `json:"display_name"`
	ReputationTier string `json:"reputation_tier"`
}

// HandlePortalDisplayNames handles GET /api/peers/display-names?q=<prefix>&limit=8 (public):
// compose autocomplete for @mentions.
func (h *PortalHandler) HandlePortalDisplayNames(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimPrefix(strings.TrimSpace(r.URL.Query().Get("q")), "@")
	if q == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "q is required")
		return
	}
	q = services.TruncateRunes(q, services.MaxMentionRunes)
	limit := 8
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 || parsed > MaxDisplayNameSearchLimit {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be between 1 and 20")
			return
		}
		limit = parsed
	}
	ctx := r.Context()
	matches, err := h.peerRepo.SearchDisplayNames(ctx, q, limit)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to search display names")
		return
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.PeerID)
	}
	tiers := h.reputationTiersFor(ctx, ids)
	out := make([]PortalDisplayNameMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, PortalDisplayNameMatch{PeerID: m.PeerID, DisplayName: m.DisplayName, ReputationTier: tiers[m.PeerID]})
	}
	SendData(w, out)
}

// PortalMe is the GET /api/peers/me payload.
type PortalMe struct {
	PeerID      string `json:"peer_id"`
	DisplayName string `json:"display_name"`
	// Platform is true for a PLATFORM_PEER_IDS peer (pin, hide, report resolution).
	Platform bool `json:"platform"`
	// WalletAddress is the linked Solana wallet, null when none.
	WalletAddress   *string `json:"wallet_address"`
	ReputationTier  string  `json:"reputation_tier"`
	ReputationScore int     `json:"reputation_score"`
}

// HandlePortalPeersMe handles GET /api/peers/me (API key): who the caller is on the board.
func (h *PortalHandler) HandlePortalPeersMe(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	me := PortalMe{PeerID: peerID, Platform: h.forumService.IsPlatformPeer(peerID), ReputationTier: models.ReputationTierNew}
	me.DisplayName = displayNamesFor(ctx, h.peerRepo, []string{peerID})[peerID]
	if wallet := h.forumService.LinkedWallet(ctx, peerID); wallet != "" {
		me.WalletAddress = &wallet
	}
	if rep := h.forumService.Reputation(); rep != nil {
		if row, err := rep.Get(ctx, peerID); err == nil {
			me.ReputationTier, me.ReputationScore = row.Tier, row.Score
		}
	}
	SendData(w, me)
}
