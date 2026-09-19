// Package: internal/daemon
// Purpose: Tests for GET/POST /api/v1/setup/identity (owner-set display name).

package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/daemon/tracker"
	"github.com/stonkagents/agent/internal/setup"
)

// heartbeatRecorder is a fake tracker that records every heartbeat body it receives.
type heartbeatRecorder struct {
	mu     sync.Mutex
	bodies []map[string]interface{}
	keys   []string
	status int
}

func newHeartbeatRecorder(t *testing.T, status int) (*httptest.Server, *heartbeatRecorder) {
	t.Helper()
	rec := &heartbeatRecorder{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tracker/heartbeat" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, body)
		rec.keys = append(rec.keys, r.Header.Get("X-API-Key"))
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		_, _ = w.Write([]byte(`{"data":{"status":"ok","next_heartbeat_seconds":120}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *heartbeatRecorder) last(t *testing.T) (map[string]interface{}, string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		t.Fatal("tracker received no heartbeat")
	}
	return r.bodies[len(r.bodies)-1], r.keys[len(r.keys)-1]
}

func (r *heartbeatRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// newIdentityTestServer is newSetupTestServer plus a registered tracker client
// pointed at trackerURL and a configured peer id.
func newIdentityTestServer(t *testing.T, trackerURL string) *Server {
	t.Helper()
	s := newSetupTestServer(t, "")
	s.config.PeerID = "12D3KooWTestPeer"
	s.config.PublicKey = "MCowBQYDK2VwAyEAtestpublickey="
	s.trackerClient = tracker.NewClient(trackerURL, s.config.PeerID)
	if err := s.saveTrackerAPIKey("api-key-1"); err != nil {
		t.Fatal(err)
	}
	return s
}

func identityData(t *testing.T, w *httptest.ResponseRecorder) identityDTO {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp identityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return resp.Data
}

func postIdentity(s *Server, name string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"displayName": name})
	return do(s, setupRequest(http.MethodPost, "/api/v1/setup/identity", string(body)))
}

func TestSetupIdentity_GetSetRoundTrip(t *testing.T) {
	trk, rec := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)

	// Fresh daemon: peer id, no name.
	got := identityData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/identity", "")))
	if got.PeerID != "12D3KooWTestPeer" || got.DisplayName != "" || got.TrackerSynced != nil {
		t.Errorf("initial = %+v", got)
	}
	if got.PublicKey != "MCowBQYDK2VwAyEAtestpublickey=" {
		t.Errorf("publicKey = %q, want the configured public key", got.PublicKey)
	}

	// Set: saved value echoed, pushed to the tracker with the API key, persisted, live.
	got = identityData(t, postIdentity(s, "  Atlas Prime  "))
	if got.DisplayName != "Atlas Prime" || got.PeerID != "12D3KooWTestPeer" {
		t.Errorf("set = %+v", got)
	}
	if got.TrackerSynced == nil || !*got.TrackerSynced || got.TrackerError != "" {
		t.Errorf("tracker sync = %+v", got)
	}
	body, key := rec.last(t)
	if body["display_name"] != "Atlas Prime" || key != "api-key-1" || body["client_version"] != "9.9.9-test" {
		t.Errorf("tracker heartbeat = %v key=%q", body, key)
	}
	if _, has := body["multiaddrs"]; has {
		t.Errorf("identity push must not send multiaddrs: %v", body)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisplayName != "Atlas Prime" || cfg.DataDir != s.config.DataDir {
		t.Errorf("config.yaml = %+v", cfg)
	}
	if s.displayName() != "Atlas Prime" {
		t.Errorf("live config = %q", s.displayName())
	}
	if got := identityData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/identity", ""))); got.DisplayName != "Atlas Prime" {
		t.Errorf("get after set = %+v", got)
	}
	// The regular heartbeat reads the same live value (doHeartbeat itself needs a P2P host).
	if s.displayName() != "Atlas Prime" {
		t.Errorf("heartbeat source = %q", s.displayName())
	}

	// Clear: an empty string resets to the generated placeholder (every agent keeps a
	// name); the placeholder is pushed too and the tracker decides whether it applies.
	placeholder := config.DefaultDisplayName("12D3KooWTestPeer")
	got = identityData(t, postIdentity(s, ""))
	if got.DisplayName != placeholder || got.TrackerSynced == nil || !*got.TrackerSynced {
		t.Errorf("clear = %+v, want placeholder %q", got, placeholder)
	}
	if !config.IsPlaceholderDisplayName(got.DisplayName) {
		t.Errorf("clear produced a non-placeholder %q", got.DisplayName)
	}
	body, _ = rec.last(t)
	if body["display_name"] != placeholder {
		t.Errorf("clear heartbeat must carry the placeholder: %v", body)
	}
	cfg, _ = config.Load()
	if cfg.DisplayName != placeholder {
		t.Errorf("config.yaml after clear = %q", cfg.DisplayName)
	}
	if s.displayName() != placeholder {
		t.Errorf("live config after clear = %q", s.displayName())
	}
	if c := statusChecks(t, s, "")[setup.IDTracker]; c.Status != setup.StatusOK {
		t.Errorf("tracker check after push = %+v", c)
	}
}

func TestSetupIdentity_Sanitization(t *testing.T) {
	trk, rec := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)

	// Angle brackets are stripped, surrounding whitespace trimmed, inner kept.
	if got := identityData(t, postIdentity(s, " <b>Nova</b> Bot ")); got.DisplayName != "bNova/b Bot" {
		t.Errorf("stripped = %q", got.DisplayName)
	}
	// Exactly 50 characters (multibyte) is accepted.
	fifty := strings.Repeat("é", 50)
	if got := identityData(t, postIdentity(s, fifty)); got.DisplayName != fifty {
		t.Errorf("50 runes = %q", got.DisplayName)
	}
	before := rec.count()

	// Rejected: too long, control characters, missing field, bad JSON, wrong type.
	rejected := []string{
		`{"displayName": "` + strings.Repeat("a", 51) + `"}`,
		`{"displayName": "` + strings.Repeat("é", 51) + `"}`,
		`{"displayName": "line\nbreak"}`,
		`{"displayName": "tab\tname"}`,
		"{\"displayName\": \"nul\u0000\"}",
		"{\"displayName\": \"esc\u001b[31m\"}",
		`{}`,
		``,
		`{bad`,
		`{"displayName": 42}`,
	}
	for _, body := range rejected {
		w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/identity", body))
		if w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST" {
			t.Errorf("%q: %d %s", body, w.Code, w.Body.String())
		}
	}
	// Rejected values touch neither the tracker, config.yaml nor the live config.
	if rec.count() != before {
		t.Errorf("tracker called %d extra times by rejected requests", rec.count()-before)
	}
	if cfg, _ := config.Load(); cfg.DisplayName != fifty || s.displayName() != fifty {
		t.Errorf("state changed by a rejected request: file=%q live=%q", cfg.DisplayName, s.displayName())
	}
	// Whitespace-only trims to empty and therefore resets to the placeholder.
	if got := identityData(t, postIdentity(s, "   ")); !config.IsPlaceholderDisplayName(got.DisplayName) {
		t.Errorf("blank = %q", got.DisplayName)
	}
}

func TestSetupIdentity_HeaderEnforcement(t *testing.T) {
	trk, rec := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)

	// Non-loopback: both verbs refused.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := setupRequest(method, "/api/v1/setup/identity", `{"displayName":"x"}`)
		req.RemoteAddr = "10.0.0.9:4444"
		if w := do(s, req); w.Code != http.StatusForbidden {
			t.Errorf("non-loopback %s = %d", method, w.Code)
		}
	}
	// POST without the CSRF-defeating headers is refused, whichever is missing.
	variants := []struct{ contentType, setupHeader string }{
		{"", ""}, {"application/json", ""}, {"text/plain", "1"}, {"application/x-www-form-urlencoded", "1"}, {"application/json", "yes"},
	}
	for _, v := range variants {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/identity", strings.NewReader(`{"displayName":"x"}`))
		req.RemoteAddr = loopbackAddr
		req.Header.Set("Origin", "https://dev.stonkagents.com")
		if v.contentType != "" {
			req.Header.Set("Content-Type", v.contentType)
		}
		if v.setupHeader != "" {
			req.Header.Set(SetupHeader, v.setupHeader)
		}
		if w := do(s, req); w.Code != http.StatusForbidden || errorCode(t, w) != "FORBIDDEN" {
			t.Errorf("%+v: %d %s", v, w.Code, w.Body.String())
		}
	}
	req := setupRequest(http.MethodPost, "/api/v1/setup/identity", `{"displayName":"x"}`)
	req.Header.Set("Origin", "null")
	if w := do(s, req); w.Code != http.StatusForbidden {
		t.Errorf("Origin null = %d", w.Code)
	}
	if rec.count() != 0 || s.displayName() != "" {
		t.Errorf("refused requests reached the tracker (%d) or config (%q)", rec.count(), s.displayName())
	}
	// GET needs no mutation headers; the preflight for POST lists the setup header.
	if w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/identity", "")); w.Code != http.StatusOK {
		t.Errorf("GET = %d %s", w.Code, w.Body.String())
	}
	pre := setupRequest(http.MethodOptions, "/api/v1/setup/identity", "")
	pre.Header.Set("Origin", "https://dev.stonkagents.com")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	pre.Header.Set("Access-Control-Request-Headers", "content-type, x-stonkagents-setup")
	if w := do(s, pre); w.Code != http.StatusNoContent || !strings.Contains(strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), strings.ToLower(SetupHeader)) {
		t.Errorf("preflight = %d %v", w.Code, w.Header())
	}
	// The rate limiter is shared with the other setup fixes.
	s.setupSt().limiter = newSetupFixLimiter()
	var last int
	for i := 0; i < 8; i++ {
		last = postIdentity(s, "Atlas").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("8th POST = %d, want 429", last)
	}
}

func TestSetupIdentity_TrackerFailureStillSaves(t *testing.T) {
	trk, rec := newHeartbeatRecorder(t, http.StatusServiceUnavailable)
	s := newIdentityTestServer(t, trk.URL)

	got := identityData(t, postIdentity(s, "Atlas"))
	if got.DisplayName != "Atlas" || got.TrackerSynced == nil || *got.TrackerSynced || !strings.Contains(got.TrackerError, "503") {
		t.Errorf("tracker down = %+v", got)
	}
	if rec.count() == 0 {
		t.Error("tracker was not tried")
	}
	if cfg, _ := config.Load(); cfg.DisplayName != "Atlas" || s.displayName() != "Atlas" {
		t.Errorf("name not saved: file=%q live=%q", cfg.DisplayName, s.displayName())
	}

	// Not registered yet (no API key, no P2P host): saved, sent with the next registration.
	s2 := newSetupTestServer(t, "")
	s2.trackerClient = tracker.NewClient(trk.URL, "p")
	got = identityData(t, postIdentity(s2, "Nova"))
	if got.DisplayName != "Nova" || *got.TrackerSynced || !strings.Contains(got.TrackerError, "not registered") {
		t.Errorf("unregistered = %+v", got)
	}
	if got := identityData(t, do(s2, setupRequest(http.MethodGet, "/api/v1/setup/identity", ""))); got.DisplayName != "Nova" || got.PeerID != "" {
		t.Errorf("unregistered get = %+v", got)
	}
}

func TestSetupIdentity_NotCaughtByFixRoute(t *testing.T) {
	// "identity" is not a setup check id; the literal routes must win over POST /setup/{id}.
	trk, _ := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)
	if w := postIdentity(s, "Atlas"); w.Code != http.StatusOK {
		t.Errorf("POST identity = %d %s", w.Code, w.Body.String())
	}
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/bogus", "")); w.Code != http.StatusNotFound {
		t.Errorf("POST bogus = %d", w.Code)
	}
}

func TestEnsureDefaultDisplayName(t *testing.T) {
	trk, _ := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)

	// No name: a placeholder derived from the peer id is assigned, persisted and served.
	s.ensureDefaultDisplayName()
	want := config.DefaultDisplayName("12D3KooWTestPeer")
	if s.displayName() != want || !config.IsPlaceholderDisplayName(want) {
		t.Fatalf("live = %q, want placeholder %q", s.displayName(), want)
	}
	if cfg, _ := config.Load(); cfg.DisplayName != want {
		t.Errorf("config.yaml = %q, want %q", cfg.DisplayName, want)
	}
	if got := identityData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/identity", ""))); got.DisplayName != want {
		t.Errorf("GET identity = %+v", got)
	}
	// Idempotent, and never touches an owner-set name.
	s.ensureDefaultDisplayName()
	if s.displayName() != want {
		t.Errorf("second call changed the name to %q", s.displayName())
	}
	identityData(t, postIdentity(s, "Atlas"))
	s.ensureDefaultDisplayName()
	if s.displayName() != "Atlas" {
		t.Errorf("owner name replaced by %q", s.displayName())
	}
	// A name already in config.yaml (installer wrote it after we loaded) wins over the placeholder.
	s2 := newIdentityTestServer(t, trk.URL)
	if err := config.UpdateFile(func(doc map[string]any) error { doc["display_name"] = "Installer Name"; return nil }); err != nil {
		t.Fatal(err)
	}
	s2.ensureDefaultDisplayName()
	if s2.displayName() != "Installer Name" {
		t.Errorf("file name not adopted: %q", s2.displayName())
	}
}

func TestAdoptTrackerDisplayName(t *testing.T) {
	trk, _ := newHeartbeatRecorder(t, http.StatusOK)
	s := newIdentityTestServer(t, trk.URL)
	s.ensureDefaultDisplayName()
	placeholder := s.displayName()

	// Tracker placeholders and blanks are ignored.
	s.adoptTrackerDisplayName("")
	s.adoptTrackerDisplayName("swarm_operator_42")
	if s.displayName() != placeholder {
		t.Fatalf("placeholder replaced by %q", s.displayName())
	}
	// A real tracker-side name (a claim's <symbol>_agent) replaces the local placeholder and persists.
	s.adoptTrackerDisplayName("lalala_agent")
	if s.displayName() != "lalala_agent" {
		t.Fatalf("live = %q, want lalala_agent", s.displayName())
	}
	if cfg, _ := config.Load(); cfg.DisplayName != "lalala_agent" {
		t.Errorf("config.yaml = %q, want lalala_agent", cfg.DisplayName)
	}
	// Once the name is real (adopted or owner-set) the tracker never overrides it.
	s.adoptTrackerDisplayName("other_agent")
	if s.displayName() != "lalala_agent" {
		t.Errorf("adopted name replaced by %q", s.displayName())
	}
	// A case-only difference is adopted (the token default moved to <SYMBOL>_Agent).
	s.adoptTrackerDisplayName("LALALA_Agent")
	if s.displayName() != "LALALA_Agent" {
		t.Errorf("case-only rename not adopted: %q", s.displayName())
	}
	identityData(t, postIdentity(s, "Atlas"))
	s.adoptTrackerDisplayName("lalala_agent")
	s.adoptTrackerDisplayName("ATLAS")
	if s.displayName() != "Atlas" {
		t.Errorf("owner name replaced by %q", s.displayName())
	}
}
