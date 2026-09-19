// Package services: Community board phase 0 tests over the memory repositories.
// Covers the feed filters (category, search, mine, bounties sort), the board counts cache,
// bounty expiry with escrow refund, the single 7-day extension, and the activity fan-out.
package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type boardEnv struct {
	svc      *ForumService
	forum    *repository.MemoryForumRepository
	activity *repository.MemoryBoardActivityRepository
	credits  creditTestDeps
	clk      *clock.MockClock
}

// newBoardEnv wires a ForumService with escrow (memory credits), refunds (real CreditService)
// and the activity feed, all on one mock clock.
func newBoardEnv(t *testing.T) *boardEnv {
	t.Helper()
	deps := newCreditTestDeps()
	forumRepo := repository.NewMemoryForumRepository()
	activityRepo := repository.NewMemoryBoardActivityRepository()
	svc := NewForumService(forumRepo, deps.accounts, deps.credits)
	svc.SetClock(deps.clock)
	svc.SetActivityRepo(activityRepo)
	svc.SetCreditRefunder(newCreditService(deps, "secret"))
	return &boardEnv{svc: svc, forum: forumRepo, activity: activityRepo, credits: deps, clk: deps.clock}
}

func (e *boardEnv) account(t *testing.T, peerID string, free int) string {
	t.Helper()
	return e.accountWith(t, peerID, free, 0)
}

func (e *boardEnv) accountWith(t *testing.T, peerID string, free, paid int) string {
	t.Helper()
	id := "acct-" + peerID
	seedAccountWithCredits(t, e.credits, id, peerID, free, paid)
	return id
}

func (e *boardEnv) balanceOf(t *testing.T, accountID string) *models.CreditBalance {
	t.Helper()
	bal, err := e.credits.credits.GetBalance(context.Background(), accountID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return bal
}

func (e *boardEnv) balance(t *testing.T, accountID string) int {
	t.Helper()
	bal := e.balanceOf(t, accountID)
	return bal.FreeBalance + bal.PaidBalance
}

func (e *boardEnv) hasTx(accountID, requestID string) bool {
	_, err := e.credits.credits.GetTransactionByRequestID(context.Background(), accountID, requestID)
	return err == nil
}

func (e *boardEnv) post(t *testing.T, peer, title, body, category string) *models.ForumPost {
	t.Helper()
	p, err := e.svc.CreatePost(context.Background(), peer, CreatePostInput{Title: title, Description: body, Category: category})
	if err != nil {
		t.Fatalf("create post %q: %v", title, err)
	}
	return p
}

func (e *boardEnv) bounty(t *testing.T, peer, title string, amount, days int) *models.ForumPost {
	t.Helper()
	p, err := e.svc.CreatePost(context.Background(), peer, CreatePostInput{
		Title: title, Description: title + " body", Category: "bounty", BountyAmount: &amount, BountyDays: &days,
	})
	if err != nil {
		t.Fatalf("create bounty %q: %v", title, err)
	}
	return p
}

func (e *boardEnv) feed(t *testing.T, peer string, kind string) []*models.BoardActivity {
	t.Helper()
	rows, _, err := e.svc.ListActivity(context.Background(), peer, repository.ActivityQuery{Limit: 100})
	if err != nil {
		t.Fatalf("list activity: %v", err)
	}
	var out []*models.BoardActivity
	for _, a := range rows {
		if kind == "" || a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

func ids(posts []*models.ForumPost) []string {
	out := make([]string, len(posts))
	for i, p := range posts {
		out[i] = p.ID
	}
	return out
}

// --- Feed filters ---

func TestQueryPosts_CategoryAndSearch(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.post(t, "alice", "Need a dataset curator", "Looking for CSV help", "request")
	e.post(t, "alice", "Sharing my model", "trained on cats", "discovery")
	e.post(t, "bob", "General chatter", "nothing about datasets here", "general")

	posts, total, err := e.svc.QueryPosts(ctx, repository.PostQuery{Category: "request"})
	if err != nil || total != 1 || len(posts) != 1 || posts[0].Title != "Need a dataset curator" {
		t.Fatalf("category=request: posts=%d total=%d err=%v", len(posts), total, err)
	}
	posts, total, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Search: "DATASET"})
	if total != 2 || len(posts) != 2 {
		t.Fatalf("q=DATASET should match title and body case-insensitively: got %d", total)
	}
	posts, total, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Search: "dataset", Category: "general"})
	if total != 1 || posts[0].AuthorPeerID != "bob" {
		t.Fatalf("q + category should combine: total=%d", total)
	}
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if _, _, err := e.svc.QueryPosts(ctx, repository.PostQuery{Search: string(long)}); err != nil {
		t.Fatalf("over-long q must be capped, not rejected: %v", err)
	}
}

func TestQueryPosts_BountiesTab_OpenOnlyOrderedByAmountThenExpiry(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 10000)
	small := e.bounty(t, "alice", "small", 100, 30)
	bigLate := e.bounty(t, "alice", "big late", 500, 20)
	bigSoon := e.bounty(t, "alice", "big soon", 500, 5)
	expiredSoon := e.bounty(t, "alice", "already past", 900, 1)
	e.post(t, "alice", "plain", "no bounty", "general")
	_ = e.forum.ExpireBounty(ctx, expiredSoon.ID)

	posts, total, err := e.svc.QueryPosts(ctx, repository.PostQuery{Sort: repository.PostSortBounties})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{bigSoon.ID, bigLate.ID, small.ID}
	got := ids(posts)
	if total != 3 || len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("bounties tab order = %v (total %d), want %v", got, total, want)
	}
}

func TestQueryPosts_Mine(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 10000)
	e.account(t, "bob", 10000)
	mine := e.post(t, "alice", "mine", "by alice", "general")
	theirs := e.post(t, "bob", "theirs", "by bob", "general")
	won := e.bounty(t, "bob", "bob bounty", 200, 10)
	posted := e.bounty(t, "alice", "alice bounty", 300, 10)
	reply, err := e.svc.CreateReply(ctx, won.ID, "alice", "my answer", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AwardBounty(ctx, "bob", won.ID, reply.ID); err != nil {
		t.Fatalf("award: %v", err)
	}

	cases := []struct {
		mine string
		want map[string]bool
	}{
		{repository.MinePosts, map[string]bool{mine.ID: true, posted.ID: true}},
		{repository.MineReplies, map[string]bool{won.ID: true}},
		{repository.MineBounties, map[string]bool{won.ID: true, posted.ID: true}},
	}
	for _, c := range cases {
		posts, total, err := e.svc.QueryPosts(ctx, repository.PostQuery{Mine: c.mine, MinePeerID: "alice"})
		if err != nil {
			t.Fatalf("mine=%s: %v", c.mine, err)
		}
		if total != len(c.want) {
			t.Errorf("mine=%s total=%d want %d (%v)", c.mine, total, len(c.want), ids(posts))
		}
		for _, p := range posts {
			if !c.want[p.ID] {
				t.Errorf("mine=%s returned unexpected post %s (%s)", c.mine, p.ID, p.Title)
			}
			if p.ID == theirs.ID {
				t.Errorf("mine=%s leaked bob's post", c.mine)
			}
		}
	}
	if _, _, err := e.svc.QueryPosts(ctx, repository.PostQuery{Mine: repository.MinePosts}); err != models.ErrInvalidInput {
		t.Fatalf("mine without a peer must be ErrInvalidInput, got %v", err)
	}
}

func TestBoardCounts_AggregatesAndCaches(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 10000)
	e.post(t, "alice", "g", "g", "general")
	e.post(t, "alice", "r", "r", "request")
	e.bounty(t, "alice", "b", 100, 5)
	c, err := e.svc.BoardCounts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.All != 3 || c.ByCategory["general"] != 1 || c.ByCategory["request"] != 1 || c.ByCategory["bounty"] != 1 || c.OpenBounties != 1 {
		t.Fatalf("counts = %+v", c)
	}
	e.post(t, "alice", "later", "later", "discovery")
	c, _ = e.svc.BoardCounts(ctx, "")
	if c.All != 3 {
		t.Fatalf("counts must be cached for %s: all=%d", BoardCountsTTL, c.All)
	}
	e.clk.Advance(BoardCountsTTL + time.Second)
	c, _ = e.svc.BoardCounts(ctx, "")
	if c.All != 4 || c.ByCategory["discovery"] != 1 {
		t.Fatalf("counts must refresh after the TTL: %+v", c)
	}
}

// --- Bounty expiry ---

func TestExpireBounties_RefundsEscrowOnceAndNotifies(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	acct := e.account(t, "alice", 1000)
	p := e.bounty(t, "alice", "expiring", 400, 2)
	if e.balance(t, acct) != 600 {
		t.Fatalf("escrow should hold 400: balance=%d", e.balance(t, acct))
	}
	if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 0 {
		t.Fatalf("nothing expired yet: n=%d err=%v", n, err)
	}

	e.clk.Advance(3 * 24 * time.Hour)
	n, err := e.svc.ExpireBounties(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	got, _ := e.svc.GetPost(ctx, p.ID)
	if got.BountyStatus != "expired" || got.BountyRefundedAt == nil || !got.BountyRefundedAt.Equal(e.clk.Now()) {
		t.Fatalf("post after expiry: status=%s refunded_at=%v", got.BountyStatus, got.BountyRefundedAt)
	}
	if e.balance(t, acct) != 1000 {
		t.Fatalf("escrow must come back: balance=%d", e.balance(t, acct))
	}
	tx, err := e.credits.credits.GetTransactionByRequestID(ctx, acct, "refund:"+*p.BountyEscrowRequestID+":free")
	if err != nil || tx.Reason != BountyRefundReason {
		t.Fatalf("refund transaction: %+v err=%v", tx, err)
	}
	// Idempotent: a second run finds nothing and moves no credits.
	if n, _ := e.svc.ExpireBounties(ctx); n != 0 || e.balance(t, acct) != 1000 {
		t.Fatalf("second run must be a no-op: n=%d balance=%d", n, e.balance(t, acct))
	}
	acts := e.feed(t, "alice", models.ActivityBountyExpiredRefunded)
	if len(acts) != 1 || acts[0].Amount == nil || *acts[0].Amount != 400 || acts[0].PostID != p.ID || acts[0].ActorPeerID != "" {
		t.Fatalf("bounty_expired_refunded activity = %+v", acts)
	}
}

// TestExpireBounties_RefundReturnsToOriginBuckets: the escrow split is recorded at spend time
// and the refund puts the paid part back as paid and the free part back as free.
func TestExpireBounties_RefundReturnsToOriginBuckets(t *testing.T) {
	cases := []struct {
		name               string
		free, paid, amount int
		wantPaid, wantFree int
		wantEscrowPaid     int
	}{
		{name: "paid only", free: 0, paid: 500, amount: 300, wantEscrowPaid: 300, wantPaid: 500, wantFree: 0},
		{name: "free only", free: 500, paid: 200, amount: 300, wantEscrowPaid: 0, wantPaid: 200, wantFree: 500},
		{name: "mixed", free: 100, paid: 400, amount: 300, wantEscrowPaid: 200, wantPaid: 400, wantFree: 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newBoardEnv(t)
			ctx := context.Background()
			acct := e.accountWith(t, "alice", c.free, c.paid)
			p := e.bounty(t, "alice", "b", c.amount, 1)
			if p.BountyEscrowPaid != c.wantEscrowPaid {
				t.Fatalf("escrow paid part = %d, want %d", p.BountyEscrowPaid, c.wantEscrowPaid)
			}
			after := e.balanceOf(t, acct)
			if after.FreeBalance+after.PaidBalance != c.free+c.paid-c.amount {
				t.Fatalf("escrow not taken: %+v", after)
			}

			e.clk.Advance(2 * 24 * time.Hour)
			if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 1 {
				t.Fatalf("expire: n=%d err=%v", n, err)
			}
			bal := e.balanceOf(t, acct)
			if bal.PaidBalance != c.wantPaid || bal.FreeBalance != c.wantFree {
				t.Fatalf("after refund free=%d paid=%d, want free=%d paid=%d", bal.FreeBalance, bal.PaidBalance, c.wantFree, c.wantPaid)
			}
			if bal.LifetimePurchased != c.paid {
				t.Fatalf("refund must not count as a purchase: lifetime_purchased=%d, want %d", bal.LifetimePurchased, c.paid)
			}
			base := "refund:" + *p.BountyEscrowRequestID
			if e.hasTx(acct, base+":paid") != (c.wantEscrowPaid > 0) || e.hasTx(acct, base+":free") != (c.amount-c.wantEscrowPaid > 0) {
				t.Fatalf("refund transactions: paid=%v free=%v", e.hasTx(acct, base+":paid"), e.hasTx(acct, base+":free"))
			}
			// Second run: nothing moves.
			if n, _ := e.svc.ExpireBounties(ctx); n != 0 {
				t.Fatalf("second run n=%d", n)
			}
			bal = e.balanceOf(t, acct)
			if bal.PaidBalance != c.wantPaid || bal.FreeBalance != c.wantFree {
				t.Fatalf("second run moved credits: %+v", bal)
			}
		})
	}
}

// TestExpireBounties_FreeRefundKeepsExistingExpiry: a free refund onto a free balance that still
// has an expiry leaves that expiry alone; onto an empty free bucket it sets a fresh 30 days.
func TestExpireBounties_FreeRefundKeepsExistingExpiry(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	acct := e.accountWith(t, "alice", 500, 0)
	seededExpiry := *e.balanceOf(t, acct).FreeCreditsExpiresAt
	p := e.bounty(t, "alice", "keep expiry", 200, 1)
	if got := e.balanceOf(t, acct); got.FreeCreditsExpiresAt == nil || !got.FreeCreditsExpiresAt.Equal(seededExpiry) {
		t.Fatalf("escrow must not move the free expiry: %v", got.FreeCreditsExpiresAt)
	}
	e.clk.Advance(2 * 24 * time.Hour)
	if _, err := e.svc.ExpireBounties(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.balanceOf(t, acct)
	if got.FreeBalance != 500 || got.FreeCreditsExpiresAt == nil || !got.FreeCreditsExpiresAt.Equal(seededExpiry) {
		t.Fatalf("refund moved the free expiry: free=%d expiry=%v want %v", got.FreeBalance, got.FreeCreditsExpiresAt, seededExpiry)
	}
	_ = p

	// Empty free bucket: the whole free balance was escrowed, so the refund starts a fresh window.
	e2 := newBoardEnv(t)
	acct2 := e2.accountWith(t, "bob", 200, 100)
	e2.bounty(t, "bob", "all free", 200, 1)
	if got := e2.balanceOf(t, acct2); got.FreeBalance != 0 || got.FreeCreditsExpiresAt != nil {
		t.Fatalf("escrow of the whole free balance must clear the expiry: %+v", got)
	}
	e2.clk.Advance(2 * 24 * time.Hour)
	if _, err := e2.svc.ExpireBounties(ctx); err != nil {
		t.Fatal(err)
	}
	got = e2.balanceOf(t, acct2)
	want := e2.clk.Now().Add(FreeExpiryWindow)
	if got.FreeBalance != 200 || got.PaidBalance != 100 || got.FreeCreditsExpiresAt == nil || !got.FreeCreditsExpiresAt.Equal(want) {
		t.Fatalf("refund onto an empty free bucket: free=%d paid=%d expiry=%v want %v", got.FreeBalance, got.PaidBalance, got.FreeCreditsExpiresAt, want)
	}
}

func TestCreatePost_BountyWithoutDaysGetsDefaultDeadline(t *testing.T) {
	e := newBoardEnv(t)
	e.account(t, "alice", 1000)
	amount := 100
	p, err := e.svc.CreatePost(context.Background(), "alice", CreatePostInput{Title: "no days", Description: "x", Category: "bounty", BountyAmount: &amount})
	if err != nil {
		t.Fatal(err)
	}
	want := e.clk.Now().Add(DefaultBountyDays * 24 * time.Hour)
	if p.BountyExpiresAt == nil || !p.BountyExpiresAt.Equal(want) {
		t.Fatalf("default deadline = %v, want %v", p.BountyExpiresAt, want)
	}
	tooLong := MaxBountyDays + 1
	if _, err := e.svc.CreatePost(context.Background(), "alice", CreatePostInput{Title: "long", Description: "x", BountyAmount: &amount, BountyDays: &tooLong}); err != models.ErrInvalidInput {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestExpireBounties_LegacyWithoutEscrowJustFlips(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	amount := 50
	past := e.clk.Now().Add(-time.Hour)
	legacy := &models.ForumPost{AuthorPeerID: "carol", Title: "legacy", Description: "x", Category: "bounty",
		BountyAmount: &amount, BountyStatus: "open", BountyExpiresAt: &past, CreatedAt: e.clk.Now(), UpdatedAt: e.clk.Now()}
	if err := e.forum.CreatePost(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.ExpireBounties(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := e.svc.GetPost(ctx, legacy.ID)
	if got.BountyStatus != "expired" || got.BountyRefundedAt != nil {
		t.Fatalf("legacy: status=%s refunded_at=%v", got.BountyStatus, got.BountyRefundedAt)
	}
	if acts := e.feed(t, "carol", ""); len(acts) != 0 {
		t.Fatalf("no refund, no activity: %+v", acts)
	}
}

func TestExpireBounties_EmitsBountyExpiringOnce(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	soon := e.bounty(t, "alice", "soon", 100, 1)
	e.bounty(t, "alice", "far", 100, 10)

	for i := 0; i < 3; i++ {
		if _, err := e.svc.ExpireBounties(ctx); err != nil {
			t.Fatal(err)
		}
	}
	acts := e.feed(t, "alice", models.ActivityBountyExpiring)
	if len(acts) != 1 || acts[0].PostID != soon.ID {
		t.Fatalf("bounty_expiring must be emitted once for the bounty inside 24h: %+v", acts)
	}

	// Extended: a second warning arrives before the new deadline, and only one.
	if _, err := e.svc.ExtendBounty(ctx, "alice", soon.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ExpireBounties(ctx); err != nil {
		t.Fatal(err)
	}
	if acts := e.feed(t, "alice", models.ActivityBountyExpiring); len(acts) != 1 {
		t.Fatalf("no warning while the new deadline is 8 days out: %+v", acts)
	}
	e.clk.Advance(7*24*time.Hour + 12*time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := e.svc.ExpireBounties(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if acts := e.feed(t, "alice", models.ActivityBountyExpiring); len(acts) != 2 {
		t.Fatalf("extended bounty must be warned once more before its new deadline: %d rows", len(acts))
	}
}

func TestTruncateRunes(t *testing.T) {
	s := "日本語テキスト"
	if got := TruncateRunes(s, 3); got != "日本語" {
		t.Fatalf("TruncateRunes = %q", got)
	}
	if got := TruncateRunes("abc", 10); got != "abc" {
		t.Fatalf("short string changed: %q", got)
	}
	if got := TruncateRunes("abc", 0); got != "" {
		t.Fatalf("n=0: %q", got)
	}
	long := strings.Repeat("語", MaxSearchQueryLen+5)
	e := newBoardEnv(t)
	e.post(t, "alice", "語 title", "body", "general")
	posts, total, err := e.svc.QueryPosts(context.Background(), repository.PostQuery{Search: long})
	if err != nil {
		t.Fatalf("multibyte q must not error: %v", err)
	}
	_ = posts
	_ = total
}

// --- Extend ---

func TestExtendBounty_OncePushesSevenDays(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	p := e.bounty(t, "alice", "extend me", 100, 3)
	before := *p.BountyExpiresAt

	if _, err := e.svc.ExtendBounty(ctx, "bob", p.ID); err != ErrBountyNotAuthor {
		t.Fatalf("other peer: %v", err)
	}
	got, err := e.svc.ExtendBounty(ctx, "alice", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.BountyExtended || !got.BountyExpiresAt.Equal(before.Add(BountyExtension)) {
		t.Fatalf("extended=%v expires=%v want %v", got.BountyExtended, got.BountyExpiresAt, before.Add(BountyExtension))
	}
	if _, err := e.svc.ExtendBounty(ctx, "alice", p.ID); err != ErrBountyAlreadyExtended {
		t.Fatalf("second extend: %v", err)
	}
	if _, err := e.svc.ExtendBounty(ctx, "alice", "missing"); err != models.ErrNotFound {
		t.Fatalf("missing post: %v", err)
	}

	expired := e.bounty(t, "alice", "expired", 100, 1)
	_ = e.forum.ExpireBounty(ctx, expired.ID)
	if _, err := e.svc.ExtendBounty(ctx, "alice", expired.ID); err != ErrBountyNotOpen {
		t.Fatalf("expired bounty: %v", err)
	}
	plain := e.post(t, "alice", "plain", "no bounty", "general")
	if _, err := e.svc.ExtendBounty(ctx, "alice", plain.ID); err != ErrBountyNotOpen {
		t.Fatalf("post without bounty: %v", err)
	}
}

// --- Activity fan-out ---

func TestActivity_ReplyAwardUpvoteFanOut(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)
	p := e.bounty(t, "alice", "help wanted", 250, 10)

	// Own reply: no activity. Bob's reply: reply_on_post for alice.
	if _, err := e.svc.CreateReply(ctx, p.ID, "alice", "bump", false); err != nil {
		t.Fatal(err)
	}
	reply, err := e.svc.CreateReply(ctx, p.ID, "bob", "I can help", false)
	if err != nil {
		t.Fatal(err)
	}
	acts := e.feed(t, "alice", models.ActivityReplyOnPost)
	if len(acts) != 1 || acts[0].ActorPeerID != "bob" || acts[0].ReplyID == nil || *acts[0].ReplyID != reply.ID {
		t.Fatalf("reply_on_post = %+v", acts)
	}

	// Upvotes: bob toggles on, off, on again: one post_upvoted for alice. Alice's own: none.
	for i := 0; i < 3; i++ {
		if _, _, err := e.svc.ToggleUpvote(ctx, "bob", p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.svc.ToggleUpvote(ctx, "alice", p.ID); err != ErrUpvoteOwn {
		t.Fatalf("own upvote must be refused: %v", err)
	}
	if acts := e.feed(t, "alice", models.ActivityPostUpvoted); len(acts) != 1 || acts[0].ActorPeerID != "bob" {
		t.Fatalf("post_upvoted must be deduped per actor: %+v", acts)
	}
	if _, _, err := e.svc.ToggleUpvote(ctx, "bob", "missing"); err != models.ErrNotFound {
		t.Fatalf("upvote on missing post: %v", err)
	}

	// Award: bounty_awarded for bob with the amount, actor alice. Alice escrowed from her free
	// bucket, so the reward lands in bob's free bucket (the paid part of an escrow lands as
	// paid, see TestAwardBounty_FollowsEscrowBuckets); nothing counts as a purchase.
	if err := e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID); err != nil {
		t.Fatalf("award: %v", err)
	}
	if bob := e.balanceOf(t, "acct-bob"); bob.FreeBalance != 250 || bob.PaidBalance != 0 || bob.LifetimePurchased != 0 {
		t.Fatalf("bob after award: free=%d paid=%d lifetime_purchased=%d, want 250, 0 and 0", bob.FreeBalance, bob.PaidBalance, bob.LifetimePurchased)
	}
	acts = e.feed(t, "bob", models.ActivityBountyAwarded)
	if len(acts) != 1 || acts[0].Amount == nil || *acts[0].Amount != 250 || acts[0].ActorPeerID != "alice" || *acts[0].ReplyID != reply.ID {
		t.Fatalf("bounty_awarded = %+v", acts)
	}
	if acts := e.feed(t, "bob", ""); len(acts) != 1 {
		t.Fatalf("bob must not be told about his own reply or upvote: %+v", acts)
	}
}

func TestActivity_ListSinceLimitAndMarkRead(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	p := e.post(t, "alice", "thread", "body", "general")
	for _, who := range []string{"bob", "carol", "dave"} {
		if _, err := e.svc.CreateReply(ctx, p.ID, who, "hi", false); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(time.Minute)
	}
	rows, unread, err := e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 2})
	if err != nil || len(rows) != 2 || unread != 3 {
		t.Fatalf("limit=2: rows=%d unread=%d err=%v", len(rows), unread, err)
	}
	if !rows[0].CreatedAt.After(rows[1].CreatedAt) || rows[0].ActorPeerID != "dave" {
		t.Fatalf("newest first: %+v", rows)
	}
	since := rows[1].CreatedAt
	rows, _, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Since: &since, Limit: 10})
	if len(rows) != 1 || rows[0].ActorPeerID != "dave" {
		t.Fatalf("since is exclusive: %+v", rows)
	}

	all, _, _ := e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10})
	if err := e.svc.MarkActivityRead(ctx, "alice", []string{all[0].ID}, false); err != nil {
		t.Fatal(err)
	}
	_, unread, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10})
	if unread != 2 {
		t.Fatalf("one marked read: unread=%d", unread)
	}
	// Another peer cannot mark alice's rows.
	_ = e.svc.MarkActivityRead(ctx, "bob", []string{all[1].ID}, false)
	if _, unread, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10}); unread != 2 {
		t.Fatalf("bob must not mark alice's rows: unread=%d", unread)
	}
	if err := e.svc.MarkActivityRead(ctx, "alice", nil, true); err != nil {
		t.Fatal(err)
	}
	rows, unread, _ = e.svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10})
	if unread != 0 || rows[0].ReadAt == nil {
		t.Fatalf("all read: unread=%d read_at=%v", unread, rows[0].ReadAt)
	}
}

func TestActivity_DisabledWithoutRepo(t *testing.T) {
	deps := newCreditTestDeps()
	svc := NewForumService(repository.NewMemoryForumRepository(), nil, nil)
	svc.SetClock(deps.clock)
	ctx := context.Background()
	p, err := svc.CreatePost(ctx, "alice", CreatePostInput{Title: "t", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateReply(ctx, p.ID, "bob", "hi", false); err != nil {
		t.Fatal(err)
	}
	rows, unread, err := svc.ListActivity(ctx, "alice", repository.ActivityQuery{Limit: 10})
	if err != nil || len(rows) != 0 || unread != 0 {
		t.Fatalf("no activity repo: rows=%d unread=%d err=%v", len(rows), unread, err)
	}
	if err := svc.MarkActivityRead(ctx, "alice", nil, true); err != nil {
		t.Fatal(err)
	}
}
