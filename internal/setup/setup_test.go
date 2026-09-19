package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckJSONShape(t *testing.T) {
	b, err := json.Marshal(Missing(IDFirewall, "Windows only", nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"id":"firewall","status":"missing","message":"Windows only"}` {
		t.Errorf("json = %s", b)
	}
	b, _ = json.Marshal(OK(IDService, nil))
	if string(b) != `{"id":"service","status":"ok"}` {
		t.Errorf("json = %s", b)
	}
}

func TestWindowsOnlyStubsOnThisOS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stubs only exist off Windows")
	}
	for _, c := range []Check{Firewall("/x"), FixFirewall("/x"), Autostart(), FixAutostart()} {
		if c.Status != StatusMissing || c.Message != "Windows only" {
			t.Errorf("%s = %+v, want missing/Windows only", c.ID, c)
		}
	}
}

func TestStorageCheck(t *testing.T) {
	dir := t.TempDir()
	c := StorageCheck(dir)
	if c.Status != StatusOK {
		t.Fatalf("temp dir: %+v", c)
	}
	if c.Detail["path"] != dir {
		t.Errorf("detail.path = %v", c.Detail["path"])
	}
	if free, ok := c.Detail["freeBytes"].(uint64); !ok || free < MinFreeBytes {
		t.Errorf("detail.freeBytes = %v", c.Detail["freeBytes"])
	}
	cands, _ := c.Detail["candidates"].([]string)
	if len(cands) == 0 || cands[0] != dir {
		t.Errorf("candidates = %v", cands)
	}

	if c := StorageCheck(filepath.Join(dir, "nope")); c.Status != StatusMissing {
		t.Errorf("missing dir: %+v", c)
	}
	if c := StorageCheck(""); c.Status != StatusMissing {
		t.Errorf("empty path: %+v", c)
	}
	file := filepath.Join(dir, "f")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if c := StorageCheck(file); c.Status != StatusFailed {
		t.Errorf("file path: %+v", c)
	}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		ro := filepath.Join(dir, "ro")
		_ = os.Mkdir(ro, 0o500)
		if c := StorageCheck(ro); c.Status != StatusFailed {
			t.Errorf("read-only dir: %+v", c)
		}
	}
}

func TestPrepareStorage(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new", "data")
	c, err := PrepareStorage(target)
	if err != nil || c.Status != StatusOK {
		t.Fatalf("prepare: %v %+v", err, c)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("dir not created: %v", err)
	}
	if _, err := PrepareStorage("relative/path"); err == nil {
		t.Error("relative path accepted")
	}
	if _, err := PrepareStorage(""); err == nil {
		t.Error("empty path accepted")
	}
}

func TestStorageRootsAndValidate(t *testing.T) {
	current := filepath.Join(t.TempDir(), "sa", "data")
	roots := StorageRoots(current)
	home, _ := os.UserHomeDir()
	wantParent := filepath.Dir(current)
	hasParent, hasHome := false, false
	for _, r := range roots {
		if r == wantParent {
			hasParent = true
		}
		if home != "" && r == filepath.Clean(home) {
			hasHome = true
		}
	}
	if !hasParent || !hasHome {
		t.Errorf("roots = %v; want parent %s and home %s", roots, wantParent, home)
	}

	if _, err := ValidateStoragePath(filepath.Join(home, "StonkAgents"), roots); err != nil {
		t.Errorf("home candidate rejected: %v", err)
	}

	// Boundary semantics against a single explicit root (the temp dir may
	// itself live under the home directory, so the real roots are too broad).
	only := []string{wantParent}
	ok := []string{
		filepath.Join(wantParent, "moved"),
		current,
		wantParent,
		filepath.Join(wantParent, "x", "..", "y"), // cleans to a sibling
	}
	for _, p := range ok {
		if got, err := ValidateStoragePath(p, only); err != nil || got != filepath.Clean(p) {
			t.Errorf("ValidateStoragePath(%q) = %q, %v", p, got, err)
		}
	}
	bad := []string{
		"", "relative/dir",
		filepath.Join(wantParent, "..", "escape"), // cleans out of the root
		wantParent + "2", // shares the prefix, not the directory
		`\\server\share\data`, "//server/share/data",
	}
	if runtime.GOOS == "windows" {
		bad = append(bad, `C:\Windows\System32`, `Z:\elsewhere`)
	} else {
		bad = append(bad, "/etc", "/var/lib/other")
	}
	for _, p := range bad {
		if _, err := ValidateStoragePath(p, only); err == nil {
			t.Errorf("ValidateStoragePath(%q) accepted", p)
		}
	}
}

func TestHasMutationHeaders(t *testing.T) {
	mk := func(ct, hdr, origin string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/setup/x", nil)
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		if hdr != "" {
			r.Header.Set(MutationHeader, hdr)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	ok := []*http.Request{
		mk("application/json", "1", ""),
		mk("application/json; charset=utf-8", "1", "https://dev.stonkagents.com"),
		mk("Application/JSON", " 1 ", ""),
	}
	for i, r := range ok {
		if !HasMutationHeaders(r) {
			t.Errorf("ok[%d] rejected: %v", i, r.Header)
		}
	}
	bad := []*http.Request{
		mk("", "", ""),
		mk("application/json", "", ""),
		mk("", "1", ""),
		mk("text/plain", "1", ""),
		mk("application/x-www-form-urlencoded", "1", ""),
		mk("multipart/form-data; boundary=x", "1", ""),
		mk("application/json", "true", ""),
		mk("application/json", "1", "null"), // sandboxed iframe
	}
	for i, r := range bad {
		if HasMutationHeaders(r) {
			t.Errorf("bad[%d] accepted: %v", i, r.Header)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	SetMutationHeaders(r.Header)
	if !HasMutationHeaders(r) {
		t.Errorf("SetMutationHeaders does not satisfy HasMutationHeaders: %v", r.Header)
	}
}

func TestValidateCapMbps(t *testing.T) {
	for _, v := range []float64{MinCapMbps, 1, 10, 50, MaxCapMbps} {
		if err := ValidateCapMbps(v); err != nil {
			t.Errorf("%g: %v", v, err)
		}
	}
	for _, v := range []float64{0, 0.1, 0.49, -5, MaxCapMbps + 1, 1e9} {
		if err := ValidateCapMbps(v); err == nil {
			t.Errorf("%g accepted", v)
		}
	}
}

func TestBandwidthCheck(t *testing.T) {
	c := BandwidthCheck(10, 50)
	if c.Status != StatusOK || c.Detail["uploadMbps"] != 10.0 || c.Detail["downloadMbps"] != 50.0 {
		t.Errorf("both: %+v", c)
	}
	c = BandwidthCheck(0, 0)
	if c.Status != StatusMissing || c.Detail["uploadMbps"] != nil || c.Detail["downloadMbps"] != nil {
		t.Errorf("none: %+v", c)
	}
	if c := BandwidthCheck(10, 0); c.Status != StatusMissing || c.Message != "no download cap configured" {
		t.Errorf("upload only: %+v", c)
	}
	if got := MbpsToBytesPerSecond(8); got != 1_000_000 {
		t.Errorf("8 Mbps = %d B/s", got)
	}
	if got := MbpsToBytesPerSecond(0); got != 0 {
		t.Errorf("0 Mbps = %d", got)
	}
}

func TestControllerCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"daemon":"running","healthy":true,"version":"1.2.3"}`))
	}))
	defer srv.Close()
	c := ControllerCheck(srv.URL)
	if c.Status != StatusOK || c.Detail["version"] != "1.2.3" || c.Detail["daemon"] != "running" {
		t.Fatalf("check = %+v", c)
	}
	srv.Close()
	if c := ControllerCheck(srv.URL); c.Status != StatusFailed {
		t.Errorf("closed server: %+v", c)
	}
}
