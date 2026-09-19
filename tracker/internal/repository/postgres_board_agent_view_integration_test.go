// Package repository: Postgres integration test for the agent activity view statements: the
// author and participant feed predicates of QueryPosts (room posts included, hidden post and
// hidden reply rules) and BoardActivity (visible posts, replies, newest time). Runs only with
// TRACKER_TEST_DATABASE_URL set, like the phase 1 to 3 tests; rows are scoped to a random
// prefix and deleted at the end.
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestPostgres_BoardAgentViewQueries(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "it4-" + uuid.New().String()[:8] + "-"
	agent, bob, platform := run+"agent", run+"bob", run+"platform"
	now := time.Now().UTC().Truncate(time.Microsecond)

	peers := NewPostgresPeerRepository(pool)
	for _, p := range []*models.Peer{
		{PeerID: agent, PublicKey: "pk-" + agent, Multiaddrs: []string{}, DisplayName: run + "Agent", FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: bob, PublicKey: "pk-" + bob, Multiaddrs: []string{}, DisplayName: run + "Bob", FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: platform, PublicKey: "pk-" + platform, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
	} {
		if err := peers.Create(ctx, p); err != nil {
			t.Fatalf("create peer %s: %v", p.PeerID, err)
		}
	}
	// The room record (Create never stores peer_id on Postgres; the claim flow binds it).
	launches := NewPostgresLaunchRepository(pool)
	mint := run + "mint"
	if err := launches.Create(ctx, &models.TokenLaunch{Mint: mint, CreatorWallet: run + "wallet", QuoteMint: "Q", Name: "AAA Token", Symbol: "AAA",
		LaunchSignature: run + "sig", CreatedAt: now}); err != nil {
		t.Fatalf("create launch: %v", err)
	}
	if err := launches.Bind(ctx, mint, agent, now); err != nil {
		t.Fatalf("bind launch: %v", err)
	}
	t.Cleanup(func() {
		// forum_posts and forum_replies cascade from peers; the launch row does not.
		for _, q := range []string{
			`DELETE FROM token_launches WHERE mint LIKE $1`,
			`DELETE FROM peers WHERE peer_id LIKE $1`,
		} {
			if _, err := pool.Exec(ctx, q, run+"%"); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	forum := NewPostgresForumRepository(pool)
	agentMain := &models.ForumPost{AuthorPeerID: agent, Title: run + " agent main routing", Description: "m", Category: "general", Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	agentRoom := &models.ForumPost{AuthorPeerID: agent, Title: run + " agent room", Description: "r", Category: "request", Tags: []string{}, RoomMint: &mint, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}
	bobPost := &models.ForumPost{AuthorPeerID: bob, Title: run + " bob routing", Description: "b", Category: "request", Tags: []string{}, CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute)}
	bobRoom := &models.ForumPost{AuthorPeerID: bob, Title: run + " bob room", Description: "br", Category: "general", Tags: []string{}, RoomMint: &mint, CreatedAt: now.Add(3 * time.Minute), UpdatedAt: now.Add(3 * time.Minute)}
	for _, p := range []*models.ForumPost{agentMain, agentRoom, bobPost, bobRoom} {
		if err := forum.CreatePost(ctx, p); err != nil {
			t.Fatalf("create post %s: %v", p.Title, err)
		}
	}
	agentReply := &models.ForumReply{PostID: bobPost.ID, AuthorPeerID: agent, Body: "agent answers", CreatedAt: now.Add(4 * time.Minute)}
	agentRoomReply := &models.ForumReply{PostID: bobRoom.ID, AuthorPeerID: agent, Body: "agent answers in the room", CreatedAt: now.Add(5 * time.Minute)}
	bobReply := &models.ForumReply{PostID: agentMain.ID, AuthorPeerID: bob, Body: "bob answers", CreatedAt: now.Add(6 * time.Minute)}
	for _, rp := range []*models.ForumReply{agentReply, agentRoomReply, bobReply} {
		if err := forum.CreateReply(ctx, rp); err != nil {
			t.Fatalf("create reply %q: %v", rp.Body, err)
		}
	}

	// Scope every query to this run through the search text (other rows may share the table).
	base := PostQuery{Limit: 10, Sort: PostSortRecent, Search: run}
	q := func(mut func(*PostQuery)) PostQuery {
		pq := base
		mut(&pq)
		return pq
	}
	ids := func(list []*models.ForumPost) map[string]bool {
		out := map[string]bool{}
		for _, p := range list {
			out[p.ID] = true
		}
		return out
	}

	// Author: room posts included, newest first; combines with category and the top sort.
	list, total, err := forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent }))
	if err != nil || total != 2 || len(list) != 2 || list[0].ID != agentRoom.ID || list[1].ID != agentMain.ID {
		t.Fatalf("author: total=%d list=%v err=%v", total, ids(list), err)
	}
	if list, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent; pq.Category = "request" })); total != 1 || list[0].ID != agentRoom.ID {
		t.Errorf("author + category: total=%d %v", total, ids(list))
	}
	if list, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent; pq.Sort = PostSortTop })); total != 2 || len(list) != 2 {
		t.Errorf("author + top: total=%d %v", total, ids(list))
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent; pq.Room = mint })); total != 1 {
		t.Errorf("author + room: total=%d", total)
	}
	// Participant: the posts the agent replied in, rooms included; the author view and the
	// participant view AND together.
	if list, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Participant = agent })); total != 2 || !ids(list)[bobPost.ID] || !ids(list)[bobRoom.ID] {
		t.Errorf("participant: total=%d %v", total, ids(list))
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Participant = bob })); total != 1 {
		t.Errorf("participant bob: total=%d", total)
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = bob; pq.Participant = agent; pq.Category = "request" })); total != 1 {
		t.Errorf("author + participant + category: total=%d", total)
	}
	// The main feed still leaves room posts out.
	if _, total, _ = forum.QueryPosts(ctx, base); total != 2 {
		t.Errorf("main feed: total=%d, want 2", total)
	}

	// BoardActivity before any hide: 2 posts, 2 replies, newest = the room reply.
	act, err := forum.BoardActivity(ctx, agent)
	if err != nil || act.Posts != 2 || act.Replies != 2 || act.LastActiveAt == nil || !act.LastActiveAt.Equal(agentRoomReply.CreatedAt) {
		t.Errorf("activity = %+v err=%v", act, err)
	}
	if act, err = forum.BoardActivity(ctx, platform); err != nil || act.Posts != 0 || act.Replies != 0 || act.LastActiveAt != nil {
		t.Errorf("activity of a peer without rows = %+v err=%v", act, err)
	}

	// Hidden post: out of the public author view, in for the author and a platform viewer.
	if err := forum.SetPostHidden(ctx, agentMain.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent })); total != 1 {
		t.Errorf("author after hide (anonymous): total=%d", total)
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent; pq.Viewer = bob })); total != 1 {
		t.Errorf("author after hide (other viewer): total=%d", total)
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent; pq.Viewer = agent })); total != 2 {
		t.Errorf("author after hide (self): total=%d", total)
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Author = agent; pq.Viewer = platform; pq.ShowHidden = true })); total != 2 {
		t.Errorf("author after hide (platform): total=%d", total)
	}
	// Hidden reply: the post leaves the public participant view, stays for the participant and the platform.
	if err := forum.SetReplyHidden(ctx, agentReply.ID, true); err != nil {
		t.Fatal(err)
	}
	if list, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Participant = agent })); total != 1 || list[0].ID != bobRoom.ID {
		t.Errorf("participant after hide (anonymous): total=%d %v", total, ids(list))
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Participant = agent; pq.Viewer = agent })); total != 2 {
		t.Errorf("participant after hide (self): total=%d", total)
	}
	if _, total, _ = forum.QueryPosts(ctx, q(func(pq *PostQuery) { pq.Participant = agent; pq.ShowHidden = true })); total != 2 {
		t.Errorf("participant after hide (platform): total=%d", total)
	}
	if act, err = forum.BoardActivity(ctx, agent); err != nil || act.Posts != 1 || act.Replies != 1 || act.LastActiveAt == nil || !act.LastActiveAt.Equal(agentRoomReply.CreatedAt) {
		t.Errorf("activity after hide = %+v err=%v", act, err)
	}
	if err := forum.SetReplyHidden(ctx, agentRoomReply.ID, true); err != nil {
		t.Fatal(err)
	}
	if act, _ = forum.BoardActivity(ctx, agent); act.Replies != 0 || act.LastActiveAt == nil || !act.LastActiveAt.Equal(agentRoom.CreatedAt) {
		t.Errorf("activity newest time falls back to the visible post = %+v", act)
	}
}
