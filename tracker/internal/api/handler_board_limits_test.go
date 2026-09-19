// Package api: the community board abuse limits (hardening pass). Every board write of one
// peer shares a budget of BoardWriteLimit per BoardWriteWindow and reports have their own
// BoardReportLimit per BoardReportWindow; the 429 body names the window; reads are never
// limited; the budget is per peer, not per address.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// newLimitedBoardEnv is newBoardEnv with a memory rate limiter on the server (and the phase 1
// repos the report route needs).
func newLimitedBoardEnv(t *testing.T) *boardEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	forumRepo := repository.NewMemoryForumRepository()
	activityRepo := repository.NewMemoryBoardActivityRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	store := presence.NewMemoryPresenceStore(clk)
	t.Cleanup(store.Close)

	peerSvc := services.NewPeerService(peerRepo, store, apiKeyRepo)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	forumSvc := services.NewForumService(forumRepo, nil, nil)
	forumSvc.SetClock(clk)
	forumSvc.SetActivityRepo(activityRepo)
	reports := repository.NewMemoryBoardReportRepository()
	forumSvc.SetReportRepo(reports)
	forumSvc.SetWatchRepo(repository.NewMemoryBoardWatchRepository())
	forumSvc.SetPeerRepo(peerRepo)
	forumSvc.SetReputationService(services.NewReputationService(repository.NewMemoryBoardReputationRepository(), forumRepo, reports, clk))

	repRepo := repository.NewMemoryReputationRepository()
	portal := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, repository.NewMemoryPeerTrustBlockRepository(),
		apiKeyRepo, repository.NewMemoryGuestKeyRepository(), repRepo, &geo.StubResolver{}, nil, nil, nil)
	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		PortalHandler: portal,
		ForumHandler:  NewForumHandler(forumSvc, apiKeyRepo),
		APIKeyRepo:    apiKeyRepo,
		Limiter:       ratelimit.NewMemoryLimiter(clk),
		Address:       ":7842",
	})
	return &boardEnv{srv: srv, clock: clk, forum: forumSvc, repo: forumRepo, activity: activityRepo, peers: peerRepo, rep: repRepo, apiKeys: apiKeyRepo}
}

func rateLimitedBody(t *testing.T, raw []byte) ratelimit.RateLimitedResponse {
	t.Helper()
	var body ratelimit.RateLimitedResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("429 body %s: %v", raw, err)
	}
	return body
}

func TestBoardLimits_WritesSharePerPeerBudgetAndNameTheWindow(t *testing.T) {
	e := newLimitedBoardEnv(t)
	alice, bob := e.key(t, "alice"), e.key(t, "bob")
	post := e.post(t, bob, map[string]interface{}{"body": "a thread to act on", "category": "general"})

	// Alice: watch, unwatch, upvote, reply and post all draw on one budget of BoardWriteLimit.
	paths := []struct{ method, path string }{
		{http.MethodPost, "/api/board/posts/" + post.ID + "/watch"},
		{http.MethodDelete, "/api/board/posts/" + post.ID + "/watch"},
		{http.MethodPost, "/api/board/posts/" + post.ID + "/upvote"},
		{http.MethodPost, "/api/board/posts/" + post.ID + "/replies"},
		{http.MethodPost, "/api/board/posts"},
	}
	bodies := map[string]interface{}{
		"/api/board/posts/" + post.ID + "/replies": map[string]interface{}{"body": "hi"},
		"/api/board/posts":                         map[string]interface{}{"body": "another thread", "category": "general"},
	}
	for i := 0; i < BoardWriteLimit; i++ {
		p := paths[i%len(paths)]
		w := e.do(t, p.method, p.path, bodies[p.path], alice)
		if w.Code != http.StatusOK {
			t.Fatalf("write %d (%s %s): %d %s", i+1, p.method, p.path, w.Code, w.Body.String())
		}
		if got := w.Header().Get("X-RateLimit-Limit"); got != fmt.Sprint(BoardWriteLimit) {
			t.Fatalf("write %d: X-RateLimit-Limit = %q", i+1, got)
		}
	}
	w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/watch", nil, alice)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("write %d: %d %s, want 429", BoardWriteLimit+1, w.Code, w.Body.String())
	}
	body := rateLimitedBody(t, w.Body.Bytes())
	if body.Error.Code != "RATE_LIMITED" || body.RetryAfterSeconds < 1 {
		t.Fatalf("429 body = %+v", body)
	}
	want := "Too many requests: limit is 30 board writes per 10 minutes. Try again in "
	if len(body.Error.Message) < len(want) || body.Error.Message[:len(want)] != want {
		t.Fatalf("429 message = %q, want prefix %q", body.Error.Message, want)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing")
	}
	// The other write routes are on the same budget: raise, accept, extend, award, pay.
	for _, p := range []string{"/bounty/raise", "/accept", "/bounty/extend", "/award", "/token-offer/pay"} {
		if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+p, map[string]interface{}{"amount": 10, "reply_id": "x", "signature": "y"}, alice); w.Code != http.StatusTooManyRequests {
			t.Errorf("%s while limited: %d, want 429", p, w.Code)
		}
	}
	// The legacy daemon route shares it too.
	if w := e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+post.ID+"/replies", map[string]interface{}{"body": "hi"}, alice); w.Code != http.StatusTooManyRequests {
		t.Errorf("legacy reply while limited: %d, want 429", w.Code)
	}
	// Reads are never limited, and bob has his own budget.
	if w := e.do(t, http.MethodGet, "/api/board/posts?tab=recent", nil, alice); w.Code != http.StatusOK {
		t.Errorf("read while limited: %d", w.Code)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/watch", nil, bob); w.Code != http.StatusOK {
		t.Errorf("bob's write: %d %s", w.Code, w.Body.String())
	}
	// The window passes and alice writes again.
	e.clock.Advance(BoardWriteWindow + time.Second)
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/watch", nil, alice); w.Code != http.StatusOK {
		t.Errorf("after the window: %d %s", w.Code, w.Body.String())
	}
}

func TestBoardLimits_ReportsHaveTheirOwnDailyBudget(t *testing.T) {
	e := newLimitedBoardEnv(t)
	alice, bob := e.key(t, "alice"), e.key(t, "bob")
	var posts []PortalPost
	for i := 0; i <= BoardReportLimit; i++ {
		posts = append(posts, e.post(t, bob, map[string]interface{}{"body": fmt.Sprintf("thread %d", i), "category": "general"}))
	}
	for i := 0; i < BoardReportLimit; i++ {
		w := e.do(t, http.MethodPost, "/api/board/posts/"+posts[i].ID+"/report", map[string]interface{}{"reason": "spam"}, alice)
		if w.Code != http.StatusOK {
			t.Fatalf("report %d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	w := e.do(t, http.MethodPost, "/api/board/posts/"+posts[BoardReportLimit].ID+"/report", map[string]interface{}{"reason": "spam"}, alice)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("report %d: %d %s, want 429", BoardReportLimit+1, w.Code, w.Body.String())
	}
	body := rateLimitedBody(t, w.Body.Bytes())
	want := "Too many requests: limit is 10 reports per day. Try again in "
	if len(body.Error.Message) < len(want) || body.Error.Message[:len(want)] != want {
		t.Fatalf("429 message = %q, want prefix %q", body.Error.Message, want)
	}
	// Reply reports share the report budget; board writes are a separate budget (still open).
	if w := e.do(t, http.MethodPost, "/api/board/replies/x/report", map[string]interface{}{"reason": "spam"}, alice); w.Code != http.StatusTooManyRequests {
		t.Errorf("reply report while limited: %d, want 429", w.Code)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+posts[0].ID+"/watch", nil, alice); w.Code != http.StatusOK {
		t.Errorf("write while report-limited: %d %s", w.Code, w.Body.String())
	}
	// Nothing was filed past the cap.
	reports, err := e.forum.ListReports(t.Context(), models.ReportStatusOpen, 100)
	if err != nil || len(reports) != BoardReportLimit {
		t.Fatalf("open reports = %d err %v, want %d", len(reports), err, BoardReportLimit)
	}
}

// TestBoardLimits_WithoutALimiterNothingIsLimited keeps the memory-backed handler tests honest:
// a server without a limiter wires the bare handlers.
func TestBoardLimits_WithoutALimiterNothingIsLimited(t *testing.T) {
	e := newBoardEnv(t)
	alice := e.key(t, "alice")
	post := e.post(t, alice, map[string]interface{}{"body": "a thread", "category": "general"})
	for i := 0; i < BoardWriteLimit+5; i++ {
		if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/watch", nil, alice); w.Code != http.StatusOK {
			t.Fatalf("write %d: %d", i+1, w.Code)
		}
	}
}
