//go:build darwin
// +build darwin

// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-07 (macOS Install Sequence)
// Purpose: TDD tests for macOS-specific update operations: tar.gz extraction,
//          codesign verification, binary replacement, and rollback.

package controller

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// === F-025: Task B.1a — tar.gz Extraction ===

// createTestTarGz creates a tar.gz archive in dir with the given file entries.
// Each entry is a name→content pair. Returns the archive path.
func createTestTarGz(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	archivePath := filepath.Join(dir, "test-archive.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive file: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header for %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content for %s: %v", name, err)
		}
	}
	return archivePath
}

func TestDarwinInstall_ExtractsTarGz(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	archivePath := createTestTarGz(t, srcDir, map[string]string{
		"stonkagents":            "daemon-binary-v0.3.0",
		"stonkagents-controller": "controller-binary-v0.3.0",
	})

	err := extractTarGz(archivePath, destDir)
	if err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	// Assert both files extracted with correct content
	gotDaemon, err := os.ReadFile(filepath.Join(destDir, "stonkagents"))
	if err != nil {
		t.Fatalf("read extracted daemon: %v", err)
	}
	if string(gotDaemon) != "daemon-binary-v0.3.0" {
		t.Errorf("daemon content mismatch: got %q", gotDaemon)
	}

	gotCtrl, err := os.ReadFile(filepath.Join(destDir, "stonkagents-controller"))
	if err != nil {
		t.Fatalf("read extracted controller: %v", err)
	}
	if string(gotCtrl) != "controller-binary-v0.3.0" {
		t.Errorf("controller content mismatch: got %q", gotCtrl)
	}
}

func TestDarwinInstall_ExtractsTarGz_RejectsPathTraversal(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Archive with a malicious path traversal entry
	archivePath := createTestTarGz(t, srcDir, map[string]string{
		"../../../etc/evil": "malicious content",
	})

	err := extractTarGz(archivePath, destDir)
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}

	// Assert the evil file was NOT created
	if _, statErr := os.Stat(filepath.Join(destDir, "../../../etc/evil")); !os.IsNotExist(statErr) {
		t.Error("path traversal file should not have been created")
	}
}

func TestDarwinInstall_ExtractsTarGz_PreservesPermissions(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	archivePath := createTestTarGz(t, srcDir, map[string]string{
		"stonkagents": "binary-content",
	})

	err := extractTarGz(archivePath, destDir)
	if err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	info, err := os.Stat(filepath.Join(destDir, "stonkagents"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Our test helper creates entries with mode 0755
	if info.Mode().Perm() != 0755 {
		t.Errorf("expected mode 0755, got %o", info.Mode().Perm())
	}
}

func TestDarwinInstall_ExtractsTarGz_RejectsOversizedFile(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a tar.gz with a header claiming 600MB (exceeds 500MB limit).
	// The header size check must reject this before any bytes are read.
	archivePath := filepath.Join(srcDir, "oversize.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "stonkagents",
		Mode: 0755,
		Size: 600 * 1024 * 1024, // 600MB — exceeds maxFileSize (500MB)
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	// Write just enough content so the tar entry exists (header is what matters)
	tw.Write([]byte("x"))

	tw.Close()
	gw.Close()
	f.Close()

	err = extractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "exceeds max size") {
		t.Errorf("expected 'exceeds max size' in error, got: %s", got)
	}

	// Assert the file was NOT created
	if _, statErr := os.Stat(filepath.Join(destDir, "stonkagents")); !os.IsNotExist(statErr) {
		t.Error("oversized file should not have been extracted")
	}
}

// === F-025: Task B.1b — Codesign Verification ===

func TestDarwinInstall_VerifiesCodesign_RejectsUnsigned(t *testing.T) {
	// Create a fake binary — not signed, codesign --verify will reject it
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "stonkagents")
	os.WriteFile(fakeBin, []byte("unsigned-fake-binary"), 0755)

	err := verifyCodesign(fakeBin)
	if err == nil {
		t.Error("expected error for unsigned binary")
	}
}

func TestDarwinInstall_VerifiesCodesign_RejectsWrongTeamID(t *testing.T) {
	// Mock codesign output with wrong team identifier
	origRunner := runCodesign
	defer func() { runCodesign = origRunner }()

	runCodesign = func(binaryPath string) (string, error) {
		return "Authority=Developer ID Application: Evil Corp (WRONGTEAM)\nTeamIdentifier=WRONGTEAM\n", nil
	}

	err := verifyCodesign("/fake/path/stonkagents")
	if err == nil {
		t.Error("expected error for wrong team ID")
	}
}

func TestDarwinInstall_VerifiesCodesign_AcceptsCorrectTeamID(t *testing.T) {
	// Mock codesign output with correct team identifier
	origRunner := runCodesign
	defer func() { runCodesign = origRunner }()

	runCodesign = func(binaryPath string) (string, error) {
		return fmt.Sprintf(
			"Authority=Developer ID Application: StonkAgents (%s)\nTeamIdentifier=%s\n",
			expectedTeamID, expectedTeamID,
		), nil
	}

	err := verifyCodesign("/fake/path/stonkagents")
	if err != nil {
		t.Errorf("expected no error for correct team ID, got: %v", err)
	}
}

func TestDarwinInstall_VerifiesCodesign_RejectsCodesignFailure(t *testing.T) {
	// Mock codesign returning an error (e.g., binary tampered with)
	origRunner := runCodesign
	defer func() { runCodesign = origRunner }()

	runCodesign = func(binaryPath string) (string, error) {
		return "", fmt.Errorf("exit status 3")
	}

	err := verifyCodesign("/fake/path/stonkagents")
	if err == nil {
		t.Error("expected error when codesign command fails")
	}
}

// === F-025: Task B.1c — Replace + Restart + Rollback ===

// mockDarwinDeps overrides all injectable function vars for isolated testing.
// Returns a cleanup function to restore originals.
func mockDarwinDeps(t *testing.T) func() {
	t.Helper()
	origCodesign := runCodesign
	origStop := stopDaemonFn
	origStart := startDaemonFn

	// Default mocks: codesign passes, launchctl succeeds
	runCodesign = func(binaryPath string) (string, error) {
		return fmt.Sprintf("TeamIdentifier=%s\n", expectedTeamID), nil
	}
	stopDaemonFn = func() error { return nil }
	startDaemonFn = func() error { return nil }

	return func() {
		runCodesign = origCodesign
		stopDaemonFn = origStop
		startDaemonFn = origStart
	}
}

func TestDarwinInstall_ReplacesBinaries(t *testing.T) {
	cleanup := mockDarwinDeps(t)
	defer cleanup()

	extractDir := t.TempDir()
	binDir := t.TempDir()

	// Create "new" binaries in extract dir
	os.WriteFile(filepath.Join(extractDir, "stonkagents"), []byte("new-daemon-v0.3.0"), 0755)
	os.WriteFile(filepath.Join(extractDir, "stonkagents-controller"), []byte("new-controller-v0.3.0"), 0755)

	// Create "old" binaries in bin dir
	os.WriteFile(filepath.Join(binDir, "stonkagents"), []byte("old-daemon-v0.2.0"), 0755)
	os.WriteFile(filepath.Join(binDir, "stonkagents-controller"), []byte("old-controller-v0.2.0"), 0755)

	err := replaceBinaries(extractDir, binDir)
	if err != nil {
		t.Fatalf("replaceBinaries: %v", err)
	}

	// Assert new binaries in place
	gotDaemon, _ := os.ReadFile(filepath.Join(binDir, "stonkagents"))
	if string(gotDaemon) != "new-daemon-v0.3.0" {
		t.Errorf("daemon not replaced: got %q", gotDaemon)
	}
	gotCtrl, _ := os.ReadFile(filepath.Join(binDir, "stonkagents-controller"))
	if string(gotCtrl) != "new-controller-v0.3.0" {
		t.Errorf("controller not replaced: got %q", gotCtrl)
	}
}

func TestDarwinInstall_RejectsMissingDaemonBinary(t *testing.T) {
	cleanup := mockDarwinDeps(t)
	defer cleanup()

	srcDir := t.TempDir()
	binDir := t.TempDir()
	backupDir := t.TempDir()
	extractDir := t.TempDir()

	// Archive that has controller but NOT daemon — should be rejected
	archivePath := createTestTarGz(t, srcDir, map[string]string{
		"stonkagents-controller": "controller-only",
	})

	u := NewUpdater(UpdaterConfig{
		CurrentVersion:     "0.2.0",
		HealthCheckTimeout: 200 * time.Millisecond,
		HealthPollInterval: 50 * time.Millisecond,
	})

	err := u.installDarwin(context.Background(), archivePath, extractDir, binDir, backupDir, "http://localhost/health")
	if err == nil {
		t.Fatal("expected error for missing daemon binary, got nil")
	}
	if !strings.Contains(err.Error(), "required binary") {
		t.Errorf("expected 'required binary' in error, got: %s", err)
	}
}

func TestDarwinInstall_FullSequence_Success(t *testing.T) {
	cleanup := mockDarwinDeps(t)
	defer cleanup()

	// Set up directories
	srcDir := t.TempDir()
	binDir := t.TempDir()
	backupDir := t.TempDir()
	extractDir := t.TempDir()

	// Create archive with new binaries
	archivePath := createTestTarGz(t, srcDir, map[string]string{
		"stonkagents":            "new-daemon-v0.3.0",
		"stonkagents-controller": "new-controller-v0.3.0",
	})

	// Create old binaries in bin dir
	os.WriteFile(filepath.Join(binDir, "stonkagents"), []byte("old-daemon-v0.2.0"), 0755)
	os.WriteFile(filepath.Join(binDir, "stonkagents-controller"), []byte("old-controller-v0.2.0"), 0755)

	// Mock a healthy daemon
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthSrv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion:     "0.2.0",
		HealthCheckTimeout: 500 * time.Millisecond,
		HealthPollInterval: 50 * time.Millisecond,
	})

	err := u.installDarwin(context.Background(), archivePath, extractDir, binDir, backupDir, healthSrv.URL+"/health")
	if err != nil {
		t.Fatalf("installDarwin: %v", err)
	}

	// Assert new binaries installed
	gotDaemon, _ := os.ReadFile(filepath.Join(binDir, "stonkagents"))
	if string(gotDaemon) != "new-daemon-v0.3.0" {
		t.Errorf("daemon not updated: got %q", gotDaemon)
	}
}

func TestDarwinInstall_RollsBackOnHealthCheckFailure(t *testing.T) {
	cleanup := mockDarwinDeps(t)
	defer cleanup()

	srcDir := t.TempDir()
	binDir := t.TempDir()
	backupDir := t.TempDir()
	extractDir := t.TempDir()

	// Create archive with new binaries
	archivePath := createTestTarGz(t, srcDir, map[string]string{
		"stonkagents":            "new-broken-daemon",
		"stonkagents-controller": "new-broken-controller",
	})

	// Create old (good) binaries in bin dir
	os.WriteFile(filepath.Join(binDir, "stonkagents"), []byte("old-daemon-v0.2.0"), 0755)
	os.WriteFile(filepath.Join(binDir, "stonkagents-controller"), []byte("old-controller-v0.2.0"), 0755)

	// Mock an unhealthy daemon — health check always fails
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer healthSrv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion:     "0.2.0",
		HealthCheckTimeout: 200 * time.Millisecond,
		HealthPollInterval: 50 * time.Millisecond,
	})

	err := u.installDarwin(context.Background(), archivePath, extractDir, binDir, backupDir, healthSrv.URL+"/health")
	if err == nil {
		t.Fatal("expected error from failed health check")
	}

	// Assert old binaries restored (rollback happened)
	gotDaemon, _ := os.ReadFile(filepath.Join(binDir, "stonkagents"))
	if string(gotDaemon) != "old-daemon-v0.2.0" {
		t.Errorf("daemon not rolled back: got %q", gotDaemon)
	}
	gotCtrl, _ := os.ReadFile(filepath.Join(binDir, "stonkagents-controller"))
	if string(gotCtrl) != "old-controller-v0.2.0" {
		t.Errorf("controller not rolled back: got %q", gotCtrl)
	}
}
