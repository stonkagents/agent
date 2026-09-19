// Package repository: Postgres integration test for the community board phase 3 statements
// (Autopilot v2): the reply ask column (migration 027), the auto ask cap count, the askers of
// a post, the bounty raise update, the outcome join with category / accepted / hidden, the
// reported target lookup and the autopilot spend sum. Runs only with TRACKER_TEST_DATABASE_URL
// set, like the phase 1 and 2 tests; rows are scoped to a random prefix and deleted at the end.
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestPostgres_BoardPhase3Queries(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "it3-" + uuid.New().String()[:8] + "-"
	author, agent, other := run+"author", run+"agent", run+"other"
	now := time.Now().UTC().Truncate(time.Microsecond)

	peers := NewPostgresPeerRepository(pool)
	for _, p := range []*models.Peer{
		{PeerID: author, PublicKey: "pk-" + author, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: agent, PublicKey: "pk-" + agent, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: other, PublicKey: "pk-" + other, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
	} {
		if err := peers.Create(ctx, p); err != nil {
			t.Fatalf("create peer %s: %v", p.PeerID, err)
		}
	}
	t.Cleanup(func() {
		// forum_posts and forum_replies cascade from peers; accounts and credit rows do not.
		for _, q := range []string{
			`DELETE FROM credit_transactions WHERE account_id IN (SELECT id FROM accounts WHERE peer_id LIKE $1)`,
			`DELETE FROM credit_balances WHERE account_id IN (SELECT id FROM accounts WHERE peer_id LIKE $1)`,
			`DELETE FROM accounts WHERE peer_id LIKE $1`,
			`DELETE FROM board_reports WHERE reporter_peer_id LIKE $1`,
			`DELETE FROM board_activity WHERE peer_id LIKE $1`,
			`DELETE FROM peers WHERE peer_id LIKE $1`,
		} {
			if _, err := pool.Exec(ctx, q, run+"%"); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	forum := NewPostgresForumRepository(pool)
	request := &models.ForumPost{AuthorPeerID: author, Title: run + " request", Description: "r", Category: "request", Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	bountyAmt := 100
	exp := now.Add(7 * 24 * time.Hour)
	escrowID := run + "escrow"
	bounty := &models.ForumPost{AuthorPeerID: author, Title: run + " bounty", Description: "b", Category: "bounty", Tags: []string{},
		CreatedAt: now, UpdatedAt: now, BountyAmount: &bountyAmt, BountyStatus: "open", BountyExpiresAt: &exp, BountyEscrowRequestID: &escrowID, BountyEscrowPaid: 30}
	general := &models.ForumPost{AuthorPeerID: author, Title: run + " general", Description: "g", Category: "general", Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	for _, p := range []*models.ForumPost{request, bounty, general} {
		if err := forum.CreatePost(ctx, p); err != nil {
			t.Fatalf("create post %s: %v", p.Title, err)
		}
	}

	// Ask column: stored, read back, counted apart from the auto answer, askers distinct.
	relevance := 0.62
	signals := &models.RelevanceSignals{Library: 0.5, History: 1, Instruction: 0, Routed: 1}
	ask := &models.ForumReply{PostID: request.ID, AuthorPeerID: agent, Body: "ask", CreatedAt: now.Add(time.Minute), Auto: true, Ask: 60, Relevance: &relevance, RelevanceSignals: signals}
	answer := &models.ForumReply{PostID: request.ID, AuthorPeerID: agent, Body: "answer", CreatedAt: now.Add(2 * time.Minute), Auto: true}
	otherAsk := &models.ForumReply{PostID: request.ID, AuthorPeerID: other, Body: "manual ask", CreatedAt: now.Add(3 * time.Minute), Ask: 80}
	secondAsk := &models.ForumReply{PostID: request.ID, AuthorPeerID: other, Body: "manual ask again", CreatedAt: now.Add(4 * time.Minute), Ask: 90}
	plain := &models.ForumReply{PostID: request.ID, AuthorPeerID: author, Body: "plain", CreatedAt: now.Add(5 * time.Minute)}
	for _, rp := range []*models.ForumReply{ask, answer, otherAsk, secondAsk, plain} {
		if err := forum.CreateReply(ctx, rp); err != nil {
			t.Fatalf("create reply %q: %v", rp.Body, err)
		}
	}
	if got, err := forum.GetReplyByID(ctx, ask.ID); err != nil || got.Ask != 60 || !got.Auto || got.Relevance == nil || *got.Relevance != 0.62 ||
		got.RelevanceSignals == nil || *got.RelevanceSignals != *signals {
		t.Errorf("ask reply = %+v err=%v", got, err)
	}
	if got, _ := forum.GetReplyByID(ctx, plain.ID); got.Ask != 0 || got.Relevance != nil || got.RelevanceSignals != nil {
		t.Errorf("plain reply = %+v", got)
	}
	if list, _, err := forum.ListRepliesFiltered(ctx, request.ID, ReplyQuery{Limit: 10, ShowHidden: true}); err != nil || len(list) != 5 || list[0].Ask != 60 || list[2].Ask != 80 {
		t.Errorf("thread asks = %+v err=%v", list, err)
	}
	if n, err := forum.CountAutoRepliesByPeerOnPost(ctx, agent, request.ID); err != nil || n != 2 {
		t.Errorf("auto replies on post = %d err=%v, want 2", n, err)
	}
	if n, err := forum.CountAutoAsksByPeerOnPost(ctx, agent, request.ID); err != nil || n != 1 {
		t.Errorf("auto asks on post = %d err=%v, want 1", n, err)
	}
	if n, _ := forum.CountAutoAsksByPeerOnPost(ctx, other, request.ID); n != 0 {
		t.Errorf("manual asks counted as auto: %d", n)
	}
	if askers, err := forum.AskerPeerIDs(ctx, request.ID); err != nil || len(askers) != 2 || askers[0] != agent || askers[1] != other {
		t.Errorf("askers = %v err=%v", askers, err)
	}
	if askers, err := forum.AskerPeerIDs(ctx, general.ID); err != nil || len(askers) != 0 {
		t.Errorf("askers of a post without asks = %v err=%v", askers, err)
	}

	// Raise: one conditional update that returns the previous amount. An existing open bounty
	// keeps its escrow id and deadline; a post without a bounty takes the new ones; an amount
	// not above the current one (a double submit) and a completed bounty are refused; the
	// escrow split lands separately and a failed escrow reverts the claim.
	raiseID := run + "raise"
	newExp := now.Add(14 * 24 * time.Hour)
	prev, err := forum.RaiseBounty(ctx, bounty.ID, 250, raiseID, newExp)
	if err != nil || prev != 100 {
		t.Fatalf("raise existing: prev=%d err=%v", prev, err)
	}
	if err := forum.SetBountyEscrowPaid(ctx, bounty.ID, 130); err != nil {
		t.Fatalf("set escrow paid: %v", err)
	}
	got, err := forum.GetPostByID(ctx, bounty.ID)
	if err != nil || *got.BountyAmount != 250 || got.BountyEscrowPaid != 130 || got.BountyStatus != "open" ||
		*got.BountyEscrowRequestID != escrowID || !got.BountyExpiresAt.Equal(exp) || *got.BountyCurrency != "credits" {
		t.Errorf("raised bounty = %+v err=%v", got, err)
	}
	if _, err := forum.RaiseBounty(ctx, bounty.ID, 250, raiseID, newExp); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("raise to the current amount (double submit): %v, want ErrInvalidInput", err)
	}
	if got, _ = forum.GetPostByID(ctx, bounty.ID); *got.BountyAmount != 250 || got.BountyEscrowPaid != 130 {
		t.Errorf("refused raise changed the row: %+v", got)
	}
	if err := forum.RevertBountyRaise(ctx, bounty.ID, 100); err != nil {
		t.Fatalf("revert existing: %v", err)
	}
	if got, _ = forum.GetPostByID(ctx, bounty.ID); *got.BountyAmount != 100 || got.BountyStatus != "open" || *got.BountyEscrowRequestID != escrowID {
		t.Errorf("reverted bounty = %+v", got)
	}
	if prev, err := forum.RaiseBounty(ctx, bounty.ID, 250, raiseID, newExp); err != nil || prev != 100 {
		t.Fatalf("raise again after the revert: prev=%d err=%v", prev, err)
	}
	prev, err = forum.RaiseBounty(ctx, request.ID, 60, raiseID, newExp)
	if err != nil || prev != 0 {
		t.Fatalf("raise new: prev=%d err=%v", prev, err)
	}
	got, _ = forum.GetPostByID(ctx, request.ID)
	if !got.HasBounty() || *got.BountyAmount != 60 || got.BountyStatus != "open" || got.BountyEscrowRequestID == nil || *got.BountyEscrowRequestID != raiseID ||
		got.BountyExpiresAt == nil || !got.BountyExpiresAt.Equal(newExp) || got.BountyCurrency == nil || *got.BountyCurrency != "credits" {
		t.Errorf("new bounty = %+v", got)
	}
	if err := forum.RevertBountyRaise(ctx, request.ID, 0); err != nil {
		t.Fatalf("revert new: %v", err)
	}
	if got, _ = forum.GetPostByID(ctx, request.ID); got.HasBounty() || got.BountyExpiresAt != nil || got.BountyEscrowRequestID != nil || got.BountyCurrency != nil || got.BountyEscrowPaid != 0 {
		t.Errorf("phantom bounty after the revert: %+v", got)
	}
	if _, err := forum.RaiseBounty(ctx, request.ID, 60, raiseID, newExp); err != nil {
		t.Fatalf("raise new again: %v", err)
	}
	if err := forum.AwardBounty(ctx, bounty.ID, agent); err != nil {
		t.Fatalf("award: %v", err)
	}
	if _, err := forum.RaiseBounty(ctx, bounty.ID, 300, raiseID, newExp); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("raise on a completed bounty: %v, want ErrInvalidInput", err)
	}

	// Outcomes: category, accepted, awarded, hidden from the join; reported from board_reports.
	wonReply := &models.ForumReply{PostID: bounty.ID, AuthorPeerID: agent, Body: "won", CreatedAt: now.Add(6 * time.Minute), Auto: true}
	hiddenReply := &models.ForumReply{PostID: general.ID, AuthorPeerID: agent, Body: "hidden", CreatedAt: now.Add(7 * time.Minute), Auto: true}
	for _, rp := range []*models.ForumReply{wonReply, hiddenReply} {
		if err := forum.CreateReply(ctx, rp); err != nil {
			t.Fatalf("create reply %q: %v", rp.Body, err)
		}
	}
	if err := forum.SetAcceptedReply(ctx, request.ID, &answer.ID); err != nil {
		t.Fatal(err)
	}
	if err := forum.SetReplyHidden(ctx, hiddenReply.ID, true); err != nil {
		t.Fatal(err)
	}
	outcomes, err := forum.ListAutoReplyOutcomes(ctx, agent, now)
	if err != nil {
		t.Fatalf("outcomes: %v", err)
	}
	if len(outcomes) != 4 || outcomes[0].ReplyID != hiddenReply.ID {
		t.Fatalf("outcomes = %d rows, first %+v", len(outcomes), outcomes[0])
	}
	byReply := map[string]*models.AutoReplyOutcome{}
	for _, o := range outcomes {
		byReply[o.ReplyID] = o
	}
	if o := byReply[answer.ID]; !o.Accepted || o.Awarded || o.Category != "request" || o.BountyAmount == nil || *o.BountyAmount != 60 || o.Hidden {
		t.Errorf("accepted outcome = %+v", o)
	}
	if o := byReply[ask.ID]; o.Accepted || o.Awarded || o.Hidden || o.Relevance == nil || *o.Relevance != 0.62 || o.RelevanceSignals == nil || o.RelevanceSignals.Routed != 1 {
		t.Errorf("ask outcome = %+v", o)
	}
	if o := byReply[answer.ID]; o.Relevance != nil || o.RelevanceSignals != nil {
		t.Errorf("answer outcome carries relevance it never had: %+v", o)
	}
	if o := byReply[wonReply.ID]; !o.Awarded || o.Accepted || o.Category != "bounty" || o.AwardedAmount() != 250 {
		t.Errorf("won outcome = %+v", o)
	}
	if o := byReply[hiddenReply.ID]; !o.Hidden || o.Category != "general" || o.BountyAmount != nil {
		t.Errorf("hidden outcome = %+v", o)
	}
	if later, _ := forum.ListAutoReplyOutcomes(ctx, agent, now.Add(6*time.Minute)); len(later) != 2 {
		t.Errorf("since narrows to %d rows, want 2", len(later))
	}

	reports := NewPostgresBoardReportRepository(pool)
	open := &models.BoardReport{TargetType: models.ReportTargetReply, TargetID: wonReply.ID, TargetAuthorPeerID: agent, ReporterPeerID: other, Reason: "spam", CreatedAt: now}
	dismissed := &models.BoardReport{TargetType: models.ReportTargetReply, TargetID: hiddenReply.ID, TargetAuthorPeerID: agent, ReporterPeerID: other, Reason: "spam", CreatedAt: now}
	postReport := &models.BoardReport{TargetType: models.ReportTargetPost, TargetID: answer.ID, TargetAuthorPeerID: agent, ReporterPeerID: other, Reason: "spam", CreatedAt: now}
	for _, rep := range []*models.BoardReport{open, dismissed, postReport} {
		if err := reports.Create(ctx, rep); err != nil {
			t.Fatalf("create report: %v", err)
		}
	}
	if err := reports.SetStatus(ctx, dismissed.ID, models.ReportStatusDismissed, now); err != nil {
		t.Fatal(err)
	}
	reported, err := reports.ReportedTargetIDs(ctx, models.ReportTargetReply, []string{wonReply.ID, hiddenReply.ID, answer.ID, "nope"})
	if err != nil || len(reported) != 1 || !reported[wonReply.ID] {
		t.Errorf("ReportedTargetIDs = %v err=%v", reported, err)
	}
	if err := reports.SetStatus(ctx, open.ID, models.ReportStatusUpheld, now); err != nil {
		t.Fatal(err)
	}
	if reported, _ = reports.ReportedTargetIDs(ctx, models.ReportTargetReply, []string{wonReply.ID}); !reported[wonReply.ID] {
		t.Errorf("upheld report not counted: %v", reported)
	}
	if reported, err = reports.ReportedTargetIDs(ctx, models.ReportTargetReply, nil); err != nil || len(reported) != 0 {
		t.Errorf("empty ids = %v err=%v", reported, err)
	}

	// Autopilot spend sum: only debits with the autopilot reasons inside the window.
	accounts := NewPostgresAccountRepository(pool)
	credits := NewPostgresCreditRepository(pool)
	acct := &models.Account{ID: uuid.New().String(), PeerID: agent, Status: models.AccountStatusActive, CreatedAt: now}
	if err := accounts.Create(ctx, acct); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := credits.CreateBalance(ctx, &models.CreditBalance{AccountID: acct.ID, UpdatedAt: now}); err != nil {
		t.Fatalf("create balance: %v", err)
	}
	if err := credits.CreditPaid(ctx, acct.ID, 100, "test", run+"seed"); err != nil {
		t.Fatalf("credit: %v", err)
	}
	for i, reason := range []string{"agent_completion_auto", "agent_completion_detailed_auto", "agent_completion"} {
		if err := credits.Spend(ctx, acct.ID, 10, reason, run+"spend-"+string(rune('a'+i))); err != nil {
			t.Fatalf("spend %s: %v", reason, err)
		}
	}
	if spent, err := credits.SumSpentByReasonsSince(ctx, acct.ID, []string{"agent_completion_auto", "agent_completion_detailed_auto"}, now.Add(-time.Hour)); err != nil || spent != 20 {
		t.Errorf("autopilot spend = %d err=%v, want 20", spent, err)
	}
	if spent, _ := credits.SumSpentByReasonsSince(ctx, acct.ID, []string{"agent_completion_auto"}, now.Add(24*time.Hour)); spent != 0 {
		t.Errorf("spend outside the window = %d", spent)
	}
	if spent, err := credits.SumSpentByReasonsSince(ctx, acct.ID, nil, now); err != nil || spent != 0 {
		t.Errorf("no reasons = %d err=%v", spent, err)
	}
}
