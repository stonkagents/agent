// Package: internal/daemon
// Purpose: Tests for the setup surface (GET/POST /api/v1/setup/*)

package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/upload"
	"github.com/stonkagents/agent/internal/setup"
	"golang.org/x/time/rate"
)

const loopbackAddr = "127.0.0.1:54321"

// newSetupTestServer returns a Server with a temp data dir and config.yaml,
// its controllerURL pointed at controller (may be "" for unreachable).
func newSetupTestServer(t *testing.T, controllerURL string) *Server {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "daemon_port: 7841\ndata_dir: '" + dataDir + "'\ntracker_url: https://tracker.test\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STONKAGENTS_CONFIG_PATH", cfgPath)
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	if controllerURL == "" {
		controllerURL = "http://127.0.0.1:1" // nothing listens here
	}
	s := &Server{
		config:          &Config{Version: "9.9.9-test", DataDir: dataDir, TrackerURL: "https://tracker.test"},
		controllerURL:   controllerURL,
		uploadManager:   upload.NewManager(1),
		downloadManager: download.NewManager(filepath.Join(dataDir, "downloads"), 1),
	}
	s.setupSt().exePath = func() (string, error) { return "/opt/stonkagents/stonkagents-daemon", nil }
	s.setupSt().limiter = rate.NewLimiter(rate.Inf, 0) // TestSetupFix_RateLimited installs a real one
	return s
}

// setupRequest builds a loopback request. POSTs carry the CSRF headers the
// setup surface requires (Content-Type: application/json + X-StonkAgents-Setup: 1);
// TestSetupFix_RequiresMutationHeaders strips them.
func setupRequest(method, path, body string) *http.Request {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = loopbackAddr
	if method == http.MethodPost {
		setup.SetMutationHeaders(req.Header)
	}
	return req
}

func do(s *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.buildSetupMux().ServeHTTP(w, req)
	return w
}

func statusChecks(t *testing.T, s *Server, origin string) map[string]setup.Check {
	t.Helper()
	req := setupRequest(http.MethodGet, "/api/v1/setup/status", "")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := do(s, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp setup.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Checks) != len(setup.CheckIDs) {
		t.Fatalf("checks = %d, want %d", len(resp.Checks), len(setup.CheckIDs))
	}
	out := map[string]setup.Check{}
	for i, c := range resp.Checks {
		if c.ID != setup.CheckIDs[i] {
			t.Errorf("check[%d].id = %s, want %s", i, c.ID, setup.CheckIDs[i])
		}
		out[c.ID] = c
	}
	return out
}

func fixCheck(t *testing.T, w *httptest.ResponseRecorder) setup.Check {
	t.Helper()
	var resp setup.FixResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return resp.Check
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if resp.Error.Message == "" {
		t.Errorf("error message empty: %s", w.Body.String())
	}
	return resp.Error.Code
}

func mockController(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestSetupStatus_LoopbackOnly(t *testing.T) {
	s := newSetupTestServer(t, "")
	req := setupRequest(http.MethodGet, "/api/v1/setup/status", "")
	req.RemoteAddr = "10.0.0.9:4444"
	if w := do(s, req); w.Code != http.StatusForbidden {
		t.Errorf("non-loopback GET status = %d", w.Code)
	}
	req = setupRequest(http.MethodPost, "/api/v1/setup/storage", "")
	req.RemoteAddr = "10.0.0.9:4444"
	if w := do(s, req); w.Code != http.StatusForbidden {
		t.Errorf("non-loopback POST status = %d", w.Code)
	}
	req = setupRequest(http.MethodGet, "/api/v1/setup/status", "")
	req.RemoteAddr = "[::1]:4444"
	if w := do(s, req); w.Code != http.StatusOK {
		t.Errorf("IPv6 loopback status = %d", w.Code)
	}
}

func TestSetupStatus_CheckSemantics(t *testing.T) {
	ctrl := mockController(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"daemon":"running","healthy":true,"version":"1.0.0"}`))
			return
		}
		http.NotFound(w, r)
	})
	s := newSetupTestServer(t, ctrl.URL)
	checks := statusChecks(t, s, "https://dev.stonkagents.com")

	if c := checks[setup.IDService]; c.Status != setup.StatusOK || c.Detail["version"] != "9.9.9-test" {
		t.Errorf("service = %+v", c)
	}
	if c := checks[setup.IDController]; c.Status != setup.StatusOK || c.Detail["version"] != "1.0.0" {
		t.Errorf("controller = %+v", c)
	}
	if runtime.GOOS != "windows" {
		for _, id := range []string{setup.IDFirewall, setup.IDAutostart} {
			if c := checks[id]; c.Status != setup.StatusMissing || c.Message != "Windows only" {
				t.Errorf("%s = %+v", id, c)
			}
		}
	}
	if c := checks[setup.IDP2P]; c.Status != setup.StatusFailed || c.Message == "" {
		t.Errorf("p2p without host = %+v", c)
	}
	if c := checks[setup.IDTracker]; c.Status != setup.StatusMissing || c.Detail["trackerUrl"] != "https://tracker.test" {
		t.Errorf("tracker unregistered = %+v", c)
	}
	if c := checks[setup.IDStorage]; c.Status != setup.StatusOK || c.Detail["path"] != s.config.DataDir || c.Detail["freeBytes"] == nil {
		t.Errorf("storage = %+v", c)
	}
	if cands, ok := checks[setup.IDStorage].Detail["candidates"].([]any); !ok || len(cands) == 0 || cands[0] != s.config.DataDir {
		t.Errorf("storage candidates = %v", checks[setup.IDStorage].Detail["candidates"])
	}
	if c := checks[setup.IDBandwidth]; c.Status != setup.StatusMissing || c.Detail["uploadMbps"] != nil || c.Detail["downloadMbps"] != nil {
		t.Errorf("bandwidth = %+v", c)
	}
	if c := checks[setup.IDOrigin]; c.Status != setup.StatusOK || c.Detail["origin"] != "https://dev.stonkagents.com" {
		t.Errorf("origin allowed = %+v", c)
	}

	// Controller down → failed
	ctrl.Close()
	checks = statusChecks(t, s, "https://not-allowed.example")
	if c := checks[setup.IDController]; c.Status != setup.StatusFailed {
		t.Errorf("controller down = %+v", c)
	}
	if c := checks[setup.IDOrigin]; c.Status != setup.StatusMissing || !strings.Contains(c.Message, "not-allowed.example") {
		t.Errorf("origin disallowed = %+v", c)
	}
	// No Origin header (CLI caller) → ok
	if c := statusChecks(t, s, "")[setup.IDOrigin]; c.Status != setup.StatusOK {
		t.Errorf("origin without header = %+v", c)
	}
}

func TestSetupStatus_TrackerRegistrationOutcome(t *testing.T) {
	s := newSetupTestServer(t, "")
	if err := s.saveTrackerAPIKey("k"); err != nil {
		t.Fatal(err)
	}
	if c := statusChecks(t, s, "")[setup.IDTracker]; c.Status != setup.StatusMissing || c.Message != "tracker registration pending" {
		t.Errorf("key but no attempt = %+v", c)
	}
	s.recordTrackerRegistration(nil)
	if c := statusChecks(t, s, "")[setup.IDTracker]; c.Status != setup.StatusOK || c.Detail["lastRegistrationAt"] == nil {
		t.Errorf("registered = %+v", c)
	}
	s.recordTrackerRegistration(errors.New("HTTP 503"))
	if c := statusChecks(t, s, "")[setup.IDTracker]; c.Status != setup.StatusFailed || !strings.Contains(c.Message, "HTTP 503") {
		t.Errorf("failed registration = %+v", c)
	}
}

func TestSetupFix_UnknownAndReportedOnly(t *testing.T) {
	s := newSetupTestServer(t, "")
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/bogus", "")); w.Code != http.StatusNotFound || errorCode(t, w) != "UNKNOWN_CHECK" {
		t.Errorf("unknown id: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{setup.IDService, setup.IDController, setup.IDP2P, setup.IDTracker} {
		if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/"+id, "")); w.Code != http.StatusMethodNotAllowed || errorCode(t, w) != "NOT_FIXABLE" {
			t.Errorf("%s: %d %s", id, w.Code, w.Body.String())
		}
	}
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/status", "")); w.Code != http.StatusNotFound {
		t.Errorf("POST status: %d", w.Code)
	}
}

func TestSetupFix_Bandwidth(t *testing.T) {
	s := newSetupTestServer(t, "")

	w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", `{"uploadMbps": 8, "downloadMbps": 16}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	c := fixCheck(t, w)
	if c.ID != setup.IDBandwidth || c.Status != setup.StatusOK || c.Detail["uploadMbps"] != 8.0 || c.Detail["downloadMbps"] != 16.0 {
		t.Errorf("check = %+v", c)
	}
	// Applied live to both throttles
	if got := s.downloadManager.BandwidthLimit(); got != 2_000_000 {
		t.Errorf("download limit = %d B/s, want 2000000", got)
	}
	if got := s.uploadManager.BandwidthLimit(); got != 1_000_000 {
		t.Errorf("upload limit = %d B/s, want 1000000", got)
	}
	if !s.uploadManager.AllowUpload(1_000_000) || s.uploadManager.AllowUpload(500_000) {
		t.Error("upload throttle not applied at 1 MB/s")
	}
	// Persisted
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UploadCapMbps != 8 || cfg.DownloadCapMbps != 16 || cfg.DataDir != s.config.DataDir {
		t.Errorf("config.yaml = %+v", cfg)
	}
	// Status now reports ok
	if c := statusChecks(t, s, "")[setup.IDBandwidth]; c.Status != setup.StatusOK {
		t.Errorf("status after fix = %+v", c)
	}

	// Empty body: keeps configured values (already set) / defaults otherwise
	w = do(s, setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", ""))
	if w.Code != http.StatusOK || fixCheck(t, w).Detail["uploadMbps"] != 8.0 {
		t.Errorf("empty body: %d %s", w.Code, w.Body.String())
	}
	s2 := newSetupTestServer(t, "")
	w = do(s2, setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("defaults: %d %s", w.Code, w.Body.String())
	}
	if c := fixCheck(t, w); c.Detail["uploadMbps"] != setup.DefaultUploadMbps || c.Detail["downloadMbps"] != setup.DefaultDownloadMbps {
		t.Errorf("defaults = %+v", c)
	}

	// Invalid values: negative, zero, below the floor, above the ceiling, bad JSON
	for _, body := range []string{
		`{"uploadMbps": -1, "downloadMbps": 5}`, `{"uploadMbps": 0}`,
		`{"uploadMbps": 0.1}`, `{"downloadMbps": 0.49}`, `{"downloadMbps": 10001}`, `{"uploadMbps": 1e12}`,
		`{bad`,
	} {
		if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", body)); w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST" {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// Rejected values leave config and throttles untouched
	if up, down := s.bandwidthCaps(); up != 8 || down != 16 {
		t.Errorf("caps after rejected requests = %g/%g", up, down)
	}
	// Boundary values are accepted
	for _, body := range []string{`{"uploadMbps": 0.5}`, `{"downloadMbps": 10000}`} {
		if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", body)); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSetupFix_RequiresMutationHeaders(t *testing.T) {
	s := newSetupTestServer(t, "")
	ctrlCalled := false
	ctrl := mockController(t, func(w http.ResponseWriter, r *http.Request) { ctrlCalled = true })
	s.controllerURL = ctrl.URL

	type variant struct {
		name        string
		contentType string
		setupHeader string
	}
	variants := []variant{
		{"no headers (no-cors form post)", "", ""},
		{"json without custom header", "application/json", ""},
		{"custom header with text/plain", "text/plain", "1"},
		{"custom header with form encoding", "application/x-www-form-urlencoded", "1"},
		{"custom header with wrong value", "application/json", "yes"},
	}
	targets := []string{"/api/v1/setup/storage", "/api/v1/setup/bandwidth", "/api/v1/setup/origin", "/api/v1/setup/firewall", "/api/v1/setup/autostart"}
	for _, v := range variants {
		for _, path := range targets {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"uploadMbps": 8, "downloadMbps": 16}`))
			req.RemoteAddr = loopbackAddr
			req.Header.Set("Origin", "https://dev.stonkagents.com")
			if v.contentType != "" {
				req.Header.Set("Content-Type", v.contentType)
			}
			if v.setupHeader != "" {
				req.Header.Set(SetupHeader, v.setupHeader)
			}
			w := do(s, req)
			if w.Code != http.StatusForbidden || errorCode(t, w) != "FORBIDDEN" {
				t.Errorf("%s %s: %d %s", v.name, path, w.Code, w.Body.String())
			}
		}
	}
	// A sandboxed iframe (Origin: null, which the default allowlist admits for
	// the file:// SPA) passes the preflight, so the gate refuses it explicitly.
	for _, path := range targets {
		req := setupRequest(http.MethodPost, path, `{"uploadMbps": 8, "downloadMbps": 16}`)
		req.Header.Set("Origin", "null")
		if w := do(s, req); w.Code != http.StatusForbidden {
			t.Errorf("Origin null %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if ctrlCalled {
		t.Error("privileged fix was proxied without the mutation headers")
	}
	if up, down := s.bandwidthCaps(); up != 0 || down != 0 {
		t.Errorf("caps changed by a rejected request: %g/%g", up, down)
	}

	// charset parameter is fine; header value is trimmed
	req := setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", `{"uploadMbps": 8, "downloadMbps": 16}`)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set(SetupHeader, " 1 ")
	if w := do(s, req); w.Code != http.StatusOK {
		t.Errorf("json with charset: %d %s", w.Code, w.Body.String())
	}
}

func TestSetup_PreflightAllowsSetupHeader(t *testing.T) {
	s := newSetupTestServer(t, "")
	req := setupRequest(http.MethodOptions, "/api/v1/setup/bandwidth", "")
	req.Header.Set("Origin", "https://dev.stonkagents.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-stonkagents-setup")
	w := do(s, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d", w.Code)
	}
	allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowed, strings.ToLower(SetupHeader)) || !strings.Contains(allowed, "content-type") {
		t.Errorf("Allow-Headers = %q must list %s", w.Header().Get("Access-Control-Allow-Headers"), SetupHeader)
	}
	// An unlisted origin gets no CORS grant on the preflight: the browser
	// never sends the real request.
	req.Header.Set("Origin", "https://attacker.example")
	w = do(s, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Headers") != "" {
		t.Errorf("attacker preflight granted: %v", w.Header())
	}
}

func TestSetupFix_StorageRejectsPathsOutsideRoots(t *testing.T) {
	s := newSetupTestServer(t, "")
	bad := []string{`\\server\share\data`, "//server/share/data", "relative/dir"}
	if runtime.GOOS == "windows" {
		bad = append(bad, `C:\Windows\System32\stonk`, `Z:\elsewhere`)
	} else {
		bad = append(bad, "/etc/stonk", "/var/lib/other")
	}
	for _, p := range bad {
		body, _ := json.Marshal(map[string]string{"path": p})
		w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/storage", string(body)))
		if w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST" {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	cfg, _ := config.Load()
	if cfg.DataDir != s.config.DataDir {
		t.Errorf("config data_dir changed to %s", cfg.DataDir)
	}
	// A sibling of the current data dir (under its parent) is fine.
	sibling := filepath.Join(filepath.Dir(s.config.DataDir), "data2")
	body, _ := json.Marshal(map[string]string{"path": sibling})
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/storage", string(body))); w.Code != http.StatusAccepted {
		t.Errorf("sibling %s: %d %s", sibling, w.Code, w.Body.String())
	}
}

func TestIsProductOrigin(t *testing.T) {
	ok := []string{
		"https://stonkagents.com", "https://dev.stonkagents.com", "https://stg.stonkagents.com", "https://pr-42.preview.stonkagents.com",
		"https://stonkagents.com", "https://app.stonkagents.com", "https://STONKAGENTS.com", "https://stonkagents.com:443",
		"http://localhost:3000", "http://127.0.0.1:3000", "http://localhost", "http://127.0.0.1:65535",
	}
	for _, o := range ok {
		if !isProductOrigin(o) {
			t.Errorf("%s rejected", o)
		}
	}
	bad := []string{
		"https://attacker.example", "https://stonkagents.com.attacker.example", "https://notstonkagents.com", "https://xstonkagents.com",
		"http://stonkagents.com", "http://dev.stonkagents.com", "https://stonkagents.com:8443",
		"http://localhost.attacker.example", "http://127.0.0.1.nip.io", "http://[::1]:3000", "http://localhost:0", "http://localhost:70000",
		"https://localhost:3000", "https://preview-42.stonkagents.pages.dev", "null", "",
	}
	for _, o := range bad {
		if isProductOrigin(o) {
			t.Errorf("%s accepted", o)
		}
	}
}

func TestSetupFix_StorageNewPathRequiresRestart(t *testing.T) {
	s := newSetupTestServer(t, "")
	// A new path is accepted only under the data dir's parent, the profile or a
	// candidate; point the profile at a temp dir so this holds off Windows too
	// (Linux /tmp is not under $HOME).
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	newDir := filepath.Join(home, "moved", "data")
	body, _ := json.Marshal(map[string]string{"path": newDir})
	w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/storage", string(body)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	c := fixCheck(t, w)
	if c.Status != setup.StatusOK || c.Message != "restart to apply" || c.Detail["restartRequired"] != true || c.Detail["path"] != newDir {
		t.Errorf("check = %+v", c)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Errorf("new dir not created: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != newDir {
		t.Errorf("config data_dir = %s, want %s", cfg.DataDir, newDir)
	}
	if s.config.DataDir == newDir {
		t.Error("live DataDir must not change before restart")
	}
	if c := statusChecks(t, s, "")[setup.IDStorage]; c.Detail["pendingPath"] != newDir || c.Detail["restartRequired"] != true {
		t.Errorf("status after fix = %+v", c)
	}

	// Validation
	for _, body := range []string{`{"path": "relative/dir"}`, `{"path": "   "}` /* falls back to candidates → ok */} {
		w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/storage", body))
		if body == `{"path": "relative/dir"}` && (w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST") {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSetupFix_StorageRepairsCurrentDirInPlace(t *testing.T) {
	s := newSetupTestServer(t, "")
	if err := os.RemoveAll(s.config.DataDir); err != nil {
		t.Fatal(err)
	}
	if c := statusChecks(t, s, "")[setup.IDStorage]; c.Status != setup.StatusMissing {
		t.Fatalf("before fix = %+v", c)
	}
	w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/storage", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	c := fixCheck(t, w)
	if c.Status != setup.StatusOK || c.Detail["path"] != s.config.DataDir || c.Detail["restartRequired"] != nil {
		t.Errorf("check = %+v", c)
	}
	cfg, _ := config.Load()
	if cfg.DataDir != s.config.DataDir {
		t.Errorf("config data_dir changed to %s", cfg.DataDir)
	}
}

func TestSetupFix_Origin(t *testing.T) {
	s := newSetupTestServer(t, "")
	const origin = "https://preview-42.stonkagents.com"

	req := setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
	if w := do(s, req); w.Code != http.StatusBadRequest || errorCode(t, w) != "INVALID_REQUEST" {
		t.Errorf("no Origin: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"ftp://x", "https://a.b/path", "not an origin", "https://user:pw@a.b"} {
		req := setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
		req.Header.Set("Origin", bad)
		if w := do(s, req); w.Code != http.StatusBadRequest {
			t.Errorf("origin %q accepted: %d", bad, w.Code)
		}
	}
	// Origin: null (sandboxed iframe / file://) is stopped by the mutation gate.
	req = setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
	req.Header.Set("Origin", "null")
	if w := do(s, req); w.Code != http.StatusForbidden || errorCode(t, w) != "FORBIDDEN" {
		t.Errorf("origin null: %d %s", w.Code, w.Body.String())
	}
	// Well-formed but foreign origins are refused with 403 even with the
	// mutation headers, and never reach config.yaml or the live allowlist.
	for _, foreign := range []string{"https://attacker.example", "https://stonkagents.com.attacker.example", "http://dev.stonkagents.com", "https://preview-42.stonkagents.pages.dev"} {
		req := setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
		req.Header.Set("Origin", foreign)
		w := do(s, req)
		if w.Code != http.StatusForbidden || errorCode(t, w) != "ORIGIN_NOT_ALLOWED" {
			t.Errorf("origin %q: %d %s", foreign, w.Code, w.Body.String())
		}
		if s.corsPolicy().IsOriginAllowed(foreign) {
			t.Errorf("%q entered the live allowlist", foreign)
		}
	}
	if cfg, _ := config.Load(); len(cfg.CORSAllowedOrigins) != 0 {
		t.Errorf("foreign origins persisted: %v", cfg.CORSAllowedOrigins)
	}

	req = setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
	req.Header.Set("Origin", origin)
	w := do(s, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if c := fixCheck(t, w); c.Status != setup.StatusOK || c.Detail["origin"] != origin {
		t.Errorf("check = %+v", c)
	}
	// Live CORS: the fix response itself and later requests carry Allow-Origin
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" && got != origin {
		t.Errorf("Allow-Origin on fix response = %q", got)
	}
	if !s.corsPolicy().IsOriginAllowed(origin) {
		t.Error("origin not in live allowlist")
	}
	if c := statusChecks(t, s, origin)[setup.IDOrigin]; c.Status != setup.StatusOK {
		t.Errorf("status after fix = %+v", c)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORSAllowedOrigins) != 1 || cfg.CORSAllowedOrigins[0] != origin {
		t.Errorf("config cors_allowed_origins = %v", cfg.CORSAllowedOrigins)
	}
	// Idempotent
	req = setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
	req.Header.Set("Origin", origin)
	if w := do(s, req); w.Code != http.StatusOK {
		t.Errorf("second add: %d", w.Code)
	}
	cfg, _ = config.Load()
	if len(cfg.CORSAllowedOrigins) != 1 {
		t.Errorf("origin duplicated: %v", cfg.CORSAllowedOrigins)
	}
}

func TestSetupFix_PrivilegedProxiedToController(t *testing.T) {
	var gotPath, gotBody string
	var gotHeaders bool
	ctrl := mockController(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotHeaders = setup.HasMutationHeaders(r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/setup/autostart" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"check":{"id":"autostart","status":"ok","detail":{"StonkAgentsDaemon":"auto"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"check":{"id":"firewall","status":"failed","message":"netsh add rule failed: Access is denied."}}`))
	})
	s := newSetupTestServer(t, ctrl.URL)

	w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/firewall", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("firewall status = %d body=%s", w.Code, w.Body.String())
	}
	if gotPath != "POST /setup/firewall" || !gotHeaders {
		t.Errorf("controller saw %q mutationHeaders=%v", gotPath, gotHeaders)
	}
	// The controller derives the daemon path itself; the proxy must not send one.
	if strings.Contains(gotBody, "exePath") {
		t.Errorf("proxy sent a path to the controller: %s", gotBody)
	}
	if c := fixCheck(t, w); c.ID != setup.IDFirewall || c.Status != setup.StatusFailed || !strings.Contains(c.Message, "Access is denied") {
		t.Errorf("relayed check = %+v", c)
	}
	w = do(s, setupRequest(http.MethodPost, "/api/v1/setup/autostart", ""))
	if w.Code != http.StatusOK || gotPath != "POST /setup/autostart" {
		t.Errorf("autostart: %d %q", w.Code, gotPath)
	}
	if c := fixCheck(t, w); c.Status != setup.StatusOK {
		t.Errorf("autostart check = %+v", c)
	}

	// Controller down → 502 with error envelope
	ctrl.Close()
	w = do(s, setupRequest(http.MethodPost, "/api/v1/setup/firewall", ""))
	if w.Code != http.StatusBadGateway || errorCode(t, w) != "CONTROLLER_UNREACHABLE" {
		t.Errorf("controller down: %d %s", w.Code, w.Body.String())
	}
}

func TestSetupFix_RateLimited(t *testing.T) {
	s := newSetupTestServer(t, "")
	s.setupSt().limiter = newSetupFixLimiter()
	var last int
	for i := 0; i < 8; i++ {
		req := setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
		req.Header.Set("Origin", "https://dev.stonkagents.com")
		last = do(s, req).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("8th fix status = %d, want 429", last)
	}
	// GET is not rate limited
	if w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/status", "")); w.Code != http.StatusOK {
		t.Errorf("GET after burst = %d", w.Code)
	}
}

func TestSetup_PreflightPrivateNetwork(t *testing.T) {
	s := newSetupTestServer(t, "")
	req := setupRequest(http.MethodOptions, "/api/v1/setup/status", "")
	req.Header.Set("Origin", "https://stonkagents.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w := do(s, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Private-Network") != "true" || w.Header().Get("Access-Control-Allow-Origin") != "https://stonkagents.com" {
		t.Errorf("headers = %v", w.Header())
	}
}

func TestSetupStatus_BoundedBySlowChecks(t *testing.T) {
	// A controller that never answers: the controller check alone takes its
	// own 2s timeout; the status handler must still return within the budget
	// and report the remaining checks rather than hang or time out the client.
	release := make(chan struct{})
	ctrl := mockController(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) })
	s := newSetupTestServer(t, ctrl.URL)

	start := time.Now()
	checks := statusChecks(t, s, "")
	if elapsed := time.Since(start); elapsed > setupStatusTimeout {
		t.Errorf("status took %v, budget %v", elapsed, setupStatusTimeout)
	}
	if c := checks[setup.IDController]; c.Status != setup.StatusFailed {
		t.Errorf("controller = %+v", c)
	}
	if c := checks[setup.IDService]; c.Status != setup.StatusOK {
		t.Errorf("service = %+v", c)
	}

	// With a tiny budget every unfinished check is reported failed, in order.
	got := s.setupChecksWithin(setupRequest(http.MethodGet, "/api/v1/setup/status", ""), time.Nanosecond)
	if len(got) != len(setup.CheckIDs) {
		t.Fatalf("checks = %d", len(got))
	}
	for i, c := range got {
		if c.ID != setup.CheckIDs[i] {
			t.Errorf("check[%d].id = %s", i, c.ID)
		}
	}
	if c := got[1]; c.ID != setup.IDController || c.Status != setup.StatusFailed || !strings.Contains(c.Message, "did not finish") {
		t.Errorf("controller under 1ns budget = %+v", c)
	}
}

func TestSetup_ConcurrentCapReadsAndWrites(t *testing.T) {
	// Handlers read the caps while a fix rewrites them; run under -race.
	s := newSetupTestServer(t, "")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // single writer: config.yaml rewrites are not concurrent-safe on Windows
		defer wg.Done()
		for i := 0; i < 4; i++ {
			body := `{"uploadMbps": ` + strconv.Itoa(1+i) + `, "downloadMbps": 16}`
			if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/bandwidth", body)); w.Code != http.StatusOK {
				t.Errorf("bandwidth fix %d: %d %s", i, w.Code, w.Body.String())
			}
			req := setupRequest(http.MethodPost, "/api/v1/setup/origin", "")
			req.Header.Set("Origin", "https://pr-"+strconv.Itoa(i)+".stonkagents.com")
			if w := do(s, req); w.Code != http.StatusOK {
				t.Errorf("origin fix %d: %d %s", i, w.Code, w.Body.String())
			}
		}
	}()
	for r := 0; r < 2; r++ { // each status call shells out to netsh/sc on Windows (~1s); keep the count low
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				req := setupRequest(http.MethodGet, "/api/v1/setup/status", "")
				req.Header.Set("Origin", "https://dev.stonkagents.com")
				if w := do(s, req); w.Code != http.StatusOK {
					t.Errorf("status: %d", w.Code)
				}
			}
		}()
	}
	wg.Wait()
	if up, down := s.bandwidthCaps(); up != 4 || down != 16 {
		t.Errorf("caps = %g/%g", up, down)
	}
}

func TestSetup_ConfigOriginsSeedAllowlist(t *testing.T) {
	s := newSetupTestServer(t, "")
	s.config.CORSAllowedOrigins = []string{"https://extra.example"}
	if !s.corsPolicy().IsOriginAllowed("https://extra.example") {
		t.Error("config.yaml cors_allowed_origins not applied")
	}
	if s.corsPolicy().IsOriginAllowed("https://other.example") {
		t.Error("unexpected origin allowed")
	}
}
