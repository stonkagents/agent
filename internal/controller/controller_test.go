// Feature: F-025 (Auto-Update System)
// Story: US-025-01 (Version Identity)
// Purpose: Tests for controller version reporting via /status endpoint
package controller

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/update"
)

// newTestServerWithManifest creates a controller Server backed by a mock manifest
// server so that Notify()'s eager fetch succeeds in tests.
func newTestServerWithManifest(t *testing.T, version string) (*Server, func()) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)

	key := runtime.GOOS + "/" + runtime.GOARCH
	content := update.ManifestContent{
		SchemaVersion: 1,
		Version:       "0.3.0",
		MinSupported:  "0.1.0",
		Released:      "2026-02-15",
		ReleaseNotes:  "Bug fixes",
		Platforms: map[string]update.PlatformRelease{
			key: {
				URL:    "https://releases.stonkagents.com/0.3.0/stonkagents-" + key + ".tar.gz",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   1024000,
			},
		},
	}
	env, err := update.SignManifest(content, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	body, _ := json.Marshal(env)

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))

	srv, err := NewServer(version)
	if err != nil {
		mockSrv.Close()
		t.Fatalf("NewServer: %v", err)
	}
	// Override updater to use test keypair and mock manifest server; give it an
	// install pipeline so Start() behaves the same on every platform.
	srv.updater.installFn = noopInstallFn
	srv.updater.pubKey = pub
	srv.updater.manifestURL = mockSrv.URL + "/manifest.json"
	srv.updater.retryBaseDelay = 1 * time.Millisecond

	return srv, mockSrv.Close
}

func TestStatusEndpoint_IncludesVersion(t *testing.T) {
	srv, err := NewServer("1.5.0")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Version != "1.5.0" {
		t.Errorf("Version = %q, want %q", resp.Version, "1.5.0")
	}
}

func TestStatusEndpoint_DevVersion(t *testing.T) {
	srv, err := NewServer("dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	var resp statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Version != "dev" {
		t.Errorf("Version = %q, want %q", resp.Version, "dev")
	}
}

func TestNewServer_ReturnsNonNilServer(t *testing.T) {
	srv, err := NewServer("1.0.0")
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}
	if srv == nil {
		t.Fatal("NewServer returned nil server")
	}
}

func TestEmbeddedPublicKey_IsValid32ByteEd25519Key(t *testing.T) {
	// updatePublicKey is loaded via go:embed from update_pubkey.pub
	key := GetUpdatePublicKey()
	if len(key) == 0 {
		t.Fatal("GetUpdatePublicKey() is empty — go:embed not wired or file missing")
	}
	if len(key) != ed25519.PublicKeySize {
		t.Errorf("GetUpdatePublicKey() length = %d, want %d", len(key), ed25519.PublicKeySize)
	}
}

// TestEmbeddedPublicKey_MatchesReleaseKey pins the embedded key to the public
// half of the release signing keypair (keys/manifest-public.key). Until 2026-09
// the two had silently diverged, so no daemon ever accepted a signed manifest.
func TestEmbeddedPublicKey_MatchesReleaseKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "keys", "manifest-public.key"))
	if err != nil {
		t.Skipf("keys/manifest-public.key not available: %v", err)
	}
	want, err := update.LoadPublicKey(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("keys/manifest-public.key is not a valid Ed25519 public key: %v", err)
	}
	if got := GetUpdatePublicKey(); !got.Equal(want) {
		t.Fatalf("embedded update_pubkey.pub does not match keys/manifest-public.key — manifests signed with keys/manifest-private.key would be rejected by every daemon")
	}
}

func TestGetUpdatePublicKey_ReturnsCopy(t *testing.T) {
	key1 := GetUpdatePublicKey()
	key2 := GetUpdatePublicKey()
	// Mutate key1 — key2 must be unaffected
	key1[0] ^= 0xFF
	if key1[0] == key2[0] {
		t.Error("GetUpdatePublicKey returns same slice, not a copy — mutation leaked")
	}
}

// === F-025: Task A.6b — Update HTTP endpoints ===

func TestUpdateNotify_TransitionsToAvailable(t *testing.T) {
	srv, cleanup := newTestServerWithManifest(t, "0.2.0")
	defer cleanup()
	body := `{"latest_version":"0.3.0","release_notes":"Bug fixes"}`
	req := httptest.NewRequest(http.MethodPost, "/update/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if srv.updater.State() != StateAvailable {
		t.Errorf("expected AVAILABLE, got %s", srv.updater.State())
	}
}

func TestUpdateStatus_ReturnsCurrentState(t *testing.T) {
	srv, _ := NewServer("0.2.0")
	req := httptest.NewRequest(http.MethodGet, "/update/status", nil)
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp StatusResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.State != StateIdle {
		t.Errorf("expected IDLE, got %s", resp.State)
	}
	if resp.CurrentVersion != "0.2.0" {
		t.Errorf("expected current_version=0.2.0, got %s", resp.CurrentVersion)
	}
}

func TestUpdateStart_BeginsDownload(t *testing.T) {
	srv, cleanup := newTestServerWithManifest(t, "0.2.0")
	defer cleanup()
	// First notify to get AVAILABLE
	body := `{"latest_version":"0.3.0","release_notes":"Notes"}`
	req := httptest.NewRequest(http.MethodPost, "/update/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	// Then start
	req = httptest.NewRequest(http.MethodPost, "/update/start", nil)
	w = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if srv.updater.State() != StateDownloading {
		t.Errorf("expected DOWNLOADING, got %s", srv.updater.State())
	}
}

func TestUpdateStart_WhenIdle_Returns409(t *testing.T) {
	srv, _ := NewServer("0.2.0")
	req := httptest.NewRequest(http.MethodPost, "/update/start", nil)
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestUpdateCancel_CancelsDownload(t *testing.T) {
	srv, cleanup := newTestServerWithManifest(t, "0.2.0")
	defer cleanup()
	body := `{"latest_version":"0.3.0","release_notes":"Notes"}`
	req := httptest.NewRequest(http.MethodPost, "/update/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	// Start download
	req = httptest.NewRequest(http.MethodPost, "/update/start", nil)
	w = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	// Cancel
	req = httptest.NewRequest(http.MethodPost, "/update/cancel", nil)
	w = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if srv.updater.State() != StateCancelled {
		t.Errorf("expected CANCELLED, got %s", srv.updater.State())
	}
}

func TestUpdateCancel_WhenNotDownloading_Returns409(t *testing.T) {
	srv, _ := NewServer("0.2.0")
	req := httptest.NewRequest(http.MethodPost, "/update/cancel", nil)
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}
