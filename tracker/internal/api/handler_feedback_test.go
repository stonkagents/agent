// Package: tracker/internal/api
// Feature: StonkAgents portal feedback
// Purpose: HTTP tests for POST /api/v1/feedback and GET /api/v1/admin/feedback

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
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const feedbackTestAdminKey = "admin-secret-key"

// fakeForwarder records forwarded submissions and can fail on demand.
type fakeForwarder struct {
	mu    sync.Mutex
	calls []*models.Feedback
	err   error
}

func (f *fakeForwarder) Forward(_ context.Context, fb *models.Feedback) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *fb
	f.calls = append(f.calls, &c)
	return f.err
}

func (f *fakeForwarder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type feedbackTestEnv struct {
	srv     *Server
	handler *FeedbackHandler
	repo    *repository.MemoryFeedbackRepository
	fwd     *fakeForwarder
	clock   *clock.MockClock
}

func newFeedbackTestEnv(t *testing.T, adminKey string, withLimiter bool) *feedbackTestEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	env := &feedbackTestEnv{repo: repository.NewMemoryFeedbackRepository(), fwd: &fakeForwarder{}, clock: clk}
	env.handler = NewFeedbackHandler(FeedbackHandlerDeps{Repo: env.repo, Forwarder: env.fwd, AdminKey: adminKey, Clock: clk})
	deps := ServerDeps{FeedbackHandler: env.handler, Address: ":7842"}
	if withLimiter {
		deps.Limiter = ratelimit.NewMemoryLimiter(clk)
	}
	env.srv = NewServer(deps)
	return env
}

func (e *feedbackTestEnv) do(t *testing.T, method, path string, body interface{}, headers map[string]string) *httptest.ResponseRecorder {
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

func feedbackBody() map[string]interface{} {
	return map[string]interface{}{
		"kind": "bug", "message": "The launch button does nothing on Safari.\nSecond line.", "path": "/launch",
		"contact": "@tev", "contactVia": "telegram", "walletAddress": lCreator,
	}
}

func TestFeedbackSubmit_StoresForwardsAndAnswersOK(t *testing.T) {
	env := newFeedbackTestEnv(t, feedbackTestAdminKey, true)
	w := env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil)
	if w.Code != http.StatusOK || w.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("status=%d body=%q, want 200 {\"ok\":true}", w.Code, w.Body.String())
	}
	items, _ := env.repo.List(context.Background(), repository.ListFeedbackOptions{})
	if len(items) != 1 {
		t.Fatalf("stored = %d, want 1", len(items))
	}
	fb := items[0]
	sum := sha256.Sum256([]byte("203.0.113.9"))
	want := &models.Feedback{
		ID: 1, Kind: "bug", Message: "The launch button does nothing on Safari.\nSecond line.", Path: "/launch",
		Contact: "@tev", ContactVia: "telegram", WalletAddress: lCreator, UserAgent: "portal-test/1.0",
		IPHash: hex.EncodeToString(sum[:]), CreatedAt: env.clock.Now(),
	}
	if !fb.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", fb.CreatedAt, want.CreatedAt)
	}
	fb.CreatedAt, want.CreatedAt = time.Time{}, time.Time{}
	if *fb != *want {
		t.Errorf("stored feedback = %+v\nwant %+v", fb, want)
	}
	env.handler.WaitForwards()
	if env.fwd.count() != 1 || env.fwd.calls[0].ID != 1 || env.fwd.calls[0].Message != fb.Message {
		t.Errorf("forwarded = %+v, want the stored submission once", env.fwd.calls)
	}
}

func TestFeedbackSubmit_ForwardFailureIsNeverReturned(t *testing.T) {
	env := newFeedbackTestEnv(t, "", false)
	env.fwd.err = errors.New("webhook down")
	w := env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 despite forward failure", w.Code, w.Body.String())
	}
	env.handler.WaitForwards()
	if env.fwd.count() != 1 {
		t.Errorf("forward attempts = %d, want 1", env.fwd.count())
	}
	if items, _ := env.repo.List(context.Background(), repository.ListFeedbackOptions{}); len(items) != 1 {
		t.Errorf("stored = %d, want 1", len(items))
	}
}

func TestFeedbackSubmit_NoForwarderConfigured(t *testing.T) {
	env := newFeedbackTestEnv(t, "", false)
	env.handler.forwarder = nil
	if w := env.do(t, http.MethodPost, "/api/v1/feedback", map[string]string{"kind": "idea", "message": "hi"}, nil); w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFeedbackSubmit_Validation(t *testing.T) {
	env := newFeedbackTestEnv(t, "", false)
	long := func(n int) string { return string(bytes.Repeat([]byte("x"), n)) }
	cases := []struct {
		name string
		mut  func(map[string]interface{})
	}{
		{"unknown kind", func(b map[string]interface{}) { b["kind"] = "praise" }},
		{"missing kind", func(b map[string]interface{}) { delete(b, "kind") }},
		{"empty message", func(b map[string]interface{}) { b["message"] = "   " }},
		{"message too long", func(b map[string]interface{}) { b["message"] = long(2001) }},
		{"message control char", func(b map[string]interface{}) { b["message"] = "bad\x00byte" }},
		{"path too long", func(b map[string]interface{}) { b["path"] = long(201) }},
		{"contact too long", func(b map[string]interface{}) { b["contact"] = long(201) }},
		{"contactVia too long", func(b map[string]interface{}) { b["contactVia"] = long(41) }},
		{"bad wallet", func(b map[string]interface{}) { b["walletAddress"] = "not-a-pubkey" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := feedbackBody()
			tc.mut(b)
			w := env.do(t, http.MethodPost, "/api/v1/feedback", b, nil)
			if w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" {
				t.Fatalf("status=%d body=%s, want 400 VALIDATION_ERROR", w.Code, w.Body.String())
			}
		})
	}
	if w := env.do(t, http.MethodPost, "/api/v1/feedback", "{not json", nil); w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST" {
		t.Errorf("invalid json: status=%d body=%s", w.Code, w.Body.String())
	}
	// Boundary values are accepted; kind is case-insensitive; optional fields may be absent.
	ok := map[string]interface{}{"kind": "WANTED", "message": long(2000), "path": long(200), "contact": long(200)}
	if w := env.do(t, http.MethodPost, "/api/v1/feedback", ok, nil); w.Code != http.StatusOK {
		t.Errorf("boundary body: status=%d body=%s", w.Code, w.Body.String())
	}
	if items, _ := env.repo.List(context.Background(), repository.ListFeedbackOptions{}); len(items) != 1 || items[0].Kind != "wanted" {
		t.Errorf("stored after validation cases = %+v, want exactly the boundary body as kind=wanted", items)
	}
	if env.fwd.count() != 0 {
		env.handler.WaitForwards()
	}
}

func TestFeedbackSubmit_RateLimited5PerMinutePerIP(t *testing.T) {
	env := newFeedbackTestEnv(t, "", true)
	for i := 0; i < 5; i++ {
		if w := env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil); w.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	w := env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil)
	if w.Code != http.StatusTooManyRequests || errorCode(t, w) != "RATE_LIMITED" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("6th request: status=%d body=%s headers=%v, want 429 RATE_LIMITED", w.Code, w.Body.String(), w.Header())
	}
	// Another IP has its own bucket.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", bytes.NewBufferString(`{"kind":"other","message":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.7:1"
	rec := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("other IP: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The window rolls over.
	env.clock.Advance(61 * time.Second)
	if w := env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil); w.Code != http.StatusOK {
		t.Errorf("after window: status=%d body=%s", w.Code, w.Body.String())
	}
	env.handler.WaitForwards()
}

func decodeFeedbackList(t *testing.T, w *httptest.ResponseRecorder) (items []map[string]interface{}, next string) {
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

func TestFeedbackAdmin_AuthAndCursorPaging(t *testing.T) {
	env := newFeedbackTestEnv(t, feedbackTestAdminKey, true)
	for i := 0; i < 7; i++ {
		_ = env.repo.Create(context.Background(), &models.Feedback{Kind: "idea", Message: "m", CreatedAt: env.clock.Now()})
	}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/feedback", nil, nil); w.Code != http.StatusUnauthorized || errorCode(t, w) != "UNAUTHORIZED" {
		t.Errorf("no key: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/feedback", nil, map[string]string{AdminKeyHeader: "wrong"}); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status=%d body=%s", w.Code, w.Body.String())
	}
	auth := map[string]string{AdminKeyHeader: feedbackTestAdminKey}
	if w := env.do(t, http.MethodGet, "/api/v1/admin/feedback?cursor=abc", nil, auth); w.Code != http.StatusBadRequest {
		t.Errorf("bad cursor: status=%d body=%s", w.Code, w.Body.String())
	}

	w := env.do(t, http.MethodGet, "/api/v1/admin/feedback?limit=3", nil, auth)
	if w.Code != http.StatusOK {
		t.Fatalf("page 1: status=%d body=%s", w.Code, w.Body.String())
	}
	page1, next1 := decodeFeedbackList(t, w)
	if len(page1) != 3 || page1[0]["id"].(float64) != 7 || page1[2]["id"].(float64) != 5 || next1 != "5" {
		t.Fatalf("page 1 = %v next=%q, want ids 7,6,5 next=5", page1, next1)
	}
	w = env.do(t, http.MethodGet, "/api/v1/admin/feedback?limit=3&cursor="+next1, nil, auth)
	page2, next2 := decodeFeedbackList(t, w)
	if len(page2) != 3 || page2[0]["id"].(float64) != 4 || page2[2]["id"].(float64) != 2 || next2 != "2" {
		t.Fatalf("page 2 = %v next=%q, want ids 4,3,2 next=2", page2, next2)
	}
	w = env.do(t, http.MethodGet, "/api/v1/admin/feedback?limit=3&cursor="+next2, nil, auth)
	page3, next3 := decodeFeedbackList(t, w)
	if len(page3) != 1 || page3[0]["id"].(float64) != 1 || next3 != "" {
		t.Fatalf("page 3 = %v next=%q, want id 1 and no cursor", page3, next3)
	}
	// Default limit serves everything here; the ip_hash and user_agent columns are exposed to admins.
	w = env.do(t, http.MethodGet, "/api/v1/admin/feedback", nil, auth)
	all, next := decodeFeedbackList(t, w)
	if len(all) != 7 || next != "" {
		t.Errorf("default page = %d items next=%q", len(all), next)
	}
}

// Without ADMIN_API_KEY the admin route answers a clear 503 ADMIN_DISABLED (not a 404 that
// looks like a wrong path, nor a 401 that looks like a wrong key).
func TestFeedbackAdmin_503WithoutAdminKey(t *testing.T) {
	env := newFeedbackTestEnv(t, "", true)
	w := env.do(t, http.MethodGet, "/api/v1/admin/feedback", nil, map[string]string{AdminKeyHeader: "anything"})
	if w.Code != http.StatusServiceUnavailable || errorCode(t, w) != AdminDisabledCode {
		t.Errorf("status=%d body=%s, want 503 %s", w.Code, w.Body.String(), AdminDisabledCode)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("ADMIN_API_KEY")) {
		t.Errorf("message must name the missing env var: %s", w.Body.String())
	}
	// The public route is still up.
	if w := env.do(t, http.MethodPost, "/api/v1/feedback", map[string]string{"kind": "other", "message": "x"}, nil); w.Code != http.StatusOK {
		t.Errorf("submit: status=%d body=%s", w.Code, w.Body.String())
	}
	env.handler.WaitForwards()
}

func TestFeedbackCORS_AllowsAdminKeyHeader(t *testing.T) {
	env := newFeedbackTestEnv(t, feedbackTestAdminKey, false)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/admin/feedback", nil)
	req.Header.Set("Origin", "https://portal.example")
	rec := httptest.NewRecorder()
	CORSMiddleware(env.srv.Router()).ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !bytes.Contains([]byte(got), []byte(AdminKeyHeader)) {
		t.Errorf("Allow-Headers = %q, want it to include %s", got, AdminKeyHeader)
	}
}

// fakeTurnstile scripts the verifier outcome and records the token/IP it was handed.
type fakeTurnstile struct {
	err   error
	token string
	ip    string
	calls int
}

func (f *fakeTurnstile) Verify(_ context.Context, token, ip string) error {
	f.calls++
	f.token, f.ip = token, ip
	return f.err
}

// With TURNSTILE_SECRET_KEY set the X-Turnstile-Token header is verified before the row is
// stored: missing/rejected -> 403 TURNSTILE_FAILED, siteverify down -> 503 (fail closed).
func TestFeedbackSubmit_TurnstileVerified(t *testing.T) {
	env := newFeedbackTestEnv(t, feedbackTestAdminKey, false)
	ts := &fakeTurnstile{}
	env.handler = NewFeedbackHandler(FeedbackHandlerDeps{Repo: env.repo, Forwarder: env.fwd, Turnstile: ts, AdminKey: feedbackTestAdminKey, Clock: env.clock})
	env.srv = NewServer(ServerDeps{FeedbackHandler: env.handler, Address: ":7842"})

	w := env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), map[string]string{TurnstileTokenHeader: "widget-token"})
	if w.Code != http.StatusOK {
		t.Fatalf("verified submit: status=%d body=%s", w.Code, w.Body.String())
	}
	if ts.calls != 1 || ts.token != "widget-token" || ts.ip != "203.0.113.9" {
		t.Errorf("verifier got calls=%d token=%q ip=%q", ts.calls, ts.token, ts.ip)
	}

	ts.err = services.ErrTurnstileMissing
	w = env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil)
	if w.Code != http.StatusForbidden || errorCode(t, w) != "TURNSTILE_FAILED" {
		t.Errorf("missing token: status=%d body=%s, want 403 TURNSTILE_FAILED", w.Code, w.Body.String())
	}
	ts.err = services.ErrTurnstileRejected
	w = env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), map[string]string{TurnstileTokenHeader: "bad"})
	if w.Code != http.StatusForbidden || errorCode(t, w) != "TURNSTILE_FAILED" {
		t.Errorf("rejected token: status=%d body=%s, want 403 TURNSTILE_FAILED", w.Code, w.Body.String())
	}
	ts.err = services.ErrTurnstileUnavailable
	w = env.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), map[string]string{TurnstileTokenHeader: "tok"})
	if w.Code != http.StatusServiceUnavailable || errorCode(t, w) != "TURNSTILE_UNAVAILABLE" {
		t.Errorf("siteverify down: status=%d body=%s, want 503 TURNSTILE_UNAVAILABLE", w.Code, w.Body.String())
	}
	// Only the verified submission was stored; validation still runs first (no verifier call on a bad body).
	items, _ := env.repo.List(context.Background(), repository.ListFeedbackOptions{})
	if len(items) != 1 {
		t.Errorf("stored = %d, want 1", len(items))
	}
	calls := ts.calls
	if w := env.do(t, http.MethodPost, "/api/v1/feedback", map[string]string{"kind": "nope"}, map[string]string{TurnstileTokenHeader: "tok"}); w.Code != http.StatusBadRequest {
		t.Errorf("invalid body: status=%d", w.Code)
	}
	if ts.calls != calls {
		t.Errorf("verifier called on an invalid body (calls %d -> %d)", calls, ts.calls)
	}
	env.handler.WaitForwards()

	// Without a verifier (TURNSTILE_SECRET_KEY unset) the header is ignored.
	plain := newFeedbackTestEnv(t, feedbackTestAdminKey, false)
	if w := plain.do(t, http.MethodPost, "/api/v1/feedback", feedbackBody(), nil); w.Code != http.StatusOK {
		t.Errorf("no verifier: status=%d body=%s", w.Code, w.Body.String())
	}
	plain.handler.WaitForwards()
}

func TestFeedbackCORS_AllowsTurnstileHeader(t *testing.T) {
	env := newFeedbackTestEnv(t, "", false)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/feedback", nil)
	req.Header.Set("Origin", "https://portal.example")
	rec := httptest.NewRecorder()
	CORSMiddleware(env.srv.Router()).ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !bytes.Contains([]byte(got), []byte(TurnstileTokenHeader)) {
		t.Errorf("Allow-Headers = %q, want it to include %s", got, TurnstileTokenHeader)
	}
}
