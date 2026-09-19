// Package services: credit hardening of the bounty lifecycle (community hardening pass).
// Each test is a proof of concept of an exploit that used to pay out and now fails closed:
// awarding an expired (refunded) bounty, awarding the same bounty concurrently, turning free
// credits into paid ones through a second account, and the expiry refunding a bounty that was
// awarded meanwhile.
package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// TestAwardBounty_ExpiredBountyPaysNobody: the escrow went back to the author on expiry, so
// an award afterwards would mint the bounty a second time.
func TestAwardBounty_ExpiredBountyPaysNobody(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)
	p := e.bounty(t, "alice", "help wanted", 300, 1)
	reply, err := e.svc.CreateReply(ctx, p.ID, "bob", "I can help", false)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(2 * 24 * time.Hour)
	if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	if got := e.balance(t, "acct-alice"); got != 1000 {
		t.Fatalf("alice after refund = %d, want 1000", got)
	}
	if err := e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID); err != ErrBountyNotClaimable {
		t.Fatalf("award of an expired bounty = %v, want ErrBountyNotClaimable", err)
	}
	if got := e.balance(t, "acct-bob"); got != 0 {
		t.Fatalf("bob was paid %d from a refunded escrow", got)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if post.BountyStatus != "expired" || post.BountyClaimedBy != nil {
		t.Fatalf("post after refused award: status=%q claimed_by=%v", post.BountyStatus, post.BountyClaimedBy)
	}
}

// TestAwardBounty_ConcurrentAwardsPayOnce: N awards of one bounty in flight at once credit the
// winner exactly once; every other call is refused.
func TestAwardBounty_ConcurrentAwardsPayOnce(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)
	p := e.bounty(t, "alice", "help wanted", 250, 7)
	reply, err := e.svc.CreateReply(ctx, p.ID, "bob", "I can help", false)
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID)
		}(i)
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch err {
		case nil:
			ok++
		case ErrBountyNotClaimable:
			refused++
		default:
			t.Fatalf("unexpected award error: %v", err)
		}
	}
	if ok != 1 || refused != n-1 {
		t.Fatalf("awards: ok=%d refused=%d, want 1 and %d", ok, refused, n-1)
	}
	if got := e.balance(t, "acct-bob"); got != 250 {
		t.Fatalf("bob after %d concurrent awards = %d, want 250", n, got)
	}
	if acts := e.feed(t, "bob", models.ActivityBountyAwarded); len(acts) != 1 {
		t.Fatalf("bounty_awarded rows = %d, want 1", len(acts))
	}
}

// TestAwardBounty_FollowsEscrowBuckets: the payout is split the way the escrow was taken, so
// a free-funded bounty awarded to the owner's second account stays free (and expiring), and
// only the part that was paid for lands as paid credits.
func TestAwardBounty_FollowsEscrowBuckets(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.accountWith(t, "alice", 100, 400)
	e.account(t, "bob", 0)
	p := e.bounty(t, "alice", "help wanted", 300, 7)
	if post, _ := e.svc.GetPost(ctx, p.ID); post.BountyEscrowPaid != 200 {
		t.Fatalf("escrow paid part = %d, want 200 (100 free first, then 200 paid)", post.BountyEscrowPaid)
	}
	reply, err := e.svc.CreateReply(ctx, p.ID, "bob", "I can help", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID); err != nil {
		t.Fatalf("award: %v", err)
	}
	bob := e.balanceOf(t, "acct-bob")
	if bob.FreeBalance != 100 || bob.PaidBalance != 200 || bob.LifetimePurchased != 0 {
		t.Fatalf("bob after award: free=%d paid=%d lifetime_purchased=%d, want 100, 200 and 0", bob.FreeBalance, bob.PaidBalance, bob.LifetimePurchased)
	}
	want := e.clk.Now().Add(FreeExpiryWindow)
	if bob.FreeCreditsExpiresAt == nil || !bob.FreeCreditsExpiresAt.Equal(want) {
		t.Fatalf("free part expiry = %v, want %v", bob.FreeCreditsExpiresAt, want)
	}
	if !e.hasTx("acct-bob", BountyRewardReason+":"+p.ID+":paid") || !e.hasTx("acct-bob", BountyRewardReason+":"+p.ID+":free") {
		t.Fatal("payout request ids must be per post and bucket")
	}
	// A second award of the same post is refused and the deterministic ids would not pay again anyway.
	if err := e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID); err != ErrBountyNotClaimable {
		t.Fatalf("second award = %v, want ErrBountyNotClaimable", err)
	}
	if got := e.balance(t, "acct-bob"); got != 300 {
		t.Fatalf("bob after second award = %d, want 300", got)
	}
}

// TestExpireBounties_AwardedMeanwhileIsNotRefunded: a bounty awarded after its deadline but
// before the expiry job ran is completed, not expired, and the author gets no refund on top
// of the payout.
func TestExpireBounties_AwardedMeanwhileIsNotRefunded(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)
	p := e.bounty(t, "alice", "help wanted", 250, 1)
	reply, err := e.svc.CreateReply(ctx, p.ID, "bob", "I can help", false)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(2 * 24 * time.Hour)
	if err := e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID); err != nil {
		t.Fatalf("award after the deadline while still open: %v", err)
	}
	if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 0 {
		t.Fatalf("expire after award: n=%d err=%v, want 0 and nil", n, err)
	}
	if got := e.balance(t, "acct-alice"); got != 750 {
		t.Fatalf("alice after award and expiry run = %d, want 750 (no refund)", got)
	}
	if got := e.balance(t, "acct-bob"); got != 250 {
		t.Fatalf("bob = %d, want 250", got)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if post.BountyStatus != "completed" || post.BountyRefundedAt != nil {
		t.Fatalf("post: status=%q refunded_at=%v, want completed and nil", post.BountyStatus, post.BountyRefundedAt)
	}
}

type failingRefunder struct{ calls int }

func (f *failingRefunder) RefundEscrow(context.Context, string, int, int, string, string) error {
	f.calls++
	return errors.New("credits unavailable")
}

// TestExpireBounties_RefundRetriedAfterFailure: the status flips before the refund; when the
// refund fails the post stays expired (no award can land on it) and the next run refunds it.
func TestExpireBounties_RefundRetriedAfterFailure(t *testing.T) {
	e := newBoardEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)
	p := e.bounty(t, "alice", "help wanted", 300, 1)
	reply, err := e.svc.CreateReply(ctx, p.ID, "bob", "I can help", false)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(2 * 24 * time.Hour)
	failing := &failingRefunder{}
	e.svc.SetCreditRefunder(failing)
	if n, err := e.svc.ExpireBounties(ctx); err == nil || n != 0 || failing.calls != 1 {
		t.Fatalf("failed refund run: n=%d err=%v calls=%d", n, err, failing.calls)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if post.BountyStatus != "expired" || post.BountyRefundedAt != nil {
		t.Fatalf("after failed refund: status=%q refunded_at=%v, want expired and nil", post.BountyStatus, post.BountyRefundedAt)
	}
	if err := e.svc.AwardBounty(ctx, "alice", p.ID, reply.ID); err != ErrBountyNotClaimable {
		t.Fatalf("award while the refund is pending = %v, want ErrBountyNotClaimable", err)
	}
	e.svc.SetCreditRefunder(newCreditService(e.credits, "secret"))
	if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 1 {
		t.Fatalf("retry run: n=%d err=%v, want 1 and nil", n, err)
	}
	if got := e.balance(t, "acct-alice"); got != 1000 {
		t.Fatalf("alice after retried refund = %d, want 1000", got)
	}
	post, _ = e.svc.GetPost(ctx, p.ID)
	if post.BountyRefundedAt == nil {
		t.Fatal("refunded_at must be set after the retry")
	}
	if n, err := e.svc.ExpireBounties(ctx); err != nil || n != 0 {
		t.Fatalf("third run must find nothing: n=%d err=%v", n, err)
	}
	if got := e.balance(t, "acct-alice"); got != 1000 {
		t.Fatalf("alice after third run = %d, want 1000 (refund is idempotent)", got)
	}
}
