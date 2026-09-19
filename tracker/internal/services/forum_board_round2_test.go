// Package services: Community board round 2 tests over the memory repositories:
// author edits within the window with history, soft deletes as tombstones, duplicate posts
// and replies, link rules, the daily upvote cap, bounty disputes and the reviewer queue, room
// controls (routing switch, minimum holding, mutes), the per-room autopilot cap and the global
// circuit breaker, visits with unread counts, notification preferences and routing reasons.
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// r2Env is p2Env with the round 2 repositories wired and the round 2 settings on.
type r2Env struct {
	*p2Env
	edits  *repository.MemoryBoardEditHistoryRepository
	rooms  *repository.MemoryBoardRoomRepository
	visits *repository.MemoryBoardVisitRepository
	prefs  *repository.MemoryBoardNotificationPrefRepository
}

func newR2Env(t *testing.T) *r2Env {
	t.Helper()
	e := newP2Env(t)
	env := &r2Env{
		p2Env:  e,
		edits:  repository.NewMemoryBoardEditHistoryRepository(),
		rooms:  repository.NewMemoryBoardRoomRepository(),
		visits: repository.NewMemoryBoardVisitRepository(),
		prefs:  repository.NewMemoryBoardNotificationPrefRepository(),
	}
	e.svc.SetRound2(Round2Deps{Edits: env.edits, Rooms: env.rooms, Visits: env.visits, Prefs: env.prefs},
		Round2Settings{MaxUpvotesPerPeerPerDay: 3, AutopilotMaxRepliesPerRoom: 2, MinBountyAmount: 10})
	return env
}

func TestRound2_EditWithinWindowKeepsHistory(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "First title", "first body", "general")
	if _, err := e.svc.EditPost(ctx, "bob", p.ID, "", "bob was here"); !errors.Is(err, ErrNotAuthor) {
		t.Errorf("edit by other: %v", err)
	}
	e.clk.Advance(10 * time.Minute)
	edited, err := e.svc.EditPost(ctx, "alice", p.ID, "Second title", "second body with https://example.com/a")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Title != "Second title" || edited.Description != "second body with https://example.com/a" || edited.EditCount != 1 || edited.EditedAt == nil {
		t.Errorf("edited post = %+v", edited)
	}
	// The same text again is a no-op (no history row, no count).
	again, _ := e.svc.EditPost(ctx, "alice", p.ID, "Second title", "second body with https://example.com/a")
	if again.EditCount != 1 {
		t.Errorf("no-op edit bumped the count: %d", again.EditCount)
	}
	history, err := e.svc.EditHistory(ctx, "alice", models.ReportTargetPost, p.ID)
	if err != nil || len(history) != 1 || history[0].PreviousTitle != "First title" || history[0].PreviousBody != "first body" || history[0].EditorPeerID != "alice" {
		t.Errorf("history = %+v err=%v", history, err)
	}
	if _, err := e.svc.EditHistory(ctx, "bob", models.ReportTargetPost, p.ID); !errors.Is(err, ErrNotAuthor) {
		t.Errorf("history for other: %v", err)
	}
	if h, err := e.svc.EditHistory(ctx, "platform", models.ReportTargetPost, p.ID); err != nil || len(h) != 1 {
		t.Errorf("history for platform: %v %v", h, err)
	}
	// Past the window the edit is refused.
	e.clk.Advance(DefaultEditWindow)
	if _, err := e.svc.EditPost(ctx, "alice", p.ID, "", "too late"); !errors.Is(err, ErrEditWindowClosed) {
		t.Errorf("late edit: %v", err)
	}
	// Replies edit the same way.
	r := e.reply(t, p.ID, "bob", "a reply")
	if _, err := e.svc.EditReply(ctx, "alice", r.ID, "not mine"); !errors.Is(err, ErrNotAuthor) {
		t.Errorf("reply edit by other: %v", err)
	}
	er, err := e.svc.EditReply(ctx, "bob", r.ID, "a better reply")
	if err != nil || er.Body != "a better reply" || er.EditCount != 1 {
		t.Errorf("reply edit = %+v err=%v", er, err)
	}
	if h, _ := e.svc.EditHistory(ctx, "bob", models.ReportTargetReply, r.ID); len(h) != 1 || h[0].PreviousBody != "a reply" {
		t.Errorf("reply history = %+v", h)
	}
}

func TestRound2_DeleteIsATombstone(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	p := e.post(t, "alice", "Keep me", "body", "general")
	r := e.reply(t, p.ID, "bob", "hello")
	if err := e.svc.DeletePost(ctx, "bob", p.ID); !errors.Is(err, ErrNotAuthor) {
		t.Errorf("delete by other: %v", err)
	}
	if err := e.svc.DeletePost(ctx, "alice", p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := e.svc.DeletePost(ctx, "alice", p.ID); !errors.Is(err, ErrDeleted) {
		t.Errorf("second delete: %v", err)
	}
	got, err := e.svc.GetPost(ctx, p.ID)
	if err != nil || !got.IsDeleted() || got.Title != "Keep me" {
		t.Errorf("tombstone = %+v err=%v", got, err)
	}
	// Feeds, counts and the per-agent views leave the post out; the thread still reads.
	if _, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10}); total != 0 {
		t.Errorf("feed total after delete = %d", total)
	}
	if _, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10, Author: "alice"}); total != 0 {
		t.Errorf("author view after delete = %d", total)
	}
	if c, _ := e.svc.BoardCounts(ctx, ""); c.All != 0 {
		t.Errorf("counts after delete = %d", c.All)
	}
	replies, _, _ := e.svc.ListReplies(ctx, p.ID, repository.ReplyQuery{})
	if len(replies) != 1 || replies[0].ID != r.ID {
		t.Errorf("thread of a deleted post = %v", replies)
	}
	// No more writes on it.
	if _, err := e.svc.CreateReply(ctx, p.ID, "carol", "late", false); !errors.Is(err, ErrDeleted) {
		t.Errorf("reply on deleted: %v", err)
	}
	if _, _, err := e.svc.ToggleUpvote(ctx, "carol", p.ID); !errors.Is(err, ErrDeleted) {
		t.Errorf("upvote on deleted: %v", err)
	}
	// A reply tombstone keeps its place; the platform may delete anyone's.
	p2 := e.post(t, "alice", "Two", "body two", "general")
	r2 := e.reply(t, p2.ID, "bob", "gone soon")
	if err := e.svc.DeleteReply(ctx, "platform", r2.ID); err != nil {
		t.Fatalf("platform delete reply: %v", err)
	}
	got2, _ := e.svc.GetReply(ctx, r2.ID)
	if !got2.IsDeleted() {
		t.Errorf("reply not a tombstone: %+v", got2)
	}
	if _, err := e.svc.EditReply(ctx, "bob", r2.ID, "edit a tombstone"); !errors.Is(err, ErrDeleted) {
		t.Errorf("edit deleted reply: %v", err)
	}
	// An open bounty blocks the delete until it settles.
	b := e.bounty(t, "alice", "Bounty", 100, 7)
	if err := e.svc.DeletePost(ctx, "alice", b.ID); !errors.Is(err, ErrBountyOpenOnDelete) {
		t.Errorf("delete with open bounty: %v", err)
	}
}

func TestRound2_DuplicatesAndLinks(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	body := "Selling signals, DM me now"
	if _, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "One", Description: body, Category: "general"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same body, other spacing and case: a duplicate; another peer: fine.
	if _, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "Two", Description: "  selling   SIGNALS, dm me now ", Category: "general"}); !errors.Is(err, ErrDuplicatePost) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := e.svc.CreatePost(ctx, "bob", CreatePostInput{Title: "Two", Description: body, Category: "general"}); err != nil {
		t.Errorf("other peer same body: %v", err)
	}
	e.clk.Advance(DuplicateWindow + time.Minute)
	if _, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "Three", Description: body, Category: "general"}); err != nil {
		t.Errorf("after the window: %v", err)
	}
	p := e.post(t, "carol", "Thread", "thread body", "general")
	if _, err := e.svc.CreateReply(ctx, p.ID, "alice", "same reply", false); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, p.ID, "alice", "same reply", false); !errors.Is(err, ErrDuplicateReply) {
		t.Errorf("duplicate reply: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, p.ID, "bob", "same reply", false); err != nil {
		t.Errorf("other peer duplicate reply: %v", err)
	}
	// Links: at most five, http(s)/ipfs/mailto only.
	var links []string
	for i := 0; i < 6; i++ {
		links = append(links, fmt.Sprintf("https://example.com/%d", i))
	}
	_, err := e.svc.CreatePost(ctx, "dave", CreatePostInput{Title: "Links", Description: strings.Join(links, " "), Category: "general"})
	var textErr *BoardTextError
	if !errors.As(err, &textErr) || !strings.Contains(err.Error(), "at most 5 links") {
		t.Errorf("six links: %v", err)
	}
	if _, err := e.svc.CreatePost(ctx, "dave", CreatePostInput{Title: "Links", Description: strings.Join(links[:5], " "), Category: "general"}); err != nil {
		t.Errorf("five links: %v", err)
	}
	_, err = e.svc.CreateReply(ctx, p.ID, "dave", "click javascript:alert(1) please", false)
	if !errors.As(err, &textErr) || !strings.Contains(err.Error(), "javascript") {
		t.Errorf("javascript scheme: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, p.ID, "dave", "note: this is fine, see ipfs://bafy123 and mailto:a@b.c", false); err != nil {
		t.Errorf("prose colon and allowed schemes: %v", err)
	}
}

func TestRound2_UpvoteDailyCapAndMinBounty(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	var posts []*models.ForumPost
	for i := 0; i < 4; i++ {
		posts = append(posts, e.post(t, "alice", fmt.Sprintf("P%d", i), fmt.Sprintf("body %d", i), "general"))
	}
	for i := 0; i < 3; i++ {
		if _, _, err := e.svc.ToggleUpvote(ctx, "bob", posts[i].ID); err != nil {
			t.Fatalf("upvote %d: %v", i, err)
		}
	}
	if _, _, err := e.svc.ToggleUpvote(ctx, "bob", posts[3].ID); !errors.Is(err, ErrUpvoteLimit) {
		t.Errorf("fourth upvote: %v", err)
	}
	// Removing one does not free a slot today; tomorrow does.
	if _, _, err := e.svc.ToggleUpvote(ctx, "bob", posts[0].ID); err != nil {
		t.Errorf("remove: %v", err)
	}
	if _, _, err := e.svc.ToggleUpvote(ctx, "bob", posts[3].ID); !errors.Is(err, ErrUpvoteLimit) {
		t.Errorf("after removal: %v", err)
	}
	e.clk.Advance(24 * time.Hour)
	if _, _, err := e.svc.ToggleUpvote(ctx, "bob", posts[3].ID); err != nil {
		t.Errorf("next day: %v", err)
	}
	// The minimum bounty.
	amount := 5
	if _, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "Tiny", Description: "tiny bounty", Category: "bounty", BountyAmount: &amount}); !errors.Is(err, ErrBountyBelowMin) {
		t.Errorf("bounty under the minimum: %v", err)
	}
	amount = 10
	if _, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "Ok", Description: "ok bounty", Category: "bounty", BountyAmount: &amount}); err != nil {
		t.Errorf("bounty at the minimum: %v", err)
	}
}

func TestRound2_BountyDisputeLifecycle(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)
	e.account(t, "carol", 0)
	b := e.bounty(t, "alice", "Help", 100, 7)
	rb := e.reply(t, b.ID, "bob", "bob's answer")
	e.reply(t, b.ID, "carol", "carol's answer")
	if _, err := e.svc.DisputeBounty(ctx, "carol", b.ID, "not fair"); !errors.Is(err, ErrBountyNotDisputable) {
		t.Errorf("dispute an open bounty: %v", err)
	}
	if err := e.svc.AwardBounty(ctx, "alice", b.ID, rb.ID); err != nil {
		t.Fatalf("award: %v", err)
	}
	if _, err := e.svc.DisputeBounty(ctx, "alice", b.ID, "me"); !errors.Is(err, ErrDisputeNotReplier) {
		t.Errorf("author disputes: %v", err)
	}
	if _, err := e.svc.DisputeBounty(ctx, "dave", b.ID, "me"); !errors.Is(err, ErrDisputeNotReplier) {
		t.Errorf("stranger disputes: %v", err)
	}
	disputed, err := e.svc.DisputeBounty(ctx, "carol", b.ID, "<b>my answer</b> was better")
	if err != nil {
		t.Fatalf("dispute: %v", err)
	}
	if !disputed.HasOpenDispute() || disputed.BountyDisputeBy != "carol" || disputed.BountyDisputeNote != "my answer was better" {
		t.Errorf("disputed = %+v", disputed)
	}
	if rows := e.feed(t, "alice", models.ActivityBountyDisputed); len(rows) != 1 || rows[0].ActorPeerID != "carol" || *rows[0].Amount != 100 {
		t.Errorf("author activity = %+v", rows)
	}
	if _, err := e.svc.DisputeBounty(ctx, "carol", b.ID, "again"); !errors.Is(err, ErrBountyNotDisputable) {
		t.Errorf("second dispute: %v", err)
	}
	// The reviewer queue lists it; a platform peer resolves it.
	_, disputes, err := e.svc.ModerationQueue(ctx)
	if err != nil || len(disputes) != 1 || disputes[0].ID != b.ID {
		t.Errorf("queue disputes = %v err=%v", disputes, err)
	}
	resolved, err := e.svc.ResolveBountyDispute(ctx, "platform", b.ID, false)
	if err != nil || resolved.BountyDisputeStatus != models.BountyDisputeDismissed || resolved.BountyDisputeResolvedAt == nil {
		t.Errorf("resolved = %+v err=%v", resolved, err)
	}
	if _, err := e.svc.ResolveBountyDispute(ctx, "platform", b.ID, true); !errors.Is(err, ErrNoOpenDispute) {
		t.Errorf("resolve twice: %v", err)
	}
	for _, peer := range []string{"alice", "carol"} {
		if rows := e.feed(t, peer, models.ActivityBountyDisputeResolved); len(rows) != 1 {
			t.Errorf("%s resolved activity = %+v", peer, rows)
		}
	}
	// The window: an expiry older than BountyDisputeWindow is closed.
	b2 := e.bounty(t, "alice", "Old", 100, 1)
	e.reply(t, b2.ID, "carol", "answer")
	e.clk.Advance(2 * 24 * time.Hour)
	if _, err := e.svc.ExpireBounties(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	}
	e.clk.Advance(BountyDisputeWindow)
	if _, err := e.svc.DisputeBounty(ctx, "carol", b2.ID, "late"); !errors.Is(err, ErrDisputeWindowClosed) {
		t.Errorf("late dispute: %v", err)
	}
}

func TestRound2_ModerationQueueGroupsReports(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Spam", "buy now", "general")
	r := e.reply(t, p.ID, "alice", "and here")
	for _, who := range []string{"bob", "carol", "dave"} {
		if _, _, err := e.svc.Report(ctx, who, models.ReportTargetPost, p.ID, "spam", ""); err != nil {
			t.Fatalf("report by %s: %v", who, err)
		}
	}
	if _, _, err := e.svc.Report(ctx, "bob", models.ReportTargetReply, r.ID, "abuse", "rude"); err != nil {
		t.Fatalf("report reply: %v", err)
	}
	targets, _, err := e.svc.ModerationQueue(ctx)
	if err != nil || len(targets) != 2 {
		t.Fatalf("queue = %v err=%v", targets, err)
	}
	if targets[0].TargetID != p.ID || len(targets[0].Reports) != 3 || targets[0].Excerpt != "buy now" || targets[0].PostID != p.ID {
		t.Errorf("first target = %+v", targets[0])
	}
	if targets[1].TargetID != r.ID || len(targets[1].Reports) != 1 || targets[1].PostID != p.ID {
		t.Errorf("second target = %+v", targets[1])
	}
}

func TestRound2_RoomControls(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintR", "RRR", "agent")
	e.holds(t, "holder", mint, 5)
	e.holds(t, "small", mint, 2)
	e.wallet(t, "agent", "wallet-agent")

	// Defaults, then the agent raises the minimum holding: a small holder loses access.
	s, err := e.svc.RoomSettings(ctx, mint)
	if err != nil || !s.Routing || s.MinHoldRaw != 1 {
		t.Fatalf("defaults = %+v err=%v", s, err)
	}
	if _, err := e.svc.SetRoomSettings(ctx, "holder", mint, false, 1); !errors.Is(err, ErrRoomNotAgent) {
		t.Errorf("settings by holder: %v", err)
	}
	if _, err := e.svc.SetRoomSettings(ctx, "agent", mint, true, 3); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if _, err := e.svc.CreatePost(ctx, "small", CreatePostInput{Title: "hi", Description: "small holder", RoomMint: mint}); !errors.Is(err, ErrRoomNotHolder) {
		t.Errorf("small holder under the minimum: %v", err)
	}
	if _, err := e.svc.CreatePost(ctx, "holder", CreatePostInput{Title: "hi", Description: "big holder", RoomMint: mint}); err != nil {
		t.Errorf("holder at the minimum: %v", err)
	}
	// Mutes: the agent mutes a holder, who can no longer post or reply; the agent itself never.
	if _, err := e.svc.MuteInRoom(ctx, "agent", mint, "agent", ""); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("mute self: %v", err)
	}
	if _, err := e.svc.MuteInRoom(ctx, "holder", mint, "small", ""); !errors.Is(err, ErrRoomNotAgent) {
		t.Errorf("mute by holder: %v", err)
	}
	m, err := e.svc.MuteInRoom(ctx, "agent", mint, "holder", "spam <script>x</script>")
	if err != nil || m.Reason != "spam" {
		t.Fatalf("mute = %+v err=%v", m, err)
	}
	if _, err := e.svc.CreatePost(ctx, "holder", CreatePostInput{Title: "hi", Description: "muted holder", RoomMint: mint}); !errors.Is(err, ErrRoomMuted) {
		t.Errorf("muted post: %v", err)
	}
	agentPost := e.roomPost(t, "agent", mint, "Agent post", "general")
	if _, err := e.svc.CreateReply(ctx, agentPost.ID, "holder", "muted reply", false); !errors.Is(err, ErrRoomMuted) {
		t.Errorf("muted reply: %v", err)
	}
	if room, _ := e.svc.GetRoom(ctx, mint, "holder"); room.Role != "" {
		t.Errorf("muted holder role = %q", room.Role)
	}
	mutes, _ := e.svc.ListRoomMutes(ctx, "agent", mint)
	if len(mutes) != 1 || mutes[0].PeerID != "holder" {
		t.Errorf("mutes = %+v", mutes)
	}
	if err := e.svc.UnmuteInRoom(ctx, "agent", mint, "holder"); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if err := e.svc.UnmuteInRoom(ctx, "agent", mint, "holder"); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("unmute twice: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, agentPost.ID, "holder", "back again", false); err != nil {
		t.Errorf("after unmute: %v", err)
	}
	// Routing off: a Request in the room reaches nobody.
	e.seen(t, "holder")
	_ = e.autopilot.Set(ctx, "holder", []string{"request"}, e.clk.Now())
	if _, err := e.svc.SetRoomSettings(ctx, "agent", mint, false, 1); err != nil {
		t.Fatalf("routing off: %v", err)
	}
	req := e.roomPost(t, "agent", mint, "Need help", "request")
	if len(req.RoutedTo) != 0 {
		t.Errorf("routed with routing off: %v", req.RoutedTo)
	}
	if _, err := e.svc.SetRoomSettings(ctx, "agent", mint, true, 1); err != nil {
		t.Fatalf("routing on: %v", err)
	}
	req2 := e.roomPost(t, "agent", mint, "Need help again", "request")
	if len(req2.RoutedTo) != 1 || req2.RoutedTo[0] != "holder" || req2.RoutedReasons == nil {
		t.Errorf("routed with routing on = %v reasons=%v", req2.RoutedTo, req2.RoutedReasons)
	}
	if reasons := req2.RoutedReasons["holder"]; len(reasons) < 1 || reasons[0] != RoutingReasonCategory {
		t.Errorf("reasons = %v", reasons)
	}
}

func TestRound2_AutopilotRoomCapAndBreaker(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintC", "CCC", "agent")
	e.holds(t, "bot", mint, 1)
	e.wallet(t, "agent", "wallet-agent")
	var posts []*models.ForumPost
	for i := 0; i < 3; i++ {
		posts = append(posts, e.roomPost(t, "agent", mint, fmt.Sprintf("Q%d", i), "request"))
	}
	for i := 0; i < 2; i++ {
		if _, err := e.svc.CreateReply(ctx, posts[i].ID, "bot", fmt.Sprintf("auto %d", i), true); err != nil {
			t.Fatalf("auto reply %d: %v", i, err)
		}
	}
	if _, err := e.svc.CreateReply(ctx, posts[2].ID, "bot", "auto 2", true); !errors.Is(err, ErrAutoReplyLimit) {
		t.Errorf("third auto reply in the room today: %v", err)
	}
	// A manual reply is never capped; the main feed has no room cap.
	if _, err := e.svc.CreateReply(ctx, posts[2].ID, "bot", "manual", false); err != nil {
		t.Errorf("manual reply: %v", err)
	}
	main := e.post(t, "agent", "Main", "main body", "request")
	if _, err := e.svc.CreateReply(ctx, main.ID, "bot", "auto main", true); err != nil {
		t.Errorf("auto reply on the main feed: %v", err)
	}
	e.clk.Advance(24 * time.Hour)
	if _, err := e.svc.CreateReply(ctx, posts[2].ID, "bot", "auto next day", true); err != nil {
		t.Errorf("next day: %v", err)
	}
	// The circuit breaker refuses every auto write and nothing else.
	e.svc.SetAutopilotPaused(true)
	p := e.post(t, "agent", "Later", "later body", "request")
	if _, err := e.svc.CreateReply(ctx, p.ID, "bot", "auto paused", true); !errors.Is(err, ErrAutopilotPaused) {
		t.Errorf("paused: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, p.ID, "bot", "manual while paused", false); err != nil {
		t.Errorf("manual while paused: %v", err)
	}
}

func TestRound2_VisitsUnreadAndCursor(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintV", "VVV", "agent")
	e.wallet(t, "agent", "wallet-agent")
	e.roomPost(t, "agent", mint, "Old", "general")
	e.post(t, "agent", "Main old", "main old", "general")
	// Never visited: everything in the last week counts.
	unread := e.svc.UnreadByScope(ctx, "agent", []string{"", mint})
	if unread[""] != 1 || unread[mint] != 1 {
		t.Errorf("unread before any visit = %v", unread)
	}
	prev, err := e.svc.RecordVisit(ctx, "agent", mint)
	if err != nil || prev != nil {
		t.Errorf("first visit prev=%v err=%v", prev, err)
	}
	e.clk.Advance(time.Minute)
	e.roomPost(t, "agent", mint, "New", "general")
	e.clk.Advance(time.Minute)
	unread = e.svc.UnreadByScope(ctx, "agent", []string{mint})
	if unread[mint] != 1 {
		t.Errorf("unread after a new post = %v", unread)
	}
	prev, _ = e.svc.RecordVisit(ctx, "agent", mint)
	if prev == nil || !prev.Equal(e.clk.Now().Add(-2*time.Minute)) {
		t.Errorf("second visit prev=%v", prev)
	}
	if unread = e.svc.UnreadByScope(ctx, "agent", []string{mint}); unread[mint] != 0 {
		t.Errorf("unread right after a visit = %v", unread)
	}

	// Keyset cursor: a post landing between two pages never shifts the next page.
	for i := 0; i < 5; i++ {
		e.clk.Advance(time.Second)
		e.post(t, "agent", fmt.Sprintf("M%d", i), fmt.Sprintf("m body %d", i), "general")
	}
	page1, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 2})
	if total != 6 || len(page1) != 2 || page1[0].Title != "M4" {
		t.Fatalf("page 1 = %v total=%d", titlesOf(page1), total)
	}
	e.clk.Advance(time.Second)
	e.post(t, "agent", "Landed", "landed between pages", "general")
	last := page1[len(page1)-1]
	page2, _, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 2, Before: &repository.PostCursor{CreatedAt: last.CreatedAt, ID: last.ID}})
	if got := titlesOf(page2); strings.Join(got, ",") != "M2,M1" {
		t.Errorf("page 2 = %v", got)
	}
}

func titlesOf(posts []*models.ForumPost) []string {
	out := make([]string, 0, len(posts))
	for _, p := range posts {
		out = append(out, p.Title)
	}
	return out
}

func TestRound2_NotificationPrefsMuteTheBellOnly(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Q", "question", "general")
	e.reply(t, p.ID, "bob", "answer")
	if _, _, err := e.svc.ToggleUpvote(ctx, "bob", p.ID); err != nil {
		t.Fatalf("upvote: %v", err)
	}
	if _, err := e.svc.SetNotificationPrefs(ctx, "alice", []string{"bogus"}); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("bogus kind: %v", err)
	}
	prefs, err := e.svc.SetNotificationPrefs(ctx, "alice", []string{" Post_Upvoted ", "post_upvoted"})
	if err != nil || strings.Join(prefs.MutedKinds, ",") != "post_upvoted" {
		t.Errorf("prefs = %+v err=%v", prefs, err)
	}
	items, unread, err := e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10})
	if err != nil || len(items) != 1 || items[0].Kind != models.ActivityReplyOnPost || unread != 1 {
		t.Errorf("bell = %v unread=%d err=%v", items, unread, err)
	}
	if n, _ := e.svc.CountUnreadActivity(ctx, "alice"); n != 1 {
		t.Errorf("unread count = %d", n)
	}
	// A caller naming kinds (the daemon) still gets the muted kind.
	items, _, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10, Kinds: []string{models.ActivityPostUpvoted}})
	if len(items) != 1 {
		t.Errorf("explicit kinds = %v", items)
	}
	prefs, _ = e.svc.SetNotificationPrefs(ctx, "alice", []string{})
	if len(prefs.MutedKinds) != 0 {
		t.Errorf("cleared prefs = %+v", prefs)
	}
	if _, unread, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10}); unread != 2 {
		t.Errorf("unread after clearing = %d", unread)
	}
}

func TestRound2_ReputationVoterGuard(t *testing.T) {
	e := newR2Env(t)
	ctx := context.Background()
	now := e.clk.Now()
	// old: a week-old peer with a reply of its own. fresh: registered today. idle: old but never
	// wrote anything. twin: old and active but on alice's own wallet.
	e.forum.PeerFirstSeen = map[string]time.Time{"old": now.Add(-7 * 24 * time.Hour), "fresh": now, "idle": now.Add(-7 * 24 * time.Hour), "twin": now.Add(-7 * 24 * time.Hour), "alice": now.Add(-30 * 24 * time.Hour)}
	e.forum.PeerWallet = map[string]string{"alice": "W1", "twin": "W1", "old": "W2"}
	p := e.post(t, "alice", "Q", "question", "general")
	other := e.post(t, "old", "Other", "other body", "general")
	e.reply(t, other.ID, "twin", "twin wrote")
	for _, voter := range []string{"old", "fresh", "idle", "twin"} {
		if _, _, err := e.svc.ToggleUpvote(ctx, voter, p.ID); err != nil {
			t.Fatalf("upvote by %s: %v", voter, err)
		}
	}
	// An accept by a fresh author counts for nothing either.
	fresh := e.post(t, "fresh", "Fresh Q", "fresh question", "general")
	r := e.reply(t, fresh.ID, "alice", "alice answers")
	if _, err := e.svc.AcceptReply(ctx, "fresh", fresh.ID, r.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	e.rep.MinVoterAge = 0
	rep, err := e.rep.Recompute(ctx, "alice")
	if err != nil || rep.UpvotesReceived != 4 || rep.AnswersAccepted != 1 {
		t.Errorf("unguarded = %+v err=%v", rep, err)
	}
	e.rep.MinVoterAge = 3 * 24 * time.Hour
	rep, err = e.rep.Recompute(ctx, "alice")
	if err != nil || rep.UpvotesReceived != 1 || rep.AnswersAccepted != 0 {
		t.Errorf("guarded = %+v err=%v", rep, err)
	}
}
