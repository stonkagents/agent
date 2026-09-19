// Package repository: Postgres integration test for the community board phase 1 queries.
// Runs only with TRACKER_TEST_DATABASE_URL set (a scratch Postgres the embedded migrations may
// be applied to); skipped otherwise. It exists because the memory repositories cannot catch
// SQL type errors such as uuid = text, so every phase 1 statement
// is executed here at least once against real column types. Rows are scoped to a random peer
// id prefix and deleted at the end.
package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/db"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// pgTestPool connects to TRACKER_TEST_DATABASE_URL and applies the embedded migrations.
func pgTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("TRACKER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("TRACKER_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	if err := db.RunMigrations(url); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPostgres_BoardPhase1Queries(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "it-" + uuid.New().String()[:8] + "-"
	alice, bob, carol, bot := run+"alice", run+"bob", run+"carol", run+"bot"
	now := time.Now().UTC().Truncate(time.Microsecond)

	peers := NewPostgresPeerRepository(pool)
	for _, p := range []*models.Peer{
		{PeerID: alice, PublicKey: "pk-" + alice, Multiaddrs: []string{}, DisplayName: run + "Alice", FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: bob, PublicKey: "pk-" + bob, Multiaddrs: []string{}, DisplayName: run + "bob_builder", FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: carol, PublicKey: "pk-" + carol, Multiaddrs: []string{}, DisplayName: run + "carol99", FirstSeen: now, LastSeen: now},
		{PeerID: bot, PublicKey: "pk-" + bot, Multiaddrs: []string{}, FirstSeen: now, LastSeen: now},
	} {
		if err := peers.Create(ctx, p); err != nil {
			t.Fatalf("create peer %s: %v", p.PeerID, err)
		}
	}
	t.Cleanup(func() {
		// forum_posts, forum_replies and forum_upvotes cascade from peers; the phase 1 tables
		// carry plain ids and are cleaned by prefix.
		for _, q := range []string{
			`DELETE FROM peer_reputation WHERE peer_id LIKE $1`,
			`DELETE FROM board_reports WHERE reporter_peer_id LIKE $1`,
			`DELETE FROM board_watches WHERE peer_id LIKE $1`,
			`DELETE FROM board_activity WHERE peer_id LIKE $1`,
			`DELETE FROM token_offer_payments WHERE from_wallet LIKE $1`,
			`DELETE FROM peers WHERE peer_id LIKE $1`,
		} {
			if _, err := pool.Exec(ctx, q, run+"%"); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	forum := NewPostgresForumRepository(pool)
	amount, max, decimals := 300, 2, 6
	mint, symbol := run+"mint", "AAA"
	post := &models.ForumPost{
		AuthorPeerID: alice, Title: run + " question", Description: "how do I", Category: "token-offer", Tags: []string{"go"},
		CreatedAt: now, UpdatedAt: now,
		TokenOfferAmount: &amount, TokenOfferToken: &symbol, TokenOfferMint: &mint, TokenOfferSymbol: &symbol,
		TokenOfferDecimals: &decimals, TokenOfferMax: &max, MentionPeerIDs: []string{bob},
	}
	if err := forum.CreatePost(ctx, post); err != nil {
		t.Fatalf("create post: %v", err)
	}
	bountyAmt := 250
	bountyPost := &models.ForumPost{AuthorPeerID: alice, Title: run + " bounty", Description: "b", Category: "bounty", Tags: []string{},
		CreatedAt: now, UpdatedAt: now, BountyAmount: &bountyAmt, BountyStatus: "open"}
	if err := forum.CreatePost(ctx, bountyPost); err != nil {
		t.Fatalf("create bounty post: %v", err)
	}
	// Autopilot answers first, Bob's manual reply follows within the hour, Carol later.
	botReply := &models.ForumReply{PostID: post.ID, AuthorPeerID: bot, Body: "auto", CreatedAt: now.Add(time.Second), Auto: true}
	bobReply := &models.ForumReply{PostID: post.ID, AuthorPeerID: bob, Body: "manual @" + run + "carol99", CreatedAt: now.Add(10 * time.Minute), MentionPeerIDs: []string{carol}}
	carolReply := &models.ForumReply{PostID: post.ID, AuthorPeerID: carol, Body: "late", CreatedAt: now.Add(2 * time.Hour)}
	for _, rp := range []*models.ForumReply{botReply, bobReply, carolReply} {
		if err := forum.CreateReply(ctx, rp); err != nil {
			t.Fatalf("create reply: %v", err)
		}
	}
	for _, voter := range []string{bob, carol} {
		if err := forum.AddUpvote(ctx, voter, post.ID); err != nil {
			t.Fatalf("upvote: %v", err)
		}
	}
	if err := forum.AwardBounty(ctx, bountyPost.ID, bob); err != nil {
		t.Fatalf("award: %v", err)
	}

	// Accepted answer: the uuid = text join that broke on dev.
	if err := forum.SetAcceptedReply(ctx, post.ID, &bobReply.ID); err != nil {
		t.Fatalf("set accepted: %v", err)
	}
	got, err := forum.GetReplyByID(ctx, bobReply.ID)
	if err != nil || got.AuthorPeerID != bob || len(got.MentionPeerIDs) != 1 || got.MentionPeerIDs[0] != carol {
		t.Fatalf("get reply: %+v err=%v", got, err)
	}
	stats, err := forum.BoardStats(ctx, bob, BoardStatsGuard{})
	if err != nil {
		t.Fatalf("BoardStats(bob): %v", err)
	}
	if stats.AnswersAccepted != 1 || stats.BountiesWon != 1 || stats.CreditsWon != 250 || stats.FirstReplies1h != 1 || stats.UpvotesReceived != 0 {
		t.Errorf("bob stats = %+v", stats)
	}
	aliceStats, err := forum.BoardStats(ctx, alice, BoardStatsGuard{})
	if err != nil || aliceStats.UpvotesReceived != 2 || aliceStats.FirstReplies1h != 0 {
		t.Errorf("alice stats = %+v err=%v", aliceStats, err)
	}
	botStats, _ := forum.BoardStats(ctx, bot, BoardStatsGuard{})
	if botStats.FirstReplies1h != 0 {
		t.Errorf("autopilot first reply counted: %+v", botStats)
	}
	if err := forum.SetAcceptedReply(ctx, post.ID, nil); err != nil {
		t.Fatalf("clear accepted: %v", err)
	}
	if p, _ := forum.GetPostByID(ctx, post.ID); p.AcceptedReplyID != nil || len(p.MentionPeerIDs) != 1 || p.TokenOfferMint == nil || *p.TokenOfferMint != mint {
		t.Errorf("post after clear = %+v", p)
	}
	ids, err := forum.BoardPeerIDs(ctx)
	if err != nil || !contains(ids, alice) || !contains(ids, bob) || !contains(ids, bot) {
		t.Errorf("BoardPeerIDs = %v err=%v", ids, err)
	}

	// Visibility, pin, hide.
	if err := forum.SetReplyHidden(ctx, carolReply.ID, true); err != nil {
		t.Fatalf("hide reply: %v", err)
	}
	replies, total, err := forum.ListRepliesFiltered(ctx, post.ID, ReplyQuery{Limit: 10, Viewer: bob})
	if err != nil || total != 2 || len(replies) != 2 {
		t.Errorf("filtered replies (bob) = %d/%d err=%v", len(replies), total, err)
	}
	replies, total, _ = forum.ListRepliesFiltered(ctx, post.ID, ReplyQuery{Limit: 10, Viewer: carol, HideAuto: true})
	if total != 2 || len(replies) != 2 || !replies[1].Hidden {
		t.Errorf("filtered replies (carol, hide auto) = %d/%d", len(replies), total)
	}
	if err := forum.PinPost(ctx, post.ID); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := forum.SetPostHidden(ctx, bountyPost.ID, true); err != nil {
		t.Fatalf("hide post: %v", err)
	}
	posts, _, err := forum.QueryPosts(ctx, PostQuery{Limit: 5, Mine: MinePosts, MinePeerID: alice, Viewer: bob})
	if err != nil || len(posts) != 1 || posts[0].ID != post.ID || !posts[0].Pinned {
		t.Errorf("query as bob = %v err=%v", ids2(posts), err)
	}
	posts, _, _ = forum.QueryPosts(ctx, PostQuery{Limit: 5, Mine: MinePosts, MinePeerID: alice, ShowHidden: true})
	if len(posts) != 2 {
		t.Errorf("query with ShowHidden = %d", len(posts))
	}
	if err := forum.UnpinPost(ctx, post.ID); err != nil {
		t.Fatalf("unpin: %v", err)
	}

	// Token offer slots: conditional increment stops at max, decrement gives one back.
	for i := 1; i <= max; i++ {
		if n, err := forum.IncrementTokenOfferPaid(ctx, post.ID); err != nil || n != i {
			t.Fatalf("increment %d: n=%d err=%v", i, n, err)
		}
	}
	if _, err := forum.IncrementTokenOfferPaid(ctx, post.ID); err != models.ErrInvalidInput {
		t.Errorf("increment past max: %v", err)
	}
	if _, err := forum.IncrementTokenOfferPaid(ctx, uuid.New().String()); err != models.ErrNotFound {
		t.Errorf("increment unknown post: %v", err)
	}
	if err := forum.DecrementTokenOfferPaid(ctx, post.ID); err != nil {
		t.Fatalf("decrement: %v", err)
	}
	if p, _ := forum.GetPostByID(ctx, post.ID); p.TokenOfferPaid != max-1 {
		t.Errorf("paid after decrement = %d", p.TokenOfferPaid)
	}

	// Reputation rows.
	rep := NewPostgresBoardReputationRepository(pool)
	row := &models.PeerReputation{PeerID: bob, Score: 44, Tier: "active", BountiesWon: 1, CreditsWon: 250, AnswersAccepted: 1, FirstReplies1h: 1, ComputedAt: now}
	if err := rep.Upsert(ctx, row); err != nil {
		t.Fatalf("upsert reputation: %v", err)
	}
	row.Score = 45
	if err := rep.Upsert(ctx, row); err != nil {
		t.Fatalf("upsert reputation again: %v", err)
	}
	if got, err := rep.Get(ctx, bob); err != nil || got.Score != 45 || got.Tier != "active" {
		t.Errorf("get reputation = %+v err=%v", got, err)
	}
	if _, err := rep.Get(ctx, carol); err != models.ErrNotFound {
		t.Errorf("get unknown reputation: %v", err)
	}
	if m, err := rep.GetByIDs(ctx, []string{bob, carol}); err != nil || len(m) != 1 || m[bob] == nil {
		t.Errorf("reputation by ids = %v err=%v", m, err)
	}

	// Reports.
	reports := NewPostgresBoardReportRepository(pool)
	r1 := &models.BoardReport{TargetType: "post", TargetID: post.ID, TargetAuthorPeerID: alice, ReporterPeerID: bob, Reason: "spam", Note: "ad", CreatedAt: now}
	if err := reports.Create(ctx, r1); err != nil {
		t.Fatalf("create report: %v", err)
	}
	if err := reports.Create(ctx, &models.BoardReport{TargetType: "post", TargetID: post.ID, TargetAuthorPeerID: alice, ReporterPeerID: bob, Reason: "abuse", CreatedAt: now}); err != models.ErrAlreadyExists {
		t.Errorf("duplicate report: %v", err)
	}
	r2 := &models.BoardReport{TargetType: "reply", TargetID: bobReply.ID, TargetAuthorPeerID: bob, ReporterPeerID: carol, Reason: "scam", CreatedAt: now.Add(time.Second)}
	if err := reports.Create(ctx, r2); err != nil {
		t.Fatalf("create report 2: %v", err)
	}
	if got, err := reports.Get(ctx, r1.ID); err != nil || got.Status != "open" || got.Note != "ad" {
		t.Errorf("get report = %+v err=%v", got, err)
	}
	if rs, err := reports.ReporterPeerIDs(ctx, "post", post.ID); err != nil || len(rs) != 1 || rs[0] != bob {
		t.Errorf("reporters = %v err=%v", rs, err)
	}
	if err := reports.SetStatus(ctx, r2.ID, "upheld", now); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if n, err := reports.CountUpheldAgainst(ctx, bob); err != nil || n != 1 {
		t.Errorf("upheld against bob = %d err=%v", n, err)
	}
	if list, err := reports.List(ctx, "open", 10); err != nil || len(list) < 1 {
		t.Errorf("open reports = %d err=%v", len(list), err)
	}
	if list, err := reports.List(ctx, "", 500); err != nil || len(list) < 2 {
		t.Errorf("all reports = %d err=%v", len(list), err)
	}

	// Watches.
	watches := NewPostgresBoardWatchRepository(pool)
	if err := watches.AddIfAbsent(ctx, post.ID, alice, now); err != nil {
		t.Fatalf("auto watch: %v", err)
	}
	if err := watches.Set(ctx, post.ID, alice, false, now); err != nil {
		t.Fatalf("unwatch: %v", err)
	}
	if err := watches.AddIfAbsent(ctx, post.ID, alice, now); err != nil {
		t.Fatalf("auto watch again: %v", err)
	}
	if w, _ := watches.IsWatching(ctx, post.ID, alice); w {
		t.Error("explicit unwatch undone")
	}
	_ = watches.Set(ctx, post.ID, carol, true, now)
	if ws, err := watches.Watchers(ctx, post.ID); err != nil || len(ws) != 1 || ws[0] != carol {
		t.Errorf("watchers = %v err=%v", ws, err)
	}
	if m, err := watches.WatchingByPostIDs(ctx, carol, []string{post.ID, bountyPost.ID}); err != nil || !m[post.ID] || m[bountyPost.ID] {
		t.Errorf("watching by ids = %v err=%v", m, err)
	}

	// Payments.
	payments := NewPostgresTokenOfferPaymentRepository(pool)
	pay := &models.TokenOfferPayment{Signature: run + "sig1", PostID: post.ID, ReplyID: bobReply.ID, FromWallet: run + "walletA", ToWallet: run + "walletB", AmountRaw: 300, VerifiedAt: now}
	if err := payments.Create(ctx, pay); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if err := payments.Create(ctx, &models.TokenOfferPayment{Signature: run + "sig2", PostID: post.ID, ReplyID: bobReply.ID, FromWallet: run + "walletA", ToWallet: run + "walletB", AmountRaw: 300, VerifiedAt: now}); err != models.ErrAlreadyExists {
		t.Errorf("same reply paid twice: %v", err)
	}
	if seen, err := payments.HasSignature(ctx, run+"sig1"); err != nil || !seen {
		t.Errorf("has signature = %v err=%v", seen, err)
	}
	if paid, err := payments.PaidReplyIDs(ctx, post.ID); err != nil || !paid[bobReply.ID] || len(paid) != 1 {
		t.Errorf("paid reply ids = %v err=%v", paid, err)
	}

	// Activity with symbol and a large amount.
	activity := NewPostgresBoardActivityRepository(pool)
	big := 5_000_000_000
	a := &models.BoardActivity{PeerID: bob, Kind: models.ActivityTokenOfferPaid, PostID: post.ID, ReplyID: &bobReply.ID, ActorPeerID: alice, Amount: &big, Symbol: symbol, CreatedAt: now}
	if err := activity.Create(ctx, a); err != nil {
		t.Fatalf("create activity: %v", err)
	}
	rows, err := activity.List(ctx, bob, ActivityQuery{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Symbol != symbol || rows[0].Amount == nil || *rows[0].Amount != big {
		t.Errorf("activity rows = %+v err=%v", rows, err)
	}

	// Display name lookups.
	names, err := peers.PeerIDsByDisplayNames(ctx, []string{strings.ToLower(run + "alice"), strings.ToLower(run + "BOB_BUILDER"), "nobody"})
	if err != nil || names[strings.ToLower(run+"alice")] != alice || names[strings.ToLower(run+"bob_builder")] != bob || len(names) != 2 {
		t.Errorf("PeerIDsByDisplayNames = %v err=%v", names, err)
	}
	matches, err := peers.SearchDisplayNames(ctx, strings.ToUpper(run), 8)
	if err != nil || len(matches) != 3 || matches[0].DisplayName != run+"Alice" {
		t.Errorf("SearchDisplayNames = %v err=%v", matches, err)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func ids2(posts []*models.ForumPost) []string {
	out := make([]string, len(posts))
	for i, p := range posts {
		out[i] = p.ID
	}
	return out
}
