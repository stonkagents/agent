// Package: internal/daemon
// Purpose: Tests for the Agent Autopilot watcher: decision table and run gates.

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

const (
	apPeer  = "12D3KooWAutopilotPeerAAAA"
	apOther = "12D3KooWSomeoneElseBBBBBB"
)

var apNow = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// fakeBoard is a tracker stand-in for the board, balance and completions routes.
type fakeBoard struct {
	mu            sync.Mutex
	posts         []autopilotPost
	replies       map[string][]autopilotReply
	balance       int
	draftStatus   int    // 0 = 200
	replyStatus   int    // 0 = 200
	replyCode     string // error code with replyStatus; "" = X
	drafts        []map[string]interface{}
	draftHeaders  []http.Header
	draftText     string // completion content; "" = the default sentence
	replyBodies   []map[string]interface{}
	replyHeaders  []http.Header
	replyPostIDs  []string
	nextReplyID   int
	balanceCalls  int
	repliesCalled []string

	// Phase 2: routed activity, rooms, digest and post creation.
	routedIDs      []string // post ids answered by GET /api/activity?kinds=request_routed&unread=1
	activityStatus int      // 0 = 200
	byID           map[string]autopilotPost
	rooms          []autopilotRoom // GET /api/board/rooms?mine=1
	digest         autopilotDigestData
	postStatus     int // 0 = 200 for POST /api/board/posts
	postBodies     []map[string]interface{}
	postHeaders    []http.Header
	readIDs        []string // activity ids sent to POST /api/activity/read
	readStatus     int      // 0 = 200
	roomsCalls     int
	calls          []string // "GET /api/activity", "GET /api/board/posts", ... in order

	// Phase 3: bounty_raised activity, outcomes.
	raisedIDs      []string                 // post ids answered by GET /api/activity?kinds=bounty_raised&unread=1
	outcomes       []map[string]interface{} // GET /api/v1/tracker/autopilot/outcomes
	outcomesStatus int                      // 0 = 200
	outcomesSince  []string                 // since= values seen
}

func newFakeBoard(t *testing.T) (*httptest.Server, *fakeBoard) {
	t.Helper()
	fb := &fakeBoard{replies: map[string][]autopilotReply{}, byID: map[string]autopilotPost{}, balance: 500}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		fb.calls = append(fb.calls, r.Method+" "+path)
		switch {
		case r.Method == http.MethodGet && path == "/api/v1/tracker/credits/balance": // tracker server.go: api subrouter under /api/v1/tracker
			fb.balanceCalls++
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]int{"total": fb.balance}})
		case r.Method == http.MethodGet && path == "/api/board/posts":
			if r.URL.Query().Get("tab") != "recent" {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": fb.posts})
		case r.Method == http.MethodGet && path == "/api/activity":
			q := r.URL.Query()
			if fb.activityStatus != 0 {
				w.WriteHeader(fb.activityStatus)
				return
			}
			kind := q.Get("kinds")
			if (kind != "request_routed" && kind != "bounty_raised") || q.Get("unread") != "1" || q.Get("mark") != "0" {
				w.WriteHeader(400)
				return
			}
			ids, prefix := fb.routedIDs, "a-"
			if kind == "bounty_raised" {
				ids, prefix = fb.raisedIDs, "raise-"
			}
			items := []map[string]interface{}{}
			for _, id := range ids {
				read := false
				for _, r := range fb.readIDs {
					if r == prefix+id {
						read = true
					}
				}
				if read {
					continue // unread=1: rows marked read no longer come back
				}
				items = append(items, map[string]interface{}{"id": prefix + id, "kind": kind, "post_id": id, "post_title": "routed"})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"items": items, "unread": len(items)}})
		case r.Method == http.MethodGet && path == "/api/v1/tracker/autopilot/outcomes":
			fb.outcomesSince = append(fb.outcomesSince, r.URL.Query().Get("since"))
			if fb.outcomesStatus != 0 {
				w.WriteHeader(fb.outcomesStatus)
				return
			}
			items := fb.outcomes
			if items == nil {
				items = []map[string]interface{}{}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": items})
		case r.Method == http.MethodPost && path == "/api/activity/read":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(raw, &body)
			if fb.readStatus != 0 {
				w.WriteHeader(fb.readStatus)
				return
			}
			fb.readIDs = append(fb.readIDs, body.IDs...)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"ok": true, "unread": 0}})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/board/posts/") && !strings.HasSuffix(path, "/replies"):
			id := strings.TrimPrefix(path, "/api/board/posts/")
			p, ok := fb.byID[id]
			if !ok {
				for _, cand := range fb.posts {
					if cand.ID == id {
						p, ok = cand, true
					}
				}
			}
			if !ok {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no such post"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": p})
		case r.Method == http.MethodGet && path == "/api/board/rooms":
			fb.roomsCalls++
			rooms := fb.rooms
			if rooms == nil {
				rooms = []autopilotRoom{}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": rooms})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/board/rooms/") && strings.HasSuffix(path, "/digest"):
			if r.URL.Query().Get("days") != "7" {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": fb.digest})
		case r.Method == http.MethodPost && path == "/api/board/posts":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]interface{}
			_ = json.Unmarshal(raw, &body)
			fb.postBodies = append(fb.postBodies, body)
			fb.postHeaders = append(fb.postHeaders, r.Header.Clone())
			if fb.postStatus != 0 {
				w.WriteHeader(fb.postStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"ROOM_NOT_HOLDER","message":"nope"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]string{"id": fmt.Sprintf("post-%d", len(fb.postBodies))}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/replies"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/board/posts/"), "/replies")
			fb.repliesCalled = append(fb.repliesCalled, id)
			list := fb.replies[id]
			if list == nil {
				list = []autopilotReply{}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/replies"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/board/posts/"), "/replies")
			raw, _ := io.ReadAll(r.Body)
			var body map[string]interface{}
			_ = json.Unmarshal(raw, &body)
			fb.replyBodies = append(fb.replyBodies, body)
			fb.replyHeaders = append(fb.replyHeaders, r.Header.Clone())
			fb.replyPostIDs = append(fb.replyPostIDs, id)
			if fb.replyStatus != 0 {
				code := fb.replyCode
				if code == "" {
					code = "X"
				}
				w.WriteHeader(fb.replyStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"nope"}}`))
				return
			}
			fb.nextReplyID++
			rid := fmt.Sprintf("r-%d", fb.nextReplyID)
			fb.replies[id] = append(fb.replies[id], autopilotReply{ID: rid, Author: r.Header.Get("X-API-Key")})
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]string{"id": rid, "postId": id}})
		case r.Method == http.MethodPost && path == "/api/v1/agents/completions":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]interface{}
			_ = json.Unmarshal(raw, &body)
			fb.drafts = append(fb.drafts, body)
			fb.draftHeaders = append(fb.draftHeaders, r.Header.Clone())
			if fb.draftStatus != 0 {
				w.WriteHeader(fb.draftStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"INSUFFICIENT_CREDITS","message":"Not enough credits"}}`))
				return
			}
			w.Header().Set("X-Credits-Deducted", "10")
			text := fb.draftText
			if text == "" {
				text = "I can help with that. Send me the spec."
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{{"message": map[string]string{"content": text}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, fb
}

func (fb *fakeBoard) postCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return len(fb.postBodies)
}

func (fb *fakeBoard) draftCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return len(fb.drafts)
}

func (fb *fakeBoard) replyCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return len(fb.replyBodies)
}

func (fb *fakeBoard) set(fn func(*fakeBoard)) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fn(fb)
}

// callIndex returns the position of the first call with the given method and
// path, or -1.
func (fb *fakeBoard) callIndex(call string) int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for i, c := range fb.calls {
		if c == call {
			return i
		}
	}
	return -1
}

// newAutopilotTestServer is newSetupTestServer pointed at the fake tracker,
// registered, named, with a frozen clock and the given policy.
func newAutopilotTestServer(t *testing.T, trackerURL string, policy config.AutopilotConfig) *Server {
	t.Helper()
	s := newSetupTestServer(t, "")
	s.config.PeerID = apPeer
	s.config.DisplayName = "Atlas Prime"
	s.config.TrackerURL = trackerURL
	policy.ApplyDefaults()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	s.config.Autopilot = policy
	if err := s.saveTrackerAPIKey(apPeer); err != nil { // the fake echoes the key back as the reply author
		t.Fatal(err)
	}
	s.autopilot = &autopilotState{now: func() time.Time { return apNow }}
	return s
}

// policyWith builds a policy for the MVP and phase 2 tests with relevance
// scoring off (an empty test library would skip every post); the phase 3
// tests switch it on themselves.
func policyWith(mode string, mut ...func(*config.AutopilotConfig)) config.AutopilotConfig {
	p := config.DefaultAutopilotConfig()
	p.Mode = mode
	p.RelevanceMode = config.AutopilotRelevanceOff
	for _, m := range mut {
		m(&p)
	}
	return p
}

func post(id, author, category string, ageHours int, mut ...func(*autopilotPost)) autopilotPost {
	p := autopilotPost{
		ID: id, Author: author, AuthorDisplayName: "Someone", Title: "Need a Go reviewer",
		Content: "Looking for help reviewing a Go service.", Category: category,
		Time: apNow.Add(-time.Duration(ageHours) * time.Hour).Format(time.RFC3339),
	}
	for _, m := range mut {
		m(&p)
	}
	return p
}

func withBounty(amount int, status string) func(*autopilotPost) {
	return func(p *autopilotPost) {
		p.Bounty = &autopilotBounty{Amount: amount, Currency: "credits", DaysRemaining: 3, Status: status}
	}
}

// --- decision table ---------------------------------------------------

func TestAutopilotDecide_Table(t *testing.T) {
	cand := func(p autopilotPost) autopilotCandidate {
		return autopilotCandidate{post: p, peerID: apPeer, displayName: "Atlas Prime"}
	}
	suggest := policyWith(config.AutopilotSuggest)
	bounty := policyWith(config.AutopilotBounty)
	auto := policyWith(config.AutopilotAuto)
	cases := []struct {
		name   string
		policy config.AutopilotConfig
		c      autopilotCandidate
		action autopilotAction
		reason string
	}{
		{"own post by id", suggest, cand(post("p", apPeer, "request", 1)), autopilotSkip, "own_post"},
		{"own post by flag", suggest, cand(post("p", apOther, "request", 1, func(p *autopilotPost) { p.IsAuthor = true })), autopilotSkip, "own_post"},
		{"already replied", suggest, func() autopilotCandidate { c := cand(post("p", apOther, "request", 1)); c.replied = true; return c }(), autopilotSkip, "already_replied"},
		{"pending suggestion", suggest, func() autopilotCandidate { c := cand(post("p", apOther, "request", 1)); c.pending = true; return c }(), autopilotSkip, "suggestion_pending"},
		{"cooldown", suggest, func() autopilotCandidate {
			c := cand(post("p", apOther, "request", 1))
			c.seen, c.seenAt = true, apNow.Add(-2*time.Hour)
			return c
		}(), autopilotSkip, "cooldown"},
		{"cooldown elapsed", suggest, func() autopilotCandidate {
			c := cand(post("p", apOther, "request", 1))
			c.seen, c.seenAt = true, apNow.Add(-25*time.Hour)
			return c
		}(), autopilotSuggestAction, "category"},
		{"no timestamp", suggest, cand(post("p", apOther, "request", 1, func(p *autopilotPost) { p.Time = "" })), autopilotSkip, "no_timestamp"},
		{"createdAt fallback", suggest, cand(post("p", apOther, "request", 1, func(p *autopilotPost) { p.CreatedAt, p.Time = p.Time, "" })), autopilotSuggestAction, "category"},
		{"too old", suggest, cand(post("p", apOther, "request", 49)), autopilotSkip, "too_old"},
		{"wrong category", suggest, cand(post("p", apOther, "general", 1)), autopilotSkip, "no_trigger"},
		{"discovery never by category", policyWith(config.AutopilotAuto, func(p *config.AutopilotConfig) { p.Categories = []string{"request", "general", "token-offer"} }), cand(post("p", apOther, "discovery", 1)), autopilotSkip, "no_trigger"},
		{"mention by name", suggest, cand(post("p", apOther, "general", 1, func(p *autopilotPost) { p.Content = "hey @atlas prime can you look?" })), autopilotSuggestAction, "mention"},
		{"mention by peer prefix", suggest, cand(post("p", apOther, "general", 1, func(p *autopilotPost) { p.Content = "ping " + apPeer[:16] })), autopilotSuggestAction, "mention"},
		{"bounty below multiple", suggest, cand(post("p", apOther, "bounty", 1, withBounty(29, "open"))), autopilotSkip, "no_trigger"},
		{"bounty at multiple", suggest, cand(post("p", apOther, "bounty", 1, withBounty(30, "open"))), autopilotSuggestAction, "bounty"},
		{"bounty not open", suggest, cand(post("p", apOther, "bounty", 1, withBounty(100, "completed"))), autopilotSkip, "no_trigger"},
		{"bounty other currency", suggest, cand(post("p", apOther, "bounty", 1, func(p *autopilotPost) {
			p.Bounty = &autopilotBounty{Amount: 100, Currency: "STONK", Status: "open"}
		})), autopilotSkip, "no_trigger"},
		{"suggest mode never posts", suggest, cand(post("p", apOther, "request", 1, withBounty(100, "open"))), autopilotSuggestAction, "bounty"},
		{"bounty mode posts on bounty", bounty, cand(post("p", apOther, "request", 1, withBounty(30, "open"))), autopilotPostAction, "bounty"},
		{"bounty mode asks without bounty", bounty, cand(post("p", apOther, "request", 1)), autopilotAskAction, "category"},
		{"bounty mode asks on small bounty", bounty, cand(post("p", apOther, "request", 1, withBounty(10, "open"))), autopilotAskAction, "category"},
		{"bounty mode suggests general without bounty", policyWith(config.AutopilotBounty, func(p *config.AutopilotConfig) { p.Categories = []string{"general"} }), cand(post("p", apOther, "general", 1)), autopilotSuggestAction, "category"},
		{"bounty mode asks on mention in request", bounty, cand(post("p", apOther, "request", 1, func(p *autopilotPost) { p.Content = "@Atlas Prime help?" })), autopilotAskAction, "mention"},
		{"ask pending", bounty, func() autopilotCandidate {
			c := cand(post("p", apOther, "request", 1))
			c.asked, c.askAmount = true, 40
			return c
		}(), autopilotSkip, "ask_pending"},
		{"raised under the ask", bounty, func() autopilotCandidate {
			c := cand(post("p", apOther, "request", 1, withBounty(30, "open")))
			c.asked, c.askAmount, c.raised = true, 40, true
			c.seen, c.seenAt = true, apNow.Add(-time.Hour) // the ask started the cooldown
			return c
		}(), autopilotSkip, "bounty_under_ask"},
		{"raised to the ask posts", bounty, func() autopilotCandidate {
			c := cand(post("p", apOther, "request", 60, withBounty(40, "open"))) // older than max age: a raise is answered anyway
			c.asked, c.askAmount, c.raised = true, 40, true
			c.seen, c.seenAt = true, apNow.Add(-time.Hour)
			return c
		}(), autopilotPostAction, "bounty"},
		{"raised in suggest mode suggests", suggest, func() autopilotCandidate {
			c := cand(post("p", apOther, "request", 1, withBounty(40, "open")))
			c.asked, c.askAmount, c.raised = true, 40, true
			return c
		}(), autopilotSuggestAction, "bounty"},
		{"auto mode posts", auto, cand(post("p", apOther, "request", 1)), autopilotPostAction, "category"},
		{"auto mode token-offer only suggests", policyWith(config.AutopilotAuto, func(p *config.AutopilotConfig) { p.Categories = []string{"token-offer"} }), cand(post("p", apOther, "token-offer", 1)), autopilotSuggestAction, "category"},
		{"bounty mode token-offer with bounty only suggests", bounty, cand(post("p", apOther, "token-offer", 1, withBounty(100, "open"))), autopilotSuggestAction, "bounty"},
		{"custom multiple asks", policyWith(config.AutopilotBounty, func(p *config.AutopilotConfig) { p.MinBountyMultiple = 5 }), cand(post("p", apOther, "request", 1, withBounty(49, "open"))), autopilotAskAction, "category"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := autopilotDecide(tc.policy, tc.c, apNow)
			if d.action != tc.action || d.reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", d.action, d.reason, tc.action, tc.reason)
			}
		})
	}
}

// --- watcher runs -----------------------------------------------------

func TestAutopilotRun_OffAndRunLevelGates(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) { f.posts = []autopilotPost{post("p1", apOther, "request", 1)} })

	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "mode_off" {
		t.Errorf("off: %+v", res)
	}
	if fb.balanceCalls != 0 || fb.draftCount() != 0 {
		t.Error("off mode must not touch the tracker")
	}

	// Office hours: 12:00 UTC is outside 14:00-16:00.
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) {
		p.OfficeHours = &config.AutopilotOfficeHours{Start: "14:00", End: "16:00", TZ: "UTC"}
	}))
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "outside_office_hours" {
		t.Errorf("office hours: %+v", res)
	}

	// Not registered: no API key.
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	s.trackerAPIKeyMu.Lock()
	s.trackerAPIKey = ""
	s.trackerAPIKeyMu.Unlock()
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "not_registered" {
		t.Errorf("unregistered: %+v", res)
	}

	// Balance floor.
	fb.set(func(f *fakeBoard) { f.balance = 19 })
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "balance_floor" {
		t.Errorf("floor: %+v", res)
	}
	if dto := s.autopilotDTOFor(); !strings.Contains(dto.Status.DowngradedReason, "below the floor") {
		t.Errorf("downgradedReason = %q", dto.Status.DowngradedReason)
	}
	fb.set(func(f *fakeBoard) { f.balance = 500 })

	// Tracker unreachable.
	s = newAutopilotTestServer(t, "http://127.0.0.1:1", policyWith(config.AutopilotSuggest))
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "tracker_unreachable" {
		t.Errorf("unreachable: %+v", res)
	}
	if fb.draftCount() != 0 {
		t.Error("gated runs must not draft")
	}
}

func TestAutopilotRun_SuggestModeStoresWithoutPosting(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("own", apPeer, "request", 1),
			post("gen", apOther, "general", 1),
			post("req", apOther, "request", 2, withBounty(50, "open")),
			post("old", apOther, "request", 72),
		}
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) {
		p.Instruction = "Offer Go reviews only."
	}))
	res := s.runAutopilotOnce(context.Background())
	if res.Skipped != "" || res.Examined != 4 || res.Suggested != 1 || res.Posted != 0 || res.Credits != 10 {
		t.Fatalf("run = %+v", res)
	}
	if fb.replyCount() != 0 {
		t.Error("suggest mode posted a reply")
	}
	if fb.draftCount() != 1 {
		t.Fatalf("drafts = %d", fb.draftCount())
	}
	// Prompt carries the owner's instruction and the post, Fast model (no model field),
	// marked as an autopilot draft so the tracker books the debit apart from owner chat.
	draft := fb.drafts[0]
	if _, has := draft["model"]; has {
		t.Errorf("draft must use the default Fast model, got %v", draft["model"])
	}
	if fb.draftHeaders[0].Get(AutopilotAutoHeader) != "1" || fb.draftHeaders[0].Get("X-API-Key") != apPeer {
		t.Errorf("draft headers = %v", fb.draftHeaders[0])
	}
	msgs, _ := draft["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	sys := msgs[0].(map[string]interface{})["content"].(string)
	usr := msgs[1].(map[string]interface{})["content"].(string)
	if !strings.Contains(sys, "Offer Go reviews only.") || !strings.Contains(sys, "Atlas Prime") || !strings.Contains(sys, "Do not promise files") {
		t.Errorf("system prompt = %q", sys)
	}
	if !strings.Contains(usr, "Need a Go reviewer") || !strings.Contains(usr, "Bounty: 50 credits") || !strings.Contains(usr, "Category: request") {
		t.Errorf("user prompt = %q", usr)
	}

	list := s.autopilotSt().store.pendingSuggestions()
	if len(list) != 1 || list[0].PostID != "req" || list[0].Draft == "" || list[0].EstimatedCredits != 10 ||
		list[0].Bounty == nil || list[0].Bounty.Amount != 50 || list[0].PostAuthorName != "Someone" ||
		!list[0].ExpiresAt.Equal(apNow.Add(72*time.Hour)) {
		t.Errorf("suggestions = %+v", list)
	}
	replies, credits := s.autopilotSt().store.counters()
	if replies != 0 || credits != 10 {
		t.Errorf("counters = %d/%d", replies, credits)
	}

	// Second run: the pending suggestion blocks a second draft.
	res = s.runAutopilotOnce(context.Background())
	if res.Suggested != 0 || fb.draftCount() != 1 {
		t.Errorf("second run drafted again: %+v", res)
	}

	// Persistence: a fresh Server over the same data dir sees the suggestion and counters.
	s2 := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	s2.config.DataDir = s.config.DataDir
	if got := s2.autopilotSt().store.pendingSuggestions(); len(got) != 1 || got[0].ID != list[0].ID {
		t.Errorf("suggestion lost across restart: %+v", got)
	}
	if _, credits := s2.autopilotSt().store.counters(); credits != 10 {
		t.Errorf("credits lost across restart: %d", credits)
	}
	if _, err := os.Stat(filepath.Join(s.config.DataDir, autopilotStateFilename)); err != nil {
		t.Errorf("state file: %v", err)
	}
}

func TestAutopilotRun_BountyModePostsWithAutoFlag(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("big", apOther, "request", 1, withBounty(30, "open")),
			post("small", apOther, "request", 1, withBounty(20, "open")),
		}
	})
	fb.set(func(f *fakeBoard) { f.draftText = "I can help " + string(rune(0x2014)) + " send the spec." })
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty))
	res := s.runAutopilotOnce(context.Background())
	// "big" is answered; "small" (under 3 x 10) gets a bounty ask (phase 3).
	if res.Posted != 1 || res.Asked != 1 || res.Suggested != 0 || res.Credits != 20 {
		t.Fatalf("run = %+v", res)
	}
	if fb.replyCount() != 2 || fb.replyPostIDs[0] != "big" || fb.replyPostIDs[1] != "small" {
		t.Fatalf("replies = %v", fb.replyPostIDs)
	}
	body, hdr := fb.replyBodies[0], fb.replyHeaders[0]
	if body["auto"] != true || body["body"] != "I can help, send the spec." || hdr.Get(AutopilotAutoHeader) != "1" || hdr.Get("X-API-Key") != apPeer {
		t.Errorf("reply body=%v header=%v", body, hdr)
	}
	if sys := fb.drafts[0]["messages"].([]interface{})[0].(map[string]interface{})["content"].(string); !strings.Contains(sys, "Never use em dashes or en dashes") {
		t.Errorf("answer prompt lacks the dash rule: %q", sys)
	}
	if _, has := body["ask"]; has {
		t.Errorf("answer must carry no ask: %v", body)
	}
	if _, has := body["relevance"]; has {
		t.Errorf("scoring off: the reply must carry no relevance: %v", body)
	}
	if ask := fb.replyBodies[1]; ask["auto"] != true || ask["ask"] != float64(30) {
		t.Errorf("ask body = %v", ask)
	}
	st := s.autopilotSt().store
	if !st.replied("big") || st.replied("small") {
		t.Error("replied set wrong")
	}
	if a, ok := st.ask("small"); !ok || a.ReplyID != "r-2" || a.Ask != 30 {
		t.Errorf("ask record = %+v %v", a, ok)
	}
	out := st.outcomes()
	if len(out) != 1 || out[0].ReplyID != "r-1" || out[0].PostID != "big" || out[0].Trigger != "bounty" || out[0].Bounty != 30 || out[0].Scored {
		t.Errorf("outcomes = %+v", out)
	}
	replies, credits := st.counters()
	if replies != 2 || credits != 20 {
		t.Errorf("counters = %d/%d", replies, credits)
	}

	// Next run: "big" is replied (local), "small" has an ask pending. Nothing spent.
	res = s.runAutopilotOnce(context.Background())
	if res.Credits != 0 || res.Posted != 0 || res.Suggested != 0 || res.Asked != 0 {
		t.Errorf("rerun = %+v", res)
	}
}

func TestAutopilotRun_RemoteReplyCheckAndMention(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("done", apOther, "request", 1),
			post("ment", apOther, "general", 1, func(p *autopilotPost) { p.Content = "@Atlas Prime what do you think?" }),
		}
		f.replies["done"] = []autopilotReply{{ID: "r-old", Author: apPeer}}
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotAuto))
	res := s.runAutopilotOnce(context.Background())
	if res.Posted != 1 || res.Credits != 10 || fb.replyPostIDs[0] != "ment" {
		t.Fatalf("run = %+v replies=%v", res, fb.replyPostIDs)
	}
	if !s.autopilotSt().store.replied("done") {
		t.Error("remote reply must be recorded locally")
	}
	if out := s.autopilotSt().store.outcomes(); len(out) != 1 || out[0].Trigger != "mention" {
		t.Errorf("outcomes = %+v", out)
	}
}

func TestAutopilotRun_DailyCaps(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("a", apOther, "request", 1), post("b", apOther, "request", 1),
			post("c", apOther, "request", 1), post("d", apOther, "request", 1),
		}
	})
	// Reply cap 2 stops posting after two; credit cap 30 would allow three drafts.
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotAuto, func(p *config.AutopilotConfig) { p.MaxRepliesPerDay = 2 }))
	res := s.runAutopilotOnce(context.Background())
	if res.Posted != 2 || res.StoppedFor != "daily_reply_cap" || fb.draftCount() != 2 {
		t.Fatalf("reply cap run = %+v drafts=%d", res, fb.draftCount())
	}
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "daily_reply_cap" {
		t.Errorf("next run = %+v", res)
	}

	// Credit cap: 25 allows two drafts (20), the third would exceed it.
	fb.set(func(f *fakeBoard) {
		f.drafts, f.replyBodies, f.replyPostIDs = nil, nil, nil
		f.replies = map[string][]autopilotReply{} // forget the two replies above
	})
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) { p.DailyCreditCap = 25 }))
	res = s.runAutopilotOnce(context.Background())
	if res.Suggested != 2 || res.StoppedFor != "budget_exhausted" || res.Credits != 20 {
		t.Fatalf("credit cap run = %+v", res)
	}
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "daily_credit_cap" {
		t.Errorf("next run = %+v", res)
	}

	// Balance floor binds inside a run: balance 35, floor 20 leaves one draft.
	fb.set(func(f *fakeBoard) { f.drafts = nil; f.balance = 35 })
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) { p.DailyCreditCap = 100 }))
	res = s.runAutopilotOnce(context.Background())
	if res.Suggested != 1 || res.StoppedFor != "budget_exhausted" {
		t.Fatalf("floor run = %+v", res)
	}

	// Day rollover resets the counters.
	st := s.autopilotSt()
	st.now = func() time.Time { return apNow.Add(24 * time.Hour) }
	st.store.now = st.now
	if replies, credits := st.store.counters(); replies != 0 || credits != 0 {
		t.Errorf("after rollover = %d/%d", replies, credits)
	}
}

func TestAutopilotRun_InsufficientCreditsBacksOff(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("a", apOther, "request", 1), post("b", apOther, "request", 1)}
		f.draftStatus = http.StatusPaymentRequired
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotAuto))
	res := s.runAutopilotOnce(context.Background())
	if !res.BackedOff || res.StoppedFor != "backoff" || fb.draftCount() != 1 || res.Posted != 0 {
		t.Fatalf("run = %+v drafts=%d", res, fb.draftCount())
	}
	dto := s.autopilotDTOFor()
	if !strings.Contains(dto.Status.DowngradedReason, "402") {
		t.Errorf("downgradedReason = %q", dto.Status.DowngradedReason)
	}
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "backoff" {
		t.Errorf("during backoff = %+v", res)
	}
	// An hour later the watcher resumes.
	st := s.autopilotSt()
	st.now = func() time.Time { return apNow.Add(61 * time.Minute) }
	fb.set(func(f *fakeBoard) { f.draftStatus = 0 })
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "" || res.Posted != 2 {
		t.Errorf("after backoff = %+v", res)
	}

	// 429 on the reply itself backs off too and parks the draft as a suggestion.
	trk2, fb2 := newFakeBoard(t)
	fb2.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("a", apOther, "request", 1)}
		f.replyStatus = http.StatusTooManyRequests
	})
	s2 := newAutopilotTestServer(t, trk2.URL, policyWith(config.AutopilotAuto))
	res = s2.runAutopilotOnce(context.Background())
	if !res.BackedOff || res.Suggested != 1 || res.Posted != 0 {
		t.Errorf("429 run = %+v", res)
	}
	if got := s2.autopilotSt().store.pendingSuggestions(); len(got) != 1 {
		t.Errorf("draft not parked: %+v", got)
	}
}
