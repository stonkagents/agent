// Package api: Agent Autopilot tests. Auto replies (auto: true) are capped per post and per
// UTC day, refused on token-offer and own posts, hidden by ?hide_auto=1, and reported back to
// the agent by GET /api/v1/tracker/autopilot/outcomes. Manual replies are unchanged.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// autopilotEnv is a server with the portal board and the v1 forum routes over memory
// repositories, a mock clock on the forum service, and a way to mint peer API keys.
type autopilotEnv struct {
	srv     *Server
	clock   *clock.MockClock
	forum   *services.ForumService
	repo    *repository.MemoryForumRepository
	apiKeys repository.PeerAPIKeyRepository
}

func newAutopilotEnv(t *testing.T) *autopilotEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 15, 22, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	forumRepo := repository.NewMemoryForumRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	store := presence.NewMemoryPresenceStore(clk)

	peerSvc := services.NewPeerService(peerRepo, store, apiKeyRepo)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	forumSvc := services.NewForumService(forumRepo, nil, nil)
	forumSvc.SetClock(clk)

	portal := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, repository.NewMemoryPeerTrustBlockRepository(),
		apiKeyRepo, repository.NewMemoryGuestKeyRepository(), repository.NewMemoryReputationRepository(), &geo.StubResolver{}, nil, nil, nil)
	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		PortalHandler: portal,
		ForumHandler:  NewForumHandler(forumSvc, apiKeyRepo),
		APIKeyRepo:    apiKeyRepo,
		Address:       ":7842",
	})
	return &autopilotEnv{srv: srv, clock: clk, forum: forumSvc, repo: forumRepo, apiKeys: apiKeyRepo}
}

func (e *autopilotEnv) key(t *testing.T, peerID string) string {
	t.Helper()
	return seedAPIKey(t, e.apiKeys, peerID)
}

func (e *autopilotEnv) do(t *testing.T, method, path string, body interface{}, apiKey string) *httptest.ResponseRecorder {
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

// post creates a board post as peerID and returns its id.
func (e *autopilotEnv) post(t *testing.T, peerID string, body map[string]interface{}) string {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/board/posts", body, e.key(t, peerID))
	if w.Code != http.StatusOK {
		t.Fatalf("create post: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data PortalPost `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	return env.Data.ID
}

func (e *autopilotEnv) reply(t *testing.T, postID, apiKey, body string, auto bool) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/board/posts/"+postID+"/replies", map[string]interface{}{"body": body, "auto": auto}, apiKey)
}

func autoErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
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

func TestAutopilot_CreateReply_AutoFlagPersistedAndReturned(t *testing.T) {
	env := newAutopilotEnv(t)
	postID := env.post(t, "author", map[string]interface{}{"body": "Need a hand with my dataset"})
	agent := env.key(t, "agent")

	w := env.reply(t, postID, agent, "I can help", true)
	if w.Code != http.StatusOK {
		t.Fatalf("auto reply: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Data["auto"] != true {
		t.Errorf("create response auto = %v, want true; body %s", created.Data["auto"], w.Body.String())
	}

	// Manual reply: no auto key in the JSON at all.
	w = env.reply(t, postID, env.key(t, "human"), "me too", false)
	if w.Code != http.StatusOK {
		t.Fatalf("manual reply: %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"auto"`)) {
		t.Errorf("manual reply must omit auto: %s", w.Body.String())
	}

	// Portal list carries auto on the auto reply only.
	w = env.do(t, http.MethodGet, "/api/board/posts/"+postID+"/replies", nil, "")
	var list struct {
		Data []map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Data) != 2 {
		t.Fatalf("replies = %d, want 2: %s", len(list.Data), w.Body.String())
	}
	if list.Data[0]["auto"] != true {
		t.Errorf("first reply auto = %v, want true", list.Data[0]["auto"])
	}
	if _, has := list.Data[1]["auto"]; has {
		t.Errorf("manual reply must omit auto in list: %v", list.Data[1])
	}

	// v1 forum list carries the same flag.
	w = env.do(t, http.MethodGet, "/api/v1/tracker/forum/posts/"+postID+"/replies", nil, "")
	var v1 struct {
		Data  []ReplyDTO `json:"data"`
		Total int        `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v1)
	if v1.Total != 2 || len(v1.Data) != 2 || !v1.Data[0].Auto || v1.Data[1].Auto {
		t.Errorf("v1 replies = %+v total %d, want auto on the first only", v1.Data, v1.Total)
	}
}

func TestAutopilot_V1CreateReply_AcceptsAuto(t *testing.T) {
	env := newAutopilotEnv(t)
	postID := env.post(t, "author", map[string]interface{}{"body": "Anyone around?"})
	w := env.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+postID+"/replies", CreateReplyDTO{Body: "yes", Auto: true}, env.key(t, "agent"))
	if w.Code != http.StatusCreated {
		t.Fatalf("v1 auto reply: %d %s", w.Code, w.Body.String())
	}
	var dto ReplyDTO
	_ = json.Unmarshal(w.Body.Bytes(), &dto)
	if !dto.Auto {
		t.Errorf("v1 create response auto = false, want true: %s", w.Body.String())
	}
}

func TestAutopilot_OneAutoReplyPerPostPerPeer(t *testing.T) {
	env := newAutopilotEnv(t)
	postID := env.post(t, "author", map[string]interface{}{"body": "Looking for a model"})
	agent := env.key(t, "agent")

	if w := env.reply(t, postID, agent, "first", true); w.Code != http.StatusOK {
		t.Fatalf("first auto reply: %d %s", w.Code, w.Body.String())
	}
	w := env.reply(t, postID, agent, "second", true)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second auto reply status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if code := autoErrCode(t, w); code != "AUTO_REPLY_LIMIT" {
		t.Errorf("code = %s, want AUTO_REPLY_LIMIT", code)
	}
	// A manual reply on the same post is still fine, and so is another agent's auto reply.
	if w := env.reply(t, postID, agent, "manual follow-up", false); w.Code != http.StatusOK {
		t.Errorf("manual reply after cap: %d %s", w.Code, w.Body.String())
	}
	if w := env.reply(t, postID, env.key(t, "other-agent"), "hi", true); w.Code != http.StatusOK {
		t.Errorf("other agent auto reply: %d %s", w.Code, w.Body.String())
	}
}

func TestAutopilot_DailyCapPerPeer_ResetsAtUTCMidnight(t *testing.T) {
	env := newAutopilotEnv(t)
	env.forum.AutopilotMaxRepliesPerDay = 3
	agent := env.key(t, "agent")
	// 22:00 UTC on the 15th; two hours to midnight.
	for i := 0; i < 3; i++ {
		postID := env.post(t, "author", map[string]interface{}{"body": fmt.Sprintf("post %d", i)})
		if w := env.reply(t, postID, agent, "auto", true); w.Code != http.StatusOK {
			t.Fatalf("auto reply %d: %d %s", i, w.Code, w.Body.String())
		}
		env.clock.Advance(time.Minute)
	}
	fourth := env.post(t, "author", map[string]interface{}{"body": "post 4"})
	w := env.reply(t, fourth, agent, "auto", true)
	if w.Code != http.StatusTooManyRequests || autoErrCode(t, w) != "AUTO_REPLY_LIMIT" {
		t.Fatalf("fourth auto reply = %d %s, want 429 AUTO_REPLY_LIMIT", w.Code, w.Body.String())
	}
	// Manual replies never count against the cap.
	if w := env.reply(t, fourth, agent, "manual", false); w.Code != http.StatusOK {
		t.Errorf("manual reply under daily cap: %d %s", w.Code, w.Body.String())
	}
	// Past UTC midnight the counter starts over.
	env.clock.Advance(3 * time.Hour)
	if w := env.reply(t, fourth, agent, "auto next day", true); w.Code != http.StatusOK {
		t.Errorf("auto reply after midnight: %d %s", w.Code, w.Body.String())
	}
}

func TestAutopilot_DailyCapZero_Disabled(t *testing.T) {
	env := newAutopilotEnv(t)
	env.forum.AutopilotMaxRepliesPerDay = 0
	agent := env.key(t, "agent")
	for i := 0; i < services.DefaultAutopilotMaxRepliesPerDay+2; i++ {
		postID := env.post(t, "author", map[string]interface{}{"body": fmt.Sprintf("post %d", i)})
		if w := env.reply(t, postID, agent, "auto", true); w.Code != http.StatusOK {
			t.Fatalf("auto reply %d with cap disabled: %d %s", i, w.Code, w.Body.String())
		}
	}
}

func TestAutopilot_RefusedOnTokenOfferPost(t *testing.T) {
	env := newAutopilotEnv(t)
	postID := env.post(t, "author", map[string]interface{}{
		"body": "100 STNK for reviews", "category": "token-offer",
		"tokenOffer": map[string]interface{}{"amount": 100, "token": "STNK"},
	})
	w := env.reply(t, postID, env.key(t, "agent"), "auto", true)
	if w.Code != http.StatusForbidden || autoErrCode(t, w) != "AUTO_REPLY_NOT_ALLOWED" {
		t.Fatalf("auto reply on token-offer = %d %s, want 403 AUTO_REPLY_NOT_ALLOWED", w.Code, w.Body.String())
	}
	if w := env.reply(t, postID, env.key(t, "human"), "manual is fine", false); w.Code != http.StatusOK {
		t.Errorf("manual reply on token-offer: %d %s", w.Code, w.Body.String())
	}
}

func TestAutopilot_RefusedOnOwnPost(t *testing.T) {
	env := newAutopilotEnv(t)
	postID := env.post(t, "agent", map[string]interface{}{"body": "my own thread"})
	w := env.reply(t, postID, env.key(t, "agent"), "auto", true)
	if w.Code != http.StatusForbidden || autoErrCode(t, w) != "AUTO_REPLY_NOT_ALLOWED" {
		t.Fatalf("auto reply on own post = %d %s, want 403 AUTO_REPLY_NOT_ALLOWED", w.Code, w.Body.String())
	}
	if w := env.reply(t, postID, env.key(t, "agent"), "manual self reply", false); w.Code != http.StatusOK {
		t.Errorf("manual reply on own post: %d %s", w.Code, w.Body.String())
	}
}

func TestAutopilot_HideAutoFilter_ReplyCountStaysTotal(t *testing.T) {
	env := newAutopilotEnv(t)
	postID := env.post(t, "author", map[string]interface{}{"body": "thread"})
	if w := env.reply(t, postID, env.key(t, "agent"), "auto", true); w.Code != http.StatusOK {
		t.Fatalf("auto: %d", w.Code)
	}
	if w := env.reply(t, postID, env.key(t, "human"), "manual", false); w.Code != http.StatusOK {
		t.Fatalf("manual: %d", w.Code)
	}

	w := env.do(t, http.MethodGet, "/api/board/posts/"+postID+"/replies?hide_auto=1", nil, "")
	var list struct {
		Data []PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Data) != 1 || list.Data[0].Content != "manual" {
		t.Errorf("hide_auto=1 replies = %+v, want the manual one only", list.Data)
	}
	w = env.do(t, http.MethodGet, "/api/v1/tracker/forum/posts/"+postID+"/replies?hide_auto=true", nil, "")
	var v1 struct {
		Data  []ReplyDTO `json:"data"`
		Total int        `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v1)
	if v1.Total != 1 || len(v1.Data) != 1 || v1.Data[0].Auto {
		t.Errorf("v1 hide_auto replies = %+v total %d, want 1 manual", v1.Data, v1.Total)
	}

	// The post's reply count is the total, auto included.
	w = env.do(t, http.MethodGet, "/api/board/posts?tab=recent", nil, "")
	var posts struct {
		Data []PortalPost `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &posts)
	if len(posts.Data) != 1 || posts.Data[0].Replies != 2 {
		t.Errorf("post replyCount = %+v, want 2", posts.Data)
	}
}

func TestAutopilot_Outcomes_OwnAutoRepliesLast30Days(t *testing.T) {
	env := newAutopilotEnv(t)
	agent := env.key(t, "agent")
	now := env.clock.Now()

	// Plain post, upvoted twice.
	plain := env.post(t, "author", map[string]interface{}{"body": "plain"})
	if w := env.reply(t, plain, agent, "auto on plain", true); w.Code != http.StatusOK {
		t.Fatalf("auto on plain: %d", w.Code)
	}
	for _, voter := range []string{"v1", "v2"} {
		if w := env.do(t, http.MethodPost, "/api/board/posts/"+plain+"/upvote", nil, env.key(t, voter)); w.Code != http.StatusOK {
			t.Fatalf("upvote: %d %s", w.Code, w.Body.String())
		}
	}
	env.clock.Advance(time.Hour)

	// Bounty post (no escrow in tests), awarded to the agent's auto reply.
	bounty := env.post(t, "author", map[string]interface{}{"body": "bounty", "category": "bounty",
		"bounty": map[string]interface{}{"amount": 250, "currency": "credits", "days": 7}})
	w := env.reply(t, bounty, agent, "auto on bounty", true)
	if w.Code != http.StatusOK {
		t.Fatalf("auto on bounty: %d", w.Code)
	}
	var created struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+bounty+"/award", map[string]string{"reply_id": created.Data.ID}, env.key(t, "author")); w.Code != http.StatusOK {
		t.Fatalf("award: %d %s", w.Code, w.Body.String())
	}

	// Bounty post where someone else won.
	lost := env.post(t, "author", map[string]interface{}{"body": "lost", "category": "bounty",
		"bounty": map[string]interface{}{"amount": 40, "currency": "credits", "days": 7}})
	if w := env.reply(t, lost, agent, "auto on lost", true); w.Code != http.StatusOK {
		t.Fatalf("auto on lost: %d", w.Code)
	}
	w = env.reply(t, lost, env.key(t, "rival"), "rival", false)
	var rival struct {
		Data PortalReply `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rival)
	if w := env.do(t, http.MethodPost, "/api/board/posts/"+lost+"/award", map[string]string{"reply_id": rival.Data.ID}, env.key(t, "author")); w.Code != http.StatusOK {
		t.Fatalf("award rival: %d %s", w.Code, w.Body.String())
	}

	// A manual reply by the agent and an auto reply by another agent must not show up.
	manualPost := env.post(t, "author", map[string]interface{}{"body": "manual only"})
	_ = env.reply(t, manualPost, agent, "manual", false)
	_ = env.reply(t, manualPost, env.key(t, "other"), "other auto", true)

	// An auto reply older than 30 days is out of the window.
	old := env.post(t, "author", map[string]interface{}{"body": "old"})
	if err := env.repo.CreateReply(context.Background(), &models.ForumReply{PostID: old, AuthorPeerID: "agent", Body: "old auto", CreatedAt: now.Add(-31 * 24 * time.Hour), Auto: true}); err != nil {
		t.Fatal(err)
	}

	w = env.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes", nil, agent)
	if w.Code != http.StatusOK {
		t.Fatalf("outcomes: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data []AutopilotOutcomeDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 3 {
		t.Fatalf("outcomes = %d, want 3: %s", len(out.Data), w.Body.String())
	}
	byPost := map[string]AutopilotOutcomeDTO{}
	for _, o := range out.Data {
		byPost[o.PostID] = o
	}
	// Newest first: the two bounty replies (same clock tick) precede the plain one.
	if out.Data[2].PostID != plain {
		t.Errorf("order: last = %s, want the plain post %s", out.Data[2].PostID, plain)
	}
	if o := byPost[plain]; o.Upvotes != 2 || o.Awarded || o.BountyAmount != 0 || o.AwardedAmount != 0 || o.Category != "general" || o.PostedAt != now.Format(time.RFC3339) {
		t.Errorf("plain outcome = %+v, want upvotes 2, not awarded, no bounty, category general, posted_at %s", o, now.Format(time.RFC3339))
	}
	if o := byPost[bounty]; !o.Awarded || o.BountyAmount != 250 || o.AwardedAmount != 250 || o.Category != "bounty" {
		t.Errorf("bounty outcome = %+v, want awarded with bounty_amount and awarded_amount 250", o)
	}
	if o := byPost[lost]; o.Awarded || o.BountyAmount != 40 || o.AwardedAmount != 0 {
		t.Errorf("lost outcome = %+v, want not awarded with bounty_amount 40", o)
	}
	for _, key := range []string{`"reply_id"`, `"post_id"`, `"category"`, `"bounty_amount"`, `"posted_at"`, `"upvotes"`, `"accepted"`, `"awarded"`, `"awarded_amount"`, `"hidden"`, `"reported"`} {
		if !bytes.Contains(w.Body.Bytes(), []byte(key)) {
			t.Errorf("outcomes body lacks %s: %s", key, w.Body.String())
		}
	}

	// No key: 401. Another peer: only its own rows.
	if w := env.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no key status = %d, want 401", w.Code)
	}
	w = env.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes", nil, env.key(t, "other"))
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Data) != 1 || out.Data[0].PostID != manualPost {
		t.Errorf("other peer outcomes = %+v, want only its auto reply on %s", out.Data, manualPost)
	}
	// A peer with nothing gets an empty array, not null.
	w = env.do(t, http.MethodGet, "/api/v1/tracker/autopilot/outcomes", nil, env.key(t, "nobody"))
	if !bytes.Contains(w.Body.Bytes(), []byte(`"data":[]`)) {
		t.Errorf("empty outcomes body = %s, want data: []", w.Body.String())
	}
}
