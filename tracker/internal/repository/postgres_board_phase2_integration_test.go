// Package repository: Postgres integration test for the community board phase 2 queries
// (rooms, request routing, digest, autopilot categories, holder snapshots, activity filters).
// Runs only with TRACKER_TEST_DATABASE_URL set, like the phase 1 test; every phase 2 statement
// runs at least once against real column types. Rows are scoped to a random prefix and
// deleted at the end.
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestPostgres_BoardPhase2Queries(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "it2-" + uuid.New().String()[:8] + "-"
	agent, holder, other := run+"agent", run+"holder", run+"other"
	now := time.Now().UTC().Truncate(time.Microsecond)

	peers := NewPostgresPeerRepository(pool)
	for _, p := range []*models.Peer{
		{PeerID: agent, PublicKey: "pk-" + agent, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: holder, PublicKey: "pk-" + holder, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now.Add(-time.Hour)},
		{PeerID: other, PublicKey: "pk-" + other, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now.Add(-20 * 24 * time.Hour)},
	} {
		if err := peers.Create(ctx, p); err != nil {
			t.Fatalf("create peer %s: %v", p.PeerID, err)
		}
	}
	mint := run + "mint"
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM room_holder_snapshots WHERE mint LIKE $1`,
			`DELETE FROM peer_autopilot WHERE peer_id LIKE $1`,
			`DELETE FROM board_activity WHERE peer_id LIKE $1`,
			`DELETE FROM token_launches WHERE mint LIKE $1`,
			`DELETE FROM peers WHERE peer_id LIKE $1`,
		} {
			if _, err := pool.Exec(ctx, q, run+"%"); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	// Create never stores peer_id on Postgres; the claim flow binds it.
	launches := NewPostgresLaunchRepository(pool)
	if err := launches.Create(ctx, &models.TokenLaunch{Mint: mint, CreatorWallet: run + "wallet", QuoteMint: "Q", Name: "Room", Symbol: "ROOM",
		LaunchSignature: run + "sig", CreatedAt: now}); err != nil {
		t.Fatalf("create launch: %v", err)
	}
	if err := launches.Bind(ctx, mint, agent, now); err != nil {
		t.Fatalf("bind launch: %v", err)
	}
	byMint, err := launches.GetByMints(ctx, []string{mint, "nope"})
	if err != nil || len(byMint) != 1 || byMint[mint].Symbol != "ROOM" {
		t.Errorf("GetByMints = %v err=%v", byMint, err)
	}
	mine, err := launches.ListByPeerID(ctx, agent)
	if err != nil || len(mine) != 1 || mine[0].Mint != mint {
		t.Errorf("ListByPeerID = %v err=%v", mine, err)
	}

	forum := NewPostgresForumRepository(pool)
	mainPost := &models.ForumPost{AuthorPeerID: agent, Title: run + " main", Description: "m", Category: "request", Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	roomPost := &models.ForumPost{AuthorPeerID: holder, Title: run + " room", Description: "r", Category: "request", Tags: []string{}, CreatedAt: now.Add(time.Minute), UpdatedAt: now, RoomMint: &mint, Auto: true}
	announcement := &models.ForumPost{AuthorPeerID: agent, Title: run + " launched", Description: "a", Category: "discovery", Tags: []string{}, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, RoomMint: &mint, RoomPinned: true}
	for _, p := range []*models.ForumPost{mainPost, roomPost, announcement} {
		if err := forum.CreatePost(ctx, p); err != nil {
			t.Fatalf("create post %s: %v", p.Title, err)
		}
	}
	dup := &models.ForumPost{AuthorPeerID: agent, Title: "dup", Description: "d", Category: "discovery", Tags: []string{}, CreatedAt: now, UpdatedAt: now, RoomMint: &mint, RoomPinned: true}
	if err := forum.CreatePost(ctx, dup); !errors.Is(err, models.ErrAlreadyExists) {
		t.Errorf("second pinned announcement: %v", err)
	}
	got, err := forum.GetPostByID(ctx, roomPost.ID)
	if err != nil || got.RoomMint == nil || *got.RoomMint != mint || got.RoomPinned || got.RoutedTo != nil || !got.Auto {
		t.Fatalf("room post = %+v err=%v", got, err)
	}
	if n, err := forum.CountRoutedPostsByAuthorSince(ctx, holder, now.Add(-time.Hour)); err != nil || n != 0 {
		t.Errorf("routed count before = %d err=%v", n, err)
	}
	if err := forum.SetRoutedTo(ctx, roomPost.ID, []string{agent}); err != nil {
		t.Fatalf("SetRoutedTo: %v", err)
	}
	if got, _ = forum.GetPostByID(ctx, roomPost.ID); len(got.RoutedTo) != 1 || got.RoutedTo[0] != agent {
		t.Errorf("routed_to = %v", got.RoutedTo)
	}
	if n, err := forum.CountRoutedPostsByAuthorSince(ctx, holder, now.Add(-time.Hour)); err != nil || n != 1 {
		t.Errorf("routed count after = %d err=%v", n, err)
	}
	if n, _ := forum.CountRoutedPostsByAuthorSince(ctx, holder, now.Add(time.Hour)); n != 0 {
		t.Errorf("routed count outside the window = %d", n)
	}

	// Feeds: main leaves room posts out, room feed leads with the announcement, mine includes both.
	if posts, _, err := forum.QueryPosts(ctx, PostQuery{Limit: 50, Search: run}); err != nil || !contains(ids2(posts), mainPost.ID) || contains(ids2(posts), roomPost.ID) {
		t.Errorf("main feed = %v err=%v", ids2(posts), err)
	}
	if posts, total, err := forum.QueryPosts(ctx, PostQuery{Limit: 50, Room: mint}); err != nil || total != 2 || posts[0].ID != announcement.ID {
		t.Errorf("room feed = %v total=%d err=%v", ids2(posts), total, err)
	}
	if posts, _, err := forum.QueryPosts(ctx, PostQuery{Limit: 50, Sort: PostSortTop, Room: mint}); err != nil || posts[0].ID != announcement.ID {
		t.Errorf("room top feed = %v err=%v", ids2(posts), err)
	}
	if posts, _, err := forum.QueryPosts(ctx, PostQuery{Limit: 50, Mine: MinePosts, MinePeerID: holder}); err != nil || !contains(ids2(posts), roomPost.ID) {
		t.Errorf("mine feed = %v err=%v", ids2(posts), err)
	}
	if posts, total, err := forum.QueryPosts(ctx, PostQuery{Limit: 50, Room: mint, HideAuto: true}); err != nil || total != 1 || contains(ids2(posts), roomPost.ID) {
		t.Errorf("hide_auto room feed = %v total=%d err=%v", ids2(posts), total, err)
	}
	if c, err := forum.CountPosts(ctx, now, mint); err != nil || c.All != 2 || c.ByCategory["request"] != 1 || c.ByCategory["discovery"] != 1 {
		t.Errorf("room counts = %+v err=%v", c, err)
	}
	if c, err := forum.CountPosts(ctx, now, ""); err != nil || c.All < 1 {
		t.Errorf("main counts = %+v err=%v", c, err)
	}
	stats, err := forum.RoomStats(ctx, []string{mint, "nope"}, now.Add(-7*24*time.Hour))
	if err != nil || len(stats) != 1 || stats[mint].PostsSince != 2 || stats[mint].LastPostAt == nil || !stats[mint].LastPostAt.Equal(roomPost.CreatedAt) {
		t.Errorf("RoomStats = %+v err=%v", stats[mint], err)
	}
	if ann, err := forum.GetRoomAnnouncement(ctx, mint); err != nil || ann.ID != announcement.ID {
		t.Errorf("GetRoomAnnouncement = %v err=%v", ann, err)
	}
	if _, err := forum.GetRoomAnnouncement(ctx, "nope"); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("missing announcement: %v", err)
	}

	// Routing inputs.
	if list, err := forum.ListPostsByAuthor(ctx, agent, 5); err != nil || len(list) != 2 || list[0].ID != mainPost.ID {
		t.Errorf("ListPostsByAuthor = %v err=%v", ids2(list), err)
	}
	reply := &models.ForumReply{PostID: roomPost.ID, AuthorPeerID: agent, Body: "answer", CreatedAt: now.Add(2 * time.Minute)}
	if err := forum.CreateReply(ctx, reply); err != nil {
		t.Fatalf("create reply: %v", err)
	}
	hiddenReply := &models.ForumReply{PostID: roomPost.ID, AuthorPeerID: agent, Body: "spam", CreatedAt: now.Add(2 * time.Minute)}
	if err := forum.CreateReply(ctx, hiddenReply); err != nil {
		t.Fatalf("create hidden reply: %v", err)
	}
	if err := forum.SetReplyHidden(ctx, hiddenReply.ID, true); err != nil {
		t.Fatalf("hide reply: %v", err)
	}
	if err := forum.SetAcceptedReply(ctx, roomPost.ID, &reply.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if acc, err := forum.PeersWithAcceptedReplySince(ctx, now); err != nil || !acc[agent] {
		t.Errorf("PeersWithAcceptedReplySince = %v err=%v", acc, err)
	}
	if acc, _ := forum.PeersWithAcceptedReplySince(ctx, now.Add(time.Hour)); acc[agent] {
		t.Errorf("accepted answer before since counted: %v", acc)
	}
	if seen, err := peers.PeerIDsSeenSince(ctx, now.Add(-14*24*time.Hour), 500); err != nil || !contains(seen, agent) || !contains(seen, holder) || contains(seen, other) {
		t.Errorf("PeerIDsSeenSince = %v err=%v", seen, err)
	}

	// Digest.
	bountyAmt := 120
	bounty := &models.ForumPost{AuthorPeerID: agent, Title: run + " bounty", Description: "b", Category: "bounty", Tags: []string{},
		CreatedAt: now.Add(3 * time.Minute), UpdatedAt: now, BountyAmount: &bountyAmt, BountyStatus: "open", RoomMint: &mint}
	if err := forum.CreatePost(ctx, bounty); err != nil {
		t.Fatalf("create bounty: %v", err)
	}
	if err := forum.AwardBounty(ctx, bounty.ID, holder); err != nil {
		t.Fatalf("award: %v", err)
	}
	// The room post, its reply and the bounty sit 1 to 3 minutes after now (rows are stamped
	// relative to the test start), and bounty_completed_at is the database NOW(): the period
	// must cover both, so it ends an hour past now.
	digest, err := forum.RoomDigest(ctx, mint, now.Add(-2*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("RoomDigest: %v", err)
	}
	if digest.Posts != 3 || digest.Replies != 1 || digest.BountiesAwarded != 1 || digest.CreditsAwarded != 120 || len(digest.TopThreads) != 3 ||
		digest.TopThreads[0].ID != roomPost.ID || digest.TopThreads[0].Replies != 1 {
		t.Errorf("digest = %+v (hidden reply must not count per thread)", digest)
	}

	// Autopilot categories and holder snapshots.
	autopilot := NewPostgresPeerAutopilotRepository(pool)
	if err := autopilot.Set(ctx, agent, []string{"request", "bounty"}, now); err != nil {
		t.Fatalf("autopilot set: %v", err)
	}
	if err := autopilot.Set(ctx, agent, []string{"request", "bounty"}, now.Add(time.Minute)); err != nil {
		t.Fatalf("autopilot set again: %v", err)
	}
	if err := autopilot.Set(ctx, holder, []string{}, now); err != nil {
		t.Fatalf("autopilot set empty: %v", err)
	}
	cats, err := autopilot.CategoriesByIDs(ctx, []string{agent, holder, other})
	if err != nil || len(cats) != 2 || len(cats[agent]) != 2 || cats[agent][0] != "request" || len(cats[holder]) != 0 {
		t.Errorf("CategoriesByIDs = %v err=%v", cats, err)
	}
	if err := autopilot.Clear(ctx, agent); err != nil {
		t.Fatalf("autopilot clear: %v", err)
	}
	if cats, _ = autopilot.CategoriesByIDs(ctx, []string{agent}); len(cats) != 0 {
		t.Errorf("cleared row still present: %v", cats)
	}
	snapshots := NewPostgresRoomHolderSnapshotRepository(pool)
	if err := snapshots.Record(ctx, mint, now.Add(-7*24*time.Hour), 10); err != nil {
		t.Fatalf("snapshot record: %v", err)
	}
	if err := snapshots.Record(ctx, mint, now, 16); err != nil {
		t.Fatalf("snapshot record today: %v", err)
	}
	if err := snapshots.Record(ctx, mint, now, 17); err != nil {
		t.Fatalf("snapshot overwrite: %v", err)
	}
	if s, err := snapshots.LatestAtOrBefore(ctx, mint, now.Add(-7*24*time.Hour)); err != nil || s.Holders != 10 {
		t.Errorf("LatestAtOrBefore(week ago) = %+v err=%v", s, err)
	}
	if s, err := snapshots.LatestAtOrBefore(ctx, mint, now); err != nil || s.Holders != 17 {
		t.Errorf("LatestAtOrBefore(now) = %+v err=%v", s, err)
	}
	if _, err := snapshots.LatestAtOrBefore(ctx, mint, now.Add(-30*24*time.Hour)); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("no earlier snapshot: %v", err)
	}

	// Activity kinds and unread filters.
	activity := NewPostgresBoardActivityRepository(pool)
	routed := &models.BoardActivity{PeerID: agent, Kind: models.ActivityRequestRouted, PostID: roomPost.ID, ActorPeerID: holder, CreatedAt: now}
	upvoted := &models.BoardActivity{PeerID: agent, Kind: models.ActivityPostUpvoted, PostID: mainPost.ID, ActorPeerID: holder, CreatedAt: now.Add(time.Second)}
	for _, a := range []*models.BoardActivity{routed, upvoted} {
		if err := activity.Create(ctx, a); err != nil {
			t.Fatalf("create activity: %v", err)
		}
	}
	if rows, err := activity.List(ctx, agent, ActivityQuery{Limit: 10}); err != nil || len(rows) != 2 {
		t.Errorf("all rows = %d err=%v", len(rows), err)
	}
	if rows, err := activity.List(ctx, agent, ActivityQuery{Limit: 10, Kinds: []string{models.ActivityRequestRouted}}); err != nil || len(rows) != 1 || rows[0].ID != routed.ID {
		t.Errorf("kinds filter = %+v err=%v", rows, err)
	}
	if err := activity.MarkRead(ctx, agent, []string{routed.ID}, now); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if rows, err := activity.List(ctx, agent, ActivityQuery{Limit: 10, Unread: true}); err != nil || len(rows) != 1 || rows[0].ID != upvoted.ID {
		t.Errorf("unread filter = %+v err=%v", rows, err)
	}
	if rows, err := activity.List(ctx, agent, ActivityQuery{Limit: 10, Kinds: []string{models.ActivityRequestRouted}, Unread: true}); err != nil || len(rows) != 0 {
		t.Errorf("kinds and unread = %+v err=%v", rows, err)
	}
}
