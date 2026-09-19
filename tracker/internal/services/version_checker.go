// Feature: F-025 (Auto-Update System)
// Story: US-025-03 (Tracker Version Awareness)
// Purpose: Periodically fetches the signed release manifest from CloudFront and caches the latest version
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stonkagents/agent/internal/update"
)

// maxManifestSize is the maximum response body size for manifest fetches (1MB).
// Prevents OOM from oversized or malicious upstream responses.
const maxManifestSize = 1 * 1024 * 1024

// defaultCacheTTL is the default cache duration for fetched manifests.
const defaultCacheTTL = 15 * time.Minute

// defaultPollInterval is the default interval for background version checks.
const defaultPollInterval = 15 * time.Minute

// VersionChecker fetches and caches the latest release manifest from a remote URL.
// The manifest URL is injected via constructor (DI). HTTP redirects are blocked as
// a defense-in-depth measure against SSRF/redirect attacks on the manifest endpoint.
type VersionChecker struct {
	manifestURL  string
	client       *http.Client
	cacheTTL     time.Duration
	pollInterval time.Duration

	mu             sync.RWMutex
	cachedManifest *update.ManifestContent
	cachedAt       time.Time
}

// NewVersionChecker creates a VersionChecker for the given manifest URL.
// The HTTP client blocks redirects and enforces a 10-second timeout.
func NewVersionChecker(manifestURL string) *VersionChecker {
	return &VersionChecker{
		manifestURL:  manifestURL,
		cacheTTL:     defaultCacheTTL,
		pollInterval: defaultPollInterval,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// LatestVersion returns the cached manifest if within TTL, otherwise fetches fresh.
// On fetch failure with an existing cache, returns the stale cached value (last-known-good).
// Returns error only when there is no cache AND the fetch fails.
func (vc *VersionChecker) LatestVersion() (*update.ManifestContent, error) {
	vc.mu.RLock()
	cached := vc.cachedManifest
	cachedAt := vc.cachedAt
	vc.mu.RUnlock()

	if cached != nil && time.Since(cachedAt) < vc.cacheTTL {
		return cached, nil
	}

	// Cache expired or empty — try to fetch
	manifest, err := vc.FetchLatest()
	if err != nil {
		// Fetch failed — return stale cache if available
		if cached != nil {
			slog.Warn("[VersionChecker.LatestVersion] fetch failed, returning stale cache", "error", err)
			return cached, nil
		}
		return nil, fmt.Errorf("no cached manifest and fetch failed: %w", err)
	}

	vc.mu.Lock()
	vc.cachedManifest = manifest
	vc.cachedAt = time.Now()
	vc.mu.Unlock()

	return manifest, nil
}

// Start launches a background goroutine that periodically fetches the manifest.
// The goroutine stops when ctx is cancelled. On fetch failure, the previous cached
// value is retained (last-known-good pattern).
func (vc *VersionChecker) Start(ctx context.Context) {
	// Initial fetch
	if manifest, err := vc.FetchLatest(); err != nil {
		slog.Warn("[VersionChecker.Start] initial fetch failed", "error", err)
	} else {
		vc.mu.Lock()
		vc.cachedManifest = manifest
		vc.cachedAt = time.Now()
		vc.mu.Unlock()
	}

	go func() {
		ticker := time.NewTicker(vc.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				manifest, err := vc.FetchLatest()
				if err != nil {
					slog.Warn("[VersionChecker.poll] fetch failed, keeping previous cache", "error", err)
					continue
				}
				vc.mu.Lock()
				vc.cachedManifest = manifest
				vc.cachedAt = time.Now()
				vc.mu.Unlock()
			}
		}
	}()
}

// FetchLatest fetches the manifest from the configured URL, parses it, and returns
// the ManifestContent. Returns an error if the fetch fails, returns a non-200 status,
// the response is too large, or the JSON is invalid.
func (vc *VersionChecker) FetchLatest() (*update.ManifestContent, error) {
	resp, err := vc.client.Get(vc.manifestURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	// Reject redirects (client returns 3xx as-is due to CheckRedirect policy)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("manifest fetch returned redirect %d; redirects are blocked", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest fetch returned status %d", resp.StatusCode)
	}

	// Limit response body to prevent OOM
	limited := io.LimitReader(resp.Body, maxManifestSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read manifest body: %w", err)
	}
	if len(body) > maxManifestSize {
		return nil, fmt.Errorf("manifest response exceeds %d bytes", maxManifestSize)
	}

	// Try signed envelope first (publish-manifest.sh uploads { signed, content })
	var envelope update.Envelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Signed != "" && len(envelope.Content) > 0 {
		var manifest update.ManifestContent
		if err := json.Unmarshal(envelope.Content, &manifest); err != nil {
			return nil, fmt.Errorf("parse manifest content from envelope: %w", err)
		}
		return &manifest, nil
	}

	// Fallback: direct ManifestContent (backward compat with unsigned manifests)
	var manifest update.ManifestContent
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest JSON: %w", err)
	}

	return &manifest, nil
}
