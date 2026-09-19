package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stonkagents/agent/internal/setup"
)

// newSetupTestServer returns a controller whose daemon binary is a real temp
// file (the firewall handler refuses to run without one).
func newSetupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, err := NewServer("1.0.0-test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	exe := filepath.Join(t.TempDir(), "stonkagents-daemon"+exeSuffix())
	if err := os.WriteFile(exe, []byte("MZ"), 0o700); err != nil {
		t.Fatal(err)
	}
	s.daemonExePath = func() string { return exe }
	return s, exe
}

// setupPost builds a loopback POST carrying the mutation headers.
func setupPost(path, body string) *http.Request {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, rdr)
	req.RemoteAddr = "127.0.0.1:50000"
	setup.SetMutationHeaders(req.Header)
	return req
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

func decodeFix(t *testing.T, w *httptest.ResponseRecorder) setup.Check {
	t.Helper()
	var resp setup.FixResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return resp.Check
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) errorPayload {
	t.Helper()
	var resp errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return resp.Error
}

func TestSetupFirewall_IgnoresBodyPathAndUsesInstalledDaemon(t *testing.T) {
	s, exe := newSetupTestServer(t)
	var gotExe string
	s.fixFirewall = func(exe string) setup.Check {
		gotExe = exe
		return setup.OK(setup.IDFirewall, map[string]any{"program": exe})
	}
	// A caller-supplied path (an attacker's binary) must never reach netsh.
	w := serve(s, setupPost("/setup/firewall", `{"exePath":"C:\\evil\\backdoor.exe"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if gotExe != exe {
		t.Errorf("exePath = %q, want the installed daemon %q", gotExe, exe)
	}
	if c := decodeFix(t, w); c.ID != setup.IDFirewall || c.Status != setup.StatusOK {
		t.Errorf("check = %+v", c)
	}
	// Empty body → same path
	gotExe = ""
	if w := serve(s, setupPost("/setup/firewall", "")); w.Code != http.StatusOK || gotExe != exe {
		t.Errorf("empty body: %d exe=%q", w.Code, gotExe)
	}
}

func TestSetupFirewall_RefusesWhenDaemonBinaryMissing(t *testing.T) {
	s, exe := newSetupTestServer(t)
	called := false
	s.fixFirewall = func(string) setup.Check { called = true; return setup.OK(setup.IDFirewall, nil) }
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	w := serve(s, setupPost("/setup/firewall", ""))
	if w.Code != http.StatusConflict || decodeErr(t, w).Code != "DAEMON_EXE_MISSING" {
		t.Errorf("missing binary: %d %s", w.Code, w.Body.String())
	}
	// A directory at that path does not count either.
	if err := os.Mkdir(exe, 0o700); err != nil {
		t.Fatal(err)
	}
	if w := serve(s, setupPost("/setup/firewall", "")); w.Code != http.StatusConflict {
		t.Errorf("directory: %d %s", w.Code, w.Body.String())
	}
	if called {
		t.Error("netsh fix ran without an installed daemon binary")
	}
}

func TestSetup_RequiresLoopbackAndMutationHeaders(t *testing.T) {
	s, _ := newSetupTestServer(t)
	called := false
	s.fixFirewall = func(string) setup.Check { called = true; return setup.OK(setup.IDFirewall, nil) }
	s.fixAutostart = func() setup.Check { called = true; return setup.OK(setup.IDAutostart, nil) }
	for _, path := range []string{"/setup/firewall", "/setup/autostart"} {
		// Not loopback
		req := setupPost(path, "")
		req.RemoteAddr = "192.168.1.20:4000"
		if w := serve(s, req); w.Code != http.StatusForbidden || decodeErr(t, w).Code != "FORBIDDEN" {
			t.Errorf("%s from LAN: %d %s", path, w.Code, w.Body.String())
		}
		// Missing custom header
		req = setupPost(path, "")
		req.Header.Del(setup.MutationHeader)
		if w := serve(s, req); w.Code != http.StatusForbidden {
			t.Errorf("%s without %s: %d", path, setup.MutationHeader, w.Code)
		}
		// Wrong content type (a no-cors form post)
		req = setupPost(path, "")
		req.Header.Set("Content-Type", "text/plain")
		if w := serve(s, req); w.Code != http.StatusForbidden {
			t.Errorf("%s text/plain: %d", path, w.Code)
		}
		// No headers at all
		req = httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "127.0.0.1:1"
		if w := serve(s, req); w.Code != http.StatusForbidden {
			t.Errorf("%s bare: %d", path, w.Code)
		}
	}
	if called {
		t.Error("a privileged fix ran for a rejected request")
	}
	// IPv6 loopback with headers passes the gate
	req := setupPost("/setup/autostart", "")
	req.RemoteAddr = "[::1]:4000"
	if w := serve(s, req); w.Code != http.StatusOK {
		t.Errorf("::1: %d %s", w.Code, w.Body.String())
	}
}

func TestCORS_DoesNotGrantSetupHeader(t *testing.T) {
	// The mutation header is deliberately absent from the controller's
	// Allow-Headers: a browser preflight for /setup/* fails even from the
	// portal origin, so only the daemon proxy (no browser) can call it.
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodOptions, "/setup/firewall", nil)
	req.Header.Set("Origin", "https://dev.stonkagents.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-stonkagents-setup")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")); strings.Contains(allowed, strings.ToLower(setup.MutationHeader)) {
		t.Errorf("Allow-Headers = %q grants the setup header to browsers", allowed)
	}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func TestSetupFirewall_InvalidJSON(t *testing.T) {
	s, _ := newSetupTestServer(t)
	for _, body := range []string{`{not json`, `[]`, `"str"`} {
		w := serve(s, setupPost("/setup/firewall", body))
		if w.Code != http.StatusBadRequest || decodeErr(t, w).Code != "INVALID_REQUEST" {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSetupAutostart_ReturnsCheck(t *testing.T) {
	s, _ := newSetupTestServer(t)
	s.fixAutostart = func() setup.Check {
		return setup.OK(setup.IDAutostart, map[string]any{setup.DaemonServiceName(): "auto"})
	}
	w := serve(s, setupPost("/setup/autostart", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if c := decodeFix(t, w); c.ID != setup.IDAutostart || c.Status != setup.StatusOK {
		t.Errorf("check = %+v", c)
	}
}

func TestSetup_PlatformStubOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real netsh/sc on Windows")
	}
	s, _ := newSetupTestServer(t)
	for _, path := range []string{"/setup/firewall", "/setup/autostart"} {
		w := serve(s, setupPost(path, ""))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, w.Code)
		}
		if c := decodeFix(t, w); c.Status != setup.StatusMissing || c.Message != "Windows only" {
			t.Errorf("%s check = %+v", path, c)
		}
	}
}

func TestSetup_RateLimited(t *testing.T) {
	s, _ := newSetupTestServer(t)
	s.fixAutostart = func() setup.Check { return setup.OK(setup.IDAutostart, nil) }
	var last int
	for i := 0; i < 8; i++ {
		last = serve(s, setupPost("/setup/autostart", "")).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("8th call status = %d, want 429", last)
	}
}

func TestSetup_GetNotAllowed(t *testing.T) {
	s, _ := newSetupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/setup/firewall", nil)
	if w := serve(s, req); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", w.Code)
	}
}

func TestCORS_PrivateNetworkPreflight(t *testing.T) {
	handler := corsMiddleware(dummyHandler())
	req := httptest.NewRequest(http.MethodOptions, "/setup/firewall", nil)
	req.Header.Set("Origin", "https://dev.stonkagents.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Errorf("Allow-Private-Network = %q, want true", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://dev.stonkagents.com" {
		t.Errorf("Allow-Origin = %q", got)
	}

	// Disallowed origin: no private-network grant
	req = httptest.NewRequest(http.MethodOptions, "/setup/firewall", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("disallowed origin got Allow-Private-Network = %q", got)
	}

	// Allowed origin without the request header: header not added
	req = httptest.NewRequest(http.MethodOptions, "/status", nil)
	req.Header.Set("Origin", "https://stonkagents.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("plain preflight got Allow-Private-Network = %q", got)
	}
}
