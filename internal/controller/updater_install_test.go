// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: Tests for binary/config backup and rollback support

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// === F-025: Task A.9a — Binary + Config Backup ===

func TestBackup_CopiesBinariesToBackupDir(t *testing.T) {
	binDir := t.TempDir()
	backupDir := t.TempDir()

	// Create fake binaries
	daemonContent := []byte("fake-daemon-binary-v0.2.0")
	controllerContent := []byte("fake-controller-binary-v0.2.0")
	os.WriteFile(filepath.Join(binDir, "stonkagents"), daemonContent, 0755)
	os.WriteFile(filepath.Join(binDir, "stonkagents-controller"), controllerContent, 0755)

	err := backupBinaries(binDir, backupDir)
	if err != nil {
		t.Fatalf("backupBinaries: %v", err)
	}

	// Assert backup copies exist with correct contents
	gotDaemon, err := os.ReadFile(filepath.Join(backupDir, "stonkagents.bak"))
	if err != nil {
		t.Fatalf("read daemon backup: %v", err)
	}
	if string(gotDaemon) != string(daemonContent) {
		t.Errorf("daemon backup content mismatch")
	}

	gotController, err := os.ReadFile(filepath.Join(backupDir, "stonkagents-controller.bak"))
	if err != nil {
		t.Fatalf("read controller backup: %v", err)
	}
	if string(gotController) != string(controllerContent) {
		t.Errorf("controller backup content mismatch")
	}
}

func TestBackup_CopiesConfig(t *testing.T) {
	configDir := t.TempDir()
	backupDir := t.TempDir()

	configContent := []byte("tracker_url: http://localhost:7842\nversion: 0.2.0\n")
	configPath := filepath.Join(configDir, "config.yaml")
	os.WriteFile(configPath, configContent, 0644)

	err := backupConfig(configPath, backupDir)
	if err != nil {
		t.Fatalf("backupConfig: %v", err)
	}

	gotConfig, err := os.ReadFile(filepath.Join(backupDir, "config.yaml.bak"))
	if err != nil {
		t.Fatalf("read config backup: %v", err)
	}
	if string(gotConfig) != string(configContent) {
		t.Errorf("config backup content mismatch")
	}
}

func TestBackup_SkipsMissingBinaries(t *testing.T) {
	binDir := t.TempDir()
	backupDir := t.TempDir()

	// Only one binary exists
	os.WriteFile(filepath.Join(binDir, "stonkagents"), []byte("daemon"), 0755)

	// Should not error — just skip missing files
	err := backupBinaries(binDir, backupDir)
	if err != nil {
		t.Fatalf("backupBinaries should skip missing: %v", err)
	}

	// Daemon backup exists
	if _, err := os.Stat(filepath.Join(backupDir, "stonkagents.bak")); err != nil {
		t.Error("expected daemon backup to exist")
	}
	// Controller backup does NOT exist (not an error)
	if _, err := os.Stat(filepath.Join(backupDir, "stonkagents-controller.bak")); !os.IsNotExist(err) {
		t.Error("expected controller backup to not exist")
	}
}

// === F-025: Task A.9b — Health Check + Auto-Rollback ===

func TestWaitForHealthy_SucceedsWhenDaemonHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion:     "0.2.0",
		HealthCheckTimeout: 500 * time.Millisecond,
		HealthPollInterval: 50 * time.Millisecond,
	})

	err := u.waitForHealthy(context.Background(), srv.URL+"/health")
	if err != nil {
		t.Fatalf("waitForHealthy: %v", err)
	}
}

func TestWaitForHealthy_TimesOutWhenDaemonUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion:     "0.2.0",
		HealthCheckTimeout: 200 * time.Millisecond,
		HealthPollInterval: 50 * time.Millisecond,
	})

	err := u.waitForHealthy(context.Background(), srv.URL+"/health")
	if err == nil {
		t.Error("expected timeout error when daemon is unhealthy")
	}
}

func TestWaitForHealthy_SucceedsAfterInitialFailures(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion:     "0.2.0",
		HealthCheckTimeout: 2 * time.Second,
		HealthPollInterval: 50 * time.Millisecond,
	})

	err := u.waitForHealthy(context.Background(), srv.URL+"/health")
	if err != nil {
		t.Fatalf("expected success after initial failures: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
}

func TestRollback_RestoresPreviousBinaries(t *testing.T) {
	binDir := t.TempDir()
	backupDir := t.TempDir()

	// Create backups (as if backupBinaries ran previously)
	daemonBackup := []byte("old-daemon-v0.2.0")
	controllerBackup := []byte("old-controller-v0.2.0")
	os.WriteFile(filepath.Join(backupDir, "stonkagents.bak"), daemonBackup, 0755)
	os.WriteFile(filepath.Join(backupDir, "stonkagents-controller.bak"), controllerBackup, 0755)

	// Create "new" binaries in binDir (simulating a failed update install)
	os.WriteFile(filepath.Join(binDir, "stonkagents"), []byte("new-broken-daemon"), 0755)
	os.WriteFile(filepath.Join(binDir, "stonkagents-controller"), []byte("new-broken-controller"), 0755)

	err := rollbackBinaries(backupDir, binDir)
	if err != nil {
		t.Fatalf("rollbackBinaries: %v", err)
	}

	// Assert original binaries restored
	gotDaemon, _ := os.ReadFile(filepath.Join(binDir, "stonkagents"))
	if string(gotDaemon) != string(daemonBackup) {
		t.Errorf("daemon not restored: got %q", gotDaemon)
	}
	gotController, _ := os.ReadFile(filepath.Join(binDir, "stonkagents-controller"))
	if string(gotController) != string(controllerBackup) {
		t.Errorf("controller not restored: got %q", gotController)
	}
}

func TestRollback_SkipsMissingBackups(t *testing.T) {
	binDir := t.TempDir()
	backupDir := t.TempDir()

	// Only daemon backup exists
	os.WriteFile(filepath.Join(backupDir, "stonkagents.bak"), []byte("old-daemon"), 0755)
	os.WriteFile(filepath.Join(binDir, "stonkagents"), []byte("new-daemon"), 0755)

	err := rollbackBinaries(backupDir, binDir)
	if err != nil {
		t.Fatalf("rollbackBinaries should skip missing: %v", err)
	}

	gotDaemon, _ := os.ReadFile(filepath.Join(binDir, "stonkagents"))
	if string(gotDaemon) != "old-daemon" {
		t.Errorf("daemon not restored: got %q", gotDaemon)
	}
}

// === TD-052: 7-day backup retention ===

func TestCleanupStaleBackups_RemovesOldFiles(t *testing.T) {
	backupDir := t.TempDir()

	// Create a .bak file with mtime 10 days ago (stale)
	stalePath := filepath.Join(backupDir, "stonkagents.bak")
	os.WriteFile(stalePath, []byte("old-daemon"), 0755)
	staleTime := time.Now().Add(-10 * 24 * time.Hour)
	os.Chtimes(stalePath, staleTime, staleTime)

	// Create a .bak file with mtime 1 day ago (fresh)
	freshPath := filepath.Join(backupDir, "stonkagents-controller.bak")
	os.WriteFile(freshPath, []byte("new-controller"), 0755)

	// Create a non-.bak file (should not be touched)
	otherPath := filepath.Join(backupDir, "config.yaml")
	os.WriteFile(otherPath, []byte("config"), 0644)

	removed, err := cleanupStaleBackups(backupDir, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanupStaleBackups: %v", err)
	}

	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only stale .bak)", removed)
	}

	// Stale .bak should be gone
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale .bak file should have been removed")
	}

	// Fresh .bak should remain
	if _, err := os.Stat(freshPath); err != nil {
		t.Error("fresh .bak file should not be removed")
	}

	// Non-.bak should remain
	if _, err := os.Stat(otherPath); err != nil {
		t.Error("non-.bak file should not be removed")
	}
}

func TestCleanupStaleBackups_EmptyDir(t *testing.T) {
	backupDir := t.TempDir()
	removed, err := cleanupStaleBackups(backupDir, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanupStaleBackups: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

func TestCleanupStaleBackups_MissingDir(t *testing.T) {
	removed, err := cleanupStaleBackups("/nonexistent/path", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("cleanupStaleBackups should not error for missing dir: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

func TestArchiveBinaryPath_PrefersNewName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stonkagents"), []byte("new"), 0755)

	if got, want := archiveBinaryPath(dir, "stonkagents"), filepath.Join(dir, "stonkagents"); got != want {
		t.Errorf("archiveBinaryPath = %q, want %q", got, want)
	}
}
