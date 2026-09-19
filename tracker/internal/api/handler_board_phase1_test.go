// Package api: Community board phase 1 HTTP tests: reputation fields on posts, replies,
// activity and the peer detail, accept, report, platform pin/hide/reports, watch, token offer
// create and pay, display-name autocomplete and GET /api/peers/me.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// p1Env is boardEnv with the phase 1 repositories wired into the forum service.
type p1Env struct {
	*boardEnv
	accounts *repository.MemoryAccountRepository
	wallets  *repository.MemoryWalletRepository
	launches *repository.MemoryLaunchRepository
	repRepo  *repository.MemoryBoardReputationRepository
	verifier *services.StubTokenTransferVerifier
	// credits backs the forum service's escrow (round 2 tests fund accounts through it).
	credits *repository.MemoryCreditRepository
}

func newP1Env(t *testing.T) *p1Env {
	t.Helper()
	e := newBoardEnv(t)
	env := &p1Env{
		boardEnv: e,
		accounts: repository.NewMemoryAccountRepository(),
		wallets:  repository.NewMemoryWalletRepository(),
		launches: repository.NewMemoryLaunchRepository(),
		repRepo:  repository.NewMemoryBoardReputationRepository(),
		verifier: &services.StubTokenTransferVerifier{},
		credits:  repository.NewMemoryCreditRepositoryWithClock(e.clock),
	}
	// The forum service in newBoardEnv has no account or credit repos; rebuild one with them so
	// token offers can resolve linked wallets, keeping the same repos and clock.
	forum := services.NewForumService(e.repo, env.accounts, env.credits)
	forum.SetClock(e.clock)
	forum.SetActivityRepo(e.activity)
	reports := repository.NewMemoryBoardReportRepository()
	forum.SetReputationService(services.NewReputationService(env.repRepo, e.repo, reports, e.clock))
	forum.SetReportRepo(reports)
	forum.SetWatchRepo(repository.NewMemoryBoardWatchRepository())
	forum.SetPeerRepo(e.peers)
	forum.SetPlatformPeers([]string{"platform"})
	forum.SetTokenOfferDeps(env.launches, env.wallets, repository.NewMemoryTokenOfferPaymentRepository(), env.verifier)
	e.forum = forum
	e.srv.portal.forumService = forum
	e.srv.forum.svc = forum
	return env
}

func (e *p1Env) named(t *testing.T, peerID, name string) string {
	t.Helper()
	if err := e.peers.Create(context.Background(), &models.Peer{PeerID: peerID, DisplayName: name}); err != nil {
		t.Fatalf("peer: %v", err)
	}
	return e.key(t, peerID)
}

func (e *p1Env) wallet(t *testing.T, peerID, address string) {
	t.Helper()
	ctx := context.Background()
	_ = e.accounts.Create(ctx, &models.Account{ID: "acct-" + peerID, PeerID: peerID, Status: models.AccountStatusActive, CreatedAt: e.clock.Now()})
	if err := e.wallets.LinkWallet(ctx, &models.AccountWallet{AccountID: "acct-" + peerID, WalletAddress: address, Chain: "solana"}); err != nil {
		t.Fatalf("wallet: %v", err)
	}
}

func (e *p1Env) reply(t *testing.T, apiKey, postID, body string) PortalReply {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/board/posts/"+postID+"/replies", map[string]interface{}{"body": body}, apiKey)
	if w.Code != http.StatusOK {
		t.Fatalf("reply: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data
}

func (e *p1Env) thread(t *testing.T, postID, apiKey string) []PortalReply {
	t.Helper()
	w := e.do(t, http.MethodGet, "/api/board/posts/"+postID+"/replies", nil, apiKey)
	if w.Code != http.StatusOK {
		t.Fatalf("thread: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data []PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data
}

func (e *p1Env) getPost(t *testing.T, postID, apiKey string) (PortalPost, int) {
	t.Helper()
	w := e.do(t, http.MethodGet, "/api/board/posts/"+postID, nil, apiKey)
	var env struct {
		Data PortalPost `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data, w.Code
}

func p1Data(t *testing.T, body []byte, v interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data: %v (%s)", err, env.Data)
	}
}

// --- Reputation exposure ---

func TestP1_ReputationFieldsOnPostsRepliesAndMe(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "Alice")
	bob := e.named(t, "bob", "Bob")
	_ = e.repRepo.Upsert(context.Background(), &models.PeerReputation{PeerID: "alice", Tier: "trusted", Score: 120, BountiesWon: 2, AnswersAccepted: 3, UpvotesReceived: 10, ComputedAt: e.clock.Now()})

	post := e.post(t, alice, map[string]interface{}{"body": "hello board"})
	if post.ReputationTier != "trusted" || post.ReputationScore == nil || *post.ReputationScore != 120 {
		t.Errorf("create response tier/score = %s/%v", post.ReputationTier, post.ReputationScore)
	}
	if post.AcceptedReplyID != nil || post.Hidden || post.Pinned || !post.Watching || post.Mentions == nil {
		t.Errorf("phase 1 defaults on create: %+v", post)
	}
	// Another viewer sees the tier but no score; anonymous the same.
	got, _ := e.getPost(t, post.ID, bob)
	if got.ReputationTier != "trusted" || got.ReputationScore != nil || got.Watching {
		t.Errorf("bob's view: %+v", got)
	}
	got, _ = e.getPost(t, post.ID, "")
	if got.ReputationTier != "trusted" || got.ReputationScore != nil {
		t.Errorf("anonymous view: %+v", got)
	}
	posts, _ := e.list(t, "?tab=recent", alice)
	if posts[0].ReputationScore == nil || *posts[0].ReputationScore != 120 || !posts[0].Watching {
		t.Errorf("list as author: %+v", posts[0])
	}
	// Replies carry the author's tier and only their own score.
	rp := e.reply(t, bob, post.ID, "hi")
	if rp.ReputationTier != "new" || rp.ReputationScore == nil || *rp.ReputationScore != 0 || rp.Accepted || rp.Hidden || rp.Mentions == nil {
		t.Errorf("reply as author: %+v", rp)
	}
	thread := e.thread(t, post.ID, alice)
	if thread[0].ReputationScore != nil || thread[0].ReputationTier != "new" || thread[0].AuthorDisplayName != "Bob" {
		t.Errorf("thread as other: %+v", thread[0])
	}

	// GET /api/peers/me and /api/peers/{id}/reputation.
	w := e.do(t, http.MethodGet, "/api/peers/me", nil, alice)
	var me PortalMe
	p1Data(t, w.Body.Bytes(), &me)
	if me.PeerID != "alice" || me.DisplayName != "Alice" || me.Platform || me.ReputationTier != "trusted" || me.ReputationScore != 120 {
		t.Errorf("me = %+v", me)
	}
	if w := e.do(t, http.MethodGet, "/api/peers/me", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("me without key: %d", w.Code)
	}
	w = e.do(t, http.MethodGet, "/api/peers/alice/reputation", nil, alice)
	var rep PeerReputationResponse
	p1Data(t, w.Body.Bytes(), &rep)
	if rep.ReputationTier != "trusted" || rep.Score == nil || *rep.Score != 120 || rep.BountiesWon != 2 || rep.AnswersAccepted != 3 || rep.UpvotesReceived != 10 || rep.ComputedAt == nil {
		t.Errorf("own reputation = %+v", rep)
	}
	w = e.do(t, http.MethodGet, "/api/peers/alice/reputation", nil, bob)
	var asOther PeerReputationResponse
	p1Data(t, w.Body.Bytes(), &asOther)
	if asOther.Score != nil || asOther.ReputationTier != "trusted" {
		t.Errorf("reputation as other = %+v", asOther)
	}
	w = e.do(t, http.MethodGet, "/api/peers/nobody/reputation", nil, "")
	var unknown PeerReputationResponse
	p1Data(t, w.Body.Bytes(), &unknown)
	if unknown.ReputationTier != "new" || unknown.ComputedAt != nil {
		t.Errorf("unknown peer reputation = %+v", unknown)
	}
}

// --- Accept ---

func TestP1_Accept(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "Alice")
	bob := e.named(t, "bob", "Bob")
	post := e.post(t, alice, map[string]interface{}{"body": "question"})
	own := e.reply(t, alice, post.ID, "own")
	answer := e.reply(t, bob, post.ID, "answer")

	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/accept", map[string]string{"reply_id": answer.ID}, bob); w.Code != http.StatusForbidden || errCode(t, w) != "ACCEPT_NOT_AUTHOR" {
		t.Errorf("not author: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/accept", map[string]string{"reply_id": own.ID}, alice); w.Code != http.StatusConflict || errCode(t, w) != "ACCEPT_OWN_REPLY" {
		t.Errorf("own reply: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/accept", map[string]string{"reply_id": "nope"}, alice); w.Code != http.StatusNotFound || errCode(t, w) != "NOT_FOUND" {
		t.Errorf("unknown reply: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/accept", map[string]string{}, alice); w.Code != http.StatusBadRequest {
		t.Errorf("missing reply_id: %d", w.Code)
	}
	w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/accept", map[string]string{"reply_id": answer.ID}, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var got PortalPost
	p1Data(t, w.Body.Bytes(), &got)
	if got.AcceptedReplyID == nil || *got.AcceptedReplyID != answer.ID {
		t.Errorf("accepted_reply_id = %v", got.AcceptedReplyID)
	}
	thread := e.thread(t, post.ID, "")
	if !thread[1].Accepted || thread[0].Accepted {
		t.Errorf("thread accepted flags: %+v", thread)
	}
	w = e.do(t, http.MethodGet, "/api/activity", nil, bob)
	var feed PortalActivityResponse
	p1Data(t, w.Body.Bytes(), &feed)
	found := false
	for _, it := range feed.Items {
		if it.Kind == models.ActivityReplyAccepted && it.ActorDisplayName == "Alice" && it.ActorReputationTier == "new" {
			found = true
		}
	}
	if !found {
		t.Errorf("reply_accepted activity missing: %+v", feed.Items)
	}
}

// --- Reports, pin, hide ---

func TestP1_ReportsAndPlatformModeration(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "Alice")
	bob := e.named(t, "bob", "Bob")
	platform := e.named(t, "platform", "Platform")
	post := e.post(t, alice, map[string]interface{}{"body": "spammy"})
	rp := e.reply(t, alice, post.ID, "more spam")

	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/report", map[string]string{"reason": "spam"}, alice); w.Code != http.StatusConflict || errCode(t, w) != "REPORT_OWN" {
		t.Errorf("own: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/report", map[string]string{"reason": "nah"}, bob); w.Code != http.StatusBadRequest {
		t.Errorf("bad reason: %d", w.Code)
	}
	w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/report", map[string]string{"reason": "spam", "note": "ad"}, bob)
	if w.Code != http.StatusOK {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	var res PortalReportResult
	p1Data(t, w.Body.Bytes(), &res)
	if res.ID == "" || res.Status != "open" || res.Hidden {
		t.Errorf("report result = %+v", res)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/report", map[string]string{"reason": "abuse"}, bob); w.Code != http.StatusConflict || errCode(t, w) != "REPORT_DUPLICATE" {
		t.Errorf("duplicate: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/replies/"+rp.ID+"/report", map[string]string{"reason": "scam"}, bob); w.Code != http.StatusOK {
		t.Errorf("report reply: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/replies/nope/report", map[string]string{"reason": "scam"}, bob); w.Code != http.StatusNotFound {
		t.Errorf("report unknown reply: %d", w.Code)
	}

	// Platform-only routes refuse everyone else.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/board/reports?status=open"},
		{http.MethodPost, "/api/board/posts/" + post.ID + "/pin"},
		{http.MethodDelete, "/api/board/posts/" + post.ID + "/pin"},
		{http.MethodPost, "/api/board/posts/" + post.ID + "/hide"},
		{http.MethodDelete, "/api/board/posts/" + post.ID + "/hide"},
		{http.MethodPost, "/api/board/replies/" + rp.ID + "/hide"},
		{http.MethodPost, "/api/board/reports/" + res.ID + "/uphold"},
		{http.MethodPost, "/api/board/reports/" + res.ID + "/dismiss"},
	} {
		if w := e.do(t, route.method, route.path, nil, bob); w.Code != http.StatusForbidden || errCode(t, w) != "NOT_PLATFORM" {
			t.Errorf("%s %s as bob: %d %s", route.method, route.path, w.Code, w.Body.String())
		}
		if w := e.do(t, route.method, route.path, nil, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous: %d", route.method, route.path, w.Code)
		}
	}

	// Reports list with reporter name and tier.
	w = e.do(t, http.MethodGet, "/api/board/reports?status=open", nil, platform)
	var items []PortalReportItem
	p1Data(t, w.Body.Bytes(), &items)
	if len(items) != 2 || items[0].ReporterDisplayName != "Bob" || items[0].ReporterTier != "new" || items[0].TargetAuthorPeerID != "alice" {
		t.Errorf("reports = %+v", items)
	}
	if w := e.do(t, http.MethodGet, "/api/board/reports?status=weird", nil, platform); w.Code != http.StatusBadRequest {
		t.Errorf("bad status: %d", w.Code)
	}

	// Pin: first in the feed with pinned: true; unpin.
	e.clock.Advance(time.Second)
	other := e.post(t, bob, map[string]interface{}{"body": "newer"})
	w = e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/pin", nil, platform)
	if w.Code != http.StatusOK {
		t.Fatalf("pin: %d %s", w.Code, w.Body.String())
	}
	posts, _ := e.list(t, "?tab=recent", "")
	if posts[0].ID != post.ID || !posts[0].Pinned || posts[1].ID != other.ID {
		t.Errorf("pinned feed: %v", titles(posts))
	}
	posts, _ = e.list(t, "?tab=top", "")
	if posts[0].ID != post.ID {
		t.Errorf("pinned top: %v", titles(posts))
	}
	if w := e.do(t, http.MethodDelete, "/api/board/posts/"+post.ID+"/pin", nil, platform); w.Code != http.StatusOK {
		t.Errorf("unpin: %d", w.Code)
	}
	posts, _ = e.list(t, "?tab=recent", "")
	if posts[0].ID != other.ID || posts[1].Pinned {
		t.Errorf("unpinned feed: %v", titles(posts))
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/nope/pin", nil, platform); w.Code != http.StatusNotFound {
		t.Errorf("pin unknown: %d", w.Code)
	}

	// Hide: the post leaves the feed and the thread 404s for others; the author and the
	// platform still see it with hidden: true.
	w = e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/hide", nil, platform)
	var hidden PortalPost
	p1Data(t, w.Body.Bytes(), &hidden)
	if w.Code != http.StatusOK || !hidden.Hidden {
		t.Fatalf("hide: %d %+v", w.Code, hidden)
	}
	if _, total := e.list(t, "?tab=recent", ""); total != 1 {
		t.Errorf("anonymous feed total = %d, want 1", total)
	}
	if _, total := e.list(t, "?tab=recent", bob); total != 1 {
		t.Errorf("bob feed total = %d, want 1", total)
	}
	if posts, total := e.list(t, "?tab=recent", alice); total != 2 || !posts[1].Hidden {
		t.Errorf("author feed: total=%d %+v", total, posts)
	}
	if _, total := e.list(t, "?tab=recent", platform); total != 2 {
		t.Errorf("platform feed total = %d, want 2", total)
	}
	if _, code := e.getPost(t, post.ID, bob); code != http.StatusNotFound {
		t.Errorf("hidden post GET as bob: %d", code)
	}
	if w := e.do(t, http.MethodGet, "/api/board/posts/"+post.ID+"/replies", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("hidden post thread anonymous: %d", w.Code)
	}
	if got, code := e.getPost(t, post.ID, alice); code != http.StatusOK || !got.Hidden {
		t.Errorf("hidden post GET as author: %d %+v", code, got)
	}
	if w := e.do(t, http.MethodDelete, "/api/board/posts/"+post.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Errorf("unhide: %d", w.Code)
	}
	if _, total := e.list(t, "?tab=recent", ""); total != 2 {
		t.Errorf("after unhide total = %d", total)
	}

	// Hide a reply: others do not see it, the author sees hidden: true.
	w = e.do(t, http.MethodPost, "/api/board/replies/"+rp.ID+"/hide", nil, platform)
	if w.Code != http.StatusOK {
		t.Fatalf("hide reply: %d %s", w.Code, w.Body.String())
	}
	if thread := e.thread(t, post.ID, bob); len(thread) != 0 {
		t.Errorf("hidden reply visible to bob: %+v", thread)
	}
	if thread := e.thread(t, post.ID, alice); len(thread) != 1 || !thread[0].Hidden {
		t.Errorf("hidden reply for author: %+v", thread)
	}
	if thread := e.thread(t, post.ID, platform); len(thread) != 1 {
		t.Errorf("hidden reply for platform: %+v", thread)
	}
	if w := e.do(t, http.MethodPost, "/api/board/replies/nope/hide", nil, platform); w.Code != http.StatusNotFound {
		t.Errorf("hide unknown reply: %d", w.Code)
	}

	// Uphold and dismiss.
	w = e.do(t, http.MethodPost, "/api/board/reports/"+res.ID+"/uphold", nil, platform)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"upheld"`) {
		t.Errorf("uphold: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/reports/"+res.ID+"/dismiss", nil, platform); w.Code != http.StatusConflict || errCode(t, w) != "REPORT_NOT_OPEN" {
		t.Errorf("resolve twice: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/reports/nope/dismiss", nil, platform); w.Code != http.StatusNotFound {
		t.Errorf("dismiss unknown: %d", w.Code)
	}
	if got, _ := e.getPost(t, post.ID, alice); !got.Hidden {
		t.Error("upheld report must hide the post")
	}
	w = e.do(t, http.MethodGet, "/api/peers/alice/reputation", nil, "")
	var rep PeerReputationResponse
	p1Data(t, w.Body.Bytes(), &rep)
	if rep.ReputationTier != "new" {
		t.Errorf("alice tier after uphold = %s", rep.ReputationTier)
	}
	// me reports platform: true.
	w = e.do(t, http.MethodGet, "/api/peers/me", nil, platform)
	var me PortalMe
	p1Data(t, w.Body.Bytes(), &me)
	if !me.Platform {
		t.Error("platform peer must see platform: true")
	}
}

// --- Watch and mentions ---

func TestP1_WatchAndMentions(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "Alice")
	bob := e.named(t, "bob", "bob_b")
	carol := e.named(t, "carol", "Carol")
	e.named(t, "dave", "dave")

	post := e.post(t, alice, map[string]interface{}{"body": "hey @bob_b and @Carol look"})
	if len(post.Mentions) != 2 || post.Mentions[0].PeerID != "bob" || post.Mentions[0].DisplayName != "bob_b" || post.Mentions[1].DisplayName != "Carol" {
		t.Errorf("post mentions = %+v", post.Mentions)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/watch", nil, carol); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"watching":true`) {
		t.Errorf("watch: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/nope/watch", nil, carol); w.Code != http.StatusNotFound {
		t.Errorf("watch unknown: %d", w.Code)
	}
	if got, _ := e.getPost(t, post.ID, carol); !got.Watching {
		t.Error("carol should be watching")
	}
	rp := e.reply(t, bob, post.ID, "thanks @Alice @dave @bob_b")
	if len(rp.Mentions) != 2 || rp.Mentions[0].PeerID != "alice" || rp.Mentions[1].PeerID != "dave" {
		t.Errorf("reply mentions (self excluded) = %+v", rp.Mentions)
	}
	// Carol watches and is not the author or replier: reply_in_watched. Dave: mentioned.
	w := e.do(t, http.MethodGet, "/api/activity", nil, carol)
	var feed PortalActivityResponse
	p1Data(t, w.Body.Bytes(), &feed)
	kinds := map[string]int{}
	for _, it := range feed.Items {
		kinds[it.Kind]++
	}
	if kinds[models.ActivityReplyInWatched] != 1 || kinds[models.ActivityMentioned] != 1 {
		t.Errorf("carol kinds = %v", kinds)
	}
	w = e.do(t, http.MethodGet, "/api/activity", nil, e.key(t, "dave"))
	p1Data(t, w.Body.Bytes(), &feed)
	if len(feed.Items) != 1 || feed.Items[0].Kind != models.ActivityMentioned || feed.Items[0].ReplyID == nil {
		t.Errorf("dave feed = %+v", feed.Items)
	}
	if w := e.do(t, http.MethodDelete, "/api/board/posts/"+post.ID+"/watch", nil, carol); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"watching":false`) {
		t.Errorf("unwatch: %d %s", w.Code, w.Body.String())
	}
	if got, _ := e.getPost(t, post.ID, carol); got.Watching {
		t.Error("carol should not be watching")
	}

	// Autocomplete.
	w = e.do(t, http.MethodGet, "/api/peers/display-names?q=@b&limit=8", nil, "")
	var matches []PortalDisplayNameMatch
	p1Data(t, w.Body.Bytes(), &matches)
	if len(matches) != 1 || matches[0].PeerID != "bob" || matches[0].DisplayName != "bob_b" || matches[0].ReputationTier != "new" {
		t.Errorf("display names = %+v", matches)
	}
	w = e.do(t, http.MethodGet, "/api/peers/display-names?q=A", nil, "")
	p1Data(t, w.Body.Bytes(), &matches)
	if len(matches) != 1 || matches[0].DisplayName != "Alice" {
		t.Errorf("case-insensitive prefix = %+v", matches)
	}
	if w := e.do(t, http.MethodGet, "/api/peers/display-names", nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("missing q: %d", w.Code)
	}
	if w := e.do(t, http.MethodGet, "/api/peers/display-names?q=a&limit=99", nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("limit too high: %d", w.Code)
	}
}

// --- Token offers ---

func TestP1_TokenOfferCreateAndPay(t *testing.T) {
	e := newP1Env(t)
	agent := e.named(t, "agent", "Agent")
	bob := e.named(t, "bob", "Bob")
	carol := e.named(t, "carol", "Carol")
	_ = e.launches.Create(context.Background(), &models.TokenLaunch{Mint: "MintA", Symbol: "AAA", CreatorWallet: "walletA", LaunchSignature: "s1", TransferFeeBps: 100, PeerID: "agent", Status: models.LaunchStatusBound})
	e.wallet(t, "bob", "walletBob")

	body := func(mint string, amount int64, max int) map[string]interface{} {
		return map[string]interface{}{"body": "offer", "category": "token-offer",
			"token_offer": map[string]interface{}{"mint": mint, "amount_per_reply": amount, "max_accepts": max}}
	}
	// B-2: the token's agent needs a linked wallet too.
	if w := e.do(t, http.MethodPost, "/api/board/posts", body("MintA", 10, 1), agent); w.Code != http.StatusBadRequest || errCode(t, w) != "TOKEN_OFFER_NO_WALLET" {
		t.Errorf("agent without wallet: %d %s", w.Code, w.Body.String())
	}
	e.wallet(t, "agent", "walletA")
	if w := e.do(t, http.MethodPost, "/api/board/posts", body("Nope", 10, 1), agent); w.Code != http.StatusBadRequest || errCode(t, w) != "TOKEN_OFFER_UNKNOWN_MINT" {
		t.Errorf("unknown mint: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts", body("MintA", 10, 1), carol); w.Code != http.StatusBadRequest || errCode(t, w) != "TOKEN_OFFER_NO_WALLET" {
		t.Errorf("poster without wallet: %d %s", w.Code, w.Body.String())
	}
	// Range errors name the field: amount_per_reply < 1, max_accepts outside 1..100.
	for _, c := range []struct {
		name   string
		amount int64
		max    int
		want   string
	}{
		{"zero amount", 0, 1, "amount_per_reply must be at least 1"},
		{"zero max", 10, 0, "max_accepts must be between 1 and 100"},
		{"max above 100", 10, 101, "max_accepts must be between 1 and 100"},
	} {
		w := e.do(t, http.MethodPost, "/api/board/posts", body("MintA", c.amount, c.max), agent)
		if w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION_ERROR" || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s, want VALIDATION_ERROR %q", c.name, w.Code, w.Body.String(), c.want)
		}
	}
	post := e.post(t, agent, body("MintA", 1_000_000, 2))
	offer := post.SettledTokenOffer
	if offer == nil || offer.Mint != "MintA" || offer.Symbol != "AAA" || offer.Decimals != 6 || offer.Amount != 1_000_000 || offer.Max != 2 || offer.Paid != 0 || offer.TokenProgram != services.TokenProgram2022 {
		t.Fatalf("token_offer = %+v", offer)
	}
	if post.TokenOffer == nil || post.TokenOffer.Token != "AAA" {
		t.Errorf("legacy tokenOffer mirror = %+v", post.TokenOffer)
	}

	bobs := e.reply(t, bob, post.ID, "b")
	carols := e.reply(t, carol, post.ID, "c")
	// The author sees author_wallet (null for carol) and token_offer_paid; others see null.
	thread := e.thread(t, post.ID, agent)
	if thread[0].AuthorWallet == nil || *thread[0].AuthorWallet != "walletBob" || thread[1].AuthorWallet != nil || thread[0].TokenOfferPaid {
		t.Errorf("author thread = %+v", thread)
	}
	if other := e.thread(t, post.ID, bob); other[0].AuthorWallet != nil {
		t.Errorf("wallet leaked to non-author: %+v", other)
	}

	pay := func(key, replyID, sig string) (int, string) {
		w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/token-offer/pay", map[string]string{"reply_id": replyID, "signature": sig}, key)
		if w.Code == http.StatusOK {
			return w.Code, w.Body.String()
		}
		return w.Code, errCode(t, w)
	}
	if code, c := pay(bob, bobs.ID, "sig1"); code != http.StatusForbidden || c != "TOKEN_OFFER_NOT_AUTHOR" {
		t.Errorf("not author: %d %s", code, c)
	}
	if code, c := pay(agent, carols.ID, "sig1"); code != http.StatusBadRequest || c != "TOKEN_OFFER_NO_WALLET" {
		t.Errorf("no wallet: %d %s", code, c)
	}
	if code, c := pay(agent, "nope", "sig1"); code != http.StatusNotFound || c != "NOT_FOUND" {
		t.Errorf("unknown reply: %d %s", code, c)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/token-offer/pay", map[string]string{"reply_id": bobs.ID}, agent); w.Code != http.StatusBadRequest {
		t.Errorf("missing signature: %d", w.Code)
	}
	e.verifier.NoMemo = true
	w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/token-offer/pay", map[string]string{"reply_id": bobs.ID, "signature": "sig1"}, agent)
	if w.Code != http.StatusUnprocessableEntity || errCode(t, w) != "TOKEN_OFFER_TX_INVALID" || !strings.Contains(w.Body.String(), "memo") {
		t.Errorf("tx invalid: %d %s", w.Code, w.Body.String())
	}
	e.verifier.NoMemo = false
	w = e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/token-offer/pay", map[string]string{"reply_id": bobs.ID, "signature": "sig1"}, agent)
	if w.Code != http.StatusOK {
		t.Fatalf("pay: %d %s", w.Code, w.Body.String())
	}
	var paid struct {
		Payment struct {
			Signature string `json:"signature"`
			Amount    int64  `json:"amount"`
			ToWallet  string `json:"to_wallet"`
		} `json:"payment"`
		Post PortalPost `json:"post"`
	}
	p1Data(t, w.Body.Bytes(), &paid)
	if paid.Payment.Signature != "sig1" || paid.Payment.Amount != 1_000_000 || paid.Payment.ToWallet != "walletBob" || paid.Post.SettledTokenOffer.Paid != 1 {
		t.Errorf("pay response = %+v", paid)
	}
	if thread := e.thread(t, post.ID, agent); !thread[0].TokenOfferPaid || thread[1].TokenOfferPaid {
		t.Errorf("paid flags = %+v", thread)
	}
	if code, c := pay(agent, bobs.ID, "sig2"); code != http.StatusConflict || c != "TOKEN_OFFER_ALREADY_PAID" {
		t.Errorf("already paid: %d %s", code, c)
	}
	e.wallet(t, "carol", "walletCarol")
	if code, c := pay(agent, carols.ID, "sig1"); code != http.StatusUnprocessableEntity || c != "TOKEN_OFFER_TX_INVALID" {
		t.Errorf("signature reuse: %d %s", code, c)
	}
	if code, _ := pay(agent, carols.ID, "sig2"); code != http.StatusOK {
		t.Errorf("pay carol: %d", code)
	}
	dave := e.named(t, "dave", "Dave")
	e.wallet(t, "dave", "walletDave")
	daves := e.reply(t, dave, post.ID, "d")
	if code, c := pay(agent, daves.ID, "sig3"); code != http.StatusConflict || c != "TOKEN_OFFER_EXHAUSTED" {
		t.Errorf("exhausted: %d %s", code, c)
	}
	// Bob's activity carries the amount and symbol.
	w = e.do(t, http.MethodGet, "/api/activity", nil, bob)
	var feed PortalActivityResponse
	p1Data(t, w.Body.Bytes(), &feed)
	found := false
	for _, it := range feed.Items {
		if it.Kind == models.ActivityTokenOfferPaid && it.Symbol == "AAA" && it.Amount != nil && *it.Amount == 1_000_000 {
			found = true
		}
	}
	if !found {
		t.Errorf("token_offer_paid activity missing: %+v", feed.Items)
	}
}

// --- Tokens list and legacy forum carry the tier next to the display name ---

func TestP1_ReputationTierOnTokensAndLegacyForum(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "Alice")
	_ = e.repRepo.Upsert(context.Background(), &models.PeerReputation{PeerID: "alice", Tier: "top", Score: 900})

	tokenRepo := repository.NewMemoryTokenRepository()
	_ = tokenRepo.Create(context.Background(), &models.PeerToken{PeerID: "alice", TokenContractAddress: "MintAlice", TokenTicker: "ALC", TokenName: "Alice Coin", LaunchedAt: e.clock.Now()})
	tokenHandler := NewTokenHandler(tokenRepo)
	tokenHandler.SetPeerRepo(e.peers)
	tokenHandler.SetReputationService(e.forum.Reputation())
	forumHandler := NewForumHandler(e.forum, e.apiKeys)
	forumHandler.SetPeerRepo(e.peers)
	srv := NewServer(ServerDeps{TokenHandler: tokenHandler, ForumHandler: forumHandler, APIKeyRepo: e.apiKeys, Address: ":7842"})
	do := func(method, path, key string) map[string]interface{} {
		req := httptest.NewRequest(method, path, nil)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	list := do(http.MethodGet, "/api/tokens", "")["data"].([]interface{})
	if item := list[0].(map[string]interface{}); item["display_name"] != "Alice" || item["reputation_tier"] != "top" {
		t.Errorf("tokens list item = %v", item)
	}
	if one := do(http.MethodGet, "/api/peers/alice/token", "")["data"].(map[string]interface{}); one["reputation_tier"] != "top" {
		t.Errorf("peer token = %v", one)
	}

	post := e.post(t, alice, map[string]interface{}{"body": "legacy view"})
	legacy := do(http.MethodGet, "/api/v1/tracker/forum/posts/"+post.ID, "") // legacy route: no data envelope
	if legacy["author_display_name"] != "Alice" || legacy["author_reputation_tier"] != "top" {
		t.Errorf("legacy post = %v", legacy)
	}
	e.reply(t, alice, post.ID, "legacy reply")
	replies := do(http.MethodGet, "/api/v1/tracker/forum/posts/"+post.ID+"/replies", "")["data"].([]interface{})
	if rp := replies[0].(map[string]interface{}); rp["author_reputation_tier"] != "top" {
		t.Errorf("legacy reply = %v", rp)
	}
}

// --- Portal alignment: tokenOffer body key, report rows, activity decimals, me.wallet_address ---

func TestP1_PortalAlignment(t *testing.T) {
	e := newP1Env(t)
	agent := e.named(t, "agent", "Agent")
	bob := e.named(t, "bob", "Bob")
	platform := e.named(t, "platform", "Platform")
	_ = e.launches.Create(context.Background(), &models.TokenLaunch{Mint: "MintA", Symbol: "AAA", CreatorWallet: "walletA", LaunchSignature: "s1", PeerID: "agent", Status: models.LaunchStatusBound})
	e.wallet(t, "agent", "walletA")
	e.wallet(t, "bob", "walletBob")

	// Create with the legacy tokenOffer key carrying the settling fields.
	post := e.post(t, agent, map[string]interface{}{"body": "offer via tokenOffer", "category": "token-offer",
		"tokenOffer": map[string]interface{}{"mint": "MintA", "amount_per_reply": 2_500_000, "max_accepts": 3}})
	if post.SettledTokenOffer == nil || post.SettledTokenOffer.Amount != 2_500_000 || post.SettledTokenOffer.Max != 3 || post.SettledTokenOffer.Symbol != "AAA" {
		t.Fatalf("tokenOffer key not honoured: %+v", post.SettledTokenOffer)
	}
	// A legacy tokenOffer without a mint is still the free-text offer.
	legacy := e.post(t, agent, map[string]interface{}{"body": "legacy", "tokenOffer": map[string]interface{}{"amount": 5, "token": "$OLD"}})
	if legacy.SettledTokenOffer != nil || legacy.TokenOffer == nil || legacy.TokenOffer.Token != "$OLD" {
		t.Errorf("legacy offer = %+v / %+v", legacy.SettledTokenOffer, legacy.TokenOffer)
	}

	// me carries wallet_address (null without one).
	w := e.do(t, http.MethodGet, "/api/peers/me", nil, agent)
	var me PortalMe
	p1Data(t, w.Body.Bytes(), &me)
	if me.WalletAddress == nil || *me.WalletAddress != "walletA" {
		t.Errorf("me.wallet_address = %v", me.WalletAddress)
	}
	w = e.do(t, http.MethodGet, "/api/peers/me", nil, platform)
	p1Data(t, w.Body.Bytes(), &me)
	if me.WalletAddress != nil {
		t.Errorf("platform wallet_address = %v, want null", me.WalletAddress)
	}

	// token_offer_paid activity carries decimals next to amount and symbol.
	bobs := e.reply(t, bob, post.ID, strings.Repeat("reply body ", 20))
	w = e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/token-offer/pay", map[string]string{"reply_id": bobs.ID, "signature": "sigX"}, agent)
	if w.Code != http.StatusOK {
		t.Fatalf("pay: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodGet, "/api/activity", nil, bob)
	var feed PortalActivityResponse
	p1Data(t, w.Body.Bytes(), &feed)
	found := false
	for _, it := range feed.Items {
		if it.Kind == models.ActivityTokenOfferPaid {
			found = true
			if it.Decimals == nil || *it.Decimals != 6 || it.Symbol != "AAA" || it.Amount == nil || *it.Amount != 2_500_000 {
				t.Errorf("token_offer_paid item = %+v", it)
			}
		} else if it.Decimals != nil {
			t.Errorf("decimals on %s", it.Kind)
		}
	}
	if !found {
		t.Error("token_offer_paid activity missing")
	}

	// Report rows carry post_id (parent for a reply) and a 140 char excerpt.
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/report", map[string]string{"reason": "spam"}, bob); w.Code != http.StatusOK {
		t.Fatalf("report post: %d", w.Code)
	}
	if w := e.do(t, http.MethodPost, "/api/board/replies/"+bobs.ID+"/report", map[string]string{"reason": "abuse"}, agent); w.Code != http.StatusOK {
		t.Fatalf("report reply: %d", w.Code)
	}
	w = e.do(t, http.MethodGet, "/api/board/reports?status=open", nil, platform)
	var items []PortalReportItem
	p1Data(t, w.Body.Bytes(), &items)
	if len(items) != 2 {
		t.Fatalf("reports = %d", len(items))
	}
	for _, it := range items {
		if it.PostID != post.ID {
			t.Errorf("%s report post_id = %s, want %s", it.TargetType, it.PostID, post.ID)
		}
		switch it.TargetType {
		case "post":
			if it.Excerpt != "offer via tokenOffer" {
				t.Errorf("post excerpt = %q", it.Excerpt)
			}
		case "reply":
			if len(it.Excerpt) != ReportExcerptRunes || !strings.HasPrefix(it.Excerpt, "reply body") {
				t.Errorf("reply excerpt = %q (%d)", it.Excerpt, len(it.Excerpt))
			}
		}
	}
}
