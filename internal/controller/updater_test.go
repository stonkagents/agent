// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: TDD tests for the controller update state machine

package controller

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/update"
)

// mockNotifier records notification calls for test assertions.
type mockNotifier struct {
	mu        sync.Mutex
	available []string // versions notified as available
	completed []string // versions notified as complete
	failed    []string // versions notified as failed
}

func (m *mockNotifier) NotifyUpdateAvailable(version, notes string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.available = append(m.available, version)
	return nil
}

func (m *mockNotifier) NotifyUpdateComplete(version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, version)
	return nil
}

func (m *mockNotifier) NotifyUpdateFailed(version, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = append(m.failed, version+": "+errMsg)
	return nil
}

func TestUpdater_InitiallyIdle(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	if u.State() != StateIdle {
		t.Errorf("expected IDLE, got %s", u.State())
	}
}

func TestUpdater_Notify_TransitionsToAvailable(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Bug fixes")
	if u.State() != StateAvailable {
		t.Errorf("expected AVAILABLE, got %s", u.State())
	}
	s := u.Status()
	if s.LatestVersion != "0.3.0" {
		t.Errorf("expected latest_version=0.3.0, got %s", s.LatestVersion)
	}
	if s.ReleaseNotes != "Bug fixes" {
		t.Errorf("expected release_notes, got %s", s.ReleaseNotes)
	}
}

func TestUpdater_Notify_SameVersion_StaysIdle(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.2.0", "Same version")
	if u.State() != StateIdle {
		t.Errorf("expected IDLE (no update for same version), got %s", u.State())
	}
}

func TestUpdater_Notify_OlderVersion_StaysIdle(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.1.0", "Older version")
	if u.State() != StateIdle {
		t.Errorf("expected IDLE (no update for older version), got %s", u.State())
	}
}

// noopInstallFn stands in for a platform install pipeline in state-machine tests.
func noopInstallFn(context.Context, string, string, string, string, string) error { return nil }

func TestUpdater_Start_TransitionsToDownloading(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0", InstallFn: noopInstallFn})
	u.Notify("0.3.0", "Notes")
	err := u.Start()
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if u.State() != StateDownloading {
		t.Errorf("expected DOWNLOADING, got %s", u.State())
	}
}

func TestUpdater_Start_RejectsFromIdle(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	err := u.Start()
	if err == nil {
		t.Error("expected error when starting from IDLE")
	}
	if u.State() != StateIdle {
		t.Errorf("expected IDLE after rejected Start, got %s", u.State())
	}
}

func TestUpdater_Start_ManualInstallWhenNoInstallFn(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"}) // no InstallFn: Windows
	u.Notify("0.3.0", "Notes")
	err := u.Start()
	if !errors.Is(err, ErrManualInstall) {
		t.Fatalf("Start() error = %v, want ErrManualInstall", err)
	}
	if u.State() != StateAvailable {
		t.Errorf("expected to stay AVAILABLE, got %s", u.State())
	}
	if st := u.Status(); !st.ManualInstall {
		t.Errorf("Status().ManualInstall = false, want true")
	}
}

func TestUpdater_Cancel_FromDownloading(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0", InstallFn: noopInstallFn})
	u.Notify("0.3.0", "Notes")
	u.Start()
	err := u.Cancel()
	if err != nil {
		t.Fatalf("Cancel() returned error: %v", err)
	}
	if u.State() != StateCancelled {
		t.Errorf("expected CANCELLED, got %s", u.State())
	}
}

func TestUpdater_Cancel_RejectsFromIdle(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	err := u.Cancel()
	if err == nil {
		t.Error("expected error when cancelling from IDLE")
	}
}

func TestUpdater_Status_ReturnsCurrentVersion(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	s := u.Status()
	if s.CurrentVersion != "0.2.0" {
		t.Errorf("expected current_version=0.2.0, got %s", s.CurrentVersion)
	}
	if s.State != StateIdle {
		t.Errorf("expected state=IDLE, got %s", s.State)
	}
	if s.Error != nil {
		t.Errorf("expected nil error, got %v", *s.Error)
	}
}

func TestUpdater_Fail_TransitionsToFailed(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Notes")
	u.Start()
	u.Fail("download timeout")
	if u.State() != StateFailed {
		t.Errorf("expected FAILED, got %s", u.State())
	}
	s := u.Status()
	if s.Error == nil || *s.Error != "download timeout" {
		t.Errorf("expected error='download timeout', got %v", s.Error)
	}
}

func TestUpdater_NotifyAfterFailed_ResetsToAvailable(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Notes")
	u.Start()
	u.Fail("some error")
	// New notify with newer (or same) version resets from FAILED → AVAILABLE
	u.Notify("0.3.0", "Notes v2")
	if u.State() != StateAvailable {
		t.Errorf("expected AVAILABLE after notify post-failure, got %s", u.State())
	}
}

func TestUpdater_CancelledThenNotify_ResetsToAvailable(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Notes")
	u.Start()
	u.Cancel()
	// Notify with same version re-enables
	u.Notify("0.3.0", "Notes")
	if u.State() != StateAvailable {
		t.Errorf("expected AVAILABLE after notify post-cancel, got %s", u.State())
	}
}

// === F-025: Task A.7b — Platform selection + force-update ===

func TestSelectPlatform_MatchesCurrentOS(t *testing.T) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	manifest := &update.ManifestContent{
		Platforms: map[string]update.PlatformRelease{
			key: {
				URL:    "https://releases.stonkagents.com/0.3.0/stonkagents-" + key + ".tar.gz",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   1024000,
			},
		},
	}
	plat, err := selectPlatform(manifest)
	if err != nil {
		t.Fatalf("selectPlatform: %v", err)
	}
	if plat.Size != 1024000 {
		t.Errorf("expected size=1024000, got %d", plat.Size)
	}
}

func TestSelectPlatform_UnsupportedPlatform_ReturnsError(t *testing.T) {
	manifest := &update.ManifestContent{
		Platforms: map[string]update.PlatformRelease{
			"windows/amd64": {
				URL:    "https://releases.stonkagents.com/0.3.0/stonkagents-windows-amd64.zip",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   1024000,
			},
		},
	}
	// On darwin/arm64 this should fail (no matching platform)
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		t.Skip("test requires a non-windows/amd64 platform")
	}
	_, err := selectPlatform(manifest)
	if err == nil {
		t.Error("expected error for unsupported platform")
	}
}

func TestIsForceUpdate_BelowMinSupported(t *testing.T) {
	manifest := &update.ManifestContent{
		MinSupported: "0.2.0",
	}
	// currentVersion 0.1.0 < minSupported 0.2.0 → force=true
	if !isForceUpdate(manifest, "0.1.0") {
		t.Error("expected force=true when current < min_supported")
	}
}

func TestIsForceUpdate_AtOrAboveMinSupported(t *testing.T) {
	manifest := &update.ManifestContent{
		MinSupported: "0.2.0",
	}
	// currentVersion 0.2.0 == minSupported → force=false
	if isForceUpdate(manifest, "0.2.0") {
		t.Error("expected force=false when current == min_supported")
	}
	// currentVersion 0.3.0 > minSupported → force=false
	if isForceUpdate(manifest, "0.3.0") {
		t.Error("expected force=false when current > min_supported")
	}
}

// === F-025: Task A.10 — Notifier Interface Wiring ===

func TestUpdater_NotifiesOnAvailable(t *testing.T) {
	mock := &mockNotifier{}
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		Notifier:       mock,
	})
	u.Notify("0.3.0", "Bug fixes")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.available) != 1 {
		t.Fatalf("expected 1 available notification, got %d", len(mock.available))
	}
	if mock.available[0] != "0.3.0" {
		t.Errorf("expected version=0.3.0, got %s", mock.available[0])
	}
}

func TestUpdater_NotifiesOnFailed(t *testing.T) {
	mock := &mockNotifier{}
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		Notifier:       mock,
	})
	u.Notify("0.3.0", "Notes")
	u.Start()
	u.Fail("download timeout")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.failed) != 1 {
		t.Fatalf("expected 1 failed notification, got %d", len(mock.failed))
	}
	if mock.failed[0] != "0.3.0: download timeout" {
		t.Errorf("expected failure notification, got %s", mock.failed[0])
	}
}

func TestUpdater_NilNotifier_NoError(t *testing.T) {
	// Updater with nil notifier should not panic
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Notes")
	u.Start()
	u.Fail("some error")
	// No panic = pass
}

// === F-025: Task D — Byte count in download progress ===

func TestUpdater_UpdateProgress_SetsBytes(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Notes")
	u.Start()

	u.UpdateProgress(5*1024*1024, 20*1024*1024) // 5MB of 20MB

	s := u.Status()
	if s.BytesDownloaded != 5*1024*1024 {
		t.Errorf("expected BytesDownloaded=5242880, got %d", s.BytesDownloaded)
	}
	if s.BytesTotal != 20*1024*1024 {
		t.Errorf("expected BytesTotal=20971520, got %d", s.BytesTotal)
	}
	if s.Progress != 25 {
		t.Errorf("expected Progress=25%%, got %d", s.Progress)
	}
}

func TestUpdater_UpdateProgress_ZeroTotal_NoDiv(t *testing.T) {
	u := NewUpdater(UpdaterConfig{CurrentVersion: "0.2.0"})
	u.Notify("0.3.0", "Notes")
	u.Start()

	// Content-Length unknown (-1 or 0) — should not panic or set weird percentage
	u.UpdateProgress(1024, 0)

	s := u.Status()
	if s.BytesDownloaded != 1024 {
		t.Errorf("expected BytesDownloaded=1024, got %d", s.BytesDownloaded)
	}
	if s.BytesTotal != 0 {
		t.Errorf("expected BytesTotal=0, got %d", s.BytesTotal)
	}
	// Progress stays 0 when total is unknown
	if s.Progress != 0 {
		t.Errorf("expected Progress=0 (unknown total), got %d", s.Progress)
	}
}

func TestUpdater_NotifiesOnComplete(t *testing.T) {
	mock := &mockNotifier{}
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		Notifier:       mock,
	})
	u.Notify("0.3.0", "Notes")
	u.Start()
	u.Complete()

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.completed) != 1 {
		t.Fatalf("expected 1 complete notification, got %d", len(mock.completed))
	}
	if mock.completed[0] != "0.3.0" {
		t.Errorf("expected version=0.3.0, got %s", mock.completed[0])
	}
}

// === TD-048: Start() orchestration — download → install → complete ===

func TestUpdater_Start_Orchestration_Complete(t *testing.T) {
	// Serve a small fake archive
	archiveContent := []byte("fake-archive-content-for-orchestration-test")
	checksum := sha256Hex(archiveContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archiveContent)))
		w.Write(archiveContent)
	}))
	defer srv.Close()

	installCalled := make(chan string, 1)
	mock := &mockNotifier{}
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: 1 * time.Millisecond,
		Notifier:       mock,
		InstallFn: func(ctx context.Context, archivePath, extractDir, binDir, backupDir, healthURL string) error {
			installCalled <- archivePath
			return nil
		},
		BinDir:    t.TempDir(),
		BackupDir: t.TempDir(),
		HealthURL: "http://localhost:7841/health",
	})

	// Notify sets state to AVAILABLE (no pubKey = no manifest fetch)
	u.Notify("0.3.0", "Bug fixes")
	if u.State() != StateAvailable {
		t.Fatalf("expected AVAILABLE, got %s", u.State())
	}

	// Inject resolvedPlatform (normally set by Notify with pubKey)
	u.mu.Lock()
	u.resolvedPlatform = &update.PlatformRelease{
		URL:    srv.URL + "/archive.tar.gz",
		SHA256: checksum,
		Size:   int64(len(archiveContent)),
	}
	u.mu.Unlock()

	// Start should spawn orchestration goroutine
	if err := u.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for installFn to be called
	select {
	case path := <-installCalled:
		if path == "" {
			t.Error("installFn called with empty archive path")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout waiting for installFn — state=%s", u.State())
	}

	// Wait for state to reach COMPLETE
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if u.State() == StateComplete {
			// Verify notifications fired
			mock.mu.Lock()
			if len(mock.completed) != 1 || mock.completed[0] != "0.3.0" {
				t.Errorf("expected complete notification for 0.3.0, got %v", mock.completed)
			}
			mock.mu.Unlock()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected COMPLETE, got %s", u.State())
}

func TestUpdater_Start_Orchestration_InstallFailure(t *testing.T) {
	archiveContent := []byte("fake-archive-for-failure-test")
	checksum := sha256Hex(archiveContent)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archiveContent)
	}))
	defer srv.Close()

	mock := &mockNotifier{}
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: 1 * time.Millisecond,
		Notifier:       mock,
		InstallFn: func(ctx context.Context, archivePath, extractDir, binDir, backupDir, healthURL string) error {
			return fmt.Errorf("codesign verification failed")
		},
		BinDir:    t.TempDir(),
		BackupDir: t.TempDir(),
		HealthURL: "http://localhost:7841/health",
	})

	u.Notify("0.3.0", "Notes")
	u.mu.Lock()
	u.resolvedPlatform = &update.PlatformRelease{
		URL:    srv.URL + "/archive.tar.gz",
		SHA256: checksum,
		Size:   int64(len(archiveContent)),
	}
	u.mu.Unlock()

	u.Start()

	// Wait for state to reach FAILED
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if u.State() == StateFailed {
			s := u.Status()
			if s.Error == nil || *s.Error == "" {
				t.Error("expected error message on failure")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected FAILED, got %s", u.State())
}

func TestUpdater_Start_Orchestration_Cancel(t *testing.T) {
	// Slow server — allows cancel to fire before download completes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // slow
		w.Write([]byte("should-not-complete"))
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: 1 * time.Millisecond,
		InstallFn: func(ctx context.Context, archivePath, extractDir, binDir, backupDir, healthURL string) error {
			t.Error("installFn should not be called after cancel")
			return nil
		},
		BinDir:    t.TempDir(),
		BackupDir: t.TempDir(),
		HealthURL: "http://localhost:7841/health",
	})

	u.Notify("0.3.0", "Notes")
	u.mu.Lock()
	u.resolvedPlatform = &update.PlatformRelease{
		URL:    srv.URL + "/archive.tar.gz",
		SHA256: "deadbeef",
		Size:   1024,
	}
	u.mu.Unlock()

	u.Start()
	time.Sleep(50 * time.Millisecond) // let goroutine start

	if err := u.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if u.State() != StateCancelled {
		t.Errorf("expected CANCELLED, got %s", u.State())
	}
}

// TestNewUpdater_DefaultReleaseURLs verifies the manifest and archive URLs
// default off the single release host constant.
func TestNewUpdater_DefaultReleaseURLs(t *testing.T) {
	t.Setenv("STONKAGENTS_RELEASES_URL", "")
	u := NewUpdater(UpdaterConfig{CurrentVersion: "1.0.0"})

	if u.releaseBaseURL != DefaultReleaseBaseURL {
		t.Errorf("releaseBaseURL = %q, want %q", u.releaseBaseURL, DefaultReleaseBaseURL)
	}
	if want := DefaultReleaseBaseURL + "manifest.json"; u.manifestURL != want {
		t.Errorf("manifestURL = %q, want %q", u.manifestURL, want)
	}
}

// TestNewUpdater_ReleasesURLEnvOverride verifies STONKAGENTS_RELEASES_URL moves
// both the manifest and the archive base, with the trailing slash normalised.
func TestNewUpdater_ReleasesURLEnvOverride(t *testing.T) {
	t.Setenv("STONKAGENTS_RELEASES_URL", "https://releases-staging.example.com")
	u := NewUpdater(UpdaterConfig{CurrentVersion: "1.0.0"})

	if want := "https://releases-staging.example.com/"; u.releaseBaseURL != want {
		t.Errorf("releaseBaseURL = %q, want %q", u.releaseBaseURL, want)
	}
	if want := "https://releases-staging.example.com/manifest.json"; u.manifestURL != want {
		t.Errorf("manifestURL = %q, want %q", u.manifestURL, want)
	}
}

// TestNewUpdater_ExplicitURLsWinOverEnv verifies explicit config beats the env override.
func TestNewUpdater_ExplicitURLsWinOverEnv(t *testing.T) {
	t.Setenv("STONKAGENTS_RELEASES_URL", "https://releases-staging.example.com")
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "1.0.0",
		ManifestURL:    "https://cfg.example.com/m.json",
		ReleaseBaseURL: "https://cfg.example.com/",
	})

	if u.releaseBaseURL != "https://cfg.example.com/" || u.manifestURL != "https://cfg.example.com/m.json" {
		t.Errorf("explicit URLs overridden: base=%q manifest=%q", u.releaseBaseURL, u.manifestURL)
	}
}

// signedManifestServer serves a signed manifest for version v with a Windows and
// darwin platform entry, and returns the server plus the verifying public key.
func signedManifestServer(t *testing.T, version, minSupported string) (*httptest.Server, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	key := runtime.GOOS + "/" + runtime.GOARCH
	content := update.ManifestContent{
		SchemaVersion: 1,
		Version:       version,
		MinSupported:  minSupported,
		Released:      "2026-09-13",
		ReleaseNotes:  "notes for " + version,
		Platforms: map[string]update.PlatformRelease{
			key: {
				URL:    "https://releases.example.test/" + version + "/archive.tar.gz",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   1024,
				Installer: &update.ArchiveInfo{
					URL:    "https://releases.example.test/" + version + "/Setup-" + version + ".exe",
					SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
					Size:   2048,
				},
			},
		},
	}
	env, err := update.SignManifest(content, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	body, _ := json.Marshal(env)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, pub
}

// The daemon relays whatever version the tracker saw; the controller must not
// go AVAILABLE for a manifest whose version is not newer than what it runs
// (seen live: a 2.1.2 dev controller offered "Update to v1.0.0" from the
// production manifest after the daemon relayed 2.1.4).
func TestUpdater_Notify_ManifestOlderThanCurrent_StaysIdle(t *testing.T) {
	srv, pub := signedManifestServer(t, "1.0.0", "1.0.0")
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "2.1.2",
		PubKey:         pub,
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: time.Millisecond,
	})
	u.Notify("2.1.4", "relayed by daemon")
	if u.State() != StateIdle {
		t.Fatalf("expected IDLE, got %s (latest=%s)", u.State(), u.Status().LatestVersion)
	}
}

func TestUpdater_Notify_ManifestNewer_ExposesInstallerURL(t *testing.T) {
	srv, pub := signedManifestServer(t, "2.1.4", "2.1.1")
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "2.1.2",
		PubKey:         pub,
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: time.Millisecond,
	})
	u.Notify("2.1.4", "relayed")
	st := u.Status()
	if st.State != StateAvailable || st.LatestVersion != "2.1.4" {
		t.Fatalf("status = %+v, want AVAILABLE 2.1.4", st)
	}
	if st.InstallerURL != "https://releases.example.test/2.1.4/Setup-2.1.4.exe" {
		t.Errorf("InstallerURL = %q", st.InstallerURL)
	}
	if !st.ManualInstall {
		t.Errorf("ManualInstall = false, want true when no InstallFn is wired")
	}
}
