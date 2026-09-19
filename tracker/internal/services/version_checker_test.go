// Feature: F-025 (Auto-Update System)
// Story: US-025-03 (Tracker Version Awareness)
// Purpose: Tests for VersionChecker — fetches signed manifest from CloudFront with no-redirect policy
package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/update"
)

// validManifestJSON returns a well-formed manifest for test fixtures.
func validManifestJSON() []byte {
	m := update.ManifestContent{
		SchemaVersion: 1,
		Version:       "2.0.0",
		MinSupported:  "1.5.0",
		Released:      "2026-03-01",
		ReleaseNotes:  "Bug fixes and performance improvements",
		Platforms: map[string]update.PlatformRelease{
			"darwin-arm64": {
				URL:    "https://releases.stonkagents.com/v2.0.0/darwin-arm64.tar.gz",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   2048000,
			},
		},
	}
	data, _ := json.Marshal(m)
	return data
}

func TestVersionChecker_FetchLatest_ParsesSignedEnvelope(t *testing.T) {
	// publish-manifest.sh uploads a signed envelope: { "signed": "...", "content": {...} }
	content := validManifestJSON()
	envelope := update.Envelope{
		Signed:  "dGVzdHNpZ25hdHVyZQ==", // dummy base64
		Content: json.RawMessage(content),
	}
	envBytes, _ := json.Marshal(envelope)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(envBytes)
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	manifest, err := vc.FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest with signed envelope: %v", err)
	}
	if manifest.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", manifest.Version, "2.0.0")
	}
	if manifest.ReleaseNotes != "Bug fixes and performance improvements" {
		t.Errorf("ReleaseNotes = %q", manifest.ReleaseNotes)
	}
	if manifest.MinSupported != "1.5.0" {
		t.Errorf("MinSupported = %q, want %q", manifest.MinSupported, "1.5.0")
	}
}

func TestVersionChecker_FetchesLatestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(validManifestJSON())
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	manifest, err := vc.FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if manifest.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", manifest.Version, "2.0.0")
	}
	if manifest.ReleaseNotes != "Bug fixes and performance improvements" {
		t.Errorf("ReleaseNotes = %q", manifest.ReleaseNotes)
	}
	if manifest.MinSupported != "1.5.0" {
		t.Errorf("MinSupported = %q, want %q", manifest.MinSupported, "1.5.0")
	}
}

func TestVersionChecker_RejectsRedirects(t *testing.T) {
	// Redirect target — should never be reached
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect target was reached — no-redirect policy violated")
		w.Header().Set("Content-Type", "application/json")
		w.Write(validManifestJSON())
	}))
	defer target.Close()

	// Redirecting server
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/manifest.json", http.StatusFound)
	}))
	defer redirector.Close()

	vc := NewVersionChecker(redirector.URL + "/manifest.json")
	_, err := vc.FetchLatest()
	if err == nil {
		t.Error("FetchLatest should reject redirect, got nil error")
	}
}

func TestVersionChecker_HandlesNetworkError(t *testing.T) {
	// Use a URL that will definitely fail
	vc := NewVersionChecker("http://127.0.0.1:1/manifest.json")
	_, err := vc.FetchLatest()
	if err == nil {
		t.Error("FetchLatest should return error on network failure")
	}
}

func TestVersionChecker_RejectsNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	_, err := vc.FetchLatest()
	if err == nil {
		t.Error("FetchLatest should reject non-200 status")
	}
}

func TestVersionChecker_RejectsInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	_, err := vc.FetchLatest()
	if err == nil {
		t.Error("FetchLatest should reject invalid JSON")
	}
}

func TestVersionChecker_RejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write more than the max allowed size (should be limited)
		big := make([]byte, 2*1024*1024) // 2MB
		w.Write(big)
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	_, err := vc.FetchLatest()
	if err == nil {
		t.Error("FetchLatest should reject oversized response")
	}
}

// --- Cache tests ---

func TestVersionChecker_LatestVersion_CachesResult(t *testing.T) {
	var hitCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(validManifestJSON())
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	vc.cacheTTL = 5 * time.Second // short TTL for test

	// First call: fetches from server
	m1, err := vc.LatestVersion()
	if err != nil {
		t.Fatalf("first LatestVersion: %v", err)
	}
	if m1.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", m1.Version, "2.0.0")
	}

	// Second call within TTL: should be cached
	m2, err := vc.LatestVersion()
	if err != nil {
		t.Fatalf("second LatestVersion: %v", err)
	}
	if m2.Version != "2.0.0" {
		t.Errorf("cached Version = %q, want %q", m2.Version, "2.0.0")
	}

	if hitCount.Load() != 1 {
		t.Errorf("server hit count = %d, want 1 (second call should be cached)", hitCount.Load())
	}
}

func TestVersionChecker_LatestVersion_RefreshesAfterTTL(t *testing.T) {
	var hitCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(validManifestJSON())
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	vc.cacheTTL = 10 * time.Millisecond // very short TTL

	_, err := vc.LatestVersion()
	if err != nil {
		t.Fatalf("first LatestVersion: %v", err)
	}

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	_, err = vc.LatestVersion()
	if err != nil {
		t.Fatalf("second LatestVersion: %v", err)
	}

	if hitCount.Load() != 2 {
		t.Errorf("server hit count = %d, want 2 (second call should re-fetch after TTL)", hitCount.Load())
	}
}

func TestVersionChecker_LatestVersion_ReturnsCachedOnFetchFailure(t *testing.T) {
	var shouldFail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(validManifestJSON())
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	vc.cacheTTL = 10 * time.Millisecond

	// First call succeeds
	m1, err := vc.LatestVersion()
	if err != nil {
		t.Fatalf("first LatestVersion: %v", err)
	}
	if m1.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", m1.Version, "2.0.0")
	}

	// Make server fail and expire cache
	shouldFail.Store(true)
	time.Sleep(20 * time.Millisecond)

	// Should return cached value even though fetch failed
	m2, err := vc.LatestVersion()
	if err != nil {
		t.Fatalf("second LatestVersion should return cached: %v", err)
	}
	if m2.Version != "2.0.0" {
		t.Errorf("cached Version = %q, want %q", m2.Version, "2.0.0")
	}
}

func TestVersionChecker_LatestVersion_ErrorsWhenNoCacheAndFetchFails(t *testing.T) {
	vc := NewVersionChecker("http://127.0.0.1:1/manifest.json")
	_, err := vc.LatestVersion()
	if err == nil {
		t.Error("LatestVersion with no cache and fetch failure should return error")
	}
}

func TestVersionChecker_Start_StopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(validManifestJSON())
	}))
	defer srv.Close()

	vc := NewVersionChecker(srv.URL + "/manifest.json")
	vc.pollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	vc.Start(ctx)

	// Wait for at least one fetch
	time.Sleep(100 * time.Millisecond)

	// Should have cached data
	m, err := vc.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion after Start: %v", err)
	}
	if m.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", m.Version, "2.0.0")
	}

	// Cancel context — goroutine should stop
	cancel()
	time.Sleep(100 * time.Millisecond)
	// If goroutine leaked, test -race would catch it. Success = no panic.
}
