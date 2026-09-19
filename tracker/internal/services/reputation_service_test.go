// Package services: Board reputation tests: the formula and tiers, one-peer recompute from
// the forum tables (bounties, accepted answers, upvotes, first replies, upheld reports), the
// full recompute, and the batched tier lookup.
package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

func TestComputeReputationScore_FormulaAndFloor(t *testing.T) {
	cases := []struct {
		name string
		in   ReputationInputs
		want int
	}{
		{"zero", ReputationInputs{}, 0},
		{"bounty", ReputationInputs{BountiesWon: 1}, 10},
		{"credits are divided by 10", ReputationInputs{CreditsWon: 250}, 25},
		{"credits round down", ReputationInputs{CreditsWon: 19}, 1},
		{"accepted answer", ReputationInputs{AnswersAccepted: 2}, 30},
		{"upvotes", ReputationInputs{UpvotesReceived: 7}, 14},
		{"first replies", ReputationInputs{FirstReplies1h: 3}, 15},
		{"upheld report subtracts", ReputationInputs{UpvotesReceived: 20, ReportsUpheld: 1}, 15},
		{"floor at zero", ReputationInputs{ReportsUpheld: 4}, 0},
		{"all together", ReputationInputs{BountiesWon: 2, CreditsWon: 500, AnswersAccepted: 3, UpvotesReceived: 10, FirstReplies1h: 4, ReportsUpheld: 1}, 20 + 50 + 45 + 20 + 20 - 25},
	}
	for _, c := range cases {
		if got := ComputeReputationScore(c.in); got != c.want {
			t.Errorf("%s: score = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestReputationTier_Thresholds(t *testing.T) {
	cases := map[int]string{0: "new", 19: "new", 20: "active", 99: "active", 100: "trusted", 499: "trusted", 500: "top", 5000: "top"}
	for score, want := range cases {
		if got := ReputationTier(score); got != want {
			t.Errorf("tier(%d) = %s, want %s", score, got, want)
		}
	}
	if !ReputationTierAtLeast("trusted", "active") || ReputationTierAtLeast("new", "active") || !ReputationTierAtLeast("active", "active") {
		t.Error("tier ordering is wrong")
	}
	if ReputationTierAtLeast("", "active") {
		t.Error("unknown tier must count as new")
	}
}

// repEnv is a forum + reputation setup over memory repos.
type repEnv struct {
	*boardEnv
	rep     *ReputationService
	reports *repository.MemoryBoardReportRepository
	repRepo *repository.MemoryBoardReputationRepository
}

func newRepEnv(t *testing.T) *repEnv {
	t.Helper()
	e := newBoardEnv(t)
	reports := repository.NewMemoryBoardReportRepository()
	repRepo := repository.NewMemoryBoardReputationRepository()
	rep := NewReputationService(repRepo, e.forum, reports, e.clk)
	e.svc.SetReputationService(rep)
	e.svc.SetReportRepo(reports)
	return &repEnv{boardEnv: e, rep: rep, reports: reports, repRepo: repRepo}
}

func (e *repEnv) reply(t *testing.T, postID, peer, body string) *models.ForumReply {
	t.Helper()
	rp, err := e.svc.CreateReply(context.Background(), postID, peer, body, false)
	if err != nil {
		t.Fatalf("reply on %s by %s: %v", postID, peer, err)
	}
	return rp
}

func TestReputation_RecomputeCounters(t *testing.T) {
	e := newRepEnv(t)
	ctx := context.Background()
	e.account(t, "alice", 1000)
	e.account(t, "bob", 0)

	// Bob wins a 300 credit bounty from Alice.
	b := e.bounty(t, "alice", "Need help", 300, 7)
	winner := e.reply(t, b.ID, "bob", "here you go")
	if err := e.svc.AwardBounty(ctx, "alice", b.ID, winner.ID); err != nil {
		t.Fatalf("award: %v", err)
	}
	// Bob's reply is the first on Alice's other post, within the hour, and accepted.
	p := e.post(t, "alice", "Question", "How do I", "general")
	first := e.reply(t, p.ID, "bob", "like this")
	e.clk.Advance(2 * time.Hour)
	e.reply(t, p.ID, "carol", "late")
	if _, err := e.svc.AcceptReply(ctx, "alice", p.ID, first.ID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Bob's own post gets 3 upvotes, one of which is later removed.
	own := e.post(t, "bob", "Bob's post", "body", "general")
	for _, voter := range []string{"alice", "carol", "dave"} {
		if _, _, err := e.svc.ToggleUpvote(ctx, voter, own.ID); err != nil {
			t.Fatalf("upvote: %v", err)
		}
	}
	if _, _, err := e.svc.ToggleUpvote(ctx, "dave", own.ID); err != nil {
		t.Fatalf("remove upvote: %v", err)
	}
	// One upheld report against Bob.
	_ = e.reports.Create(ctx, &models.BoardReport{TargetType: "post", TargetID: own.ID, TargetAuthorPeerID: "bob", ReporterPeerID: "eve", Reason: "spam", Status: "upheld"})

	rep, err := e.rep.Recompute(ctx, "bob")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if rep.BountiesWon != 1 || rep.CreditsWon != 300 || rep.AnswersAccepted != 1 || rep.UpvotesReceived != 2 || rep.FirstReplies1h != 2 || rep.ReportsUpheld != 1 {
		t.Fatalf("counters = %+v", rep)
	}
	// Both of Bob's replies were the first on their post within the hour.
	want := 10 + 30 + 15 + 4 + 10 - 25
	if rep.Score != want || rep.Tier != "active" {
		t.Errorf("score/tier = %d/%s, want %d/active", rep.Score, rep.Tier, want)
	}
	if !rep.ComputedAt.Equal(e.clk.Now().UTC()) {
		t.Errorf("computed_at = %v, want clock %v", rep.ComputedAt, e.clk.Now())
	}
	stored, err := e.repRepo.Get(ctx, "bob")
	if err != nil || stored.Score != want {
		t.Errorf("stored row = %+v err=%v", stored, err)
	}

	// Carol's late reply and Alice's own posts do not count as first replies.
	carol, _ := e.rep.Recompute(ctx, "carol")
	if carol.FirstReplies1h != 0 || carol.Score != 0 || carol.Tier != "new" {
		t.Errorf("carol = %+v", carol)
	}
	alice, _ := e.rep.Recompute(ctx, "alice")
	if alice.FirstReplies1h != 0 {
		t.Errorf("alice first replies on own post = %d, want 0", alice.FirstReplies1h)
	}
}

func TestReputation_FirstReplyOutsideHourDoesNotCount(t *testing.T) {
	e := newRepEnv(t)
	p := e.post(t, "alice", "Q", "body", "general")
	e.clk.Advance(61 * time.Minute)
	e.reply(t, p.ID, "bob", "too late")
	rep, _ := e.rep.Recompute(context.Background(), "bob")
	if rep.FirstReplies1h != 0 {
		t.Errorf("first replies = %d, want 0 after the hour", rep.FirstReplies1h)
	}
}

func TestReputation_RecomputeAllAndTiers(t *testing.T) {
	e := newRepEnv(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Q", "body", "general")
	e.reply(t, p.ID, "bob", "a")
	for i := 0; i < 12; i++ {
		_, _, _ = e.svc.ToggleUpvote(ctx, "voter"+string(rune('a'+i)), p.ID)
	}
	n, err := e.rep.RecomputeAll(ctx)
	if err != nil || n != 2 {
		t.Fatalf("recompute all: n=%d err=%v", n, err)
	}
	tiers := e.rep.TiersByIDs(ctx, []string{"alice", "bob", "nobody", ""})
	if tiers["alice"] != "active" || tiers["bob"] != "new" || tiers["nobody"] != "new" {
		t.Errorf("tiers = %v", tiers)
	}
	if _, ok := tiers[""]; ok {
		t.Error("blank id must be skipped")
	}
	// Get for a peer without a row is a zero "new" record, not an error.
	rep, err := e.rep.Get(ctx, "nobody")
	if err != nil || rep.Tier != "new" || rep.Score != 0 {
		t.Errorf("get unknown = %+v err=%v", rep, err)
	}
}

func TestNextNightlyRun(t *testing.T) {
	before := time.Date(2026, 9, 16, 1, 30, 0, 0, time.UTC)
	if got := NextNightlyRun(before); !got.Equal(time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("before 02:00 -> %v", got)
	}
	after := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	if got := NextNightlyRun(after); !got.Equal(time.Date(2026, 9, 17, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("at 02:00 -> %v (must be tomorrow)", got)
	}
}

func TestReputation_AutoRepliesNeverCountAsFirstReply(t *testing.T) {
	e := newRepEnv(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Q", "body", "general")
	// The autopilot answers within seconds; Bob's manual reply comes 10 minutes later.
	if _, err := e.svc.CreateReply(ctx, p.ID, "bot", "auto answer", true); err != nil {
		t.Fatalf("auto reply: %v", err)
	}
	e.clk.Advance(10 * time.Minute)
	e.reply(t, p.ID, "bob", "manual answer")
	bot, _ := e.rep.Recompute(ctx, "bot")
	if bot.FirstReplies1h != 0 {
		t.Errorf("autopilot first reply counted: %d", bot.FirstReplies1h)
	}
	bob, _ := e.rep.Recompute(ctx, "bob")
	if bob.FirstReplies1h != 1 {
		t.Errorf("first manual reply within the hour must count: %d", bob.FirstReplies1h)
	}
}
