// Package services: community board phase 3 (Autopilot v2) tests on the forum service:
// reply asks (where they are allowed, the bounty_ask activity, the auto ask and answer caps),
// bounty raises (escrow of the difference, new bounty with the default deadline, refusals,
// the bounty_raised fan-out to askers) and the autopilot outcomes with since= and the summary.
package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// p3Env is boardEnv with the report repository wired (outcomes need reported flags).
type p3Env struct {
	*boardEnv
	reports *repository.MemoryBoardReportRepository
}

func newP3Env(t *testing.T) *p3Env {
	t.Helper()
	e := newBoardEnv(t)
	reports := repository.NewMemoryBoardReportRepository()
	e.svc.SetReportRepo(reports)
	return &p3Env{boardEnv: e, reports: reports}
}

func (e *p3Env) ask(t *testing.T, postID, peer string, ask int, auto bool) *models.ForumReply {
	t.Helper()
	rp, err := e.svc.CreateReplyWithAsk(context.Background(), postID, peer, "I can do this", auto, ask)
	if err != nil {
		t.Fatalf("reply with ask %d on %s: %v", ask, postID, err)
	}
	return rp
}

func TestP3_ReplyAsk_RulesAndActivity(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	request := e.post(t, "alice", "need a dataset", "who has it", "request")
	general := e.post(t, "alice", "chat", "just talking", "general")

	rp := e.ask(t, request.ID, "bob", 60, false)
	if rp.Ask != 60 || !rp.HasAsk() {
		t.Fatalf("ask not stored: %+v", rp)
	}
	got, _ := e.forum.GetReplyByID(ctx, rp.ID)
	if got.Ask != 60 {
		t.Errorf("stored ask = %d", got.Ask)
	}
	asks := e.feed(t, "alice", models.ActivityBountyAsk)
	if len(asks) != 1 || asks[0].Amount == nil || *asks[0].Amount != 60 || asks[0].ReplyID == nil || *asks[0].ReplyID != rp.ID || asks[0].ActorPeerID != "bob" {
		t.Errorf("bounty_ask activity = %+v", asks)
	}
	// A reply without an ask emits none; ask 0 is a plain reply.
	plain, _ := e.svc.CreateReply(ctx, request.ID, "carol", "plain", false)
	if plain.Ask != 0 || len(e.feed(t, "alice", models.ActivityBountyAsk)) != 1 {
		t.Errorf("plain reply changed asks: %+v", plain)
	}

	// Not on a general post without a bounty; out of range asks are invalid everywhere.
	for _, c := range []struct {
		name string
		post string
		ask  int
	}{
		{"general post", general.ID, 10},
		{"negative", request.ID, -1},
		{"above max", request.ID, MaxBountyAmount + 1},
	} {
		if _, err := e.svc.CreateReplyWithAsk(ctx, c.post, "bob", "x", false, c.ask); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", c.name, err)
		}
	}
	// A general post that carries a bounty takes asks (it is a bounty post in all but category).
	amount, days := 100, 7
	e.account(t, "dave", 500)
	withBounty, err := e.svc.CreatePost(ctx, "dave", CreatePostInput{Title: "g", Description: "with bounty", Category: "general", BountyAmount: &amount, BountyDays: &days})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateReplyWithAsk(ctx, withBounty.ID, "bob", "x", false, MaxBountyAmount); err != nil {
		t.Errorf("ask on a general post with a bounty: %v", err)
	}
	// An ask on a token offer post is refused only for auto replies (token offers never take
	// autopilot); a manual ask on a bounty post is fine.
	if _, err := e.svc.CreateReplyWithAsk(ctx, request.ID, "alice", "own", true, 10); !errors.Is(err, ErrAutoReplyNotAllowed) {
		t.Errorf("auto ask on own post: %v", err)
	}
}

func TestP3_AutoAskThenAutoAnswer_CountApart(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	request := e.post(t, "alice", "need help", "please", "request")

	// One auto ask, then one auto answer on the same post: both allowed; a second of either is not.
	e.ask(t, request.ID, "agent", 50, true)
	if _, err := e.svc.CreateReplyWithAsk(ctx, request.ID, "agent", "again", true, 70); !errors.Is(err, ErrAutoReplyLimit) {
		t.Errorf("second auto ask: %v, want ErrAutoReplyLimit", err)
	}
	if _, err := e.svc.CreateReply(ctx, request.ID, "agent", "full answer", true); err != nil {
		t.Fatalf("auto answer after the ask: %v", err)
	}
	if _, err := e.svc.CreateReply(ctx, request.ID, "agent", "another answer", true); !errors.Is(err, ErrAutoReplyLimit) {
		t.Errorf("second auto answer: %v, want ErrAutoReplyLimit", err)
	}
	// Both count toward the daily cap.
	e.svc.AutopilotMaxRepliesPerDay = 2
	other := e.post(t, "alice", "other", "other", "request")
	if _, err := e.svc.CreateReply(ctx, other.ID, "agent", "capped", true); !errors.Is(err, ErrAutoReplyLimit) {
		t.Errorf("daily cap after ask and answer: %v, want ErrAutoReplyLimit", err)
	}
	// Manual asks are never capped.
	e.ask(t, other.ID, "agent", 10, false)
	e.ask(t, other.ID, "agent", 20, false)
}

func TestP3_RaiseBounty_EscrowsDifferenceAndNotifiesAskers(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	acct := e.accountWith(t, "alice", 100, 400)
	post := e.bounty(t, "alice", "bounty", 100, 7) // escrow: 100 free
	if e.balance(t, acct) != 400 || post.BountyEscrowPaid != 0 {
		t.Fatalf("setup: balance=%d paid=%d", e.balance(t, acct), post.BountyEscrowPaid)
	}
	e.ask(t, post.ID, "bob", 250, false)
	e.ask(t, post.ID, "carol", 300, true)
	e.ask(t, post.ID, "bob", 260, false) // two asks by bob: one activity
	_, _ = e.svc.CreateReply(ctx, post.ID, "dave", "no ask", false)
	firstEscrow := *post.BountyEscrowRequestID

	raised, err := e.svc.RaiseBounty(ctx, "alice", post.ID, 250)
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	if raised.BountyAmount == nil || *raised.BountyAmount != 250 || raised.BountyStatus != "open" {
		t.Errorf("raised post = %+v", raised)
	}
	// The difference (150) came from the paid bucket (free was empty); the escrow id is the first one.
	if e.balance(t, acct) != 250 || raised.BountyEscrowPaid != 150 || raised.BountyEscrowRequestID == nil || *raised.BountyEscrowRequestID != firstEscrow {
		t.Errorf("escrow after raise: balance=%d paid=%d id=%v (first %s)", e.balance(t, acct), raised.BountyEscrowPaid, raised.BountyEscrowRequestID, firstEscrow)
	}
	if !raised.BountyExpiresAt.Equal(*post.BountyExpiresAt) {
		t.Errorf("deadline moved on a raise: %v -> %v", post.BountyExpiresAt, raised.BountyExpiresAt)
	}
	for _, peer := range []string{"bob", "carol"} {
		rows := e.feed(t, peer, models.ActivityBountyRaised)
		if len(rows) != 1 || rows[0].Amount == nil || *rows[0].Amount != 250 || rows[0].ActorPeerID != "" || rows[0].PostID != post.ID {
			t.Errorf("%s bounty_raised = %+v", peer, rows)
		}
	}
	if len(e.feed(t, "dave", models.ActivityBountyRaised)) != 0 || len(e.feed(t, "alice", models.ActivityBountyRaised)) != 0 {
		t.Errorf("bounty_raised reached a peer without an ask")
	}
	// The author's own ask (a manual reply on their post) never notifies the author.
	e.ask(t, post.ID, "alice", 5, false)
	if _, err := e.svc.RaiseBounty(ctx, "alice", post.ID, 260); err != nil {
		t.Fatal(err)
	}
	if len(e.feed(t, "alice", models.ActivityBountyRaised)) != 0 {
		t.Errorf("author got bounty_raised for their own ask")
	}

	// Expiry refunds the whole 260 in one go: 100 free and 160 paid, keyed on the first escrow.
	e.clk.Advance(8 * 24 * time.Hour)
	if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	bal := e.balanceOf(t, acct)
	if bal.FreeBalance != 100 || bal.PaidBalance != 400 {
		t.Errorf("after refund: free=%d paid=%d, want 100/400", bal.FreeBalance, bal.PaidBalance)
	}
	if !e.hasTx(acct, "refund:"+firstEscrow+":free") || !e.hasTx(acct, "refund:"+firstEscrow+":paid") {
		t.Errorf("refund transactions missing for %s", firstEscrow)
	}
}

func TestP3_RaiseBounty_CreatesBountyWithDefaultDeadline(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	acct := e.account(t, "alice", 300)
	request := e.post(t, "alice", "need it", "please", "request")
	e.ask(t, request.ID, "bob", 80, true)

	raised, err := e.svc.RaiseBounty(ctx, "alice", request.ID, 80)
	if err != nil {
		t.Fatalf("raise on a post without a bounty: %v", err)
	}
	if !raised.HasBounty() || *raised.BountyAmount != 80 || raised.BountyStatus != "open" || raised.BountyCurrency == nil || *raised.BountyCurrency != "credits" {
		t.Errorf("new bounty = %+v", raised)
	}
	want := e.clk.Now().Add(DefaultBountyDays * 24 * time.Hour)
	if raised.BountyExpiresAt == nil || !raised.BountyExpiresAt.Equal(want) {
		t.Errorf("deadline = %v, want %v", raised.BountyExpiresAt, want)
	}
	if raised.BountyEscrowRequestID == nil || *raised.BountyEscrowRequestID == "" || e.balance(t, acct) != 220 {
		t.Errorf("escrow: id=%v balance=%d", raised.BountyEscrowRequestID, e.balance(t, acct))
	}
	if rows := e.feed(t, "bob", models.ActivityBountyRaised); len(rows) != 1 || *rows[0].Amount != 80 {
		t.Errorf("bob bounty_raised = %+v", rows)
	}
	// It is now an open bounty like any other: the bounties feed lists it.
	posts, _, _ := e.svc.QueryPosts(ctx, repository.PostQuery{Limit: 10, Sort: repository.PostSortBounties})
	if len(posts) != 1 || posts[0].ID != request.ID {
		t.Errorf("bounties feed = %v", ids(posts))
	}
}

func TestP3_RaiseBounty_Refusals(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	acct := e.account(t, "alice", 150)
	post := e.bounty(t, "alice", "bounty", 100, 7)

	cases := []struct {
		name   string
		peer   string
		amount int
		want   error
	}{
		{"not author", "bob", 200, ErrBountyNotAuthor},
		{"equal to current", "alice", 100, models.ErrInvalidInput},
		{"below current", "alice", 50, models.ErrInvalidInput},
		{"above max", "alice", MaxBountyAmount + 1, models.ErrInvalidInput},
		{"insufficient credits", "alice", 200, ErrInsufficientCredits},
	}
	for _, c := range cases {
		if _, err := e.svc.RaiseBounty(ctx, c.peer, post.ID, c.amount); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if e.balance(t, acct) != 50 {
		t.Errorf("a refused raise moved credits: balance=%d", e.balance(t, acct))
	}
	if _, err := e.svc.RaiseBounty(ctx, "alice", "missing", 200); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("missing post: %v", err)
	}
	// Completed and expired bounties are not open.
	e.account(t, "bob", 0)
	rp, _ := e.svc.CreateReply(ctx, post.ID, "bob", "answer", false)
	if err := e.svc.AwardBounty(ctx, "alice", post.ID, rp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RaiseBounty(ctx, "alice", post.ID, 120); !errors.Is(err, ErrBountyNotOpen) {
		t.Errorf("completed bounty: %v, want ErrBountyNotOpen", err)
	}
	expired := e.bounty(t, "alice", "expired", 40, 1)
	e.clk.Advance(2 * 24 * time.Hour)
	if _, err := e.svc.ExpireBounties(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RaiseBounty(ctx, "alice", expired.ID, 60); !errors.Is(err, ErrBountyNotOpen) {
		t.Errorf("expired bounty: %v, want ErrBountyNotOpen", err)
	}
}

func TestP3_AutopilotOutcomes_SinceAndFlagsAndSummary(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	agentAcct := e.account(t, "agent", 100)
	now := e.clk.Now()

	accepted := e.post(t, "alice", "q1", "question", "request")
	won := e.bounty(t, "alice", "b1", 120, 7)
	hidden := e.post(t, "alice", "q2", "question", "general")
	reported := e.post(t, "alice", "q3", "question", "request")
	oldPost := e.post(t, "alice", "q4", "question", "request")

	first, _ := e.svc.CreateReply(ctx, accepted.ID, "agent", "a", true)
	if _, err := e.svc.AcceptReply(ctx, "alice", accepted.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Hour)
	second, _ := e.svc.CreateReply(ctx, won.ID, "agent", "b", true)
	if err := e.svc.AwardBounty(ctx, "alice", won.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Hour)
	third, _ := e.svc.CreateReply(ctx, hidden.ID, "agent", "c", true)
	if err := e.forum.SetReplyHidden(ctx, third.ID, true); err != nil {
		t.Fatal(err)
	}
	fourth, _ := e.svc.CreateReply(ctx, reported.ID, "agent", "d", true)
	if _, _, err := e.svc.Report(ctx, "carol", models.ReportTargetReply, fourth.ID, "spam", ""); err != nil {
		t.Fatal(err)
	}
	// A dismissed report does not count.
	fifth, _ := e.svc.CreateReply(ctx, oldPost.ID, "agent", "e", true)
	rep, _, _ := e.svc.Report(ctx, "carol", models.ReportTargetReply, fifth.ID, "spam", "")
	_ = e.reports.SetStatus(ctx, rep.ID, models.ReportStatusDismissed, e.clk.Now())
	_, _, _ = e.svc.ToggleUpvote(ctx, "bob", reported.ID)
	// Autopilot drafts spend under the autopilot reasons; the owner's own chat does not count.
	credits := newCreditService(e.credits, "secret")
	if _, err := credits.SpendForModelAs(ctx, agentAcct, "gpt-5-mini", 10, "draft-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := credits.SpendForModelAs(ctx, agentAcct, "gpt-5-mini", 10, "draft-2", true); err != nil {
		t.Fatal(err)
	}
	if _, err := credits.SpendForModel(ctx, agentAcct, "gpt-5-mini", 10, "chat-1"); err != nil {
		t.Fatal(err)
	}

	all, err := e.svc.ListAutopilotOutcomes(ctx, "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("outcomes = %d, want 5", len(all))
	}
	byReply := map[string]*models.AutoReplyOutcome{}
	for _, o := range all {
		byReply[o.ReplyID] = o
	}
	if o := byReply[first.ID]; !o.Accepted || o.Awarded || o.Category != "request" || o.Hidden || o.Reported {
		t.Errorf("accepted outcome = %+v", o)
	}
	if o := byReply[second.ID]; !o.Awarded || o.AwardedAmount() != 120 || o.Category != "bounty" || o.Accepted {
		t.Errorf("awarded outcome = %+v", o)
	}
	if o := byReply[third.ID]; !o.Hidden || o.Reported {
		t.Errorf("hidden outcome = %+v", o)
	}
	if o := byReply[fourth.ID]; !o.Reported || o.Hidden || o.Upvotes != 1 {
		t.Errorf("reported outcome = %+v", o)
	}
	if o := byReply[fifth.ID]; o.Reported {
		t.Errorf("dismissed report still counts: %+v", o)
	}
	// since narrows the window to the rows at or after it.
	since := now.Add(2 * time.Hour)
	later, err := e.svc.ListAutopilotOutcomes(ctx, "agent", &since)
	if err != nil || len(later) != 3 {
		t.Errorf("since outcomes = %d err=%v, want 3", len(later), err)
	}
	// since never widens the window past 30 days.
	far := now.Add(-60 * 24 * time.Hour)
	if err := e.forum.CreateReply(ctx, &models.ForumReply{PostID: oldPost.ID, AuthorPeerID: "agent", Body: "old", CreatedAt: now.Add(-40 * 24 * time.Hour), Auto: true}); err != nil {
		t.Fatal(err)
	}
	if wide, _ := e.svc.ListAutopilotOutcomes(ctx, "agent", &far); len(wide) != 5 {
		t.Errorf("since before the window returned %d rows, want 5", len(wide))
	}

	sum, err := e.svc.AutopilotOutcomeSummary(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Posted != 5 || sum.Upvoted != 1 || sum.Accepted != 1 || sum.Awarded != 1 || sum.Hidden != 1 || sum.Reported != 1 || sum.CreditsWon != 120 || sum.CreditsSpent != 20 {
		t.Errorf("summary = %+v", sum)
	}
	if c := sum.ByCategory["request"]; c == nil || c.Posted != 3 || c.Won != 1 {
		t.Errorf("request category = %+v", c)
	}
	if c := sum.ByCategory["bounty"]; c == nil || c.Posted != 1 || c.Won != 1 {
		t.Errorf("bounty category = %+v", c)
	}
	if c := sum.ByCategory["general"]; c == nil || c.Posted != 1 || c.Won != 0 {
		t.Errorf("general category = %+v", c)
	}
	// A peer with nothing: zero summary with an empty (not null) by_category.
	empty, err := e.svc.AutopilotOutcomeSummary(ctx, "nobody")
	if err != nil || empty.Posted != 0 || empty.ByCategory == nil || len(empty.ByCategory) != 0 {
		t.Errorf("empty summary = %+v err=%v", empty, err)
	}
}

func TestP3_RaiseBounty_DoubleSubmitEscrowsOnce(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	acct := e.account(t, "alice", 1000)
	post := e.bounty(t, "alice", "bounty", 100, 7)
	e.ask(t, post.ID, "bob", 250, false)

	// Two racers raise to the same amount at once: one claims, the other finds nothing to update.
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = e.svc.RaiseBounty(ctx, "alice", post.ID, 250)
		}(i)
	}
	close(start)
	wg.Wait()
	okCount, invalid := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			okCount++
		case errors.Is(err, models.ErrInvalidInput):
			invalid++
		default:
			t.Errorf("unexpected racer error: %v", err)
		}
	}
	if okCount != 1 || invalid != 1 {
		t.Fatalf("racers: ok=%d invalid=%d, want 1/1 (%v)", okCount, invalid, errs)
	}
	got, _ := e.forum.GetPostByID(ctx, post.ID)
	if *got.BountyAmount != 250 || e.balance(t, acct) != 750 {
		t.Errorf("after the race: amount=%d balance=%d, want 250 / 750 (escrowed once)", *got.BountyAmount, e.balance(t, acct))
	}
	if rows := e.feed(t, "bob", models.ActivityBountyRaised); len(rows) != 1 {
		t.Errorf("bounty_raised rows = %d, want 1", len(rows))
	}
	// A later raise to the same amount is a plain 400 (not above current), still no spend.
	if _, err := e.svc.RaiseBounty(ctx, "alice", post.ID, 250); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("repeat raise: %v", err)
	}
	if e.balance(t, acct) != 750 {
		t.Errorf("repeat raise moved credits: %d", e.balance(t, acct))
	}
}

func TestP3_RaiseBounty_FailedEscrowRevertsTheClaim(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	acct := e.account(t, "alice", 120)
	post := e.bounty(t, "alice", "bounty", 100, 7) // 20 left
	if _, err := e.svc.RaiseBounty(ctx, "alice", post.ID, 200); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("raise beyond the balance: %v", err)
	}
	got, _ := e.forum.GetPostByID(ctx, post.ID)
	if *got.BountyAmount != 100 || got.BountyStatus != "open" || e.balance(t, acct) != 20 {
		t.Errorf("claim not reverted: amount=%d status=%s balance=%d", *got.BountyAmount, got.BountyStatus, e.balance(t, acct))
	}
	// On a post without a bounty the failed raise leaves no bounty behind.
	request := e.post(t, "alice", "need it", "please", "request")
	if _, err := e.svc.RaiseBounty(ctx, "alice", request.ID, 50); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("raise on a request beyond the balance: %v", err)
	}
	got, _ = e.forum.GetPostByID(ctx, request.ID)
	if got.HasBounty() || got.BountyStatus != "" || got.BountyExpiresAt != nil || got.BountyEscrowRequestID != nil || got.BountyCurrency != nil {
		t.Errorf("phantom bounty after a failed raise: %+v", got)
	}
	if _, err := e.svc.RaiseBounty(ctx, "alice", request.ID, 20); err != nil {
		t.Errorf("raise within the balance after the revert: %v", err)
	}
}

func TestP3_AskReply_OneBellRowForTheAuthor(t *testing.T) {
	e := newP3Env(t)
	ctx := context.Background()
	request := e.post(t, "alice", "need it", "please", "request")
	e.ask(t, request.ID, "bob", 60, false)
	if _, err := e.svc.CreateReply(ctx, request.ID, "carol", "plain", false); err != nil {
		t.Fatal(err)
	}
	asks, replies := e.feed(t, "alice", models.ActivityBountyAsk), e.feed(t, "alice", models.ActivityReplyOnPost)
	if len(asks) != 1 || asks[0].ActorPeerID != "bob" {
		t.Errorf("bounty_ask rows = %+v", asks)
	}
	if len(replies) != 1 || replies[0].ActorPeerID != "carol" {
		t.Errorf("reply_on_post rows = %+v, want only carol's plain reply (B-9)", replies)
	}
	if len(e.feed(t, "alice", "")) != 2 {
		t.Errorf("author feed = %d rows, want 2", len(e.feed(t, "alice", "")))
	}
}
