// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: Manifest fetch with retry + Ed25519 verification. No-redirect HTTP client
//          prevents MITM via CDN redirect. Exponential backoff with jitter on transient errors.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/update"
)

// noRedirectClient creates an HTTP client that refuses redirects.
// Prevents MITM via CDN redirect to a different manifest URL.
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are not allowed for manifest fetch")
		},
	}
}

// fetchAndVerifyManifest fetches the manifest from manifestURL, verifies Ed25519
// signature, and returns the parsed ManifestContent. Retries on 5xx with exponential
// backoff; does NOT retry on 4xx (client errors).
func (u *Updater) fetchAndVerifyManifest() (*update.ManifestContent, error) {
	rawBytes, err := u.fetchManifestWithRetry()
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}

	var envelope update.Envelope
	if err := json.Unmarshal(rawBytes, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal manifest envelope: %w", err)
	}

	manifest, err := update.VerifyManifest(envelope, u.pubKey)
	if err != nil {
		return nil, fmt.Errorf("verify manifest: %w", err)
	}

	return manifest, nil
}

// fetchManifestWithRetry performs an HTTP GET with exponential backoff retry on 5xx.
func (u *Updater) fetchManifestWithRetry() ([]byte, error) {
	client := noRedirectClient()
	maxAttempts := 1 + u.maxRetries // initial + retries

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := u.retryDelay(attempt)
			u.logger.Warn("[Updater.fetchManifest] retry",
				"attempt", attempt+1, "delay", delay, "err", lastErr)
			time.Sleep(delay)
		}

		body, err, retryable := u.doFetchManifest(client)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable {
			return nil, lastErr // 4xx — no retry
		}
	}

	return nil, fmt.Errorf("manifest fetch failed after %d attempts: %w", maxAttempts, lastErr)
}

// doFetchManifest performs a single HTTP GET. Returns (body, error, retryable).
func (u *Updater) doFetchManifest(client *http.Client) ([]byte, error, bool) {
	resp, err := client.Get(u.manifestURL)
	if err != nil {
		return nil, err, false // redirect block, DNS, etc. — not retryable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("server error: HTTP %d", resp.StatusCode), true
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: HTTP %d", resp.StatusCode), false
	}

	// 4MB limit — manifests are small JSON
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err), true
	}

	return body, nil, false
}

// selectPlatform returns the PlatformRelease for the current OS/arch.
func selectPlatform(manifest *update.ManifestContent) (*update.PlatformRelease, error) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	plat, ok := manifest.Platforms[key]
	if !ok {
		return nil, fmt.Errorf("unsupported platform: %s", key)
	}
	return &plat, nil
}

// isForceUpdate returns true if currentVersion is below the manifest's min_supported.
func isForceUpdate(manifest *update.ManifestContent, currentVersion string) bool {
	// If current is eligible for an upgrade to min_supported, it means current < min_supported.
	return update.VersionEligible(currentVersion, manifest.MinSupported)
}

// downloadArchive fetches the archive from url, verifies SHA256, and writes to destDir.
// Returns the path to the verified file. Retries on transient HTTP errors (5xx);
// does NOT retry on SHA256 mismatch or redirect block (both are hard failures).
// progressFn (optional) is called with (bytesDownloaded, totalSize) during the download.
func (u *Updater) downloadArchive(ctx context.Context, archiveURL, expectedSHA256, destDir string, progressFn func(downloaded, total int64)) (string, error) {
	// URL pinning: reject if host/scheme doesn't match configured release base URL
	if err := u.validateDownloadURL(archiveURL); err != nil {
		return "", err
	}

	client := noRedirectClient()
	maxAttempts := 1 + u.maxRetries

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := u.retryDelay(attempt)
			u.logger.Warn("[Updater.downloadArchive] retry",
				"attempt", attempt+1, "delay", delay, "err", lastErr)
			time.Sleep(delay)
		}

		path, err, retryable := u.doDownloadArchive(ctx, client, archiveURL, expectedSHA256, destDir, progressFn)
		if err == nil {
			return path, nil
		}
		lastErr = err
		if !retryable {
			return "", lastErr
		}
	}

	return "", fmt.Errorf("download failed after %d attempts: %w", maxAttempts, lastErr)
}

// validateDownloadURL checks that the archive URL starts with the configured release base URL.
// Full prefix match (scheme + host + path), not just scheme + host.
func (u *Updater) validateDownloadURL(archiveURL string) error {
	base := strings.TrimRight(u.releaseBaseURL, "/")
	if !strings.HasPrefix(archiveURL, base) {
		return fmt.Errorf("URL pinning violation: %s does not match base %s", archiveURL, u.releaseBaseURL)
	}
	return nil
}

// doDownloadArchive performs a single download attempt. Returns (path, error, retryable).
func (u *Updater) doDownloadArchive(ctx context.Context, client *http.Client, archiveURL, expectedSHA256, destDir string, progressFn func(downloaded, total int64)) (string, error, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err), false
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err, false // redirect block, DNS, cancel — not retryable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("server error: HTTP %d", resp.StatusCode), true
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: HTTP %d", resp.StatusCode), false
	}

	// Create temp file in destDir
	tmpFile, err := os.CreateTemp(destDir, "stonkagents-update-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err), false
	}
	tmpPath := tmpFile.Name()

	// Hash on-the-fly via TeeReader
	hasher := sha256.New()
	reader := io.TeeReader(resp.Body, hasher)

	// Wrap with progress-reporting writer if callback provided
	var dst io.Writer = tmpFile
	if progressFn != nil {
		dst = &progressWriter{
			w:          tmpFile,
			total:      resp.ContentLength,
			progressFn: progressFn,
		}
	}

	// 500MB limit for archive downloads
	if _, err := io.Copy(dst, io.LimitReader(reader, 500*1024*1024)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write archive: %w", err), true // network error during copy — retryable
	}
	tmpFile.Close()

	// Verify SHA256
	computed := hex.EncodeToString(hasher.Sum(nil))
	if computed != expectedSHA256 {
		os.Remove(tmpPath)
		return "", fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, computed), false // hard failure
	}

	// Rename to final path
	finalPath := filepath.Join(destDir, "stonkagents-update.tar.gz")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename temp file: %w", err), false
	}

	return finalPath, nil, false
}

// progressWriter wraps an io.Writer and reports download progress.
type progressWriter struct {
	w          io.Writer
	downloaded int64
	total      int64
	progressFn func(downloaded, total int64)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.downloaded += int64(n)
	pw.progressFn(pw.downloaded, pw.total)
	return n, err
}

// retryDelay computes exponential backoff with jitter.
func (u *Updater) retryDelay(attempt int) time.Duration {
	base := u.retryBaseDelay
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	// Add jitter: ±50% of base
	jitter := time.Duration(rand.Int63n(int64(base))) - base/2
	delay := base + jitter
	if delay < 0 {
		delay = u.retryBaseDelay
	}
	return delay
}
