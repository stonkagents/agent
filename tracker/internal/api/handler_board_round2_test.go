// Package: tracker/internal/api
// Purpose: Community board round 2 over HTTP: the edit and delete routes with their
//          codes, history access, bounty disputes and the reviewer queue behind the platform
//          gate, room settings and mutes for the token's agent, notification preferences, the
//          keyset cursor and visit meta of the board list, routing reasons in the bell, unread
//          per room, and the 4xx bodies of the new refusals.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// r2Env is p2Env with the round 2 repositories on the forum service.
type r2Env struct {
	*p2Env
	rooms *repository.MemoryBoardRoomRepository
}

func newR2Env(t *testing.T) *r2Env {
	t.Helper()
	e := newP2Env(t)
	env := &r2Env{p2Env: e, rooms: repository.NewMemoryBoardRoomRepository()}
	e.forum.SetRound2(services.Round2Deps{
		Edits: repository.NewMemoryBoardEditHistoryRepository(), Rooms: env.rooms,
		Visits: repository.NewMemoryBoardVisitRepository(), Prefs: repository.NewMemoryBoardNotificationPrefRepository(),
	}, services.Round2Settings{MaxUpvotesPerPeerPerDay: 2})
	return env
}

func decodeR2(t *testing.T, raw []byte, v interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, raw)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data: %v (%s)", err, env.Data)
	}
}

func TestR2_EditDeleteAndHistoryRoutes(t *testing.T) {
	e := newR2Env(t)
	alice, bob, platform := e.key(t, "alice"), e.key(t, "bob"), e.key(t, "platform")
	p := e.post(t, alice, map[string]interface{}{"title": "First", "body": "first body", "category": "general"})
	if p.EditCount != 0 || p.EditedAt != nil || p.Deleted {
		t.Fatalf("fresh post DTO: %+v", p)
	}

	w := e.do(t, http.MethodPatch, "/api/board/posts/"+p.ID, map[string]interface{}{"body": "bob edits"}, bob)
	if w.Code != http.StatusForbidden || errCode(t, w) != "NOT_AUTHOR" {
		t.Fatalf("edit by other: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPatch, "/api/board/posts/"+p.ID, map[string]interface{}{"body": "   "}, alice)
	if w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION_ERROR" {
		t.Fatalf("empty edit: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPatch, "/api/board/posts/"+p.ID, map[string]interface{}{"body": "see javascript:alert(1)"}, alice)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "javascript") {
		t.Fatalf("bad scheme edit: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPatch, "/api/board/posts/"+p.ID, map[string]interface{}{"title": "Second", "content": "second body"}, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	var edited PortalPost
	decodeR2(t, w.Body.Bytes(), &edited)
	if edited.Title != "Second" || edited.Content != "second body" || edited.EditCount != 1 || edited.EditedAt == nil {
		t.Errorf("edited DTO = %+v", edited)
	}
	w = e.do(t, http.MethodGet, "/api/board/posts/"+p.ID+"/history", nil, bob)
	if w.Code != http.StatusForbidden || errCode(t, w) != "NOT_AUTHOR" {
		t.Fatalf("history by other: %d %s", w.Code, w.Body.String())
	}
	for _, key := range []string{alice, platform} {
		w = e.do(t, http.MethodGet, "/api/board/posts/"+p.ID+"/history", nil, key)
		var hist []PortalEdit
		decodeR2(t, w.Body.Bytes(), &hist)
		if w.Code != http.StatusOK || len(hist) != 1 || hist[0].PreviousTitle != "First" || hist[0].PreviousBody != "first body" {
			t.Errorf("history = %d %+v", w.Code, hist)
		}
	}
	e.clock.Advance(services.DefaultEditWindow + time.Minute)
	w = e.do(t, http.MethodPatch, "/api/board/posts/"+p.ID, map[string]interface{}{"body": "late"}, alice)
	if w.Code != http.StatusConflict || errCode(t, w) != "EDIT_WINDOW_CLOSED" {
		t.Fatalf("late edit: %d %s", w.Code, w.Body.String())
	}

	// Replies: edit, delete, tombstone in the thread.
	r := e.reply(t, bob, p.ID, "a reply")
	w = e.do(t, http.MethodPatch, "/api/board/replies/"+r.ID, map[string]interface{}{"body": "a better reply"}, bob)
	var er PortalReply
	decodeR2(t, w.Body.Bytes(), &er)
	if w.Code != http.StatusOK || er.Content != "a better reply" || er.EditCount != 1 {
		t.Errorf("reply edit = %d %+v", w.Code, er)
	}
	w = e.do(t, http.MethodDelete, "/api/board/replies/"+r.ID, nil, alice)
	if w.Code != http.StatusForbidden || errCode(t, w) != "NOT_AUTHOR" {
		t.Fatalf("delete reply by other: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodDelete, "/api/board/replies/"+r.ID, nil, bob)
	if w.Code != http.StatusOK {
		t.Fatalf("delete reply: %d %s", w.Code, w.Body.String())
	}
	thread := e.thread(t, p.ID, "")
	if len(thread) != 1 || !thread[0].Deleted || thread[0].Content != "" {
		t.Errorf("tombstone in thread = %+v", thread)
	}
	w = e.do(t, http.MethodPatch, "/api/board/replies/"+r.ID, map[string]interface{}{"body": "edit tombstone"}, bob)
	if w.Code != http.StatusGone || errCode(t, w) != "DELETED" {
		t.Fatalf("edit a deleted reply: %d %s", w.Code, w.Body.String())
	}

	// Posts: the author deletes; the feed drops it; the thread reads as a tombstone; writes 410.
	w = e.do(t, http.MethodDelete, "/api/board/posts/"+p.ID, nil, bob)
	if w.Code != http.StatusForbidden {
		t.Fatalf("delete post by other: %d", w.Code)
	}
	w = e.do(t, http.MethodDelete, "/api/board/posts/"+p.ID, nil, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("delete post: %d %s", w.Code, w.Body.String())
	}
	if posts, total := e.list(t, "?tab=recent", ""); total != 0 || len(posts) != 0 {
		t.Errorf("feed after delete: %d", total)
	}
	got, code := e.getPost(t, p.ID, "")
	if code != http.StatusOK || !got.Deleted || got.Content != "" || got.Title != "Second" {
		t.Errorf("tombstone = %d %+v", code, got)
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "late reply"}, bob)
	if w.Code != http.StatusGone || errCode(t, w) != "DELETED" {
		t.Fatalf("reply on deleted post: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/upvote", nil, bob)
	if w.Code != http.StatusGone {
		t.Fatalf("upvote on deleted post: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodDelete, "/api/board/posts/"+p.ID, nil, alice)
	if w.Code != http.StatusGone {
		t.Fatalf("delete twice: %d", w.Code)
	}
	// A platform peer may delete anyone's post.
	p2 := e.post(t, bob, map[string]interface{}{"body": "bob's post", "category": "general"})
	if w = e.do(t, http.MethodDelete, "/api/board/posts/"+p2.ID, nil, platform); w.Code != http.StatusOK {
		t.Fatalf("platform delete: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodDelete, "/api/board/posts/not-a-uuid", nil, alice); w.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: %d", w.Code)
	}
}

func TestR2_DuplicateAndUpvoteCapCodes(t *testing.T) {
	e := newR2Env(t)
	alice, bob := e.key(t, "alice"), e.key(t, "bob")
	e.post(t, alice, map[string]interface{}{"body": "same text", "category": "general"})
	w := e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "Same  TEXT", "category": "general"}, alice)
	if w.Code != http.StatusConflict || errCode(t, w) != "DUPLICATE_POST" {
		t.Fatalf("duplicate post: %d %s", w.Code, w.Body.String())
	}
	p := e.post(t, bob, map[string]interface{}{"body": "thread", "category": "general"})
	e.reply(t, alice, p.ID, "my reply")
	w = e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "my reply"}, alice)
	if w.Code != http.StatusConflict || errCode(t, w) != "DUPLICATE_REPLY" {
		t.Fatalf("duplicate reply: %d %s", w.Code, w.Body.String())
	}
	links := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		links = append(links, fmt.Sprintf("https://x.example/%d", i))
	}
	w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": strings.Join(links, " "), "category": "general"}, alice)
	if w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION_ERROR" || !strings.Contains(w.Body.String(), "at most 5 links") {
		t.Fatalf("six links: %d %s", w.Code, w.Body.String())
	}
	// Upvote cap (2 in this env): the third is 429 UPVOTE_LIMIT with the standard body.
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, e.post(t, bob, map[string]interface{}{"body": fmt.Sprintf("post %d", i), "category": "general"}).ID)
	}
	for i := 0; i < 2; i++ {
		if w = e.do(t, http.MethodPost, "/api/board/posts/"+ids[i]+"/upvote", nil, alice); w.Code != http.StatusOK {
			t.Fatalf("upvote %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+ids[2]+"/upvote", nil, alice)
	if w.Code != http.StatusTooManyRequests || errCode(t, w) != "UPVOTE_LIMIT" {
		t.Fatalf("capped upvote: %d %s", w.Code, w.Body.String())
	}
}

func TestR2_DisputeAndModerationQueue(t *testing.T) {
	e := newR2Env(t)
	alice, bob, carol, platform := e.key(t, "alice"), e.key(t, "bob"), e.key(t, "carol"), e.key(t, "platform")
	e.named(t, "alice", "alice_a")
	seedAccountWithCreditsFor(t, e, "alice", 1000)
	seedAccountWithCreditsFor(t, e, "bob", 0)
	b := e.bounty(t, alice, "help me", 100, 7)
	rb := e.reply(t, bob, b.ID, "bob's answer")
	e.reply(t, carol, b.ID, "carol's answer")
	w := e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/bounty/dispute", map[string]interface{}{"note": "unfair"}, carol)
	if w.Code != http.StatusConflict || errCode(t, w) != "DISPUTE_NOT_ALLOWED" {
		t.Fatalf("dispute open bounty: %d %s", w.Code, w.Body.String())
	}
	e.named(t, "bob", "bob_the_builder")
	if w = e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/award", map[string]interface{}{"reply_id": rb.ID}, alice); w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}
	// the card names the winner, not a peer id.
	var awarded PortalPost
	decodeR2(t, w.Body.Bytes(), &awarded)
	if awarded.Bounty == nil || awarded.Bounty.AwardedTo != "bob" || awarded.Bounty.AwardedToDisplayName != "bob_the_builder" {
		t.Errorf("awarded bounty DTO = %+v", awarded.Bounty)
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/bounty/dispute", map[string]interface{}{"note": "mine"}, alice)
	if w.Code != http.StatusForbidden || errCode(t, w) != "DISPUTE_NOT_REPLIER" {
		t.Fatalf("author disputes: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/bounty/dispute", map[string]interface{}{"note": "unfair"}, carol)
	if w.Code != http.StatusOK {
		t.Fatalf("dispute: %d %s", w.Code, w.Body.String())
	}
	var disputed PortalPost
	decodeR2(t, w.Body.Bytes(), &disputed)
	if disputed.Dispute == nil || disputed.Dispute.Status != "open" || disputed.Dispute.ByPeerID != "carol" || disputed.Dispute.Note != "unfair" || disputed.Dispute.OpenedAt == nil {
		t.Errorf("dispute DTO = %+v", disputed.Dispute)
	}
	// The queue: platform only; carries the dispute and the folded reports.
	spam := e.post(t, bob, map[string]interface{}{"body": "buy now", "category": "general"})
	for _, k := range []string{alice, carol} {
		if w = e.do(t, http.MethodPost, "/api/board/posts/"+spam.ID+"/report", map[string]interface{}{"reason": "spam"}, k); w.Code != http.StatusOK {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
	}
	if w = e.do(t, http.MethodGet, "/api/board/moderation/queue", nil, bob); w.Code != http.StatusForbidden || errCode(t, w) != "NOT_PLATFORM" {
		t.Fatalf("queue for non-platform: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodGet, "/api/board/moderation/queue", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("queue without key: %d", w.Code)
	}
	w = e.do(t, http.MethodGet, "/api/board/moderation/queue", nil, platform)
	var queue PortalModerationQueue
	decodeR2(t, w.Body.Bytes(), &queue)
	if w.Code != http.StatusOK || len(queue.Targets) != 1 || queue.Targets[0].ReportCount != 2 || queue.Targets[0].Reasons["spam"] != 2 || queue.Targets[0].Excerpt != "buy now" {
		t.Errorf("queue targets = %d %+v", w.Code, queue.Targets)
	}
	if len(queue.Disputes) != 1 || queue.Disputes[0].ID != b.ID || queue.Disputes[0].AuthorDisplayName != "alice_a" {
		t.Errorf("queue disputes = %+v", queue.Disputes)
	}
	if w = e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/bounty/dispute/resolve", map[string]interface{}{"uphold": true}, carol); w.Code != http.StatusForbidden {
		t.Fatalf("resolve by non-platform: %d", w.Code)
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/bounty/dispute/resolve", map[string]interface{}{"uphold": true}, platform)
	var resolved PortalPost
	decodeR2(t, w.Body.Bytes(), &resolved)
	if w.Code != http.StatusOK || resolved.Dispute == nil || resolved.Dispute.Status != "upheld" || resolved.Dispute.ResolvedAt == nil {
		t.Errorf("resolved = %d %+v", w.Code, resolved.Dispute)
	}
	if w = e.do(t, http.MethodPost, "/api/board/posts/"+b.ID+"/bounty/dispute/resolve", map[string]interface{}{"uphold": true}, platform); w.Code != http.StatusConflict || errCode(t, w) != "DISPUTE_NOT_OPEN" {
		t.Fatalf("resolve twice: %d %s", w.Code, w.Body.String())
	}
	// Both sides hear about it in the bell.
	for _, k := range []string{alice, carol} {
		w = e.do(t, http.MethodGet, "/api/activity?kinds=bounty_dispute_resolved", nil, k)
		var feed PortalActivityResponse
		decodeR2(t, w.Body.Bytes(), &feed)
		if len(feed.Items) != 1 {
			t.Errorf("resolved activity = %+v", feed.Items)
		}
	}
}

// seedAccountWithCreditsFor gives a peer an account with free credits in the p1 env.
func seedAccountWithCreditsFor(t *testing.T, e *r2Env, peerID string, free int) {
	t.Helper()
	ctx := context.Background()
	id := "acct-" + peerID
	if err := e.accounts.Create(ctx, &models.Account{ID: id, PeerID: peerID, Status: models.AccountStatusActive, CreatedAt: e.clock.Now()}); err != nil {
		t.Fatalf("account: %v", err)
	}
	if err := e.credits.CreateBalance(ctx, &models.CreditBalance{AccountID: id}); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if free > 0 {
		if err := e.credits.CreditFree(ctx, id, free, "test", "seed-"+peerID, e.clock.Now().Add(30*24*time.Hour)); err != nil {
			t.Fatalf("credit: %v", err)
		}
	}
}

func TestR2_RoomSettingsAndMutesRoutes(t *testing.T) {
	e := newR2Env(t)
	agent, holder := e.seen(t, "agent"), e.seen(t, "holder")
	mint := e.room(t, "MintR", "RRR", "agent")
	e.holds(t, "holder", mint, 1)
	e.wallet(t, "agent", "wallet-agent")

	w := e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/settings", nil, "")
	var s PortalRoomSettings
	decodeR2(t, w.Body.Bytes(), &s)
	if w.Code != http.StatusOK || !s.Routing || s.MinHoldRaw != 1 {
		t.Fatalf("default settings = %d %+v", w.Code, s)
	}
	if w = e.do(t, http.MethodGet, "/api/board/rooms/NotAMint/settings", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("settings of a non-room: %d", w.Code)
	}
	if w = e.do(t, http.MethodPut, "/api/board/rooms/"+mint+"/settings", map[string]interface{}{"routing": false}, holder); w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_NOT_AGENT" {
		t.Fatalf("settings by holder: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodPut, "/api/board/rooms/"+mint+"/settings", map[string]interface{}{"min_hold_raw": 0}, agent); w.Code != http.StatusBadRequest {
		t.Fatalf("min hold 0: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPut, "/api/board/rooms/"+mint+"/settings", map[string]interface{}{"routing": false, "min_hold_raw": 5}, agent)
	decodeR2(t, w.Body.Bytes(), &s)
	if w.Code != http.StatusOK || s.Routing || s.MinHoldRaw != 5 {
		t.Fatalf("set settings = %d %+v", w.Code, s)
	}
	// The holder with 1 unit is now under the minimum.
	w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "hi room", "category": "general", "room_mint": mint}, holder)
	if w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_NOT_HOLDER" {
		t.Fatalf("under the minimum: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodPut, "/api/board/rooms/"+mint+"/settings", map[string]interface{}{"min_hold_raw": 1}, agent); w.Code != http.StatusOK {
		t.Fatalf("reset min hold: %d", w.Code)
	}
	// Mutes.
	if w = e.do(t, http.MethodPost, "/api/board/rooms/"+mint+"/mutes", map[string]interface{}{"peer_id": "holder"}, holder); w.Code != http.StatusForbidden {
		t.Fatalf("mute by holder: %d", w.Code)
	}
	if w = e.do(t, http.MethodPost, "/api/board/rooms/"+mint+"/mutes", map[string]interface{}{"peer_id": ""}, agent); w.Code != http.StatusBadRequest {
		t.Fatalf("mute without peer: %d", w.Code)
	}
	w = e.do(t, http.MethodPost, "/api/board/rooms/"+mint+"/mutes", map[string]interface{}{"peer_id": "holder", "reason": "spam"}, agent)
	if w.Code != http.StatusOK {
		t.Fatalf("mute: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "muted", "category": "general", "room_mint": mint}, holder)
	if w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_MUTED" {
		t.Fatalf("muted post: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/mutes", nil, agent)
	var mutes []PortalRoomMute
	decodeR2(t, w.Body.Bytes(), &mutes)
	if w.Code != http.StatusOK || len(mutes) != 1 || mutes[0].PeerID != "holder" || mutes[0].DisplayName != "holder" || mutes[0].Reason != "spam" {
		t.Errorf("mutes = %d %+v", w.Code, mutes)
	}
	if w = e.do(t, http.MethodDelete, "/api/board/rooms/"+mint+"/mutes/holder", nil, agent); w.Code != http.StatusOK {
		t.Fatalf("unmute: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodDelete, "/api/board/rooms/"+mint+"/mutes/holder", nil, agent); w.Code != http.StatusNotFound {
		t.Fatalf("unmute twice: %d", w.Code)
	}
	if w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "back", "category": "general", "room_mint": mint}, holder); w.Code != http.StatusOK {
		t.Fatalf("post after unmute: %d %s", w.Code, w.Body.String())
	}
}

func TestR2_ActivityPrefsAndReasons(t *testing.T) {
	e := newR2Env(t)
	alice, bob := e.seen(t, "alice"), e.seen(t, "bob")
	_ = e.autopilot.Set(context.Background(), "bob", []string{"request"}, e.clock.Now())
	p := e.post(t, alice, map[string]interface{}{"body": "need a hand", "category": "request"})
	if p.RoutedCount != 1 {
		t.Fatalf("routed_count = %d", p.RoutedCount)
	}
	w := e.do(t, http.MethodGet, "/api/activity", nil, bob)
	var feed PortalActivityResponse
	decodeR2(t, w.Body.Bytes(), &feed)
	if len(feed.Items) != 1 || feed.Items[0].Kind != models.ActivityRequestRouted || strings.Join(feed.Items[0].Reasons, ",") != "category" {
		t.Errorf("routed item = %+v", feed.Items)
	}
	// The post itself tells the routed viewer why, and nobody else.
	if got, _ := e.getPost(t, p.ID, bob); strings.Join(got.RoutedReasons, ",") != "category" {
		t.Errorf("routed reasons on the post for bob = %v", got.RoutedReasons)
	}
	if got, _ := e.getPost(t, p.ID, alice); len(got.RoutedReasons) != 0 {
		t.Errorf("routed reasons leaked to the author = %v", got.RoutedReasons)
	}
	// Prefs: GET defaults, PUT validates, the bell honours them, kinds= does not.
	e.reply(t, bob, p.ID, "here")
	w = e.do(t, http.MethodGet, "/api/activity/prefs", nil, alice)
	var prefs PortalNotificationPrefs
	decodeR2(t, w.Body.Bytes(), &prefs)
	if w.Code != http.StatusOK || len(prefs.MutedKinds) != 0 || len(prefs.Kinds) != len(models.ActivityKinds) {
		t.Fatalf("default prefs = %d %+v", w.Code, prefs)
	}
	if w = e.do(t, http.MethodPut, "/api/activity/prefs", map[string]interface{}{"muted_kinds": []string{"nope"}}, alice); w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodPut, "/api/activity/prefs", map[string]interface{}{}, alice); w.Code != http.StatusBadRequest {
		t.Fatalf("missing muted_kinds: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPut, "/api/activity/prefs", map[string]interface{}{"muted_kinds": []string{"reply_on_post"}}, alice)
	decodeR2(t, w.Body.Bytes(), &prefs)
	if w.Code != http.StatusOK || strings.Join(prefs.MutedKinds, ",") != "reply_on_post" {
		t.Fatalf("set prefs = %d %+v", w.Code, prefs)
	}
	w = e.do(t, http.MethodGet, "/api/activity", nil, alice)
	decodeR2(t, w.Body.Bytes(), &feed)
	if len(feed.Items) != 0 || feed.Unread != 0 {
		t.Errorf("muted bell = %+v", feed)
	}
	w = e.do(t, http.MethodGet, "/api/activity?kinds=reply_on_post", nil, alice)
	decodeR2(t, w.Body.Bytes(), &feed)
	if len(feed.Items) != 1 {
		t.Errorf("explicit kind = %+v", feed)
	}
	if w = e.do(t, http.MethodGet, "/api/activity/prefs", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("prefs without key: %d", w.Code)
	}
}

func TestR2_CursorVisitAndRoomUnread(t *testing.T) {
	e := newR2Env(t)
	alice := e.seen(t, "alice")
	for i := 0; i < 5; i++ {
		e.clock.Advance(time.Second)
		e.post(t, alice, map[string]interface{}{"body": fmt.Sprintf("post %d", i), "category": "general"})
	}
	type page struct {
		Data []PortalPost    `json:"data"`
		Meta PortalBoardMeta `json:"meta"`
	}
	w := e.do(t, http.MethodGet, "/api/board/posts?tab=recent&limit=2&visit=1", nil, alice)
	var p1 page
	if err := json.Unmarshal(w.Body.Bytes(), &p1); err != nil || w.Code != http.StatusOK {
		t.Fatalf("page 1: %d %v %s", w.Code, err, w.Body.String())
	}
	if p1.Meta.Total != 5 || p1.Meta.NextCursor == "" || p1.Meta.LastVisitAt != nil || p1.Data[0].Content != "post 4" {
		t.Fatalf("page 1 meta = %+v first=%s", p1.Meta, p1.Data[0].Content)
	}
	// A post lands between the pages; the cursor page still follows page 1 without a repeat.
	e.clock.Advance(time.Second)
	e.post(t, alice, map[string]interface{}{"body": "landed", "category": "general"})
	w = e.do(t, http.MethodGet, "/api/board/posts?tab=recent&limit=2&cursor="+p1.Meta.NextCursor, nil, alice)
	var p2 page
	_ = json.Unmarshal(w.Body.Bytes(), &p2)
	if w.Code != http.StatusOK || len(p2.Data) != 2 || p2.Data[0].Content != "post 2" || p2.Data[1].Content != "post 1" || p2.Meta.Total != 6 {
		t.Errorf("page 2 = %d %+v meta=%+v", w.Code, contentsOf(p2.Data), p2.Meta)
	}
	w = e.do(t, http.MethodGet, "/api/board/posts?tab=recent&limit=2&cursor="+p2.Meta.NextCursor, nil, alice)
	var p3 page
	_ = json.Unmarshal(w.Body.Bytes(), &p3)
	if len(p3.Data) != 1 || p3.Data[0].Content != "post 0" || p3.Meta.NextCursor != "" {
		t.Errorf("page 3 = %+v meta=%+v", contentsOf(p3.Data), p3.Meta)
	}
	if w = e.do(t, http.MethodGet, "/api/board/posts?tab=top&cursor="+p1.Meta.NextCursor, nil, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("cursor on top: %d", w.Code)
	}
	if w = e.do(t, http.MethodGet, "/api/board/posts?cursor=junk", nil, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", w.Code)
	}
	// The second visit answers the first one; an anonymous visit is ignored.
	e.clock.Advance(time.Minute)
	w = e.do(t, http.MethodGet, "/api/board/posts?tab=recent&limit=2&visit=1", nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &p1)
	if p1.Meta.LastVisitAt == nil {
		t.Errorf("second visit meta = %+v", p1.Meta)
	}
	w = e.do(t, http.MethodGet, "/api/board/posts?tab=recent&visit=1", nil, "")
	var anon page
	_ = json.Unmarshal(w.Body.Bytes(), &anon)
	if anon.Meta.LastVisitAt != nil {
		t.Errorf("anonymous visit meta = %+v", anon.Meta)
	}
	// Rooms: unread per room from the visit.
	agent := e.seen(t, "agent")
	mint := e.room(t, "MintU", "UUU", "agent")
	e.wallet(t, "agent", "wallet-agent")
	e.post(t, agent, map[string]interface{}{"body": "room post", "category": "general", "room_mint": mint})
	w = e.do(t, http.MethodGet, "/api/board/rooms?mine=1", nil, agent)
	var rooms []PortalRoom
	decodeR2(t, w.Body.Bytes(), &rooms)
	if w.Code != http.StatusOK || len(rooms) != 1 || rooms[0].Unread == nil || *rooms[0].Unread != 1 {
		t.Fatalf("rooms before visit = %d %+v", w.Code, rooms)
	}
	e.clock.Advance(time.Second)
	if w = e.do(t, http.MethodGet, "/api/board/posts?room="+mint+"&visit=1", nil, agent); w.Code != http.StatusOK {
		t.Fatalf("room visit: %d", w.Code)
	}
	w = e.do(t, http.MethodGet, "/api/board/rooms?mine=1", nil, agent)
	decodeR2(t, w.Body.Bytes(), &rooms)
	if *rooms[0].Unread != 0 {
		t.Errorf("rooms after visit = %+v", rooms)
	}
}

func contentsOf(posts []PortalPost) []string {
	out := make([]string, 0, len(posts))
	for _, p := range posts {
		out = append(out, p.Content)
	}
	return out
}

func TestR2_AutopilotPausedCode(t *testing.T) {
	e := newR2Env(t)
	alice, bot := e.key(t, "alice"), e.key(t, "bot")
	p := e.post(t, alice, map[string]interface{}{"body": "a request", "category": "request"})
	e.forum.SetAutopilotPaused(true)
	w := e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "auto", "auto": true}, bot)
	if w.Code != http.StatusServiceUnavailable || errCode(t, w) != "AUTOPILOT_PAUSED" {
		t.Fatalf("paused auto reply: %d %s", w.Code, w.Body.String())
	}
	if w = e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "manual"}, bot); w.Code != http.StatusOK {
		t.Fatalf("manual while paused: %d %s", w.Code, w.Body.String())
	}
}

func TestR2_EditViaPostAlias(t *testing.T) {
	e := newR2Env(t)
	alice := e.key(t, "alice")
	p := e.post(t, alice, map[string]interface{}{"body": "before", "category": "general"})
	w := e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/edit", map[string]interface{}{"body": "after"}, alice)
	var edited PortalPost
	decodeR2(t, w.Body.Bytes(), &edited)
	if w.Code != http.StatusOK || edited.Content != "after" || edited.EditCount != 1 {
		t.Fatalf("edit via POST alias = %d %+v", w.Code, edited)
	}
	r := e.reply(t, alice, p.ID, "reply before")
	w = e.do(t, http.MethodPost, "/api/board/replies/"+r.ID+"/edit", map[string]interface{}{"body": "reply after"}, alice)
	var er PortalReply
	decodeR2(t, w.Body.Bytes(), &er)
	if w.Code != http.StatusOK || er.Content != "reply after" {
		t.Fatalf("reply edit via POST alias = %d %+v", w.Code, er)
	}
}
