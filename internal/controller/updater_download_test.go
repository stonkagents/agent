// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: Tests for manifest fetch and Ed25519 verification

package controller

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/update"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// testManifest creates a valid signed manifest for testing.
func testManifest(t *testing.T, privKey ed25519.PrivateKey) []byte {
	t.Helper()
	content := update.ManifestContent{
		SchemaVersion: 1,
		Version:       "0.3.0",
		MinSupported:  "0.1.0",
		Released:      "2026-02-15",
		ReleaseNotes:  "Bug fixes",
		Platforms: map[string]update.PlatformRelease{
			"darwin/arm64": {
				URL:    "https://releases.stonkagents.com/0.3.0/stonkagents-darwin-arm64.tar.gz",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   1024000,
			},
		},
	}
	env, err := update.SignManifest(content, privKey)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	data, _ := json.Marshal(env)
	return data
}

func TestFetchManifest_ValidSignature_Passes(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := testManifest(t, priv)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		PubKey:         pub,
		ManifestURL:    srv.URL + "/manifest.json",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	manifest, err := u.fetchAndVerifyManifest()
	if err != nil {
		t.Fatalf("fetchAndVerifyManifest: %v", err)
	}
	if manifest.Version != "0.3.0" {
		t.Errorf("expected version 0.3.0, got %s", manifest.Version)
	}
}

func TestFetchManifest_InvalidSignature_Rejects(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil) // different key pair
	body := testManifest(t, priv)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		PubKey:         otherPub, // wrong key
		ManifestURL:    srv.URL + "/manifest.json",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	_, err := u.fetchAndVerifyManifest()
	if err == nil {
		t.Error("expected error for invalid signature")
	}
}

func TestFetchManifest_NoRedirect_Blocks302(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.com/manifest.json", http.StatusFound)
	}))
	defer srv.Close()

	pub, _, _ := ed25519.GenerateKey(nil)
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		PubKey:         pub,
		ManifestURL:    srv.URL + "/manifest.json",
		MaxRetries:     0,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	_, err := u.fetchAndVerifyManifest()
	if err == nil {
		t.Error("expected error when redirect is blocked")
	}
}

func TestFetchManifest_RetriesOnTransientError(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := testManifest(t, priv)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		PubKey:         pub,
		ManifestURL:    srv.URL + "/manifest.json",
		MaxRetries:     3,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	manifest, err := u.fetchAndVerifyManifest()
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if manifest.Version != "0.3.0" {
		t.Errorf("expected version 0.3.0, got %s", manifest.Version)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts (2 fails + 1 success), got %d", attempts)
	}
}

func TestFetchManifest_FailsAfterMaxRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	pub, _, _ := ed25519.GenerateKey(nil)
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		PubKey:         pub,
		ManifestURL:    srv.URL + "/manifest.json",
		MaxRetries:     3,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	_, err := u.fetchAndVerifyManifest()
	if err == nil {
		t.Error("expected error after max retries")
	}
	// 1 initial + 3 retries = 4 total
	if atomic.LoadInt32(&attempts) != 4 {
		t.Errorf("expected 4 total attempts, got %d", attempts)
	}
}

func TestFetchManifest_NoRetryOnClientError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	pub, _, _ := ed25519.GenerateKey(nil)
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		PubKey:         pub,
		ManifestURL:    srv.URL + "/manifest.json",
		MaxRetries:     3,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	_, err := u.fetchAndVerifyManifest()
	if err == nil {
		t.Error("expected error on 404")
	}
	// No retry on 4xx — only 1 attempt
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", attempts)
	}
}

// === F-025: Task A.8a — Download + SHA256 Verification ===

func TestDownload_SHA256Match_Passes(t *testing.T) {
	payload := []byte("hello, StonkAgents update archive\n")
	expected := sha256Hex(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "34")
		w.Write(payload)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	path, err := u.downloadArchive(context.Background(), srv.URL+"/archive.tar.gz", expected, destDir, nil)
	if err != nil {
		t.Fatalf("downloadArchive: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, payload) {
		t.Error("downloaded file contents don't match")
	}
}

func TestDownload_SHA256Mismatch_Rejects(t *testing.T) {
	payload := []byte("tampered archive contents\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	_, err := u.downloadArchive(context.Background(), srv.URL+"/archive.tar.gz", "0000000000000000000000000000000000000000000000000000000000000000", destDir, nil)
	if err == nil {
		t.Fatal("expected error for SHA256 mismatch")
	}
	// Verify temp file was cleaned up
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("expected temp file cleanup, found %d files", len(entries))
	}
}

func TestDownload_NoRedirect_Blocks302(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.com/archive.tar.gz", http.StatusFound)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		MaxRetries:     0,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	_, err := u.downloadArchive(context.Background(), srv.URL+"/archive.tar.gz", "abc123", destDir, nil)
	if err == nil {
		t.Error("expected error when redirect is blocked")
	}
	// No file should exist
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("expected no files after redirect block, found %d", len(entries))
	}
}

func TestDownload_RetriesOnTransientHTTPError(t *testing.T) {
	payload := []byte("retry-success archive\n")
	expected := sha256Hex(payload)

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		MaxRetries:     3,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	path, err := u.downloadArchive(context.Background(), srv.URL+"/archive.tar.gz", expected, destDir, nil)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, payload) {
		t.Error("downloaded file contents don't match after retry")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts (2 fails + 1 success), got %d", attempts)
	}
}

func TestDownload_NoRetryOnSHA256Mismatch(t *testing.T) {
	payload := []byte("bad hash content\n")

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Write(payload)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		MaxRetries:     3,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	_, err := u.downloadArchive(context.Background(), srv.URL+"/archive.tar.gz", "0000000000000000000000000000000000000000000000000000000000000000", destDir, nil)
	if err == nil {
		t.Error("expected error for SHA256 mismatch")
	}
	// SHA256 mismatch is a hard failure — only 1 attempt
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected 1 attempt (no retry on SHA256 mismatch), got %d", attempts)
	}
}

func TestDownload_FailsAfterMaxRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		MaxRetries:     3,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	_, err := u.downloadArchive(context.Background(), srv.URL+"/archive.tar.gz", "abc123", destDir, nil)
	if err == nil {
		t.Error("expected error after max retries")
	}
	// 1 initial + 3 retries = 4 total
	if atomic.LoadInt32(&attempts) != 4 {
		t.Errorf("expected 4 total attempts, got %d", attempts)
	}
	// Verify no leftover temp files
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("expected temp file cleanup, found %d files", len(entries))
	}
}

// === F-025: Task A.8b — URL Pinning + Cancel + Progress ===

func TestDownload_URLPinning_RejectsWrongDomain(t *testing.T) {
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: "https://releases.stonkagents.com/",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	destDir := t.TempDir()
	// URL from a different domain should be rejected before any HTTP request
	_, err := u.downloadArchive(context.Background(), "https://evil.com/0.3.0/archive.tar.gz", "abc123", destDir, nil)
	if err == nil {
		t.Error("expected error for URL pinning violation")
	}
}

func TestDownload_URLPinning_RejectsPathOutsideBase(t *testing.T) {
	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: "https://releases.stonkagents.com/releases/",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	// validateDownloadURL is the unit under test — call it directly
	err := u.validateDownloadURL("https://releases.stonkagents.com/other-path/archive.tar.gz")
	if err == nil {
		t.Error("expected URL pinning error for path outside base, got nil")
	}
}

func TestDownload_Cancel_StopsCleanly(t *testing.T) {
	// Slow server: writes 10 bytes, waits, writes more
	payload := bytes.Repeat([]byte("X"), 1024)
	expected := sha256Hex(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Write(payload[:10]) // write partial
		w.(http.Flusher).Flush()
		time.Sleep(500 * time.Millisecond) // slow — will be cancelled
		w.Write(payload[10:])
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		MaxRetries:     0,
		RetryBaseDelay: 1 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	destDir := t.TempDir()
	_, err := u.downloadArchive(ctx, srv.URL+"/0.3.0/archive.tar.gz", expected, destDir, nil)
	if err == nil {
		t.Error("expected error from cancelled download")
	}
	// Temp file should be cleaned up
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("expected temp file cleanup after cancel, found %d files", len(entries))
	}
}

func TestDownload_ProgressReporting(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 1000)
	expected := sha256Hex(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write(payload)
	}))
	defer srv.Close()

	u := NewUpdater(UpdaterConfig{
		CurrentVersion: "0.2.0",
		ReleaseBaseURL: srv.URL + "/",
		RetryBaseDelay: 1 * time.Millisecond,
	})

	var mu sync.Mutex
	var reports []int64
	progressFn := func(downloaded, total int64) {
		mu.Lock()
		reports = append(reports, downloaded)
		mu.Unlock()
	}

	destDir := t.TempDir()
	_, err := u.downloadArchive(context.Background(), srv.URL+"/0.3.0/archive.tar.gz", expected, destDir, progressFn)
	if err != nil {
		t.Fatalf("downloadArchive: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reports) == 0 {
		t.Error("expected progress reports, got none")
	}
	// Last report should equal total payload size
	last := reports[len(reports)-1]
	if last != 1000 {
		t.Errorf("expected final progress=1000, got %d", last)
	}
}
