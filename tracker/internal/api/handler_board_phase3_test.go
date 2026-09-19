// Package api: Community board phase 3 (Autopilot v2) HTTP tests: the reply ask on both
// reply endpoints and in the thread, POST /api/board/posts/{id}/bounty/raise with its error
// codes and the bounty_ask / bounty_raised activity kinds, GET /api/v1/tracker/autopilot/outcomes
// with since= and the summary endpoint.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// p3Env is boardEnv with accounts, credits (escrow and refunds) and reports wired.
type p3Env struct {
	*boardEnv
	accounts *repository.MemoryAccountRepository
	credits  *repository.MemoryCreditRepository
	reports  *repository.MemoryBoardReportRepository
}

func newP3Env(t *testing.T) *p3Env {
	t.Helper()
	e := newBoardEnv(t)
	env := &p3Env{
		boardEnv: e,
		accounts: repository.NewMemoryAccountRepository(),
		credits:  repository.NewMemoryCreditRepositoryWithClock(e.clock),
		reports:  repository.NewMemoryBoardReportRepository(),
	}
	forum := services.NewForumService(e.repo, env.accounts, env.credits)
	forum.SetClock(e.clock)
	forum.SetActivityRepo(e.activity)
	forum.SetReportRepo(env.reports)
	forum.SetCreditRefunder(services.NewCreditService(services.CreditServiceDeps{Credits: env.credits, Accounts: env.accounts, Clock: e.clock, JWTSecret: "secret"}))
	e.forum = forum
	e.srv.portal.forumService = forum
	e.srv.forum.svc = forum
	return env
}

// fund creates an account for peerID with free credits and returns the peer's API key.
func (e *p3Env) fund(t *testing.T, peerID string, free int) string {
	t.Helper()
	ctx := context.Background()
	id := "acct-" + peerID
	if err := e.accounts.Create(ctx, &models.Account{ID: id, PeerID: peerID, Status: models.AccountStatusActive, CreatedAt: e.clock.Now()}); err != nil {
		t.Fatalf("account: %v", err)
	}
	if err := e.credits.CreateBalance(ctx, &models.CreditBalance{AccountID: id}); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if free > 0 {
		if err := e.credits.CreditFree(ctx, id, free, "test", "seed-"+peerID, e.clock.Now().Add(30*24*time.Hour)); err != nil {
			t.Fatalf("credit: %v", err)
		}
	}
	return e.key(t, peerID)
}

func (e *p3Env) balance(t *testing.T, peerID string) int {
	t.Helper()
	bal, err := e.credits.GetBalance(context.Background(), "acct-"+peerID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return bal.FreeBalance + bal.PaidBalance
}

func (e *p3Env) replyWith(t *testing.T, apiKey, postID string, body map[string]interface{}) (PortalReply, int, string) {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/board/posts/"+postID+"/replies", body, apiKey)
	if w.Code != http.StatusOK {
		return PortalReply{}, w.Code, errCode(t, w)
	}
	var env struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data, w.Code, ""
}

func (e *p3Env) raise(t *testing.T, apiKey, postID string, amount int) (PortalPost, int, string) {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/board/posts/"+postID+"/bounty/raise", map[string]interface{}{"amount": amount}, apiKey)
	if w.Code != http.StatusOK {
		return PortalPost{}, w.Code, errCode(t, w)
	}
	var env struct {
		Data PortalPost `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data, w.Code, ""
}

func (e *p3Env) feed(t *testing.T, apiKey, kind string) []PortalActivityItem {
	t.Helper()
	var feed PortalActivityResponse
	p1Data(t, e.do(t, http.MethodGet, "/api/activity?kinds="+kind, nil, apiKey).Body.Bytes(), &feed)
	return feed.Items
}

func TestP3_ReplyAsk_BothEndpointsAndThread(t *testing.T) {
	e := newP3Env(t)
	alice, bob := e.key(t, "alice"), e.key(t, "bob")
	request := e.post(t, alice, map[string]interface{}{"body": "need a dataset", "category": "request"})
	general := e.post(t, alice, map[string]interface{}{"body": "chit chat"})

	rp, code, _ := e.replyWith(t, bob, request.ID, map[string]interface{}{"body": "I have it", "ask": 60})
	if code != http.StatusOK || rp.Ask != 60 {
		t.Fatalf("portal reply with ask: %d %+v", code, rp)
	}
	// The thread carries the ask; a reply without one omits the field.
	plain, _, _ := e.replyWith(t, bob, request.ID, map[string]interface{}{"body": "plain"})
	w := e.do(t, http.MethodGet, "/api/board/posts/"+request.ID+"/replies", nil, "")
	var thread struct {
		Data []PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &thread)
	if len(thread.Data) != 2 || thread.Data[0].ID != rp.ID || thread.Data[0].Ask != 60 || thread.Data[1].ID != plain.ID || thread.Data[1].Ask != 0 {
		t.Errorf("thread = %+v", thread.Data)
	}
	if body := w.Body.String(); countOf(body, `"ask":60`) != 1 || countOf(body, `"ask":0`) != 0 {
		t.Errorf("ask omitted when 0 / present when set: %s", body)
	}
	// v1 endpoint: same field, snake_case shape.
	w = e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+request.ID+"/replies", map[string]interface{}{"body": "v1 ask", "ask": 25, "auto": true}, bob)
	var v1 ReplyDTO
	_ = json.Unmarshal(w.Body.Bytes(), &v1)
	if w.Code != http.StatusCreated || v1.Ask != 25 || !v1.Auto {
		t.Errorf("v1 reply with ask: %d %s", w.Code, w.Body.String())
	}
	// The author gets one bounty_ask per ask (amount = ask, reply_id set).
	asks := e.feed(t, alice, models.ActivityBountyAsk)
	byAmount := map[int]PortalActivityItem{}
	for _, a := range asks {
		if a.Amount != nil {
			byAmount[*a.Amount] = a
		}
	}
	if len(asks) != 2 || byAmount[25].ReplyID == nil || *byAmount[25].ReplyID != v1.ID || byAmount[60].ReplyID == nil || *byAmount[60].ReplyID != rp.ID || byAmount[60].ActorPeerID != "bob" {
		t.Errorf("bounty_ask feed = %+v", asks)
	}
	// Not on a general post, not out of range: 400 VALIDATION_ERROR on both endpoints.
	if _, code, ec := e.replyWith(t, bob, general.ID, map[string]interface{}{"body": "x", "ask": 10}); code != http.StatusBadRequest || ec != "VALIDATION_ERROR" {
		t.Errorf("ask on general = %d %s", code, ec)
	}
	if _, code, ec := e.replyWith(t, bob, request.ID, map[string]interface{}{"body": "x", "ask": services.MaxBountyAmount + 1}); code != http.StatusBadRequest || ec != "VALIDATION_ERROR" {
		t.Errorf("ask above max = %d %s", code, ec)
	}
	if w := e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+general.ID+"/replies", map[string]interface{}{"body": "x", "ask": 10}, bob); w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION_ERROR" {
		t.Errorf("v1 ask on general = %d %s", w.Code, w.Body.String())
	}
	// kinds= knows the new kinds.
	for _, kind := range []string{models.ActivityBountyAsk, models.ActivityBountyRaised} {
		if w := e.do(t, http.MethodGet, "/api/activity?kinds="+kind, nil, alice); w.Code != http.StatusOK {
			t.Errorf("kinds=%s: %d", kind, w.Code)
		}
	}
}

func countOf(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}

func TestP3_RaiseBounty_HTTP(t *testing.T) {
	e := newP3Env(t)
	alice := e.fund(t, "alice", 300)
	bob, carol := e.key(t, "bob"), e.key(t, "carol")
	post := e.bounty(t, alice, "find me a dataset", 100, 7)
	if e.balance(t, "alice") != 200 {
		t.Fatalf("setup escrow: balance=%d", e.balance(t, "alice"))
	}
	if _, code, _ := e.replyWith(t, bob, post.ID, map[string]interface{}{"body": "I can", "ask": 150}); code != http.StatusOK {
		t.Fatal("bob ask")
	}
	if _, code, _ := e.replyWith(t, carol, post.ID, map[string]interface{}{"body": "no ask"}); code != http.StatusOK {
		t.Fatal("carol reply")
	}

	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/bounty/raise", map[string]interface{}{"amount": 150}, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no key = %d", w.Code)
	}
	if _, code, ec := e.raise(t, bob, post.ID, 150); code != http.StatusForbidden || ec != "BOUNTY_NOT_AUTHOR" {
		t.Errorf("not author = %d %s", code, ec)
	}
	for _, amount := range []int{0, -5, 100, 50, services.MaxBountyAmount + 1} {
		if _, code, ec := e.raise(t, alice, post.ID, amount); code != http.StatusBadRequest || ec != "VALIDATION_ERROR" {
			t.Errorf("amount %d = %d %s", amount, code, ec)
		}
	}
	if _, code, ec := e.raise(t, alice, post.ID, 400); code != http.StatusPaymentRequired || ec != "INSUFFICIENT_CREDITS" {
		t.Errorf("insufficient = %d %s", code, ec)
	}
	if _, code, ec := e.raise(t, alice, "missing", 150); code != http.StatusNotFound || ec != "NOT_FOUND" {
		t.Errorf("missing = %d %s", code, ec)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/bounty/raise", "not json", alice); w.Code != http.StatusBadRequest {
		t.Errorf("bad body = %d", w.Code)
	}
	if e.balance(t, "alice") != 200 {
		t.Errorf("refusals moved credits: %d", e.balance(t, "alice"))
	}

	// The deadline is mock clock + 7 days and daysRemaining reads the same clock (models.Now): int(168h / 24) + 1.
	raised, code, _ := e.raise(t, alice, post.ID, 150)
	if code != http.StatusOK || raised.Bounty == nil || raised.Bounty.Amount != 150 || raised.Bounty.Status != "open" || raised.Bounty.DaysRemaining != 8 {
		t.Fatalf("raise = %d %+v", code, raised.Bounty)
	}
	if e.balance(t, "alice") != 150 {
		t.Errorf("difference not escrowed: balance=%d, want 150", e.balance(t, "alice"))
	}
	got, _ := e.do(t, http.MethodGet, "/api/board/posts/"+post.ID, nil, "").Body.Bytes(), 0
	var single struct {
		Data PortalPost `json:"data"`
	}
	_ = json.Unmarshal(got, &single)
	if single.Data.Bounty == nil || single.Data.Bounty.Amount != 150 {
		t.Errorf("GET after raise = %+v", single.Data.Bounty)
	}
	// bounty_raised to bob (asked), not to carol (no ask) nor alice.
	if rows := e.feed(t, bob, models.ActivityBountyRaised); len(rows) != 1 || rows[0].Amount == nil || *rows[0].Amount != 150 || rows[0].ActorPeerID != "" || rows[0].PostID != post.ID || rows[0].PostTitle == "" {
		t.Errorf("bob bounty_raised = %+v", rows)
	}
	if rows := e.feed(t, carol, models.ActivityBountyRaised); len(rows) != 0 {
		t.Errorf("carol got bounty_raised: %+v", rows)
	}
	// Completed bounty: 409 BOUNTY_NOT_OPEN.
	bobReply := e.thread(t, post.ID, "")[0]
	e.fund(t, "bob", 0)
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/award", map[string]string{"reply_id": bobReply.ID}, alice); w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}
	if _, code, ec := e.raise(t, alice, post.ID, 200); code != http.StatusConflict || ec != "BOUNTY_NOT_OPEN" {
		t.Errorf("completed = %d %s", code, ec)
	}

	// A Request without a bounty gets one with the default 7 day deadline.
	request := e.post(t, alice, map[string]interface{}{"body": "need help", "category": "request"})
	created, code, _ := e.raise(t, alice, request.ID, 50)
	if code != http.StatusOK || created.Bounty == nil || created.Bounty.Amount != 50 || created.Bounty.Currency != "credits" || created.Bounty.DaysRemaining != 8 || created.Bounty.ExpiresAt == nil {
		t.Errorf("new bounty = %d %+v", code, created.Bounty)
	}
	if e.balance(t, "alice") != 100 {
		t.Errorf("new bounty escrow: balance=%d, want 100", e.balance(t, "alice"))
	}
}

func (e *p3Env) thread(t *testing.T, postID, apiKey string) []PortalReply {
	t.Helper()
	w := e.do(t, http.MethodGet, "/api/board/posts/"+postID+"/replies", nil, apiKey)
	var env struct {
		Data []PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Data
}

func TestP3_AutopilotOutcomes_SinceAndSummaryHTTP(t *testing.T) {
	e := newP3Env(t)
	alice := e.fund(t, "alice", 500)
	agent := e.fund(t, "agent", 100)
	now := e.clock.Now()
	first := e.post(t, alice, map[string]interface{}{"body": "q1", "category": "request"})
	won := e.bounty(t, alice, "b1", 120, 7)
	r1, _, _ := e.replyWith(t, agent, first.ID, map[string]interface{}{"body": "a", "auto": true})
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+first.ID+"/accept", map[string]string{"reply_id": r1.ID}, alice); w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	e.clock.Advance(time.Hour)
	r2, _, _ := e.replyWith(t, agent, won.ID, map[string]interface{}{"body": "b", "auto": true})
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+won.ID+"/award", map[string]string{"reply_id": r2.ID}, alice); w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/replies/"+r2.ID+"/report", map[string]string{"reason": "spam"}, e.key(t, "carol")); w.Code != http.StatusOK {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}

	if w := e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes?since=yesterday", nil, agent); w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION_ERROR" {
		t.Errorf("bad since = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data []AutopilotOutcomeDTO `json:"data"`
	}
	w := e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes", nil, agent)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || len(out.Data) != 2 {
		t.Fatalf("outcomes = %d %s", w.Code, w.Body.String())
	}
	if o := out.Data[1]; o.ReplyID != r1.ID || !o.Accepted || o.Awarded || o.Category != "request" || o.BountyAmount != 0 || o.Reported {
		t.Errorf("accepted row = %+v", o)
	}
	if o := out.Data[0]; o.ReplyID != r2.ID || !o.Awarded || o.AwardedAmount != 120 || o.BountyAmount != 120 || !o.Reported || o.Hidden || o.Accepted {
		t.Errorf("awarded row = %+v", o)
	}
	w = e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes?since="+now.Add(30*time.Minute).Format(time.RFC3339), nil, agent)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || len(out.Data) != 1 || out.Data[0].ReplyID != r2.ID {
		t.Errorf("since outcomes = %d %s", w.Code, w.Body.String())
	}

	if w := e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes/summary", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("summary no key = %d", w.Code)
	}
	w = e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes/summary", nil, agent)
	var sum struct {
		Data models.AutopilotOutcomeSummary `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sum); err != nil || w.Code != http.StatusOK {
		t.Fatalf("summary = %d %s (%v)", w.Code, w.Body.String(), err)
	}
	s := sum.Data
	if s.Posted != 2 || s.Accepted != 1 || s.Awarded != 1 || s.Reported != 1 || s.Hidden != 0 || s.Upvoted != 0 || s.CreditsWon != 120 || s.CreditsSpent != 0 {
		t.Errorf("summary = %+v", s)
	}
	if s.ByCategory["request"] == nil || s.ByCategory["request"].Posted != 1 || s.ByCategory["request"].Won != 1 || s.ByCategory["bounty"] == nil || s.ByCategory["bounty"].Won != 1 {
		t.Errorf("by_category = %+v", s.ByCategory)
	}
	for _, key := range []string{`"posted"`, `"upvoted"`, `"accepted"`, `"awarded"`, `"hidden"`, `"reported"`, `"credits_spent"`, `"credits_won"`, `"by_category"`, `"won"`} {
		if countOf(w.Body.String(), key) == 0 {
			t.Errorf("summary lacks %s: %s", key, w.Body.String())
		}
	}
	// A peer with nothing: zeros and an empty object, never null.
	w = e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes/summary", nil, e.key(t, "nobody"))
	if w.Code != http.StatusOK || countOf(w.Body.String(), `"by_category":{}`) != 1 {
		t.Errorf("empty summary = %d %s", w.Code, w.Body.String())
	}
}

func TestP3_ReplyRelevance_StoredOnAutoRepliesAndEchoed(t *testing.T) {
	e := newP3Env(t)
	alice, agent := e.key(t, "alice"), e.key(t, "agent")
	request := e.post(t, alice, map[string]interface{}{"body": "need a dataset", "category": "request"})
	signals := map[string]interface{}{"library": 0.5, "history": 1, "instruction": 0, "routed": 1}

	rp, code, _ := e.replyWith(t, agent, request.ID, map[string]interface{}{"body": "auto", "auto": true, "relevance": 0.62, "relevance_signals": signals})
	if code != http.StatusOK || rp.Relevance == nil || *rp.Relevance != 0.62 || rp.RelevanceSignals == nil || rp.RelevanceSignals.Library != 0.5 || rp.RelevanceSignals.Routed != 1 {
		t.Fatalf("auto reply with relevance: %d %+v", code, rp)
	}
	// Manual replies drop it; a reply without it omits the fields.
	manual, _, _ := e.replyWith(t, e.key(t, "bob"), request.ID, map[string]interface{}{"body": "manual", "relevance": 0.9, "relevance_signals": signals})
	if manual.Relevance != nil || manual.RelevanceSignals != nil {
		t.Errorf("manual reply kept relevance: %+v", manual)
	}
	thread := e.thread(t, request.ID, "")
	if len(thread) != 2 || thread[0].Relevance == nil || *thread[0].Relevance != 0.62 || thread[0].RelevanceSignals == nil || thread[1].Relevance != nil {
		t.Errorf("thread relevance = %+v", thread)
	}
	w := e.do(t, http.MethodGet, "/api/board/posts/"+request.ID+"/replies", nil, "")
	if countOf(w.Body.String(), `"relevance"`) != 1 || countOf(w.Body.String(), `"relevance_signals"`) != 1 || countOf(w.Body.String(), `"instruction":0`) != 1 {
		t.Errorf("thread body relevance keys: %s", w.Body.String())
	}
	// Out of range: 400 VALIDATION_ERROR.
	if _, code, ec := e.replyWith(t, agent, request.ID, map[string]interface{}{"body": "x", "auto": false, "relevance": 1.5}); code != http.StatusBadRequest || ec != "VALIDATION_ERROR" {
		t.Errorf("relevance 1.5 = %d %s", code, ec)
	}
	// v1 endpoint carries it too, and the outcomes row echoes it.
	other := e.post(t, alice, map[string]interface{}{"body": "another", "category": "request"})
	w = e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+other.ID+"/replies", map[string]interface{}{"body": "v1", "auto": true, "relevance": 0.3}, agent)
	var v1 ReplyDTO
	_ = json.Unmarshal(w.Body.Bytes(), &v1)
	if w.Code != http.StatusCreated || v1.Relevance == nil || *v1.Relevance != 0.3 || v1.RelevanceSignals != nil {
		t.Errorf("v1 relevance: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data []AutopilotOutcomeDTO `json:"data"`
	}
	w = e.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes", nil, agent)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	byReply := map[string]AutopilotOutcomeDTO{}
	for _, o := range out.Data {
		byReply[o.ReplyID] = o
	}
	first, second := byReply[rp.ID], byReply[v1.ID]
	if len(out.Data) != 2 || first.Relevance == nil || *first.Relevance != 0.62 || first.RelevanceSignals == nil || first.RelevanceSignals.History != 1 ||
		second.Relevance == nil || *second.Relevance != 0.3 || second.RelevanceSignals != nil {
		t.Errorf("outcomes relevance = %s", w.Body.String())
	}
}
