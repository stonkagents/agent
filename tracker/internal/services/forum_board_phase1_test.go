// Package services: Community board phase 1 tests over the memory repositories: accepted
// answers (with the first-answer grant), reports with auto hide and resolution, platform pin and
// hide, thread watches and the reply_in_watched fan-out, @mentions, and token offers that settle
// on chain (create validation and the pay flow against the stub verifier).
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// p1Env is repEnv plus reports, watches, peers, launches, wallets and payments.
type p1Env struct {
	*repEnv
	watches  *repository.MemoryBoardWatchRepository
	peers    *repository.MemoryPeerRepository
	launches *repository.MemoryLaunchRepository
	wallets  *repository.MemoryWalletRepository
	payments *repository.MemoryTokenOfferPaymentRepository
	verifier *StubTokenTransferVerifier
}

func newP1Env(t *testing.T) *p1Env {
	t.Helper()
	e := newRepEnv(t)
	env := &p1Env{
		repEnv:   e,
		watches:  repository.NewMemoryBoardWatchRepository(),
		peers:    repository.NewMemoryPeerRepository(),
		launches: repository.NewMemoryLaunchRepository(),
		wallets:  repository.NewMemoryWalletRepository(),
		payments: repository.NewMemoryTokenOfferPaymentRepository(),
		verifier: &StubTokenTransferVerifier{},
	}
	e.svc.SetWatchRepo(env.watches)
	e.svc.SetPeerRepo(env.peers)
	e.svc.SetPlatformPeers([]string{"platform", " ", ""})
	e.svc.SetTokenOfferDeps(env.launches, env.wallets, env.payments, env.verifier)
	return env
}

func (e *p1Env) named(t *testing.T, peerID, name string) {
	t.Helper()
	if err := e.peers.Create(context.Background(), &models.Peer{PeerID: peerID, DisplayName: name}); err != nil {
		t.Fatalf("peer %s: %v", peerID, err)
	}
}

// oldPeer creates (or ages) a peer row first seen 48 h ago, old enough to count for auto hide.
func (e *p1Env) oldPeer(t *testing.T, peerID string) {
	t.Helper()
	if err := e.peers.Upsert(context.Background(), &models.Peer{PeerID: peerID, FirstSeen: e.clk.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatalf("peer %s: %v", peerID, err)
	}
}

// tier stores a reputation row so the peer has the given tier.
func (e *p1Env) tier(t *testing.T, peerID, tier string) {
	t.Helper()
	_ = e.repRepo.Upsert(context.Background(), &models.PeerReputation{PeerID: peerID, Tier: tier, Score: 50})
}

// wallet links a Solana wallet to the peer (creating the account when needed).
func (e *p1Env) wallet(t *testing.T, peerID, address string) {
	t.Helper()
	ctx := context.Background()
	acct, err := e.credits.accounts.GetByPeerID(ctx, peerID)
	if err != nil {
		e.account(t, peerID, 0)
		acct, _ = e.credits.accounts.GetByPeerID(ctx, peerID)
	}
	if err := e.wallets.LinkWallet(ctx, &models.AccountWallet{AccountID: acct.ID, WalletAddress: address, Chain: "solana"}); err != nil {
		t.Fatalf("link wallet: %v", err)
	}
}

func (e *p1Env) launch(t *testing.T, mint, symbol, creatorWallet, peerID string, feeBps int) {
	t.Helper()
	l := &models.TokenLaunch{Mint: mint, Symbol: symbol, CreatorWallet: creatorWallet, LaunchSignature: "sig-" + mint, TransferFeeBps: feeBps, PeerID: peerID}
	if peerID != "" {
		l.Status = models.LaunchStatusBound
	}
	if err := e.launches.Create(context.Background(), l); err != nil {
		t.Fatalf("launch: %v", err)
	}
}

func (e *p1Env) offerPost(t *testing.T, peer, mint string, amount int64, max int) *models.ForumPost {
	t.Helper()
	p, err := e.svc.CreatePost(context.Background(), peer, CreatePostInput{
		Title: "Offer", Description: "paying in tokens", Category: "token-offer",
		TokenOfferMint: mint, TokenOfferAmountRaw: amount, TokenOfferMax: max,
	})
	if err != nil {
		t.Fatalf("offer post: %v", err)
	}
	return p
}

// --- Accepted answer ---

func TestAcceptReply_RulesActivityAndGrant(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	bobAcct := e.account(t, "bob", 0)
	e.account(t, "alice", 0)
	e.tier(t, "alice", "active")
	p := e.post(t, "alice", "Q", "body", "general")
	own := e.reply(t, p.ID, "alice", "my own")
	bobs := e.reply(t, p.ID, "bob", "answer")
	carols := e.reply(t, p.ID, "carol", "other")
	other := e.post(t, "dave", "Other", "body", "general")
	elsewhere := e.reply(t, other.ID, "bob", "not here")

	if _, err := e.svc.AcceptReply(ctx, "bob", p.ID, bobs.ID); !errors.Is(err, ErrAcceptNotAuthor) {
		t.Errorf("non-author: %v", err)
	}
	if _, err := e.svc.AcceptReply(ctx, "alice", p.ID, own.ID); !errors.Is(err, ErrAcceptOwnReply) {
		t.Errorf("own reply: %v", err)
	}
	if _, err := e.svc.AcceptReply(ctx, "alice", p.ID, elsewhere.ID); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("reply on another post: %v", err)
	}
	if _, err := e.svc.AcceptReply(ctx, "alice", "nope", bobs.ID); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("unknown post: %v", err)
	}

	got, err := e.svc.AcceptReply(ctx, "alice", p.ID, bobs.ID)
	if err != nil || got.AcceptedReplyID == nil || *got.AcceptedReplyID != bobs.ID {
		t.Fatalf("accept: %+v err=%v", got, err)
	}
	if rows := e.feed(t, "bob", models.ActivityReplyAccepted); len(rows) != 1 || rows[0].ReplyID == nil || *rows[0].ReplyID != bobs.ID || rows[0].ActorPeerID != "alice" {
		t.Errorf("reply_accepted rows = %+v", rows)
	}
	if e.balance(t, bobAcct) != FirstAcceptedAnswerCredits || !e.hasTx(bobAcct, "first_accepted_answer:bob") {
		t.Errorf("first accepted answer grant missing: balance=%d", e.balance(t, bobAcct))
	}
	rep, _ := e.rep.Get(ctx, "bob")
	if rep.AnswersAccepted != 1 {
		t.Errorf("bob reputation not refreshed: %+v", rep)
	}

	// Accepting again is a no-op; switching to Carol un-accepts Bob and refreshes both.
	if _, err := e.svc.AcceptReply(ctx, "alice", p.ID, bobs.ID); err != nil {
		t.Fatalf("re-accept: %v", err)
	}
	if rows := e.feed(t, "bob", models.ActivityReplyAccepted); len(rows) != 1 {
		t.Errorf("re-accept must not notify again: %d rows", len(rows))
	}
	got, err = e.svc.AcceptReply(ctx, "alice", p.ID, carols.ID)
	if err != nil || *got.AcceptedReplyID != carols.ID {
		t.Fatalf("switch: %+v err=%v", got, err)
	}
	rep, _ = e.rep.Get(ctx, "bob")
	if rep.AnswersAccepted != 0 {
		t.Errorf("bob still counted: %+v", rep)
	}
	// A second accepted answer for Bob elsewhere grants nothing more.
	if _, err := e.svc.AcceptReply(ctx, "dave", other.ID, elsewhere.ID); err != nil {
		t.Fatalf("accept elsewhere: %v", err)
	}
	if e.balance(t, bobAcct) != FirstAcceptedAnswerCredits {
		t.Errorf("grant must be once ever: balance=%d", e.balance(t, bobAcct))
	}

	// The grant needs an acceptor of tier active or above: a new-tier author accepting Carol
	// grants nothing; a later accept by an active author does.
	carolAcct := e.account(t, "carol", 0)
	e.account(t, "newbie", 0)
	q := e.post(t, "newbie", "Newbie Q", "body", "general")
	carolsQ := e.reply(t, q.ID, "carol", "answer")
	if _, err := e.svc.AcceptReply(ctx, "newbie", q.ID, carolsQ.ID); err != nil {
		t.Fatalf("newbie accept: %v", err)
	}
	if e.balance(t, carolAcct) != 0 {
		t.Errorf("new-tier acceptor must not trigger the grant: balance=%d", e.balance(t, carolAcct))
	}
	q2 := e.post(t, "alice", "Alice Q2", "body", "general")
	carolsQ2 := e.reply(t, q2.ID, "carol", "answer")
	if _, err := e.svc.AcceptReply(ctx, "alice", q2.ID, carolsQ2.ID); err != nil {
		t.Fatalf("alice accept: %v", err)
	}
	if e.balance(t, carolAcct) != FirstAcceptedAnswerCredits {
		t.Errorf("active acceptor must trigger the grant: balance=%d", e.balance(t, carolAcct))
	}
}

// --- Reports ---

func TestReport_RulesAndAutoHide(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Spam", "buy now", "general")
	rp := e.reply(t, p.ID, "alice", "and again")

	if _, _, err := e.svc.Report(ctx, "alice", "post", p.ID, "spam", ""); !errors.Is(err, ErrReportOwn) {
		t.Errorf("own content: %v", err)
	}
	if _, _, err := e.svc.Report(ctx, "bob", "post", p.ID, "bogus", ""); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("bad reason: %v", err)
	}
	if _, _, err := e.svc.Report(ctx, "bob", "post", "nope", "spam", ""); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("unknown target: %v", err)
	}
	r, hidden, err := e.svc.Report(ctx, "bob", "post", p.ID, "SPAM", " looks like an ad ")
	if err != nil || hidden || r.Status != "open" || r.Reason != "spam" || r.Note != "looks like an ad" || r.TargetAuthorPeerID != "alice" {
		t.Fatalf("report: %+v hidden=%v err=%v", r, hidden, err)
	}
	if _, _, err := e.svc.Report(ctx, "bob", "post", p.ID, "abuse", ""); !errors.Is(err, ErrReportDuplicate) {
		t.Errorf("duplicate: %v", err)
	}
	// Two more reporters, but only "new" tier: nothing hides.
	for _, who := range []string{"carol", "dave"} {
		if _, hidden, err := e.svc.Report(ctx, who, "post", p.ID, "scam", ""); err != nil || hidden {
			t.Fatalf("%s: hidden=%v err=%v", who, hidden, err)
		}
	}
	// Three reporters of tier active or above with peer rows older than 24 h hide the target.
	e.tier(t, "bob", "active")
	e.tier(t, "carol", "trusted")
	e.tier(t, "eve", "top")
	e.tier(t, "fresh", "top")
	for _, id := range []string{"bob", "carol", "eve"} {
		e.oldPeer(t, id)
	}
	_ = e.peers.Create(ctx, &models.Peer{PeerID: "fresh", FirstSeen: e.clk.Now().Add(-time.Hour)})
	if _, hidden, err := e.svc.Report(ctx, "fresh", "post", p.ID, "abuse", ""); err != nil || hidden {
		t.Fatalf("a top reporter younger than 24 h must not count: hidden=%v err=%v", hidden, err)
	}
	third, hidden, err := e.svc.Report(ctx, "eve", "post", p.ID, "abuse", "")
	if err != nil || !hidden {
		t.Fatalf("third qualifying reporter: hidden=%v err=%v", hidden, err)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if !post.Hidden {
		t.Error("post not hidden after 3 active reports")
	}
	// Auto hide resolves the open reports on the target as upheld with the note "auto hidden"
	// (the panel no longer lists them as pending) and the returned report says so.
	if third.Status != models.ReportStatusUpheld || third.ResolvedAt == nil || third.ResolutionNote != models.ReportResolutionAutoHidden {
		t.Errorf("triggering report after auto hide: %+v", third)
	}
	upheld, _ := e.svc.ListReports(ctx, "upheld", 100)
	if len(upheld) != 5 {
		t.Errorf("upheld reports after post auto hide = %d, want 5 (bob, carol, dave, fresh, eve)", len(upheld))
	}
	for _, rep := range upheld {
		if rep.ResolutionNote != models.ReportResolutionAutoHidden || rep.ResolvedAt == nil || !rep.ResolvedAt.Equal(e.clk.Now()) {
			t.Errorf("upheld by auto hide: %+v", rep)
		}
	}
	// Auto-upheld reports never carry the reputation penalty; only a platform uphold does.
	if rep, _ := e.rep.Get(ctx, "alice"); rep != nil && rep.ReportsUpheld != 0 {
		t.Errorf("alice reports_upheld after auto hide = %d, want 0", rep.ReportsUpheld)
	}
	if n, _ := e.reports.CountUpheldAgainst(ctx, "alice"); n != 0 {
		t.Errorf("CountUpheldAgainst after auto hide = %d, want 0", n)
	}
	// A platform peer confirming one auto-upheld report makes that one count; the note is
	// cleared and it cannot be resolved twice.
	confirmed, err := e.svc.ResolveReport(ctx, third.ID, true)
	if err != nil || confirmed.Status != models.ReportStatusUpheld || confirmed.ResolutionNote != "" {
		t.Fatalf("platform uphold of an auto-upheld report: %+v err=%v", confirmed, err)
	}
	if rep, _ := e.rep.Get(ctx, "alice"); rep == nil || rep.ReportsUpheld != 1 {
		t.Errorf("alice reports_upheld after the platform uphold = %+v, want 1", rep)
	}
	if _, err := e.svc.ResolveReport(ctx, third.ID, true); !errors.Is(err, ErrReportNotOpen) {
		t.Errorf("second explicit resolve: %v", err)
	}
	// A platform dismiss of another auto-upheld report clears it without counting.
	if dismissed, err := e.svc.ResolveReport(ctx, r.ID, false); err != nil || dismissed.Status != models.ReportStatusDismissed || dismissed.ResolutionNote != "" {
		t.Fatalf("platform dismiss of an auto-upheld report: %+v err=%v", dismissed, err)
	}
	if n, _ := e.reports.CountUpheldAgainst(ctx, "alice"); n != 1 {
		t.Errorf("CountUpheldAgainst after dismiss = %d, want 1", n)
	}
	// The platform can still unhide the target; the reports stay resolved.
	if err := e.svc.SetPostHidden(ctx, p.ID, false); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	if post, _ := e.svc.GetPost(ctx, p.ID); post.Hidden {
		t.Error("platform unhide must clear hidden")
	}
	// Replies hide the same way.
	for _, who := range []string{"bob", "carol"} {
		_, _, _ = e.svc.Report(ctx, who, "reply", rp.ID, "spam", "")
	}
	if _, hidden, err := e.svc.Report(ctx, "eve", "reply", rp.ID, "spam", ""); err != nil || !hidden {
		t.Fatalf("reply auto hide: hidden=%v err=%v", hidden, err)
	}
	reply, _ := e.forum.GetReplyByID(ctx, rp.ID)
	if !reply.Hidden {
		t.Error("reply not hidden")
	}
	open, _ := e.svc.ListReports(ctx, "open", 100)
	upheld, _ = e.svc.ListReports(ctx, "upheld", 100)
	if len(open) != 0 || len(upheld) != 7 {
		t.Errorf("open reports = %d, upheld = %d, want 0 and 7 (every report on an auto-hidden target is resolved; bob's was dismissed)", len(open), len(upheld))
	}
}

// TestReport_AutoHideWithBlankProfilePeers covers the seeding finding: reporters whose peer
// rows carry no country, region or masked id (NULL on Postgres before migration 029, blank
// here) must still count, so the hide fires on the third qualifying report.
func TestReport_AutoHideWithBlankProfilePeers(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Spam", "buy now", "general")
	for _, id := range []string{"bob", "carol", "eve"} {
		e.tier(t, id, "active")
		if err := e.peers.Create(ctx, &models.Peer{PeerID: id, FirstSeen: e.clk.Now().Add(-48 * time.Hour), Country: "", Region: "", MaskedPeerID: ""}); err != nil {
			t.Fatalf("peer %s: %v", id, err)
		}
	}
	for _, id := range []string{"bob", "carol"} {
		if _, hidden, err := e.svc.Report(ctx, id, "post", p.ID, "spam", ""); err != nil || hidden {
			t.Fatalf("%s: hidden=%v err=%v", id, hidden, err)
		}
	}
	if _, hidden, err := e.svc.Report(ctx, "eve", "post", p.ID, "spam", ""); err != nil || !hidden {
		t.Fatalf("third report with blank profile peers: hidden=%v err=%v", hidden, err)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if !post.Hidden {
		t.Error("post not hidden")
	}
}

func TestResolveReport_UpholdHidesAndCounts(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Post", "body", "general")
	r1, _, _ := e.svc.Report(ctx, "bob", "post", p.ID, "spam", "")
	r2, _, _ := e.svc.Report(ctx, "carol", "post", p.ID, "spam", "")

	got, err := e.svc.ResolveReport(ctx, r1.ID, true)
	if err != nil || got.Status != "upheld" || got.ResolvedAt == nil {
		t.Fatalf("uphold: %+v err=%v", got, err)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if !post.Hidden {
		t.Error("upheld report must hide the target")
	}
	rep, _ := e.rep.Get(ctx, "alice")
	if rep.ReportsUpheld != 1 {
		t.Errorf("alice reports_upheld = %d, want 1", rep.ReportsUpheld)
	}
	if _, err := e.svc.ResolveReport(ctx, r1.ID, false); !errors.Is(err, ErrReportNotOpen) {
		t.Errorf("resolve twice: %v", err)
	}
	got, err = e.svc.ResolveReport(ctx, r2.ID, false)
	if err != nil || got.Status != "dismissed" {
		t.Fatalf("dismiss: %+v err=%v", got, err)
	}
	if _, err := e.svc.ResolveReport(ctx, "nope", true); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	open, _ := e.svc.ListReports(ctx, "open", 100)
	all, _ := e.svc.ListReports(ctx, "", 100)
	if len(open) != 0 || len(all) != 2 {
		t.Errorf("open=%d all=%d", len(open), len(all))
	}
}

// --- Pin and hide ---

func TestPinAndHide_FeedAndThreadVisibility(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	old := e.post(t, "alice", "Old", "body", "general")
	e.clk.Advance(1)
	mid := e.post(t, "bob", "Mid", "body", "general")
	e.clk.Advance(1)
	newest := e.post(t, "carol", "New", "body", "general")

	if err := e.svc.PinPost(ctx, old.ID); err != nil {
		t.Fatalf("pin: %v", err)
	}
	posts, _, _ := e.svc.QueryPosts(ctx, repository.PostQuery{})
	if ids(posts)[0] != old.ID || !posts[0].Pinned {
		t.Errorf("pinned post must come first in recent: %v", ids(posts))
	}
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Sort: repository.PostSortTop})
	if ids(posts)[0] != old.ID {
		t.Errorf("pinned post must come first in top: %v", ids(posts))
	}
	// Pinning another replaces.
	_ = e.svc.PinPost(ctx, mid.ID)
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{})
	if ids(posts)[0] != mid.ID || posts[0].Pinned == false {
		t.Errorf("pin replaced: %v", ids(posts))
	}
	for _, p := range posts[1:] {
		if p.Pinned {
			t.Errorf("post %s still pinned", p.ID)
		}
	}
	_ = e.svc.UnpinPost(ctx, mid.ID)
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{})
	if ids(posts)[0] != newest.ID {
		t.Errorf("after unpin newest first: %v", ids(posts))
	}
	if err := e.svc.PinPost(ctx, "nope"); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("pin unknown: %v", err)
	}

	// Hidden post: gone for anonymous and others, visible to the author and platform peers.
	if err := e.svc.SetPostHidden(ctx, mid.ID, true); err != nil {
		t.Fatalf("hide: %v", err)
	}
	posts, total, _ := e.svc.QueryPosts(ctx, repository.PostQuery{})
	if total != 2 || len(posts) != 2 {
		t.Errorf("anonymous sees hidden: %v", ids(posts))
	}
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Viewer: "carol"})
	if len(posts) != 2 {
		t.Errorf("other viewer sees hidden: %v", ids(posts))
	}
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Viewer: "bob"})
	if len(posts) != 3 {
		t.Errorf("author does not see own hidden post: %v", ids(posts))
	}
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{Viewer: "platform", ShowHidden: true})
	if len(posts) != 3 {
		t.Errorf("platform does not see hidden: %v", ids(posts))
	}
	counts, _ := e.svc.BoardCounts(ctx, "")
	if counts.All != 2 {
		t.Errorf("counts include hidden: %d", counts.All)
	}
	if !e.svc.CanSeePost(posts[0], "bob") || e.svc.CanSeePost(&models.ForumPost{Hidden: true, AuthorPeerID: "bob"}, "carol") || !e.svc.CanSeePost(&models.ForumPost{Hidden: true, AuthorPeerID: "bob"}, "platform") {
		t.Error("CanSeePost rules")
	}
	_ = e.svc.SetPostHidden(ctx, mid.ID, false)
	posts, _, _ = e.svc.QueryPosts(ctx, repository.PostQuery{})
	if len(posts) != 3 {
		t.Errorf("unhide: %v", ids(posts))
	}

	// Hidden reply: same rules in the thread.
	rp := e.reply(t, newest.ID, "dave", "spam")
	e.reply(t, newest.ID, "erin", "fine")
	if err := e.svc.SetReplyHidden(ctx, rp.ID, true); err != nil {
		t.Fatalf("hide reply: %v", err)
	}
	replies, total, _ := e.svc.ListReplies(ctx, newest.ID, e.svc.ReplyQueryFor("", false))
	if total != 1 || len(replies) != 1 || replies[0].AuthorPeerID != "erin" {
		t.Errorf("anonymous thread = %d replies", len(replies))
	}
	replies, _, _ = e.svc.ListReplies(ctx, newest.ID, e.svc.ReplyQueryFor("dave", false))
	if len(replies) != 2 || !replies[0].Hidden {
		t.Errorf("author thread = %d replies", len(replies))
	}
	replies, _, _ = e.svc.ListReplies(ctx, newest.ID, e.svc.ReplyQueryFor("platform", false))
	if len(replies) != 2 {
		t.Errorf("platform thread = %d replies", len(replies))
	}
	if !e.svc.IsPlatformPeer("platform") || e.svc.IsPlatformPeer("alice") || e.svc.IsPlatformPeer("") {
		t.Error("platform peer set")
	}
}

// --- Watches ---

func TestWatch_AutoWatchAndFanOut(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	p := e.post(t, "alice", "Thread", "body", "general")
	e.reply(t, p.ID, "bob", "first")
	if err := e.svc.SetWatch(ctx, "carol", p.ID, true); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if err := e.svc.SetWatch(ctx, "carol", "nope", true); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("watch unknown post: %v", err)
	}
	w := e.svc.WatchingByPostIDs(ctx, "carol", []string{p.ID})
	if !w[p.ID] {
		t.Error("carol should watch")
	}
	if !e.svc.WatchingByPostIDs(ctx, "alice", []string{p.ID})[p.ID] || !e.svc.WatchingByPostIDs(ctx, "bob", []string{p.ID})[p.ID] {
		t.Error("author and replier are watched automatically")
	}

	// Dave replies: alice gets reply_on_post (not reply_in_watched), bob and carol get
	// reply_in_watched, dave gets nothing.
	rp := e.reply(t, p.ID, "dave", "second")
	if rows := e.feed(t, "alice", models.ActivityReplyInWatched); len(rows) != 0 {
		t.Errorf("author must not get reply_in_watched: %d", len(rows))
	}
	for _, who := range []string{"bob", "carol"} {
		rows := e.feed(t, who, models.ActivityReplyInWatched)
		if len(rows) != 1 || rows[0].ReplyID == nil || *rows[0].ReplyID != rp.ID || rows[0].ActorPeerID != "dave" {
			t.Errorf("%s reply_in_watched = %+v", who, rows)
		}
	}
	if rows := e.feed(t, "dave", ""); len(rows) != 0 {
		t.Errorf("replier notified: %+v", rows)
	}

	// Bob unwatches; replying again does not re-watch him.
	if err := e.svc.SetWatch(ctx, "bob", p.ID, false); err != nil {
		t.Fatalf("unwatch: %v", err)
	}
	e.reply(t, p.ID, "bob", "again")
	e.reply(t, p.ID, "erin", "hello")
	if rows := e.feed(t, "bob", models.ActivityReplyInWatched); len(rows) != 1 {
		t.Errorf("unwatched bob notified again: %d", len(rows))
	}
	if rows := e.feed(t, "carol", models.ActivityReplyInWatched); len(rows) != 3 {
		t.Errorf("carol should have 3 reply_in_watched, got %d", len(rows))
	}
}

// --- Mentions ---

func TestMentions_ResolveAndNotify(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	e.named(t, "alice", "Alice")
	e.named(t, "bob", "bob_builder")
	e.named(t, "bobby", "bob")
	e.named(t, "carol", "carol99")
	e.named(t, "spacey", "has space")

	tokens := MentionTokens("hi @Alice, ping @bob_builders and mail me@example.com @ab @carol99!")
	if strings.Join(tokens, ",") != "Alice,bob_builders,carol99" {
		t.Errorf("tokens = %v", tokens)
	}
	got := ResolveMentions(ctx, e.peers, "hi @alice, ping @bob_builders (@bob too) @carol99 @nobody @Alice @bob_builder!", "carol")
	if strings.Join(got, ",") != "alice,bobby,bob" {
		t.Errorf("resolved = %v (exact name only: @bob_builders is not bob_builder; case-insensitive, self and unknown skipped, unique)", got)
	}
	long := "@" + strings.Repeat("a", MaxMentionRunes+1)
	if toks := MentionTokens(long + " @" + strings.Repeat("b", MaxMentionRunes)); len(toks) != 1 || len(toks[0]) != MaxMentionRunes {
		t.Errorf("a run longer than %d is not a candidate: %v", MaxMentionRunes, toks)
	}
	if ResolveMentions(ctx, nil, "@alice", "x") != nil || ResolveMentions(ctx, e.peers, "no mentions", "x") != nil {
		t.Error("nil repo or no tokens must resolve to nothing")
	}

	// Cap at 5 per body.
	for i := 0; i < 8; i++ {
		e.named(t, fmt.Sprintf("p%d", i), fmt.Sprintf("peer%d", i))
	}
	body := "@peer0 @peer1 @peer2 @peer3 @peer4 @peer5 @peer6 @peer7"
	if got := ResolveMentions(ctx, e.peers, body, "x"); len(got) != MaxMentionsPerBody {
		t.Errorf("cap: %v", got)
	}

	// Stored on the post and reply, and each mentioned peer gets one activity (never the author).
	p, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "hey @bob_builder", Description: "and @Alice and @carol99"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if strings.Join(p.MentionPeerIDs, ",") != "bob,carol" {
		t.Errorf("post mentions = %v", p.MentionPeerIDs)
	}
	for _, who := range []string{"bob", "carol"} {
		rows := e.feed(t, who, models.ActivityMentioned)
		if len(rows) != 1 || rows[0].PostID != p.ID || rows[0].ReplyID != nil || rows[0].ActorPeerID != "alice" {
			t.Errorf("%s mentioned rows = %+v", who, rows)
		}
	}
	if rows := e.feed(t, "alice", models.ActivityMentioned); len(rows) != 0 {
		t.Errorf("self mention notified: %+v", rows)
	}
	rp, err := e.svc.CreateReply(ctx, p.ID, "bob", "thanks @Carol99", false)
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if strings.Join(rp.MentionPeerIDs, ",") != "carol" {
		t.Errorf("reply mentions = %v", rp.MentionPeerIDs)
	}
	stored, _ := e.forum.GetReplyByID(ctx, rp.ID)
	if strings.Join(stored.MentionPeerIDs, ",") != "carol" {
		t.Errorf("stored reply mentions = %v", stored.MentionPeerIDs)
	}
	rows := e.feed(t, "carol", models.ActivityMentioned)
	fromReply := 0
	for _, a := range rows {
		if a.ReplyID != nil && *a.ReplyID == rp.ID {
			fromReply++
		}
	}
	if len(rows) != 2 || fromReply != 1 {
		t.Errorf("carol mentioned rows = %d (from reply %d), want 2 (1)", len(rows), fromReply)
	}
}

// --- Token offers ---

func TestTokenOffer_CreateValidation(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	e.launch(t, "MintA", "AAA", "walletA", "agentA", 100)
	e.launch(t, "MintB", "BBB", "walletB", "", 0)

	_, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "x", Description: "y", TokenOfferMint: "Unknown", TokenOfferAmountRaw: 10, TokenOfferMax: 1})
	if !errors.Is(err, ErrTokenOfferUnknownMint) {
		t.Errorf("unknown mint: %v", err)
	}
	_, err = e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "x", Description: "y", TokenOfferMint: "MintB", TokenOfferAmountRaw: 10, TokenOfferMax: 1})
	if !errors.Is(err, ErrTokenOfferNoWallet) {
		t.Errorf("not the agent and no wallet: %v", err)
	}
	for _, bad := range []CreatePostInput{
		{TokenOfferMint: "MintA", TokenOfferAmountRaw: 0, TokenOfferMax: 1},
		{TokenOfferMint: "MintA", TokenOfferAmountRaw: 10, TokenOfferMax: 0},
		{TokenOfferMint: "MintA", TokenOfferAmountRaw: 10, TokenOfferMax: MaxTokenOfferAccepts + 1},
	} {
		bad.Title, bad.Description = "x", "y"
		if _, err := e.svc.CreatePost(ctx, "agentA", bad); !errors.Is(err, models.ErrInvalidInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// The token's agent needs a linked wallet too (B-2): the payment is sent from it.
	if _, err := e.svc.CreatePost(ctx, "agentA", CreatePostInput{Title: "x", Description: "y", TokenOfferMint: "MintA", TokenOfferAmountRaw: 10, TokenOfferMax: 1}); !errors.Is(err, ErrTokenOfferNoWallet) {
		t.Errorf("agent without wallet: %v", err)
	}
	e.wallet(t, "agentA", "walletA")
	p := e.offerPost(t, "agentA", "MintA", 1_000_000, 3)
	if !p.HasSettlingTokenOffer() || *p.TokenOfferSymbol != "AAA" || *p.TokenOfferDecimals != LaunchTokenDecimals || *p.TokenOfferAmount != 1_000_000 || *p.TokenOfferMax != 3 || p.TokenOfferPaid != 0 || *p.TokenOfferToken != "AAA" {
		t.Errorf("offer post = %+v", p)
	}
	if e.svc.TokenOfferProgram(ctx, p) != TokenProgram2022 {
		t.Error("launch with a transfer fee is Token-2022")
	}
	// A peer with a linked wallet may offer someone else's token.
	e.wallet(t, "alice", "walletAlice")
	p2 := e.offerPost(t, "alice", "MintB", 5, 1)
	if e.svc.TokenOfferProgram(ctx, p2) != TokenProgramClassic {
		t.Error("launch without a fee is the classic token program")
	}
	// Legacy free-text offers still work and are not settling.
	amount, token := 5, "$OLD"
	legacy, err := e.svc.CreatePost(ctx, "alice", CreatePostInput{Title: "x", Description: "y", TokenOfferAmount: &amount, TokenOfferToken: &token})
	if err != nil || legacy.HasSettlingTokenOffer() || !legacy.HasTokenOffer() {
		t.Errorf("legacy offer = %+v err=%v", legacy, err)
	}
}

func TestTokenOffer_PayFlow(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	e.launch(t, "MintA", "AAA", "walletA", "agentA", 100)
	e.wallet(t, "agentA", "walletA")
	e.wallet(t, "bob", "walletBob")
	e.wallet(t, "carol", "walletCarol")
	p := e.offerPost(t, "agentA", "MintA", 1_000_000, 2)
	own := e.reply(t, p.ID, "agentA", "mine")
	bobs := e.reply(t, p.ID, "bob", "b")
	carols := e.reply(t, p.ID, "carol", "c")
	daves := e.reply(t, p.ID, "dave", "d (no wallet)")
	plain := e.post(t, "agentA", "Plain", "no offer", "general")
	plainReply := e.reply(t, plain.ID, "bob", "x")

	check := func(name string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", name, err, want)
		}
	}
	_, err := e.svc.PayTokenOffer(ctx, "bob", p.ID, bobs.ID, "sig1")
	check("not author", err, ErrTokenOfferNotAuthor)
	_, err = e.svc.PayTokenOffer(ctx, "agentA", plain.ID, plainReply.ID, "sig1")
	check("no offer", err, ErrTokenOfferNone)
	_, err = e.svc.PayTokenOffer(ctx, "agentA", p.ID, plainReply.ID, "sig1")
	check("reply on another post", err, models.ErrNotFound)
	_, err = e.svc.PayTokenOffer(ctx, "agentA", p.ID, own.ID, "sig1")
	check("own reply", err, ErrTokenOfferOwnReply)
	_, err = e.svc.PayTokenOffer(ctx, "agentA", p.ID, daves.ID, "sig1")
	check("no wallet", err, ErrTokenOfferNoWallet)
	_, err = e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "  ")
	check("blank signature", err, models.ErrInvalidInput)

	// Transaction facts that fail: reason strings reach the caller.
	var txErr *TokenOfferTxError
	e.verifier.NotFound = true
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "not found") {
		t.Errorf("not found: %v", err)
	}
	e.verifier.NotFound = false
	e.verifier.Failed = true
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "failed") {
		t.Errorf("failed: %v", err)
	}
	e.verifier.Failed = false
	e.verifier.NotSigner = true
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "sign") {
		t.Errorf("not signer: %v", err)
	}
	e.verifier.NotSigner = false
	e.verifier.FromDebit = 999_999
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "expected 1000000") {
		t.Errorf("wrong amount: %v", err)
	}
	e.verifier.FromDebit = 0
	e.verifier.NoMemo = true
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "memo") {
		t.Errorf("memo: %v", err)
	}
	e.verifier.NoMemo = false
	e.verifier.ToCredit = 980_000 // more than the 1% fee withheld
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "receive") {
		t.Errorf("short credit: %v", err)
	}
	if e.svc.PaidReplyIDs(ctx, p.ID)[bobs.ID] {
		t.Fatal("nothing must be recorded on a failed verification")
	}

	// Success: the fee-withheld credit (990_000) is accepted.
	e.verifier.ToCredit = 990_000
	payment, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, " sig1 ")
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if payment.Signature != "sig1" || payment.FromWallet != "walletA" || payment.ToWallet != "walletBob" || payment.AmountRaw != 1_000_000 || payment.PostID != p.ID || payment.ReplyID != bobs.ID {
		t.Errorf("payment = %+v", payment)
	}
	if e.verifier.Last.Memo != "stonkagents:offer:"+p.ID+":"+bobs.ID || e.verifier.Last.Mint != "MintA" || e.verifier.Last.AmountRaw != 1_000_000 {
		t.Errorf("verifier params = %+v", e.verifier.Last)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if post.TokenOfferPaid != 1 {
		t.Errorf("paid = %d, want 1", post.TokenOfferPaid)
	}
	rows := e.feed(t, "bob", models.ActivityTokenOfferPaid)
	if len(rows) != 1 || rows[0].Amount == nil || *rows[0].Amount != 1_000_000 || rows[0].Symbol != "AAA" || rows[0].ActorPeerID != "agentA" || *rows[0].ReplyID != bobs.ID {
		t.Errorf("token_offer_paid rows = %+v", rows)
	}
	if !e.svc.PaidReplyIDs(ctx, p.ID)[bobs.ID] {
		t.Error("bob's reply must be paid")
	}
	wallets := e.svc.LinkedWallets(ctx, []string{"bob", "dave", "carol", "bob", ""})
	if wallets["bob"] != "walletBob" || wallets["carol"] != "walletCarol" || wallets["dave"] != "" {
		t.Errorf("linked wallets = %v", wallets)
	}

	// Same reply again, same signature again, then exhaustion.
	_, err = e.svc.PayTokenOffer(ctx, "agentA", p.ID, bobs.ID, "sig2")
	check("already paid", err, ErrTokenOfferAlreadyPaid)
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, carols.ID, "sig1"); !errors.As(err, &txErr) || !strings.Contains(txErr.Reason, "already used") {
		t.Errorf("signature reuse: %v", err)
	}
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, carols.ID, "sig2"); err != nil {
		t.Fatalf("pay carol: %v", err)
	}
	extra := e.reply(t, p.ID, "erin", "e")
	e.wallet(t, "erin", "walletErin")
	_, err = e.svc.PayTokenOffer(ctx, "agentA", p.ID, extra.ID, "sig3")
	check("exhausted", err, ErrTokenOfferExhausted)
}

// The paid counter is reserved atomically: two payments racing for the last slot never both
// land, and a payment that fails to record gives its slot back.
func TestTokenOffer_ExhaustedUnderConcurrency(t *testing.T) {
	e := newP1Env(t)
	ctx := context.Background()
	e.launch(t, "MintA", "AAA", "walletA", "agentA", 0)
	e.wallet(t, "agentA", "walletA")
	p := e.offerPost(t, "agentA", "MintA", 100, 1)
	const racers = 8
	replies := make([]*models.ForumReply, racers)
	for i := range replies {
		peer := fmt.Sprintf("racer%d", i)
		e.wallet(t, peer, "wallet"+peer)
		replies[i] = e.reply(t, p.ID, peer, "me")
	}
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			_, err := e.svc.PayTokenOffer(ctx, "agentA", p.ID, replies[i].ID, fmt.Sprintf("sig%d", i))
			results <- err
		}(i)
	}
	ok, exhausted := 0, 0
	for i := 0; i < racers; i++ {
		switch err := <-results; {
		case err == nil:
			ok++
		case errors.Is(err, ErrTokenOfferExhausted):
			exhausted++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || exhausted != racers-1 {
		t.Errorf("ok=%d exhausted=%d, want 1/%d", ok, exhausted, racers-1)
	}
	post, _ := e.svc.GetPost(ctx, p.ID)
	if post.TokenOfferPaid != 1 || len(e.svc.PaidReplyIDs(ctx, p.ID)) != 1 {
		t.Errorf("paid = %d (payments %d), want 1", post.TokenOfferPaid, len(e.svc.PaidReplyIDs(ctx, p.ID)))
	}

	// A duplicate payment (same reply, new signature) on an offer with room never consumes a slot.
	p2 := e.offerPost(t, "agentA", "MintA", 100, 2)
	r := e.reply(t, p2.ID, "racer0", "again")
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p2.ID, r.ID, "dup-1"); err != nil {
		t.Fatalf("first pay: %v", err)
	}
	if _, err := e.svc.PayTokenOffer(ctx, "agentA", p2.ID, r.ID, "dup-2"); !errors.Is(err, ErrTokenOfferAlreadyPaid) {
		t.Fatalf("second pay: %v", err)
	}
	if post, _ := e.svc.GetPost(ctx, p2.ID); post.TokenOfferPaid != 1 {
		t.Errorf("duplicate consumed a slot: paid=%d", post.TokenOfferPaid)
	}
}
