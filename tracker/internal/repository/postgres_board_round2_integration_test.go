// Package repository: Postgres integration test for the community board round 2 statements
// (migration 030): the round 2 columns round trip on posts and replies, edits and soft deletes,
// duplicate lookups by body hash, the upvote and per-room autopilot counts, routing reasons,
// bounty disputes, per-scope post counts, the keyset cursor of QueryPosts, the guarded
// BoardStats (voter age, activity, shared wallet), and the edit history, room, visit and
// notification preference repositories. Runs only with TRACKER_TEST_DATABASE_URL set, like the
// phase 1 to 3 tests; rows are scoped to a random prefix and deleted at the end.
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestPostgres_BoardRound2(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "itr2-" + uuid.New().String()[:8] + "-"
	alice, bob, fresh, idle, twin := run+"alice", run+"bob", run+"fresh", run+"idle", run+"twin"
	now := time.Now().UTC().Truncate(time.Microsecond)
	mint := run + "Mint"

	peers := NewPostgresPeerRepository(pool)
	for _, p := range []*models.Peer{
		{PeerID: alice, PublicKey: "pk-" + alice, Multiaddrs: []string{}, FirstSeen: now.Add(-30 * 24 * time.Hour), LastSeen: now},
		{PeerID: bob, PublicKey: "pk-" + bob, Multiaddrs: []string{}, FirstSeen: now.Add(-7 * 24 * time.Hour), LastSeen: now},
		{PeerID: fresh, PublicKey: "pk-" + fresh, Multiaddrs: []string{}, FirstSeen: now, LastSeen: now},
		{PeerID: idle, PublicKey: "pk-" + idle, Multiaddrs: []string{}, FirstSeen: now.Add(-7 * 24 * time.Hour), LastSeen: now},
		{PeerID: twin, PublicKey: "pk-" + twin, Multiaddrs: []string{}, FirstSeen: now.Add(-7 * 24 * time.Hour), LastSeen: now},
	} {
		if err := peers.Create(ctx, p); err != nil {
			t.Fatalf("create peer %s: %v", p.PeerID, err)
		}
	}
	accounts := NewPostgresAccountRepository(pool)
	wallets := NewPostgresWalletRepository(pool)
	accountOf := map[string]string{}
	for _, id := range []string{alice, twin, bob} {
		acct := &models.Account{ID: uuid.New().String(), PeerID: id, Status: models.AccountStatusActive, CreatedAt: now}
		if err := accounts.Create(ctx, acct); err != nil {
			t.Fatalf("account %s: %v", id, err)
		}
		accountOf[id] = acct.ID
	}
	// account_wallets is unique per address, so one wallet can never sit on two accounts; the
	// shared-wallet clause of the guard is exercised by the memory repository test. Here twin is
	// an old, active peer on its own wallet and counts like bob.
	for id, addr := range map[string]string{alice: run + "W1", twin: run + "W3", bob: run + "W2"} {
		if err := wallets.LinkWallet(ctx, &models.AccountWallet{AccountID: accountOf[id], WalletAddress: addr, Chain: "solana", LinkedAt: now}); err != nil {
			t.Fatalf("wallet %s: %v", id, err)
		}
	}
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM account_wallets WHERE wallet_address LIKE $1`,
			`DELETE FROM accounts WHERE peer_id LIKE $1`,
			`DELETE FROM board_edit_history WHERE editor_peer_id LIKE $1`,
			`DELETE FROM board_room_settings WHERE mint LIKE $1`,
			`DELETE FROM board_room_mutes WHERE mint LIKE $1`,
			`DELETE FROM board_visits WHERE peer_id LIKE $1`,
			`DELETE FROM board_notification_prefs WHERE peer_id LIKE $1`,
			`DELETE FROM forum_posts WHERE author_peer_id LIKE $1`,
			`DELETE FROM peers WHERE peer_id LIKE $1`,
		} {
			if _, err := pool.Exec(ctx, stmt, run+"%"); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	forum := NewPostgresForumRepository(pool)

	// Posts and replies round trip the round 2 columns.
	post := &models.ForumPost{AuthorPeerID: alice, Title: run + "q", Description: "first body", Category: "general", Tags: []string{},
		CreatedAt: now, UpdatedAt: now, BodyHash: models.BoardBodyHash("first body")}
	if err := forum.CreatePost(ctx, post); err != nil {
		t.Fatalf("create post: %v", err)
	}
	reply := &models.ForumReply{PostID: post.ID, AuthorPeerID: bob, Body: "a reply", CreatedAt: now.Add(time.Minute), BodyHash: models.BoardBodyHash("a reply")}
	if err := forum.CreateReply(ctx, reply); err != nil {
		t.Fatalf("create reply: %v", err)
	}
	got, _ := forum.GetPostByID(ctx, post.ID)
	if got.BodyHash != post.BodyHash || got.IsDeleted() || got.EditCount != 0 || got.BountyDisputeStatus != "" {
		t.Errorf("post round trip = %+v", got)
	}

	// Duplicates by body hash.
	if id, err := forum.FindRecentPostByBodyHash(ctx, alice, post.BodyHash, now.Add(-time.Hour)); err != nil || id != post.ID {
		t.Errorf("FindRecentPostByBodyHash = %q err=%v", id, err)
	}
	if _, err := forum.FindRecentPostByBodyHash(ctx, bob, post.BodyHash, now.Add(-time.Hour)); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("other author's hash: %v", err)
	}
	if _, err := forum.FindRecentPostByBodyHash(ctx, alice, post.BodyHash, now.Add(time.Hour)); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("hash outside the window: %v", err)
	}
	if id, err := forum.FindRecentReplyByBodyHash(ctx, post.ID, bob, reply.BodyHash, now); err != nil || id != reply.ID {
		t.Errorf("FindRecentReplyByBodyHash = %q err=%v", id, err)
	}

	// Edits and history.
	if err := forum.UpdatePostBody(ctx, post.ID, run+"q2", "second body", models.BoardBodyHash("second body"), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpdatePostBody: %v", err)
	}
	got, _ = forum.GetPostByID(ctx, post.ID)
	if got.Title != run+"q2" || got.Description != "second body" || got.EditCount != 1 || got.EditedAt == nil {
		t.Errorf("after edit = %+v", got)
	}
	if err := forum.UpdateReplyBody(ctx, reply.ID, "a better reply", models.BoardBodyHash("a better reply"), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpdateReplyBody: %v", err)
	}
	if r, _ := forum.GetReplyByID(ctx, reply.ID); r.Body != "a better reply" || r.EditCount != 1 || r.EditedAt == nil {
		t.Errorf("reply after edit = %+v", r)
	}
	edits := NewPostgresBoardEditHistoryRepository(pool)
	if err := edits.Add(ctx, &models.BoardEdit{TargetType: models.ReportTargetPost, TargetID: post.ID, EditorPeerID: alice, PreviousTitle: run + "q", PreviousBody: "first body", EditedAt: now.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("edit history add: %v", err)
	}
	if h, err := edits.List(ctx, models.ReportTargetPost, post.ID, 10); err != nil || len(h) != 1 || h[0].PreviousBody != "first body" {
		t.Errorf("edit history = %+v err=%v", h, err)
	}

	// Soft delete: once, then a tombstone the feed and counts leave out.
	other := &models.ForumPost{AuthorPeerID: alice, Title: run + "gone", Description: "gone body", Category: "general", Tags: []string{}, CreatedAt: now.Add(time.Second), UpdatedAt: now}
	if err := forum.CreatePost(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	if err := forum.SoftDeletePost(ctx, other.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("SoftDeletePost: %v", err)
	}
	if err := forum.SoftDeletePost(ctx, other.ID, now.Add(3*time.Minute)); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("second soft delete: %v", err)
	}
	if err := forum.SoftDeletePost(ctx, uuid.New().String(), now); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("soft delete missing: %v", err)
	}
	if err := forum.UpdatePostBody(ctx, other.ID, "x", "y", "", now); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("edit deleted: %v", err)
	}
	if g, _ := forum.GetPostByID(ctx, other.ID); !g.IsDeleted() {
		t.Errorf("tombstone = %+v", g)
	}
	if posts, _, _ := forum.QueryPosts(ctx, PostQuery{Limit: 50, Author: alice}); len(posts) != 1 || posts[0].ID != post.ID {
		t.Errorf("author view after delete = %d posts", len(posts))
	}
	if n, _ := forum.CountPostsSince(ctx, "", now.Add(-time.Hour)); n < 1 {
		t.Errorf("CountPostsSince main = %d", n)
	}
	if err := forum.SoftDeleteReply(ctx, reply.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("SoftDeleteReply: %v", err)
	}
	if replies, _, _ := forum.ListRepliesFiltered(ctx, post.ID, ReplyQuery{Limit: 10}); len(replies) != 1 || !replies[0].IsDeleted() {
		t.Errorf("thread after reply delete = %+v", replies)
	}
	if _, err := forum.FindRecentReplyByBodyHash(ctx, post.ID, bob, models.BoardBodyHash("a better reply"), now); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("deleted reply still a duplicate: %v", err)
	}

	// Upvotes carry created_at: the per-peer count.
	for _, voter := range []string{bob, fresh, idle, twin} {
		if err := forum.AddUpvote(ctx, voter, post.ID); err != nil {
			t.Fatalf("upvote by %s: %v", voter, err)
		}
	}
	if n, err := forum.CountUpvotesByPeerSince(ctx, bob, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Errorf("CountUpvotesByPeerSince = %d err=%v", n, err)
	}
	if n, _ := forum.CountUpvotesByPeerSince(ctx, bob, time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("CountUpvotesByPeerSince future = %d", n)
	}

	// Guarded BoardStats: bob and twin (old, active) count; fresh (new) and idle (no
	// activity) do not. twin is active through a reply.
	twinReply := &models.ForumReply{PostID: post.ID, AuthorPeerID: twin, Body: "twin", CreatedAt: now.Add(time.Minute)}
	if err := forum.CreateReply(ctx, twinReply); err != nil {
		t.Fatalf("twin reply: %v", err)
	}
	bobPost := &models.ForumPost{AuthorPeerID: bob, Title: run + "bobq", Description: "bob asks", Category: "general", Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := forum.CreatePost(ctx, bobPost); err != nil {
		t.Fatalf("bob post: %v", err)
	}
	freshPost := &models.ForumPost{AuthorPeerID: fresh, Title: run + "freshq", Description: "fresh asks", Category: "general", Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := forum.CreatePost(ctx, freshPost); err != nil {
		t.Fatalf("fresh post: %v", err)
	}
	// alice answers both; bob's accept counts, fresh's does not.
	for _, p := range []*models.ForumPost{bobPost, freshPost} {
		r := &models.ForumReply{PostID: p.ID, AuthorPeerID: alice, Body: "alice answers", CreatedAt: now.Add(time.Minute)}
		if err := forum.CreateReply(ctx, r); err != nil {
			t.Fatalf("alice reply: %v", err)
		}
		if err := forum.SetAcceptedReply(ctx, p.ID, &r.ID); err != nil {
			t.Fatalf("accept: %v", err)
		}
	}
	unguarded, err := forum.BoardStats(ctx, alice, BoardStatsGuard{})
	if err != nil || unguarded.UpvotesReceived != 4 || unguarded.AnswersAccepted != 2 {
		t.Errorf("unguarded stats = %+v err=%v", unguarded, err)
	}
	guarded, err := forum.BoardStats(ctx, alice, BoardStatsGuard{VoterSince: now.Add(-3 * 24 * time.Hour)})
	if err != nil || guarded.UpvotesReceived != 2 || guarded.AnswersAccepted != 1 {
		t.Errorf("guarded stats = %+v err=%v", guarded, err)
	}

	// Keyset cursor: three posts a second apart, page of two, then the rest; total is the feed's.
	var ordered []*models.ForumPost
	for i := 0; i < 3; i++ {
		p := &models.ForumPost{AuthorPeerID: idle, Title: run + "c", Description: "cursor", Category: "general", Tags: []string{}, CreatedAt: now.Add(time.Duration(10+i) * time.Second), UpdatedAt: now}
		if err := forum.CreatePost(ctx, p); err != nil {
			t.Fatalf("cursor post: %v", err)
		}
		ordered = append(ordered, p)
	}
	page1, total, err := forum.QueryPosts(ctx, PostQuery{Limit: 2, Author: idle})
	if err != nil || total != 3 || len(page1) != 2 || page1[0].ID != ordered[2].ID {
		t.Fatalf("cursor page 1 = %d posts total=%d err=%v", len(page1), total, err)
	}
	page2, total2, err := forum.QueryPosts(ctx, PostQuery{Limit: 2, Author: idle, Before: &PostCursor{CreatedAt: page1[1].CreatedAt, ID: page1[1].ID}})
	if err != nil || total2 != 3 || len(page2) != 1 || page2[0].ID != ordered[0].ID {
		t.Errorf("cursor page 2 = %d posts total=%d err=%v", len(page2), total2, err)
	}

	// Routing reasons and the per-room autopilot count.
	roomPost := &models.ForumPost{AuthorPeerID: alice, Title: run + "room", Description: "room body", Category: "request", Tags: []string{}, CreatedAt: now, UpdatedAt: now, RoomMint: &mint}
	if err := forum.CreatePost(ctx, roomPost); err != nil {
		t.Fatalf("room post: %v", err)
	}
	if err := forum.SetRoutedReasons(ctx, roomPost.ID, map[string][]string{bob: {"category", "tier:active"}}); err != nil {
		t.Fatalf("SetRoutedReasons: %v", err)
	}
	if g, _ := forum.GetPostByID(ctx, roomPost.ID); len(g.RoutedReasons[bob]) != 2 || g.RoutedReasons[bob][1] != "tier:active" {
		t.Errorf("routed reasons = %+v", g.RoutedReasons)
	}
	if err := forum.CreateReply(ctx, &models.ForumReply{PostID: roomPost.ID, AuthorPeerID: bob, Body: "auto", CreatedAt: now.Add(time.Minute), Auto: true}); err != nil {
		t.Fatalf("auto reply: %v", err)
	}
	if n, err := forum.CountAutoRepliesByPeerInRoomSince(ctx, bob, mint, now); err != nil || n != 1 {
		t.Errorf("CountAutoRepliesByPeerInRoomSince = %d err=%v", n, err)
	}
	if n, _ := forum.CountAutoRepliesByPeerInRoomSince(ctx, bob, "other-mint", now); n != 0 {
		t.Errorf("other room count = %d", n)
	}
	if n, _ := forum.CountPostsSince(ctx, mint, now.Add(-time.Hour)); n != 1 {
		t.Errorf("CountPostsSince room = %d", n)
	}

	// Disputes: only on a completed or expired bounty, once, then resolved.
	amt := 100
	bounty := &models.ForumPost{AuthorPeerID: alice, Title: run + "b", Description: "b", Category: "bounty", Tags: []string{}, CreatedAt: now, UpdatedAt: now, BountyAmount: &amt, BountyStatus: "open"}
	if err := forum.CreatePost(ctx, bounty); err != nil {
		t.Fatalf("bounty: %v", err)
	}
	if err := forum.OpenBountyDispute(ctx, bounty.ID, bob, "note", now); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("dispute an open bounty: %v", err)
	}
	if err := forum.AwardBounty(ctx, bounty.ID, bob); err != nil {
		t.Fatalf("award: %v", err)
	}
	if err := forum.OpenBountyDispute(ctx, bounty.ID, bob, "note", now); err != nil {
		t.Fatalf("OpenBountyDispute: %v", err)
	}
	if err := forum.OpenBountyDispute(ctx, bounty.ID, bob, "again", now); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("second dispute: %v", err)
	}
	if list, _ := forum.ListDisputedBounties(ctx, 100); len(list) == 0 || !containsPost(list, bounty.ID) {
		t.Errorf("ListDisputedBounties lacks the post")
	}
	if err := forum.ResolveBountyDispute(ctx, bounty.ID, models.BountyDisputeUpheld, now.Add(time.Hour)); err != nil {
		t.Fatalf("ResolveBountyDispute: %v", err)
	}
	if err := forum.ResolveBountyDispute(ctx, bounty.ID, models.BountyDisputeUpheld, now); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("resolve twice: %v", err)
	}
	if g, _ := forum.GetPostByID(ctx, bounty.ID); g.BountyDisputeStatus != models.BountyDisputeUpheld || g.BountyDisputeBy != bob || g.BountyDisputeNote != "note" || g.BountyDisputeResolvedAt == nil {
		t.Errorf("resolved dispute = %+v", g)
	}

	// Room settings and mutes.
	rooms := NewPostgresBoardRoomRepository(pool)
	if s, err := rooms.GetSettings(ctx, mint); err != nil || !s.Routing || s.MinHoldRaw != 1 {
		t.Errorf("default settings = %+v err=%v", s, err)
	}
	if err := rooms.SetSettings(ctx, &models.RoomSettings{Mint: mint, Routing: false, MinHoldRaw: 7, UpdatedAt: now}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if err := rooms.SetSettings(ctx, &models.RoomSettings{Mint: mint, Routing: false, MinHoldRaw: 9, UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("SetSettings again: %v", err)
	}
	if m, _ := rooms.SettingsByMints(ctx, []string{mint, "none"}); len(m) != 1 || m[mint].MinHoldRaw != 9 || m[mint].Routing {
		t.Errorf("SettingsByMints = %+v", m)
	}
	if err := rooms.Mute(ctx, &models.RoomMute{Mint: mint, PeerID: bob, ByPeerID: alice, Reason: "spam", CreatedAt: now}); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if muted, _ := rooms.IsMuted(ctx, mint, bob); !muted {
		t.Errorf("IsMuted = false")
	}
	if list, _ := rooms.ListMutes(ctx, mint); len(list) != 1 || list[0].Reason != "spam" {
		t.Errorf("ListMutes = %+v", list)
	}
	if err := rooms.Unmute(ctx, mint, bob); err != nil {
		t.Fatalf("Unmute: %v", err)
	}
	if err := rooms.Unmute(ctx, mint, bob); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("Unmute twice: %v", err)
	}

	// Visits and notification preferences.
	visits := NewPostgresBoardVisitRepository(pool)
	if prev, err := visits.Visit(ctx, alice, mint, now); err != nil || prev != nil {
		t.Errorf("first visit prev=%v err=%v", prev, err)
	}
	if prev, err := visits.Visit(ctx, alice, mint, now.Add(time.Minute)); err != nil || prev == nil || !prev.Equal(now) {
		t.Errorf("second visit prev=%v err=%v", prev, err)
	}
	if prev, _ := visits.Visit(ctx, alice, mint, now.Add(-time.Hour)); prev == nil || !prev.Equal(now.Add(time.Minute)) {
		t.Errorf("older visit moved the stamp back: %v", prev)
	}
	if m, _ := visits.LastVisits(ctx, alice, []string{mint, ""}); len(m) != 1 || !m[mint].Equal(now.Add(time.Minute)) {
		t.Errorf("LastVisits = %+v", m)
	}
	prefs := NewPostgresBoardNotificationPrefRepository(pool)
	if p, err := prefs.Get(ctx, alice); err != nil || len(p.MutedKinds) != 0 {
		t.Errorf("default prefs = %+v err=%v", p, err)
	}
	if err := prefs.Set(ctx, &models.NotificationPrefs{PeerID: alice, MutedKinds: []string{"post_upvoted"}, UpdatedAt: now}); err != nil {
		t.Fatalf("Set prefs: %v", err)
	}
	if p, _ := prefs.Get(ctx, alice); len(p.MutedKinds) != 1 || p.MutedKinds[0] != "post_upvoted" {
		t.Errorf("prefs = %+v", p)
	}
	// The activity exclusion and unread count.
	activity := NewPostgresBoardActivityRepository(pool)
	for _, kind := range []string{models.ActivityPostUpvoted, models.ActivityReplyOnPost} {
		if err := activity.Create(ctx, &models.BoardActivity{PeerID: alice, Kind: kind, PostID: post.ID, ActorPeerID: bob, CreatedAt: now}); err != nil {
			t.Fatalf("activity: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM board_activity WHERE peer_id LIKE $1`, run+"%") })
	if rows, _ := activity.List(ctx, alice, ActivityQuery{Limit: 10, ExcludeKinds: []string{models.ActivityPostUpvoted}}); len(rows) != 1 || rows[0].Kind != models.ActivityReplyOnPost {
		t.Errorf("excluded list = %+v", rows)
	}
	if n, _ := activity.CountUnreadExcluding(ctx, alice, []string{models.ActivityPostUpvoted}); n != 1 {
		t.Errorf("CountUnreadExcluding = %d", n)
	}
	if n, _ := activity.CountUnread(ctx, alice); n != 2 {
		t.Errorf("CountUnread = %d", n)
	}
}

func containsPost(posts []*models.ForumPost, id string) bool {
	for _, p := range posts {
		if p.ID == id {
			return true
		}
	}
	return false
}
