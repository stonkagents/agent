// Package: internal/daemon
// Purpose: Tests for the Agent Autopilot setup routes and the state store.

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

// --- endpoints ----------------------------------------------------------

func autopilotData(t *testing.T, w *httptest.ResponseRecorder) autopilotDTO {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp autopilotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return resp.Data
}

func TestSetupAutopilot_GetDefaults(t *testing.T) {
	trk, _ := newFakeBoard(t)
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	got := autopilotData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", "")))
	if got.Policy.Mode != "off" || got.Policy.DailyCreditCap != 30 || got.Policy.MaxRepliesPerDay != 3 ||
		got.Policy.MinBountyMultiple != 3 || got.Policy.BalanceFloor != 20 || got.Policy.ThreadCooldownHours != 24 ||
		got.Policy.MaxPostAgeHours != 48 || strings.Join(got.Policy.Categories, ",") != "request" {
		t.Errorf("policy = %+v", got.Policy)
	}
	if got.Status.Enabled || got.Status.LastRunAt != "" || got.Status.RepliesToday != 0 || got.Status.SuggestionsPending != 0 {
		t.Errorf("status = %+v", got.Status)
	}
	// Wire shape: camelCase keys the portal binds to.
	w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", ""))
	for _, key := range []string{`"dailyCreditCap"`, `"maxRepliesPerDay"`, `"minBountyMultiple"`, `"balanceFloor"`, `"threadCooldownHours"`, `"maxPostAgeHours"`, `"instruction"`, `"repliesToday"`, `"creditsSpentToday"`, `"suggestionsPending"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("missing %s in %s", key, w.Body.String())
		}
	}
}

func TestSetupAutopilot_PostPartialPersistsAndWakes(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) { f.posts = []autopilotPost{post("p1", apOther, "request", 1)} })
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))

	body := `{"mode":"suggest","categories":["general","request"],"dailyCreditCap":40,"instruction":"  Be brief  ","officeHours":{"start":"08:00","end":"20:00","tz":"UTC"}}`
	got := autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", body)))
	if got.Policy.Mode != "suggest" || got.Policy.DailyCreditCap != 40 || got.Policy.MaxRepliesPerDay != 3 ||
		got.Policy.Instruction != "Be brief" || strings.Join(got.Policy.Categories, ",") != "general,request" ||
		got.Policy.OfficeHours == nil || got.Policy.OfficeHours.TZ != "UTC" || !got.Status.Enabled {
		t.Errorf("post = %+v", got)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autopilot.Mode != "suggest" || cfg.Autopilot.DailyCreditCap != 40 || cfg.Autopilot.Instruction != "Be brief" ||
		cfg.Autopilot.OfficeHours == nil || cfg.DataDir != s.config.DataDir {
		t.Errorf("config.yaml = %+v", cfg.Autopilot)
	}
	if s.autopilotPolicy().Mode != "suggest" || s.config.Autopilot.Mode != "suggest" {
		t.Error("policy not applied live")
	}
	// Turning on from off queues an immediate run.
	select {
	case <-s.autopilotSt().wake:
	default:
		t.Error("expected a wake signal after enabling")
	}

	// Partial update keeps the rest; officeHours: null clears the window.
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"maxRepliesPerDay":5,"officeHours":null}`)))
	if got.Policy.Mode != "suggest" || got.Policy.DailyCreditCap != 40 || got.Policy.MaxRepliesPerDay != 5 || got.Policy.OfficeHours != nil {
		t.Errorf("partial = %+v", got.Policy)
	}
	select {
	case <-s.autopilotSt().wake:
		t.Error("a change while already enabled must not wake again")
	default:
	}
	cfg, _ = config.Load()
	if cfg.Autopilot.OfficeHours != nil || cfg.Autopilot.MaxRepliesPerDay != 5 {
		t.Errorf("config.yaml after partial = %+v", cfg.Autopilot)
	}

	// Empty body is a no-op that still answers the current state.
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", "")))
	if got.Policy.MaxRepliesPerDay != 5 {
		t.Errorf("empty body = %+v", got.Policy)
	}
}

func TestSetupAutopilot_PostValidation(t *testing.T) {
	trk, _ := newFakeBoard(t)
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	cases := map[string]string{
		"bad mode":         `{"mode":"manual"}`,
		"bad category":     `{"categories":["bounty"]}`,
		"cap zero":         `{"dailyCreditCap":0}`,
		"replies too high": `{"maxRepliesPerDay":1000}`,
		"multiple low":     `{"minBountyMultiple":0.2}`,
		"floor negative":   `{"balanceFloor":-5}`,
		"cooldown zero":    `{"threadCooldownHours":0}`,
		"age huge":         `{"maxPostAgeHours":100000}`,
		"instruction long": `{"instruction":"` + strings.Repeat("a", 501) + `"}`,
		"instruction ctrl": `{"instruction":"ab"}`,
		"office bad":       `{"officeHours":{"start":"9","end":"17:00"}}`,
		"office not obj":   `{"officeHours":"09:00-17:00"}`,
		"invalid json":     `{"mode":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}
	// Nothing was written.
	if cfg, _ := config.Load(); cfg.Autopilot.Mode != "off" {
		t.Errorf("rejected policy persisted: %+v", cfg.Autopilot)
	}
	for _, body := range cases {
		if strings.Contains(body, "—") || strings.Contains(body, "–") {
			t.Error("dash in test body")
		}
	}
}

func TestSetupAutopilot_HeaderEnforcement(t *testing.T) {
	trk, _ := newFakeBoard(t)
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))

	// Not loopback: everything refused.
	for _, p := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/setup/autopilot"},
		{http.MethodPost, "/api/v1/setup/autopilot"},
		{http.MethodGet, "/api/v1/setup/autopilot/suggestions"},
		{http.MethodPost, "/api/v1/setup/autopilot/suggestions/x/approve"},
		{http.MethodPost, "/api/v1/setup/autopilot/suggestions/x/dismiss"},
	} {
		req := setupRequest(p.method, p.path, `{}`)
		req.RemoteAddr = "203.0.113.9:4444"
		if w := do(s, req); w.Code != http.StatusForbidden {
			t.Errorf("%s %s from remote = %d", p.method, p.path, w.Code)
		}
	}
	// Loopback POST without the mutation headers: refused.
	for _, path := range []string{"/api/v1/setup/autopilot", "/api/v1/setup/autopilot/suggestions/x/approve", "/api/v1/setup/autopilot/suggestions/x/dismiss"} {
		req := setupRequest(http.MethodPost, path, `{"mode":"suggest"}`)
		req.Header.Del(SetupHeader)
		if w := do(s, req); w.Code != http.StatusForbidden {
			t.Errorf("POST %s without header = %d", path, w.Code)
		}
		req = setupRequest(http.MethodPost, path, `{"mode":"suggest"}`)
		req.Header.Set("Content-Type", "text/plain")
		if w := do(s, req); w.Code != http.StatusForbidden {
			t.Errorf("POST %s text/plain = %d", path, w.Code)
		}
	}
	if s.autopilotPolicy().Mode != "off" {
		t.Error("refused POST changed the policy")
	}
	// Rate limit shared with the other setup routes.
	s.setupSt().limiter = newSetupFixLimiter()
	var last int
	for i := 0; i < 8; i++ {
		last = do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{}`)).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("burst not limited, last = %d", last)
	}
}

func TestSetupAutopilot_SuggestionsApproveDismiss(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("p1", apOther, "request", 1), post("p2", apOther, "request", 1)}
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	if res := s.runAutopilotOnce(context.Background()); res.Suggested != 2 {
		t.Fatalf("run = %+v", res)
	}

	w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot/suggestions", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var list autopilotSuggestionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Data) != 2 {
		t.Fatalf("list body = %s err=%v", w.Body.String(), err)
	}
	for _, key := range []string{`"postId"`, `"postTitle"`, `"postAuthorName"`, `"category"`, `"draft"`, `"estimatedCredits"`, `"createdAt"`, `"expiresAt"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("missing %s", key)
		}
	}
	if got := autopilotData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", ""))); got.Status.SuggestionsPending != 2 || got.Status.LastRunAt == "" {
		t.Errorf("status = %+v", got.Status)
	}

	var byPost = map[string]autopilotSuggestion{}
	for _, sg := range list.Data {
		byPost[sg.PostID] = sg
	}

	// Approve posts the draft with auto: true and records the outcome.
	w = do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/"+byPost["p1"].ID+"/approve", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", w.Code, w.Body.String())
	}
	var approved struct {
		Data struct {
			ReplyID string `json:"replyId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &approved)
	if approved.Data.ReplyID != "r-1" || fb.replyCount() != 1 || fb.replyPostIDs[0] != "p1" ||
		fb.replyBodies[0]["auto"] != true || fb.replyBodies[0]["body"] != byPost["p1"].Draft || fb.replyHeaders[0].Get(AutopilotAutoHeader) != "1" {
		t.Errorf("approve reply = %v bodies=%v", approved, fb.replyBodies)
	}
	st := s.autopilotSt().store
	if !st.replied("p1") || len(st.outcomes()) != 1 || st.outcomes()[0].Trigger != "approve" {
		t.Errorf("approve not recorded: %+v", st.outcomes())
	}
	// Approving twice: gone.
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/"+byPost["p1"].ID+"/approve", "")); w.Code != http.StatusNotFound {
		t.Errorf("re-approve = %d", w.Code)
	}

	// Dismiss removes without posting and starts the cooldown.
	w = do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/"+byPost["p2"].ID+"/dismiss", ""))
	if w.Code != http.StatusOK || fb.replyCount() != 1 {
		t.Errorf("dismiss = %d replies=%d", w.Code, fb.replyCount())
	}
	if got := st.pendingSuggestions(); len(got) != 0 {
		t.Errorf("after dismiss = %+v", got)
	}
	if seen, ok := st.lastSeen("p2"); !ok || !seen.Equal(apNow) {
		t.Errorf("dismiss must touch the thread: %v %v", seen, ok)
	}
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/nope/dismiss", "")); w.Code != http.StatusNotFound {
		t.Errorf("dismiss unknown = %d", w.Code)
	}
	// Dismissed thread is in cooldown: the next run drafts nothing for it.
	if res := s.runAutopilotOnce(context.Background()); res.Credits != 0 {
		t.Errorf("cooldown ignored: %+v", res)
	}

	// Approve failure upstream keeps the suggestion.
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("p3", apOther, "request", 1)}
		f.replyStatus = http.StatusInternalServerError
	})
	if res := s.runAutopilotOnce(context.Background()); res.Suggested != 1 {
		t.Fatalf("p3 run = %+v", res)
	}
	id3 := st.pendingSuggestions()[0].ID
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/"+id3+"/approve", "")); w.Code != http.StatusInternalServerError {
		t.Errorf("failed approve = %d %s", w.Code, w.Body.String())
	}
	if got := st.pendingSuggestions(); len(got) != 1 || got[0].ID != id3 {
		t.Errorf("suggestion lost after failed approve: %+v", got)
	}
}

func TestAutopilotStore_ExpiryAndReload(t *testing.T) {
	dir := t.TempDir()
	now := apNow
	st := newAutopilotStore(dir, func() time.Time { return now })
	if err := st.load(); err != nil {
		t.Fatal(err)
	}
	fresh := autopilotSuggestion{ID: "a", PostID: "p", Draft: "x", CreatedAt: now, ExpiresAt: now.Add(autopilotSuggestionTTL)}
	if err := st.addSuggestion(fresh); err != nil {
		t.Fatal(err)
	}
	if err := st.recordReply(autopilotOutcome{ReplyID: "r", PostID: "q", At: now, Trigger: "category", Credits: 10}); err != nil {
		t.Fatal(err)
	}

	st2 := newAutopilotStore(dir, func() time.Time { return now })
	if err := st2.load(); err != nil {
		t.Fatal(err)
	}
	if got := st2.pendingSuggestions(); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("reload = %+v", got)
	}
	if !st2.replied("q") || len(st2.outcomes()) != 1 {
		t.Error("replied/outcomes not reloaded")
	}
	if replies, credits := st2.counters(); replies != 1 || credits != 0 {
		t.Errorf("counters = %d/%d", replies, credits)
	}
	// Three days later the suggestion is gone and cannot be taken.
	now = apNow.Add(autopilotSuggestionTTL + time.Minute)
	if got := st2.pendingSuggestions(); len(got) != 0 {
		t.Errorf("expired suggestion listed: %+v", got)
	}
	if _, ok := st2.takeSuggestion("a"); ok {
		t.Error("expired suggestion approvable")
	}
	// A corrupt file starts empty instead of crashing.
	if err := os.WriteFile(filepath.Join(dir, autopilotStateFilename), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st3 := newAutopilotStore(dir, func() time.Time { return now })
	if err := st3.load(); err == nil {
		t.Error("corrupt file must be reported")
	}
	if replies, _ := st3.counters(); replies != 0 {
		t.Error("corrupt file must start empty")
	}
}

func TestAutopilot_NoDashesInUserFacingStrings(t *testing.T) {
	for _, f := range []string{"autopilot.go", "autopilot_api.go", "autopilot_decide.go", "autopilot_digest.go", "autopilot_store.go", "autopilot_tracker.go", filepath.Join("..", "config", "autopilot.go")} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(string(raw), "—–") {
			t.Errorf("%s contains an em or en dash", f)
		}
	}
}
