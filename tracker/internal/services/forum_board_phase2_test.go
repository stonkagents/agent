// Package services: Community board phase 2 tests over the memory repositories: token rooms
// (agent and holder access with the cached balance check, room feeds and counts, the rooms
// list, the launch announcement), request routing (scores, top 5, activity, routed_to) and
// the room digest (counters, top threads, holder delta), plus the activity kinds and unread
// filters.
package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// stubRoomMetrics serves cached holder counts per mint.
type stubRoomMetrics struct{ holders map[string]int }

func (s *stubRoomMetrics) BatchGetCachedMetrics(_ context.Context, mints []string) map[string]*models.TokenMetrics {
	out := map[string]*models.TokenMetrics{}
	for _, m := range mints {
		if n, ok := s.holders[m]; ok {
			h := n
			out[m] = &models.TokenMetrics{Holders: &h}
		}
	}
	return out
}

// p2Env is p1Env plus the phase 2 deps: stub holder checker, autopilot categories, presence,
// holder snapshots, metrics and the quote catalog.
type p2Env struct {
	*p1Env
	holders   *StubTokenHolderChecker
	autopilot *repository.MemoryPeerAutopilotRepository
	presence  *presence.MemoryPresenceStore
	snapshots *repository.MemoryRoomHolderSnapshotRepository
	metrics   *stubRoomMetrics
}

func newP2Env(t *testing.T) *p2Env {
	t.Helper()
	e := newP1Env(t)
	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(&models.LaunchQuote{QuoteMint: "StonkMint", Symbol: "STONK", Name: "STONK", Decimals: 9, Enabled: true})
	env := &p2Env{
		p1Env:     e,
		holders:   &StubTokenHolderChecker{Balances: map[string]uint64{}},
		autopilot: repository.NewMemoryPeerAutopilotRepository(),
		presence:  presence.NewMemoryPresenceStore(e.clk),
		snapshots: repository.NewMemoryRoomHolderSnapshotRepository(),
		metrics:   &stubRoomMetrics{holders: map[string]int{}},
	}
	t.Cleanup(env.presence.Close)
	e.forum.NowFunc = e.clk.Now
	e.svc.SetRoomDeps(RoomDeps{
		Holders: env.holders, Metrics: env.metrics, Snapshots: env.snapshots, Autopilot: env.autopilot,
		Presence: env.presence, Quotes: quotes, RaiseUnits: 25000, PortalURL: "https://dev.stonkagents.com/",
	})
	return env
}

// room records a bound launch (the room) with a quote mint and returns its mint.
func (e *p2Env) room(t *testing.T, mint, symbol, agent string) string {
	t.Helper()
	l := &models.TokenLaunch{Mint: mint, Symbol: symbol, Name: symbol + " Token", CreatorWallet: "wallet-" + agent, QuoteMint: "StonkMint",
		LaunchSignature: "sig-" + mint, PeerID: agent, CreatedAt: e.clk.Now()}
	if agent != "" {
		l.Status = models.LaunchStatusBound
	}
	if err := e.launches.Create(context.Background(), l); err != nil {
		t.Fatalf("launch: %v", err)
	}
	return mint
}

// holds gives the peer a linked wallet holding amount of mint.
func (e *p2Env) holds(t *testing.T, peerID, mint string, amount uint64) {
	t.Helper()
	e.wallet(t, peerID, "wallet-"+peerID)
	e.holders.Balances["wallet-"+peerID+":"+mint] = amount
}

// seen records the peer as seen now (routing candidate).
func (e *p2Env) seen(t *testing.T, peerID string) {
	t.Helper()
	if err := e.peers.Upsert(context.Background(), &models.Peer{PeerID: peerID, FirstSeen: e.clk.Now().Add(-48 * time.Hour), LastSeen: e.clk.Now()}); err != nil {
		t.Fatalf("peer %s: %v", peerID, err)
	}
}

func (e *p2Env) roomPost(t *testing.T, peer, mint, title, category string) *models.ForumPost {
	t.Helper()
	p, err := e.svc.CreatePost(context.Background(), peer, CreatePostInput{Title: title, Description: title + " body", Category: category, RoomMint: mint})
	if err != nil {
		t.Fatalf("room post %q by %s: %v", title, peer, err)
	}
	return p
}

// --- Rooms ---

func TestRooms_AccessRules(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintA", "AAA", "agent")
	e.holds(t, "holder", mint, 1)
	e.holds(t, "empty", mint, 0)
	e.wallet(t, "agent", "wallet-agent")

	if _, err := e.svc.CreatePost(ctx, "agent", CreatePostInput{Title: "x", Description: "y", RoomMint: "Nope"}); !errors.Is(err, ErrRoomUnknownMint) {
		t.Errorf("unknown mint: %v", err)
	}
	agentPost := e.roomPost(t, "agent", mint, "Agent post", "general")
	if !agentPost.InRoom() || *agentPost.RoomMint != mint {
		t.Errorf("agent post room = %v", agentPost.RoomMint)
	}
	holderPost := e.roomPost(t, "holder", mint, "Holder post", "general")
	if !holderPost.InRoom() {
		t.Error("holder post not in room")
	}
	if _, err := e.svc.CreatePost(ctx, "empty", CreatePostInput{Title: "x", Description: "y", RoomMint: mint}); !errors.Is(err, ErrRoomNotHolder) {
		t.Errorf("zero balance: %v", err)
	}
	if _, err := e.svc.CreatePost(ctx, "nowallet", CreatePostInput{Title: "x", Description: "y", RoomMint: mint}); !errors.Is(err, ErrRoomNotHolder) {
		t.Errorf("no wallet: %v", err)
	}
	// Replies follow the same rule, autopilot replies included.
	if _, err := e.svc.CreateReply(ctx, agentPost.ID, "empty", "hi", false); !errors.Is(err, ErrRoomNotHolder) {
		t.Errorf("reply by non holder: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, agentPost.ID, "empty", "hi", true); !errors.Is(err, ErrRoomNotHolder) {
		t.Errorf("auto reply by non holder: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, agentPost.ID, "holder", "hi", false); err != nil {
		t.Errorf("reply by holder: %v", err)
	}
	// The agent never needs a chain read; holders are checked once per 60 s.
	calls := e.holders.Calls
	e.roomPost(t, "agent", mint, "Again", "general")
	if e.holders.Calls != calls {
		t.Errorf("agent post read the chain")
	}
	e.roomPost(t, "holder", mint, "Cached", "general")
	if e.holders.Calls != calls {
		t.Errorf("holder check not cached: %d calls", e.holders.Calls-calls)
	}
	e.clk.Advance(RoomHolderCacheTTL + time.Second)
	e.holders.Balances["wallet-holder:"+mint] = 0
	if _, err := e.svc.CreatePost(ctx, "holder", CreatePostInput{Title: "x", Description: "y", RoomMint: mint}); !errors.Is(err, ErrRoomNotHolder) {
		t.Errorf("sold out holder after the cache expired: %v", err)
	}
	// A chain failure is reported as unavailable, never as a silent refusal.
	e.clk.Advance(RoomHolderCacheTTL + time.Second)
	e.holders.Err = errors.New("rpc down")
	if _, err := e.svc.CreatePost(ctx, "holder", CreatePostInput{Title: "x", Description: "y", RoomMint: mint}); !errors.Is(err, ErrRoomCheckUnavailable) {
		t.Errorf("rpc failure: %v", err)
	}
}

func TestRooms_FeedsCountsAndMine(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintA", "AAA", "agent")
	other := e.room(t, "MintB", "BBB", "agent")
	main := e.post(t, "agent", "Main", "on the main feed", "general")
	inRoom := e.roomPost(t, "agent", mint, "Room", "request")
	e.roomPost(t, "agent", other, "Other room", "general")

	posts, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10})
	if total != 1 || posts[0].ID != main.ID {
		t.Errorf("main feed = %d posts (want the main post only)", total)
	}
	posts, total, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10, Room: mint})
	if total != 1 || posts[0].ID != inRoom.ID {
		t.Errorf("room feed = %d posts", total)
	}
	_, total, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10, Mine: repository.MinePosts, MinePeerID: "agent"})
	if total != 3 {
		t.Errorf("mine=posts should include room posts: %d", total)
	}
	c, _ := e.svc.BoardCounts(ctx, "")
	if c.All != 1 || c.ByCategory["request"] != 0 {
		t.Errorf("main counts = %+v", c)
	}
	c, _ = e.svc.BoardCounts(ctx, mint)
	if c.All != 1 || c.ByCategory["request"] != 1 {
		t.Errorf("room counts = %+v", c)
	}
}

func TestRooms_ListAndGet(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	mintA := e.room(t, "MintA", "AAA", "agent")
	mintB := e.room(t, "MintB", "BBB", "other")
	e.room(t, "MintC", "CCC", "other")
	e.holds(t, "agent", mintB, 5)
	e.metrics.holders[mintA] = 42
	e.clk.Advance(time.Hour)
	p := e.roomPost(t, "agent", mintB, "In B", "general")

	rooms, err := e.svc.ListRooms(ctx, "agent")
	if err != nil || len(rooms) != 2 {
		t.Fatalf("rooms = %d err=%v", len(rooms), err)
	}
	// B has the newest post, so it leads.
	if rooms[0].Mint != mintB || rooms[0].Role != models.RoomRoleHolder || rooms[0].Posts7d != 1 || rooms[0].LastPostAt == nil || !rooms[0].LastPostAt.Equal(p.CreatedAt) {
		t.Errorf("room B = %+v", rooms[0])
	}
	if rooms[1].Mint != mintA || rooms[1].Role != models.RoomRoleAgent || rooms[1].MembersEstimate == nil || *rooms[1].MembersEstimate != 42 || rooms[1].Symbol != "AAA" || rooms[1].AgentPeerID != "agent" {
		t.Errorf("room A = %+v", rooms[1])
	}
	if rooms[0].MembersEstimate != nil {
		t.Errorf("room B members without cached metrics = %v, want nil", *rooms[0].MembersEstimate)
	}
	if got, _ := e.svc.ListRooms(ctx, "nobody"); len(got) != 0 {
		t.Errorf("peer without launches or wallet: %d rooms", len(got))
	}

	room, err := e.svc.GetRoom(ctx, mintA, "")
	if err != nil || room.Role != "" {
		t.Errorf("anonymous room: %+v err=%v", room, err)
	}
	room, _ = e.svc.GetRoom(ctx, mintA, "agent")
	if room.Role != models.RoomRoleAgent {
		t.Errorf("agent role = %q", room.Role)
	}
	room, _ = e.svc.GetRoom(ctx, mintB, "agent")
	if room.Role != models.RoomRoleHolder {
		t.Errorf("holder role = %q", room.Role)
	}
	if _, err := e.svc.GetRoom(ctx, "Nope", "agent"); !errors.Is(err, ErrRoomUnknownMint) {
		t.Errorf("unknown room: %v", err)
	}
}

func TestRooms_AnnounceLaunchIdempotent(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintA", "aaa", "agent")
	launch, _ := e.launches.GetByMint(ctx, mint)

	first, err := e.svc.AnnounceLaunch(ctx, launch)
	if err != nil {
		t.Fatalf("announce: %v", err)
	}
	if first.Title != "AAA launched" || first.Category != "discovery" || !first.RoomPinned || first.AuthorPeerID != "agent" || !first.InRoom() {
		t.Errorf("announcement = %+v", first)
	}
	for _, want := range []string{"aaa Token (AAA)", "Fixed raise of 25,000 STONK", "Token page: /tokens/" + mint, "\nhttps://dev.stonkagents.com/tokens/" + mint} {
		if !strings.Contains(first.Description, want) {
			t.Errorf("body lacks %q:\n%s", want, first.Description)
		}
	}
	again, err := e.svc.AnnounceLaunch(ctx, launch)
	if err != nil || again.ID != first.ID {
		t.Errorf("second announce: id=%s err=%v", again.ID, err)
	}
	// The announcement leads the room feed, even after newer posts.
	e.clk.Advance(time.Minute)
	e.roomPost(t, "agent", mint, "Newer", "general")
	posts, _, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10, Room: mint})
	if len(posts) != 2 || posts[0].ID != first.ID {
		t.Errorf("room feed order = %v", posts)
	}
	// The announcement never leaks into the main feed and needs a bound agent.
	if _, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10}); total != 0 {
		t.Errorf("main feed has %d posts", total)
	}
	unbound := e.room(t, "MintU", "UUU", "")
	l, _ := e.launches.GetByMint(ctx, unbound)
	if _, err := e.svc.AnnounceLaunch(ctx, l); !errors.Is(err, ErrRoomNoAgent) {
		t.Errorf("unbound launch: %v", err)
	}
}

// --- Request routing ---

func TestRouting_ScoresTopFiveAndNotifies(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.seen(t, "alice")
	// cat: +3 category. trusted: +4. accepted: +2 (accepted answer 10 days ago) +2 (that answer
	// and its first reply made the peer active). online: +1. combo: category + active + online = 6.
	// low: nothing (0). stale: seen 20 days ago.
	for _, id := range []string{"cat", "trusted", "accepted", "online", "combo", "low"} {
		e.seen(t, id)
	}
	_ = e.peers.Upsert(ctx, &models.Peer{PeerID: "stale", LastSeen: e.clk.Now().Add(-20 * 24 * time.Hour)})
	_ = e.autopilot.Set(ctx, "cat", []string{"request", "general"}, e.clk.Now())
	_ = e.autopilot.Set(ctx, "combo", []string{"request"}, e.clk.Now())
	_ = e.autopilot.Set(ctx, "stale", []string{"request"}, e.clk.Now())
	e.tier(t, "trusted", models.ReputationTierTrusted)
	e.tier(t, "combo", models.ReputationTierActive)
	_ = e.presence.Heartbeat(ctx, "online", time.Hour)
	_ = e.presence.Heartbeat(ctx, "combo", time.Hour)
	old := e.post(t, "bob", "Old question", "body", "general")
	acc := e.reply(t, old.ID, "accepted", "the answer")
	if _, err := e.svc.AcceptReply(ctx, "bob", old.ID, acc.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	e.clk.Advance(10 * 24 * time.Hour)

	amount := 300
	post, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "Need a hand", Description: "please", Category: "request", BountyAmount: &amount})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	want := []string{"combo", "accepted", "trusted", "cat"}
	if strings.Join(post.RoutedTo, ",") != strings.Join(want, ",") {
		t.Errorf("routed_to = %v, want %v", post.RoutedTo, want)
	}
	stored, _ := e.svc.GetPost(ctx, post.ID)
	if strings.Join(stored.RoutedTo, ",") != strings.Join(want, ",") {
		t.Errorf("stored routed_to = %v", stored.RoutedTo)
	}
	for _, id := range want {
		rows := e.feed(t, id, models.ActivityRequestRouted)
		if len(rows) != 1 || rows[0].PostID != post.ID || rows[0].ActorPeerID != "alice" || rows[0].Amount == nil || *rows[0].Amount != 300 {
			t.Errorf("%s activity = %+v", id, rows)
		}
	}
	for _, id := range []string{"online", "low", "stale", "alice"} {
		if rows := e.feed(t, id, models.ActivityRequestRouted); len(rows) != 0 {
			t.Errorf("%s should not be routed (score below %d): %+v", id, RoutingMinScore, rows)
		}
	}
	// A general post is never routed.
	plain := e.post(t, "alice", "Just saying", "hi", "general")
	if len(plain.RoutedTo) != 0 {
		t.Errorf("general post routed: %v", plain.RoutedTo)
	}
}

func TestRouting_TopFiveCapAndSamePairPenalty(t *testing.T) {
	e := newP2Env(t)
	e.seen(t, "alice")
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "spammer"} {
		e.seen(t, id)
	}
	// The spammer replied to alice's last three posts without ever being accepted or awarded;
	// p1 replied to the same posts but one reply was accepted.
	for i := 0; i < 3; i++ {
		p := e.post(t, "alice", "Earlier", "body", "general")
		e.reply(t, p.ID, "spammer", "me again")
		helpful := e.reply(t, p.ID, "p1", "helpful")
		if i == 1 {
			if _, err := e.svc.AcceptReply(context.Background(), "alice", p.ID, helpful.ID); err != nil {
				t.Fatalf("accept: %v", err)
			}
		}
		e.clk.Advance(time.Minute)
	}
	// Every candidate is top tier (+6), set after the accept so the recompute does not undo it.
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "spammer"} {
		e.tier(t, id, models.ReputationTierTop)
	}
	post := e.post(t, "alice", "Request", "body", "request")
	if len(post.RoutedTo) != RoutingTopN {
		t.Fatalf("routed to %d peers, want %d: %v", len(post.RoutedTo), RoutingTopN, post.RoutedTo)
	}
	for _, id := range post.RoutedTo {
		if id == "spammer" {
			t.Errorf("spammer routed despite the same-pair penalty: %v", post.RoutedTo)
		}
	}
	// p1 escaped the penalty, and top peers with equal scores sort by id.
	if post.RoutedTo[0] != "p1" {
		t.Errorf("routed_to = %v", post.RoutedTo)
	}
}

func TestRouting_RoomOnlyToPeersWhoCanPost(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintA", "AAA", "agent")
	e.wallet(t, "agent", "wallet-agent")
	e.seen(t, "agent")
	// holder: online (+1) and holds the room token (+2) = 3. bystander: online only = 1.
	// star: top tier (+6) and online, but holds nothing: excluded from a room post outright.
	// nowallet: top tier without a linked wallet: excluded too.
	for _, id := range []string{"holder", "bystander", "star", "nowallet"} {
		e.seen(t, id)
		_ = e.presence.Heartbeat(ctx, id, time.Hour)
	}
	e.holds(t, "holder", mint, 10)
	e.holds(t, "bystander", mint, 0)
	e.holds(t, "star", mint, 0)
	e.tier(t, "star", models.ReputationTierTop)
	e.tier(t, "nowallet", models.ReputationTierTop)
	_ = e.presence.Heartbeat(ctx, "agent", time.Hour)

	post := e.roomPost(t, "agent", mint, "Room request", "request")
	if strings.Join(post.RoutedTo, ",") != "holder" {
		t.Errorf("room routing = %v, want the holder only", post.RoutedTo)
	}
	// On the main feed the same star is routed (no room to be eligible for).
	main := e.post(t, "agent", "Main request", "body", "request")
	if strings.Join(main.RoutedTo, ",") != "nowallet,star" {
		t.Errorf("main feed routing = %v", main.RoutedTo)
	}
	// The token's agent is eligible without a chain read when someone else asks.
	roomAsk := e.roomPost(t, "holder", mint, "Ask the agent", "request")
	if strings.Join(roomAsk.RoutedTo, ",") != "agent" {
		t.Errorf("room routing by a holder = %v", roomAsk.RoutedTo)
	}
}

func TestRouting_PosterHourlyCap(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	e.seen(t, "alice")
	e.seen(t, "bot")
	_ = e.autopilot.Set(ctx, "bot", []string{"request"}, e.clk.Now())
	for i := 0; i < RoutingMaxPerPosterPerHour; i++ {
		p := e.post(t, "alice", "Request", "body", "request")
		if len(p.RoutedTo) != 1 {
			t.Fatalf("request %d routed to %v", i, p.RoutedTo)
		}
		e.clk.Advance(time.Minute)
	}
	// The sixth request within the hour lands unrouted; the bot gets no more rows.
	extra := e.post(t, "alice", "One more", "body", "request")
	if len(extra.RoutedTo) != 0 {
		t.Errorf("request over the cap routed to %v", extra.RoutedTo)
	}
	if rows := e.feed(t, "bot", models.ActivityRequestRouted); len(rows) != RoutingMaxPerPosterPerHour {
		t.Errorf("bot has %d routed rows, want %d", len(rows), RoutingMaxPerPosterPerHour)
	}
	// Another poster is unaffected, and the window rolls.
	e.seen(t, "carol")
	if p := e.post(t, "carol", "Request", "body", "request"); len(p.RoutedTo) != 1 {
		t.Errorf("another poster capped: %v", p.RoutedTo)
	}
	e.clk.Advance(RoutingPosterWindow)
	if p := e.post(t, "alice", "Later", "body", "request"); len(p.RoutedTo) != 1 {
		t.Errorf("after the window: %v", p.RoutedTo)
	}
}

func TestAutoPosts_FlagAndHideAuto(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	manual := e.post(t, "agent", "Manual", "body", "general")
	auto, err := e.svc.CreatePost(ctx, "agent", CreatePostInput{Title: "AAA weekly digest", Description: "numbers", Category: "general", Auto: true})
	if err != nil || !auto.Auto || manual.Auto {
		t.Fatalf("auto flag: auto=%v manual=%v err=%v", auto.Auto, manual.Auto, err)
	}
	stored, _ := e.svc.GetPost(ctx, auto.ID)
	if !stored.Auto {
		t.Errorf("auto flag not stored")
	}
	if _, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10}); total != 2 {
		t.Errorf("feed = %d posts", total)
	}
	posts, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10, HideAuto: true})
	if total != 1 || posts[0].ID != manual.ID {
		t.Errorf("hide_auto feed = %d posts %v", total, posts)
	}
}

// --- Digest ---

func TestDigest_CountersTopThreadsAndHolderDelta(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	mint := e.room(t, "MintA", "AAA", "agent")
	e.holds(t, "holder", mint, 1)
	e.account(t, "agent", 1000)
	e.account(t, "holder", 0)
	e.metrics.holders[mint] = 10

	// First digest: nothing yet, and it records today's holder count.
	e.clk.Advance(24 * time.Hour)
	d, err := e.svc.RoomDigestFor(ctx, mint, "agent", 7)
	if err != nil || d.Posts != 0 || d.NewHolders != nil || len(d.TopThreads) != 0 {
		t.Fatalf("empty digest = %+v err=%v", d, err)
	}
	if _, err := e.svc.RoomDigestFor(ctx, mint, "holder", 7); !errors.Is(err, ErrRoomNotAgent) {
		t.Errorf("holder digest: %v", err)
	}
	if _, err := e.svc.RoomDigestFor(ctx, "Nope", "agent", 7); !errors.Is(err, ErrRoomUnknownMint) {
		t.Errorf("unknown room: %v", err)
	}

	e.clk.Advance(24 * time.Hour)
	quiet := e.roomPost(t, "agent", mint, "Quiet", "general")
	hot := e.roomPost(t, "holder", mint, "Hot", "general")
	_, _, _ = e.svc.ToggleUpvote(ctx, "agent", hot.ID)
	e.reply(t, hot.ID, "agent", "yes")
	e.reply(t, quiet.ID, "holder", "ok")
	// A hidden reply on the hot thread counts neither in the period total nor per thread.
	hiddenReply := e.reply(t, hot.ID, "holder", "spam")
	if err := e.svc.SetReplyHidden(ctx, hiddenReply.ID, true); err != nil {
		t.Fatalf("hide reply: %v", err)
	}
	amount := 150
	b, err := e.svc.CreatePost(ctx, "agent", CreatePostInput{Title: "Bounty", Description: "do it", Category: "bounty", BountyAmount: &amount, RoomMint: mint})
	if err != nil {
		t.Fatalf("room bounty: %v", err)
	}
	win := e.reply(t, b.ID, "holder", "done")
	if err := e.svc.AwardBounty(ctx, "agent", b.ID, win.ID); err != nil {
		t.Fatalf("award: %v", err)
	}
	e.post(t, "agent", "Main feed", "not in the room", "general")
	e.metrics.holders[mint] = 16
	// Exactly a week after the first digest, so its snapshot sits on the period start day.
	e.clk.Advance(6 * 24 * time.Hour)

	d, err = e.svc.RoomDigestFor(ctx, mint, "agent", 7)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if d.Posts != 3 || d.Replies != 3 || d.BountiesAwarded != 1 || d.CreditsAwarded != 150 {
		t.Errorf("digest counters = %+v", d)
	}
	if d.NewHolders == nil || *d.NewHolders != 6 {
		t.Errorf("new_holders = %v, want 6", d.NewHolders)
	}
	if len(d.TopThreads) != 3 || d.TopThreads[0].ID != hot.ID || d.TopThreads[0].Upvotes != 1 || d.TopThreads[0].Replies != 1 {
		t.Errorf("top threads = %+v", d.TopThreads)
	}
	if !d.PeriodEnd.Equal(e.clk.Now()) || !d.PeriodStart.Equal(e.clk.Now().Add(-7*24*time.Hour)) {
		t.Errorf("period = %v .. %v", d.PeriodStart, d.PeriodEnd)
	}
}

// --- Activity filters ---

func TestActivity_KindsAndUnreadFilters(t *testing.T) {
	e := newP2Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Q", "body", "general")
	rp := e.reply(t, p.ID, "bob", "a")
	_, _, _ = e.svc.ToggleUpvote(ctx, "bob", p.ID)
	if _, err := e.svc.AcceptReply(ctx, "alice", p.ID, rp.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	rows, unread, _ := e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10})
	if len(rows) != 2 || unread != 2 {
		t.Fatalf("alice feed = %d rows, %d unread", len(rows), unread)
	}
	rows, _, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10, Kinds: []string{models.ActivityPostUpvoted}})
	if len(rows) != 1 || rows[0].Kind != models.ActivityPostUpvoted {
		t.Errorf("kinds filter = %+v", rows)
	}
	_ = e.svc.MarkActivityRead(ctx, "alice", []string{rows[0].ID}, false)
	rows, unread, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10, Unread: true})
	if len(rows) != 1 || rows[0].Kind != models.ActivityReplyOnPost || unread != 1 {
		t.Errorf("unread filter = %+v (unread %d)", rows, unread)
	}
}
