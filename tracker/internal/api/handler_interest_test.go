// Package: tracker/internal/api
// Feature: StonkAgents roadmap interest
// Purpose: HTTP tests for POST /api/v1/interest, GET /api/v1/admin/interest and
//          GET /api/v1/admin/interest/summary

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

const interestTestAdminKey = "interest-admin-secret"

// fakeInterestForwarder records forwarded submissions and can fail on demand.
type fakeInterestForwarder struct {
	mu    sync.Mutex
	calls []*models.AgentInterest
	err   error
}

func (f *fakeInterestForwarder) ForwardInterest(_ context.Context, it *models.AgentInterest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *it
	f.calls = append(f.calls, &c)
	return f.err
}

func (f *fakeInterestForwarder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type interestTestEnv struct {
	srv     *Server
	handler *InterestHandler
	repo    *repository.MemoryInterestRepository
	fwd     *fakeInterestForwarder
	clock   *clock.MockClock
}

func newInterestTestEnv(t *testing.T, adminKey string, withLimiter bool) *interestTestEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	env := &interestTestEnv{repo: repository.NewMemoryInterestRepository(), fwd: &fakeInterestForwarder{}, clock: clk}
	env.handler = NewInterestHandler(InterestHandlerDeps{Repo: env.repo, Forwarder: env.fwd, AdminKey: adminKey, Clock: clk})
	deps := ServerDeps{InterestHandler: env.handler, Address: ":7842"}
	if withLimiter {
		deps.Limiter = ratelimit.NewMemoryLimiter(clk)
	}
	env.srv = NewServer(deps)
	return env
}

func (e *interestTestEnv) do(t *testing.T, method, path string, body interface{}, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "portal-test/1.0")
	req.RemoteAddr = "203.0.113.9:5555"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.srv.Router().ServeHTTP(w, req)
	return w
}

func interestBody() map[string]interface{} {
	return map[string]interface{}{
		"capabilities": []string{"trade", "alerts"}, "description": "Snipe new launches and ping me.\nSecond line.",
		"priority": "pay", "path": "/", "contact": "@tev", "contactVia": "telegram", "walletAddress": lCreator,
	}
}

func decodeInterestSubmit(t *testing.T, w *httptest.ResponseRecorder) (ok bool, counts map[string]int64) {
	t.Helper()
	var resp struct {
		OK     bool             `json:"ok"`
		Counts map[string]int64 `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return resp.OK, resp.Counts
}

func TestInterestSubmit_StoresForwardsAndAnswersCounts(t *testing.T) {
	env := newInterestTestEnv(t, interestTestAdminKey, true)
	w := env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	ok, counts := decodeInterestSubmit(t, w)
	if !ok || !reflect.DeepEqual(counts, map[string]int64{"trade": 1, "alerts": 1}) {
		t.Fatalf("body=%s, want ok:true with counts trade=1 alerts=1", w.Body.String())
	}
	items, _ := env.repo.List(context.Background(), repository.ListInterestOptions{})
	if len(items) != 1 {
		t.Fatalf("stored = %d, want 1", len(items))
	}
	it := items[0]
	sum := sha256.Sum256([]byte("203.0.113.9"))
	want := &models.AgentInterest{
		ID: 1, Capabilities: []string{"trade", "alerts"}, Description: "Snipe new launches and ping me.\nSecond line.",
		Priority: "pay", Path: "/", Contact: "@tev", ContactVia: "telegram", WalletAddress: lCreator,
		UserAgent: "portal-test/1.0", IPHash: hex.EncodeToString(sum[:]), CreatedAt: env.clock.Now(),
	}
	if !it.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", it.CreatedAt, want.CreatedAt)
	}
	it.CreatedAt, want.CreatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(it, want) {
		t.Errorf("stored interest = %+v\nwant %+v", it, want)
	}
	env.handler.WaitForwards()
	if env.fwd.count() != 1 || env.fwd.calls[0].ID != 1 || env.fwd.calls[0].Description != it.Description {
		t.Errorf("forwarded = %+v, want the stored submission once", env.fwd.calls)
	}
}

func TestInterestSubmit_CountsGrowAndOnlyCoverSelectedKeys(t *testing.T) {
	env := newInterestTestEnv(t, "", false)
	env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil) // trade, alerts
	second := map[string]interface{}{"capabilities": []string{"trade", "learn"}, "priority": "nice"}
	w := env.do(t, http.MethodPost, "/api/v1/interest", second, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	_, counts := decodeInterestSubmit(t, w)
	if !reflect.DeepEqual(counts, map[string]int64{"trade": 2, "learn": 1}) {
		t.Errorf("counts = %v, want trade=2 learn=1 (alerts not selected, not reported)", counts)
	}
	env.handler.WaitForwards()
}

func TestInterestSubmit_ForwardFailureIsNeverReturned(t *testing.T) {
	env := newInterestTestEnv(t, "", false)
	env.fwd.err = errors.New("webhook down")
	w := env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 despite forward failure", w.Code, w.Body.String())
	}
	env.handler.WaitForwards()
	if env.fwd.count() != 1 {
		t.Errorf("forward attempts = %d, want 1", env.fwd.count())
	}
	if items, _ := env.repo.List(context.Background(), repository.ListInterestOptions{}); len(items) != 1 {
		t.Errorf("stored = %d, want 1", len(items))
	}
}

func TestInterestSubmit_NoForwarderConfigured(t *testing.T) {
	env := newInterestTestEnv(t, "", false)
	env.handler.forwarder = nil
	body := map[string]interface{}{"capabilities": []string{"token"}, "priority": "important"}
	if w := env.do(t, http.MethodPost, "/api/v1/interest", body, nil); w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInterestSubmit_Validation(t *testing.T) {
	env := newInterestTestEnv(t, "", false)
	long := func(n int) string { return string(bytes.Repeat([]byte("x"), n)) }
	// Nine entries with one unknown: rejected on the unknown key (a ninth distinct key cannot exist).
	nine := append(append([]string{}, models.InterestCapabilities...), "teleport")
	cases := []struct {
		name string
		mut  func(map[string]interface{})
	}{
		{"missing capabilities", func(b map[string]interface{}) { delete(b, "capabilities") }},
		{"empty capabilities", func(b map[string]interface{}) { b["capabilities"] = []string{} }},
		{"blank capabilities", func(b map[string]interface{}) { b["capabilities"] = []string{" ", ""} }},
		{"unknown key", func(b map[string]interface{}) { b["capabilities"] = []string{"trade", "teleport"} }},
		{"nine keys", func(b map[string]interface{}) { b["capabilities"] = nine }},
		{"other without description", func(b map[string]interface{}) { b["capabilities"] = []string{"other"}; b["description"] = "  " }},
		{"description too long", func(b map[string]interface{}) { b["description"] = long(601) }},
		{"description control char", func(b map[string]interface{}) { b["description"] = "bad\x00byte" }},
		{"bad priority", func(b map[string]interface{}) { b["priority"] = "urgent" }},
		{"missing priority", func(b map[string]interface{}) { delete(b, "priority") }},
		{"path too long", func(b map[string]interface{}) { b["path"] = long(201) }},
		{"contact too long", func(b map[string]interface{}) { b["contact"] = long(201) }},
		{"contactVia too long", func(b map[string]interface{}) { b["contactVia"] = long(41) }},
		{"bad wallet", func(b map[string]interface{}) { b["walletAddress"] = "not-a-pubkey" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := interestBody()
			tc.mut(b)
			w := env.do(t, http.MethodPost, "/api/v1/interest", b, nil)
			if w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" {
				t.Fatalf("status=%d body=%s, want 400 VALIDATION_ERROR", w.Code, w.Body.String())
			}
		})
	}
	if w := env.do(t, http.MethodPost, "/api/v1/interest", "{not json", nil); w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST" {
		t.Errorf("invalid json: status=%d body=%s", w.Code, w.Body.String())
	}
	if items, _ := env.repo.List(context.Background(), repository.ListInterestOptions{}); len(items) != 0 {
		t.Fatalf("stored after rejected bodies = %d, want 0", len(items))
	}

	// Duplicates (and case/whitespace variants) collapse to one key; eight distinct keys pass.
	dup := map[string]interface{}{"capabilities": []string{"Trade", " trade", "TRADE "}, "priority": "nice"}
	w := env.do(t, http.MethodPost, "/api/v1/interest", dup, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("dup keys: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, counts := decodeInterestSubmit(t, w); !reflect.DeepEqual(counts, map[string]int64{"trade": 1}) {
		t.Errorf("dup keys counts = %v, want trade=1", counts)
	}
	// Boundary values are accepted; priority is case-insensitive; optional fields may be absent.
	ok := map[string]interface{}{"capabilities": models.InterestCapabilities, "description": long(600), "priority": "PAY", "path": long(200), "contact": long(200)}
	if w := env.do(t, http.MethodPost, "/api/v1/interest", ok, nil); w.Code != http.StatusOK {
		t.Errorf("boundary body: status=%d body=%s", w.Code, w.Body.String())
	}
	items, _ := env.repo.List(context.Background(), repository.ListInterestOptions{})
	if len(items) != 2 || items[0].Priority != "pay" || len(items[0].Capabilities) != 8 || !reflect.DeepEqual(items[1].Capabilities, []string{"trade"}) {
		t.Errorf("stored after accepted bodies = %+v, want the boundary row (pay, 8 keys) then the dedup row (trade)", items)
	}
	env.handler.WaitForwards()
}

func TestInterestSubmit_RateLimited5PerMinutePerIP(t *testing.T) {
	env := newInterestTestEnv(t, "", true)
	for i := 0; i < 5; i++ {
		if w := env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil); w.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	w := env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil)
	if w.Code != http.StatusTooManyRequests || errorCode(t, w) != "RATE_LIMITED" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("6th request: status=%d body=%s headers=%v, want 429 RATE_LIMITED", w.Code, w.Body.String(), w.Header())
	}
	// Another IP has its own bucket.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/interest", bytes.NewBufferString(`{"capabilities":["learn"],"priority":"nice"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.7:1"
	rec := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("other IP: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The window rolls over.
	env.clock.Advance(61 * time.Second)
	if w := env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil); w.Code != http.StatusOK {
		t.Errorf("after window: status=%d body=%s", w.Code, w.Body.String())
	}
	env.handler.WaitForwards()
}

func decodeInterestList(t *testing.T, w *httptest.ResponseRecorder) (items []map[string]interface{}, next string) {
	t.Helper()
	var resp struct {
		Data       []map[string]interface{} `json:"data"`
		NextCursor string                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return resp.Data, resp.NextCursor
}

func TestInterestAdmin_AuthAndCursorPaging(t *testing.T) {
	env := newInterestTestEnv(t, interestTestAdminKey, true)
	for i := 0; i < 7; i++ {
		_ = env.repo.Create(context.Background(), &models.AgentInterest{Capabilities: []string{"learn"}, Priority: "nice", CreatedAt: env.clock.Now()})
	}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/interest", nil, nil); w.Code != http.StatusUnauthorized || errorCode(t, w) != "UNAUTHORIZED" {
		t.Errorf("no key: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/interest", nil, map[string]string{AdminKeyHeader: "wrong"}); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status=%d body=%s", w.Code, w.Body.String())
	}
	auth := map[string]string{AdminKeyHeader: interestTestAdminKey}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/interest?cursor=abc", nil, auth); w.Code != http.StatusBadRequest {
		t.Errorf("bad cursor: status=%d body=%s", w.Code, w.Body.String())
	}

	w := env.do(t, http.MethodGet, "/api/v1/admin/interest?limit=3", nil, auth)
	if w.Code != http.StatusOK {
		t.Fatalf("page 1: status=%d body=%s", w.Code, w.Body.String())
	}
	page1, next1 := decodeInterestList(t, w)
	if len(page1) != 3 || page1[0]["id"].(float64) != 7 || page1[2]["id"].(float64) != 5 || next1 != "5" {
		t.Fatalf("page 1 = %v next=%q, want ids 7,6,5 next=5", page1, next1)
	}
	if caps, _ := page1[0]["capabilities"].([]interface{}); len(caps) != 1 || caps[0] != "learn" {
		t.Errorf("row capabilities = %v, want [learn]", page1[0]["capabilities"])
	}
	w = env.do(t, http.MethodGet, "/api/v1/admin/interest?limit=3&cursor="+next1, nil, auth)
	page2, next2 := decodeInterestList(t, w)
	if len(page2) != 3 || page2[0]["id"].(float64) != 4 || page2[2]["id"].(float64) != 2 || next2 != "2" {
		t.Fatalf("page 2 = %v next=%q, want ids 4,3,2 next=2", page2, next2)
	}
	w = env.do(t, http.MethodGet, "/api/v1/admin/interest?limit=3&cursor="+next2, nil, auth)
	page3, next3 := decodeInterestList(t, w)
	if len(page3) != 1 || page3[0]["id"].(float64) != 1 || next3 != "" {
		t.Fatalf("page 3 = %v next=%q, want id 1 and no cursor", page3, next3)
	}
	w = env.do(t, http.MethodGet, "/api/v1/admin/interest", nil, auth)
	if all, next := decodeInterestList(t, w); len(all) != 7 || next != "" {
		t.Errorf("default page = %d items next=%q", len(all), next)
	}
}

func TestInterestAdmin_Summary(t *testing.T) {
	env := newInterestTestEnv(t, interestTestAdminKey, true)
	auth := map[string]string{AdminKeyHeader: interestTestAdminKey}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/interest/summary", nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no key: status=%d body=%s", w.Code, w.Body.String())
	}
	decode := func(w *httptest.ResponseRecorder) models.InterestSummary {
		var s models.InterestSummary
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatalf("decode: %v (body %s)", err, w.Body.String())
		}
		return s
	}
	// Empty: every key present at 0.
	w := env.do(t, http.MethodGet, "/api/v1/admin/interest/summary", nil, auth)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	empty := decode(w)
	if empty.Total != 0 || len(empty.Capabilities) != 8 || len(empty.Priorities) != 3 || empty.Capabilities["trade"] != 0 || empty.Priorities["pay"] != 0 {
		t.Errorf("empty summary = %+v", empty)
	}

	env.do(t, http.MethodPost, "/api/v1/interest", interestBody(), nil) // trade+alerts, pay
	env.do(t, http.MethodPost, "/api/v1/interest", map[string]interface{}{"capabilities": []string{"trade", "learn"}, "priority": "important"}, nil)
	env.do(t, http.MethodPost, "/api/v1/interest", map[string]interface{}{"capabilities": []string{"other"}, "description": "Fold my laundry", "priority": "pay"}, nil)
	got := decode(env.do(t, http.MethodGet, "/api/v1/admin/interest/summary", nil, auth))
	wantCaps := map[string]int64{"trade": 2, "knowledge": 0, "learn": 1, "community": 0, "alerts": 1, "token": 0, "automate": 0, "other": 1}
	wantPris := map[string]int64{"nice": 0, "important": 1, "pay": 2}
	if got.Total != 3 || !reflect.DeepEqual(got.Capabilities, wantCaps) || !reflect.DeepEqual(got.Priorities, wantPris) {
		t.Errorf("summary = %+v\nwant total 3 caps %v pris %v", got, wantCaps, wantPris)
	}
	env.handler.WaitForwards()
}

func TestInterestAdmin_503WithoutAdminKey(t *testing.T) {
	env := newInterestTestEnv(t, "", true)
	for _, p := range []string{"/api/v1/admin/interest", "/api/v1/admin/interest/summary"} {
		w := env.do(t, http.MethodGet, p, nil, map[string]string{AdminKeyHeader: "anything"})
		if w.Code != http.StatusServiceUnavailable || errorCode(t, w) != AdminDisabledCode {
			t.Errorf("%s: status=%d body=%s, want 503 %s", p, w.Code, w.Body.String(), AdminDisabledCode)
		}
	}
	// The public route is still up.
	if w := env.do(t, http.MethodPost, "/api/v1/interest", map[string]interface{}{"capabilities": []string{"alerts"}, "priority": "nice"}, nil); w.Code != http.StatusOK {
		t.Errorf("submit: status=%d body=%s", w.Code, w.Body.String())
	}
	env.handler.WaitForwards()
}
