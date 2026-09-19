// Package api: Community board phase 0 HTTP tests: feed filters on GET /api/board/posts,
// GET /api/board/counts, POST /api/board/posts/{id}/bounty/extend, the bounty DTO fields, and
// the activity feed (GET /api/activity, POST /api/activity/read).

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
	"github.com/stonkagents/agent/tracker/internal/services"
)

type boardEnv struct {
	srv      *Server
	clock    *clock.MockClock
	forum    *services.ForumService
	repo     *repository.MemoryForumRepository
	activity *repository.MemoryBoardActivityRepository
	peers    *repository.MemoryPeerRepository
	rep      *repository.MemoryReputationRepository
	apiKeys  repository.PeerAPIKeyRepository
}

func newBoardEnv(t *testing.T) *boardEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC))
	// Bounty ages (days remaining, expiry) read the model clock; pin it to the mock so the
	// expectations below hold on any calendar day, not only the one the test was written on.
	prevNow := models.Now
	models.Now = clk.Now
	t.Cleanup(func() { models.Now = prevNow })
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

	repRepo := repository.NewMemoryReputationRepository()
	portal := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, repository.NewMemoryPeerTrustBlockRepository(),
		apiKeyRepo, repository.NewMemoryGuestKeyRepository(), repRepo, &geo.StubResolver{}, nil, nil, nil)
	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		PortalHandler: portal,
		ForumHandler:  NewForumHandler(forumSvc, apiKeyRepo),
		APIKeyRepo:    apiKeyRepo,
		Address:       ":7842",
	})
	return &boardEnv{srv: srv, clock: clk, forum: forumSvc, repo: forumRepo, activity: activityRepo, peers: peerRepo, rep: repRepo, apiKeys: apiKeyRepo}
}

func (e *boardEnv) key(t *testing.T, peerID string) string {
	t.Helper()
	return seedAPIKey(t, e.apiKeys, peerID)
}

func (e *boardEnv) do(t *testing.T, method, path string, body interface{}, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	w := httptest.NewRecorder()
	e.srv.Router().ServeHTTP(w, req)
	return w
}

// post creates a board post through the API and returns the DTO.
func (e *boardEnv) post(t *testing.T, apiKey string, body map[string]interface{}) PortalPost {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/board/posts", body, apiKey)
	if w.Code != http.StatusOK {
		t.Fatalf("create post: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data PortalPost `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	return env.Data
}

func (e *boardEnv) bounty(t *testing.T, apiKey, text string, amount, days int) PortalPost {
	t.Helper()
	return e.post(t, apiKey, map[string]interface{}{
		"body": text, "category": "bounty",
		"bounty": map[string]interface{}{"amount": amount, "currency": "credits", "days": days},
	})
}

func (e *boardEnv) list(t *testing.T, query, apiKey string) ([]PortalPost, int) {
	t.Helper()
	w := e.do(t, http.MethodGet, "/api/board/posts"+query, nil, apiKey)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/board/posts%s: %d %s", query, w.Code, w.Body.String())
	}
	var env struct {
		Data []PortalPost   `json:"data"`
		Meta PaginationMeta `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return env.Data, env.Meta.Total
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error: %v (%s)", err, w.Body.String())
	}
	for _, r := range env.Error.Message {
		if r == 0x2014 || r == 0x2013 {
			t.Errorf("error message contains a dash: %q", env.Error.Message)
		}
	}
	return env.Error.Code
}

func titles(posts []PortalPost) []string {
	out := make([]string, len(posts))
	for i, p := range posts {
		out[i] = p.Title
	}
	return out
}

// --- Feed filters ---

func TestBoardFeed_CategoryAndSearch(t *testing.T) {
	env := newBoardEnv(t)
	alice := env.key(t, "alice")
	env.post(t, alice, map[string]interface{}{"body": "Need a dataset curator", "category": "request"})
	env.post(t, alice, map[string]interface{}{"body": "Sharing weights\ntrained on a dataset of cats", "category": "discovery"})
	env.post(t, alice, map[string]interface{}{"body": "Just chatting", "category": "general"})

	posts, total := env.list(t, "?category=request", "")
	if total != 1 || len(posts) != 1 || posts[0].Category != "request" {
		t.Fatalf("category=request: %v total=%d", titles(posts), total)
	}
	posts, total = env.list(t, "?q=dataset", "")
	if total != 2 {
		t.Fatalf("q=dataset should hit title and body: %v", titles(posts))
	}
	posts, total = env.list(t, "?q=dataset&category=discovery", "")
	if total != 1 || posts[0].Category != "discovery" {
		t.Fatalf("q and category combine: %v", titles(posts))
	}
	if w := env.do(t, http.MethodGet, "/api/board/posts?category=spam", nil, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad category: %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/board/posts?tab=nope", nil, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad tab: %d %s", w.Code, w.Body.String())
	}
	// recent and top still work with no filters.
	if _, total := env.list(t, "?tab=recent", ""); total != 3 {
		t.Fatalf("tab=recent total=%d", total)
	}
	if _, total := env.list(t, "?tab=top", ""); total != 3 {
		t.Fatalf("tab=top total=%d", total)
	}
}

func TestBoardFeed_BountiesTab(t *testing.T) {
	env := newBoardEnv(t)
	alice := env.key(t, "alice")
	env.bounty(t, alice, "small", 100, 30)
	env.bounty(t, alice, "big late", 500, 20)
	env.bounty(t, alice, "big soon", 500, 5)
	expired := env.bounty(t, alice, "gone", 900, 1)
	env.post(t, alice, map[string]interface{}{"body": "no bounty here"})
	_ = env.repo.ExpireBounty(context.Background(), expired.ID)

	posts, total := env.list(t, "?tab=bounties", "")
	got := titles(posts)
	if total != 3 || len(got) != 3 || got[0] != "big soon" || got[1] != "big late" || got[2] != "small" {
		t.Fatalf("tab=bounties = %v (total %d)", got, total)
	}
	for _, p := range posts {
		if p.Tab != "bounties" || p.Bounty == nil || p.Bounty.Status != "open" || p.Bounty.ExpiresAt == nil || p.Bounty.Extended {
			t.Fatalf("bounties tab post DTO: %+v bounty=%+v", p, p.Bounty)
		}
	}
}

func TestBoardFeed_MineRequiresKey(t *testing.T) {
	env := newBoardEnv(t)
	alice, bob := env.key(t, "alice"), env.key(t, "bob")
	mine := env.post(t, alice, map[string]interface{}{"body": "alice post"})
	theirs := env.post(t, bob, map[string]interface{}{"body": "bob post"})
	won := env.bounty(t, bob, "bob bounty", 200, 10)
	env.bounty(t, alice, "alice bounty", 300, 10)

	w := env.do(t, http.MethodPost, "/api/board/posts/"+won.ID+"/replies", map[string]interface{}{"body": "on it"}, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("reply: %d %s", w.Code, w.Body.String())
	}
	var rep struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+won.ID+"/award", map[string]interface{}{"reply_id": rep.Data.ID}, bob); w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}

	w = env.do(t, http.MethodGet, "/api/board/posts?mine=posts", nil, "")
	if w.Code != http.StatusUnauthorized || errCode(t, w) != "UNAUTHORIZED" {
		t.Fatalf("mine without key: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, http.MethodGet, "/api/board/posts?mine=posts", nil, "not-a-key")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("mine with bad key: %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/board/posts?mine=everything", nil, alice); w.Code != http.StatusBadRequest {
		t.Fatalf("bad mine: %d", w.Code)
	}

	posts, total := env.list(t, "?mine=posts", alice)
	if total != 2 || !hasID(posts, mine.ID) || hasID(posts, theirs.ID) {
		t.Fatalf("mine=posts: %v", titles(posts))
	}
	for _, p := range posts {
		if !p.IsAuthor {
			t.Errorf("mine=posts must flag isAuthor: %+v", p)
		}
	}
	posts, total = env.list(t, "?mine=replies", alice)
	if total != 1 || !hasID(posts, won.ID) {
		t.Fatalf("mine=replies: %v", titles(posts))
	}
	posts, total = env.list(t, "?mine=bounties", alice)
	if total != 2 || !hasID(posts, won.ID) {
		t.Fatalf("mine=bounties (posted or won): %v", titles(posts))
	}
	posts, total = env.list(t, "?mine=bounties&category=bounty&q=alice", alice)
	if total != 1 || posts[0].Title != "alice bounty" {
		t.Fatalf("mine combines with category and q: %v", titles(posts))
	}
}

func hasID(posts []PortalPost, id string) bool {
	for _, p := range posts {
		if p.ID == id {
			return true
		}
	}
	return false
}

func TestBoardCounts_PublicAggregates(t *testing.T) {
	env := newBoardEnv(t)
	alice := env.key(t, "alice")
	env.post(t, alice, map[string]interface{}{"body": "g"})
	env.post(t, alice, map[string]interface{}{"body": "r", "category": "request"})
	env.post(t, alice, map[string]interface{}{"body": "t", "category": "token-offer", "tokenOffer": map[string]interface{}{"amount": 5, "token": "STONK"}})
	env.bounty(t, alice, "b", 100, 3)

	w := env.do(t, http.MethodGet, "/api/board/counts", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("counts: %d %s", w.Code, w.Body.String())
	}
	var env2 struct {
		Data map[string]int `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env2); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"all": 4, "general": 1, "request": 1, "bounty": 1, "token-offer": 1, "discovery": 0, "open_bounties": 1}
	for k, v := range want {
		got, ok := env2.Data[k]
		if !ok || got != v {
			t.Errorf("counts[%s] = %d (present %v), want %d", k, got, ok, v)
		}
	}
}

// --- Bounty expiry DTO and extend ---

func TestBoardBounty_DTOAndExtend(t *testing.T) {
	env := newBoardEnv(t)
	alice, bob := env.key(t, "alice"), env.key(t, "bob")
	p := env.bounty(t, alice, "extend me", 100, 3)
	if p.Bounty == nil || p.Bounty.ExpiresAt == nil || p.Bounty.Extended || p.Bounty.RefundedAt != nil || p.Bounty.Status != "open" {
		t.Fatalf("new bounty DTO: %+v", p.Bounty)
	}
	wantExp := env.clock.Now().Add(3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if *p.Bounty.ExpiresAt != wantExp {
		t.Fatalf("expires_at = %s, want %s", *p.Bounty.ExpiresAt, wantExp)
	}
	raw := env.do(t, http.MethodGet, "/api/board/posts/"+p.ID, nil, "")
	var generic struct {
		Data struct {
			Bounty map[string]interface{} `json:"bounty"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw.Body.Bytes(), &generic)
	for _, k := range []string{"expires_at", "extended", "refunded_at", "daysRemaining", "status"} {
		if _, ok := generic.Data.Bounty[k]; !ok {
			t.Errorf("bounty DTO missing %q: %v", k, generic.Data.Bounty)
		}
	}

	w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/bounty/extend", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("extend without key: %d", w.Code)
	}
	w = env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/bounty/extend", nil, bob)
	if w.Code != http.StatusForbidden || errCode(t, w) != "BOUNTY_NOT_AUTHOR" {
		t.Fatalf("extend by other: %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/bounty/extend", nil, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("extend: %d %s", w.Code, w.Body.String())
	}
	var ext struct {
		Data PortalPost `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ext)
	wantExp = env.clock.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	// Ten days on the mock clock: int(240h / 24) + 1, the DTO counts the current day.
	wantDays := 11
	if !ext.Data.Bounty.Extended || *ext.Data.Bounty.ExpiresAt != wantExp || ext.Data.Bounty.DaysRemaining != wantDays {
		t.Fatalf("after extend: %+v", ext.Data.Bounty)
	}
	w = env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/bounty/extend", nil, alice)
	if w.Code != http.StatusConflict || errCode(t, w) != "BOUNTY_ALREADY_EXTENDED" {
		t.Fatalf("second extend: %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodPost, "/api/board/posts/missing/bounty/extend", nil, alice); w.Code != http.StatusNotFound {
		t.Fatalf("missing post: %d", w.Code)
	}

	// Expire it through the job: status expired, daysRemaining 0, extend now 409 BOUNTY_NOT_OPEN.
	env.clock.Advance(11 * 24 * time.Hour)
	if n, err := env.forum.ExpireBounties(context.Background()); err != nil || n != 1 {
		t.Fatalf("expire job: n=%d err=%v", n, err)
	}
	w = env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/bounty/extend", nil, alice)
	if w.Code != http.StatusConflict || errCode(t, w) != "BOUNTY_NOT_OPEN" {
		t.Fatalf("extend expired: %d %s", w.Code, w.Body.String())
	}
	posts, _ := env.list(t, "?category=bounty", "")
	if len(posts) != 1 || posts[0].Bounty.Status != "expired" || posts[0].Bounty.DaysRemaining != 0 {
		t.Fatalf("expired DTO: %+v", posts[0].Bounty)
	}
	if _, total := env.list(t, "?tab=bounties", ""); total != 0 {
		t.Fatalf("expired bounty must leave the bounties tab: total=%d", total)
	}
}

// --- Activity feed ---

func TestBoardActivity_FeedAndRead(t *testing.T) {
	env := newBoardEnv(t)
	now := env.clock.Now()
	if err := env.peers.Create(context.Background(), &models.Peer{PeerID: "bob", DisplayName: "Bobby", MaskedPeerID: "12D3...b0b", FirstSeen: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	alice, bob := env.key(t, "alice"), env.key(t, "bob")
	p := env.bounty(t, alice, "help wanted please", 250, 10)

	w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "I can help"}, bob)
	if w.Code != http.StatusOK {
		t.Fatalf("reply: %d %s", w.Code, w.Body.String())
	}
	var rep struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	env.clock.Advance(time.Minute)
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/upvote", nil, bob); w.Code != http.StatusOK {
		t.Fatalf("upvote: %d", w.Code)
	}
	env.clock.Advance(time.Minute)
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/award", map[string]interface{}{"reply_id": rep.Data.ID}, alice); w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}

	if w := env.do(t, http.MethodGet, "/api/activity", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("activity without key: %d", w.Code)
	}
	// limit over 100 is clamped to 100; 0 is still refused.
	if w := env.do(t, http.MethodGet, "/api/activity?limit=500", nil, alice); w.Code != http.StatusOK {
		t.Fatalf("limit over 100: %d", w.Code)
	}
	if w := env.do(t, http.MethodGet, "/api/activity?limit=0", nil, alice); w.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", w.Code)
	}
	if w := env.do(t, http.MethodGet, "/api/activity?since=yesterday", nil, alice); w.Code != http.StatusBadRequest {
		t.Fatalf("bad since: %d", w.Code)
	}

	w = env.do(t, http.MethodGet, "/api/activity", nil, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("activity: %d %s", w.Code, w.Body.String())
	}
	var feed struct {
		Data PortalActivityResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.Data.Unread != 2 || len(feed.Data.Items) != 2 {
		t.Fatalf("alice feed: %+v", feed.Data)
	}
	up, reply := feed.Data.Items[0], feed.Data.Items[1]
	if up.Kind != models.ActivityPostUpvoted || reply.Kind != models.ActivityReplyOnPost {
		t.Fatalf("newest first: %s then %s", up.Kind, reply.Kind)
	}
	if reply.ActorPeerID != "bob" || reply.ActorDisplayName != "Bobby" || reply.PostTitle != "help wanted please" || reply.ReplyID == nil || *reply.ReplyID != rep.Data.ID || reply.ReadAt != nil || reply.Amount != nil {
		t.Fatalf("reply_on_post item: %+v", reply)
	}
	if up.PostID != p.ID || up.ReplyID != nil {
		t.Fatalf("post_upvoted item: %+v", up)
	}

	w = env.do(t, http.MethodGet, "/api/activity", nil, bob)
	_ = json.Unmarshal(w.Body.Bytes(), &feed)
	if len(feed.Data.Items) != 1 || feed.Data.Items[0].Kind != models.ActivityBountyAwarded || feed.Data.Items[0].Amount == nil || *feed.Data.Items[0].Amount != 250 || feed.Data.Items[0].ActorPeerID != "alice" {
		t.Fatalf("bob feed: %+v", feed.Data)
	}

	// since: only rows after the reply.
	w = env.do(t, http.MethodGet, "/api/activity?since="+reply.CreatedAt+"&limit=10", nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &feed)
	if len(feed.Data.Items) != 1 || feed.Data.Items[0].ID != up.ID || feed.Data.Unread != 2 {
		t.Fatalf("since: %+v", feed.Data)
	}

	// Mark one read by id, then all.
	if w := env.do(t, http.MethodPost, "/api/activity/read", map[string]interface{}{}, alice); w.Code != http.StatusBadRequest {
		t.Fatalf("read with empty body: %d", w.Code)
	}
	w = env.do(t, http.MethodPost, "/api/activity/read", map[string]interface{}{"ids": []string{up.ID}}, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("read ids: %d %s", w.Code, w.Body.String())
	}
	var readResp struct {
		Data struct {
			OK     bool `json:"ok"`
			Unread int  `json:"unread"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &readResp)
	if !readResp.Data.OK || readResp.Data.Unread != 1 {
		t.Fatalf("after read ids: %+v", readResp.Data)
	}
	// Bob cannot mark alice's rows.
	env.do(t, http.MethodPost, "/api/activity/read", map[string]interface{}{"ids": []string{reply.ID}}, bob)
	w = env.do(t, http.MethodGet, "/api/activity", nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &feed)
	if feed.Data.Unread != 1 || feed.Data.Items[0].ReadAt == nil || feed.Data.Items[1].ReadAt != nil {
		t.Fatalf("after bob's attempt: %+v", feed.Data)
	}
	w = env.do(t, http.MethodPost, "/api/activity/read", map[string]interface{}{"all": true}, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &readResp)
	if w.Code != http.StatusOK || readResp.Data.Unread != 0 {
		t.Fatalf("read all: %d %+v", w.Code, readResp.Data)
	}
}

// --- regressions ---

func TestBoardFeed_MultibyteSearchIsCutByRunes(t *testing.T) {
	env := newBoardEnv(t)
	alice := env.key(t, "alice")
	env.post(t, alice, map[string]interface{}{"body": "日本語のデータセット"})
	// 250 three-byte characters: 750 bytes, must be cut to 200 characters, never mid-rune.
	q := url.QueryEscape(strings.Repeat("語", 250))
	w := env.do(t, http.MethodGet, "/api/board/posts?q="+q, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("multibyte q: %d %s", w.Code, w.Body.String())
	}
	posts, total := env.list(t, "?q="+url.QueryEscape("データセット"), "")
	if total != 1 || len(posts) != 1 {
		t.Fatalf("multibyte q must still match: %v", titles(posts))
	}
}

func TestBoardBounty_ExtendAndAwardResponsesCarryTierAndUpvote(t *testing.T) {
	env := newBoardEnv(t)
	alice, bob := env.key(t, "alice"), env.key(t, "bob")
	if err := env.rep.Upsert(context.Background(), &reputation.ReputationRecord{PeerID: "alice", CompositeScore: reputation.RankGoldThreshold + 0.01, UpdatedAt: env.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	p := env.bounty(t, alice, "tiered", 100, 3)
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/upvote", nil, alice); w.Code != http.StatusConflict || errCode(t, w) != "UPVOTE_OWN" {
		t.Fatalf("own upvote: %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/upvote", nil, bob); w.Code != http.StatusOK {
		t.Fatalf("upvote: %d", w.Code)
	}

	var got struct {
		Data PortalPost `json:"data"`
	}
	w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/bounty/extend", nil, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("extend: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Data.AuthorTier != reputation.RankGold || got.Data.UpvotedByMe || !got.Data.IsAuthor || got.Data.Upvotes != 1 {
		t.Fatalf("extend response: tier=%s upvotedByMe=%v isAuthor=%v", got.Data.AuthorTier, got.Data.UpvotedByMe, got.Data.IsAuthor)
	}

	w = env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "me"}, bob)
	var rep struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rep)
	w = env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/award", map[string]interface{}{"reply_id": rep.Data.ID}, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Data.AuthorTier != reputation.RankGold || got.Data.UpvotedByMe || got.Data.Upvotes != 1 || got.Data.Bounty == nil || got.Data.Bounty.Status != "completed" {
		t.Fatalf("award response: tier=%s upvotedByMe=%v bounty=%+v", got.Data.AuthorTier, got.Data.UpvotedByMe, got.Data.Bounty)
	}

	// Deep link with a key: the same enrichment on GET /api/board/posts/{id}.
	w = env.do(t, http.MethodGet, "/api/board/posts/"+p.ID, nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Data.AuthorTier != reputation.RankGold || got.Data.UpvotedByMe || !got.Data.IsAuthor || got.Data.Upvotes != 1 {
		t.Fatalf("get response: tier=%s upvotedByMe=%v isAuthor=%v", got.Data.AuthorTier, got.Data.UpvotedByMe, got.Data.IsAuthor)
	}
}

// --- minor regressions ---

// TestBoardActivity_SinceEchoOfNewestCreatedAtIsExclusive: created_at carries sub-second
// precision, so passing the newest row's created_at back as since= returns nothing.
func TestBoardActivity_SinceEchoOfNewestCreatedAtIsExclusive(t *testing.T) {
	env := newBoardEnv(t)
	alice := env.key(t, "alice")
	p := env.post(t, alice, map[string]interface{}{"body": "thread"})
	for i, who := range []string{"bob", "carol", "dave"} {
		env.clock.Advance(time.Duration(i+1)*77*time.Millisecond + 253*time.Microsecond)
		if w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "hi"}, env.key(t, who)); w.Code != http.StatusOK {
			t.Fatalf("reply by %s: %d %s", who, w.Code, w.Body.String())
		}
	}
	var feed struct {
		Data PortalActivityResponse `json:"data"`
	}
	w := env.do(t, http.MethodGet, "/api/activity", nil, alice)
	if err := json.Unmarshal(w.Body.Bytes(), &feed); err != nil || len(feed.Data.Items) != 3 {
		t.Fatalf("feed: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	newest := feed.Data.Items[0].CreatedAt
	if _, err := time.Parse(time.RFC3339Nano, newest); err != nil || !strings.Contains(newest, ".") {
		t.Fatalf("created_at must carry sub-second precision: %q (%v)", newest, err)
	}
	w = env.do(t, http.MethodGet, "/api/activity?since="+url.QueryEscape(newest), nil, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("since echo: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &feed)
	if len(feed.Data.Items) != 0 || feed.Data.Unread != 3 {
		t.Fatalf("since=<newest created_at> must return nothing: %+v", feed.Data)
	}
	w = env.do(t, http.MethodGet, "/api/activity", nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &feed)
	w = env.do(t, http.MethodGet, "/api/activity?since="+url.QueryEscape(feed.Data.Items[1].CreatedAt), nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &feed)
	if len(feed.Data.Items) != 1 || feed.Data.Items[0].CreatedAt != newest {
		t.Fatalf("since=<second newest> must return only the newest: %+v", feed.Data)
	}
}

// upvotedByMe on the enriched responses, seen from the upvoter (the author can no longer
// upvote their own post).
func TestBoardPost_GetAsUpvoterCarriesUpvotedByMe(t *testing.T) {
	env := newBoardEnv(t)
	alice, bob := env.key(t, "alice"), env.key(t, "bob")
	p := env.post(t, alice, map[string]interface{}{"body": "vote me"})
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/upvote", nil, bob); w.Code != http.StatusOK {
		t.Fatalf("upvote: %d", w.Code)
	}
	var got struct {
		Data PortalPost `json:"data"`
	}
	w := env.do(t, http.MethodGet, "/api/board/posts/"+p.ID, nil, bob)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.Data.UpvotedByMe || got.Data.IsAuthor || got.Data.Upvotes != 1 {
		t.Fatalf("get as upvoter: upvotedByMe=%v isAuthor=%v upvotes=%d", got.Data.UpvotedByMe, got.Data.IsAuthor, got.Data.Upvotes)
	}
}
