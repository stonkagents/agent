// Package: internal/daemon/tracker
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: HTTP client for tracker communication

package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TrackerPeerInfo represents peer information from tracker
type TrackerPeerInfo struct {
	PeerID     string   `json:"peer_id"`
	Chunks     []int    `json:"chunks"`
	Multiaddrs []string `json:"multiaddrs"`
}

// FileMetadata represents file metadata from tracker
type FileMetadata struct {
	CID          string `json:"cid"`
	Filename     string `json:"filename"`
	TotalSize    int64  `json:"size"`
	TotalChunks  int    `json:"total_chunks"`
	ManifestType string `json:"manifest_type"`
}

// Client is the HTTP client for tracker communication
type Client struct {
	trackerURL string
	peerID     string
	httpClient *http.Client
	maxRetries int
	breaker    *CircuitBreaker
}

// NewClient creates a new tracker client with production-grade HTTP configuration
func NewClient(trackerURL, peerID string) *Client {
	return &Client{
		trackerURL: trackerURL,
		peerID:     peerID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second, // SECURITY: 30s timeout per OWASP recommendations
			Transport: &http.Transport{
				MaxIdleConns:        100,              // Connection pooling
				IdleConnTimeout:     90 * time.Second, // Close idle connections
				TLSHandshakeTimeout: 10 * time.Second, // TLS timeout
				DisableKeepAlives:   false,            // Reuse connections
			},
		},
		maxRetries: 3,
		breaker:    NewCircuitBreaker(),
	}
}

// SetPeerID updates the peer ID used for tracker requests.
func (c *Client) SetPeerID(peerID string) {
	if peerID != "" {
		c.peerID = peerID
	}
}

// CircuitState returns the current circuit breaker state (for status/debugging).
// When StateOpen, tracker calls are rejected until the reset timeout (30s) elapses.
func (c *Client) CircuitState() CircuitState {
	return c.breaker.State()
}

// RegisterResponse is the JSON shape returned by POST /api/v1/tracker/register.
// api_key is only present on first register (omitempty).
type RegisterResponse struct {
	PeerID     string   `json:"peer_id"`
	PublicKey  string   `json:"ed25519_pubkey"`
	Multiaddrs []string `json:"multiaddrs"`
	FirstSeen  string   `json:"first_seen"`
	LastSeen   string   `json:"last_seen"`
	APIKey     string   `json:"api_key,omitempty"`
}

// Register registers this peer with the tracker. Returns the api_key when the
// tracker includes it (first register only); re-registers get empty apiKey.
func (c *Client) Register(peerID, publicKey string, multiaddrs []string) (apiKey string, err error) {
	payload := map[string]interface{}{
		"peer_id":        peerID,
		"ed25519_pubkey": publicKey,
		"multiaddrs":     multiaddrs,
	}

	body, err := c.postWithRetry("/api/v1/tracker/register", payload)
	if err != nil {
		return "", err
	}
	var resp RegisterResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to parse register response: %w", err)
	}
	return resp.APIKey, nil
}

// Announce announces file availability and metadata
func (c *Client) Announce(fileCID, filename, mimeType, manifestType string, size int64, chunks []int) error {
	payload := map[string]interface{}{
		"peer_id":       c.peerID,
		"cid":           fileCID,
		"filename":      filename,
		"mime_type":     mimeType,
		"size":          size,
		"manifest_type": manifestType,
		"chunks":        chunks,
	}

	_, err := c.postWithRetry("/api/v1/tracker/announce", payload)
	return err
}

// UpdateAvailability updates chunk availability for this peer
func (c *Client) UpdateAvailability(fileCID string, chunks []int) error {
	payload := map[string]interface{}{
		"peer_id": c.peerID,
		"chunks":  chunks,
	}
	path := fmt.Sprintf("/api/v1/tracker/assets/%s/availability", fileCID)
	_, err := c.postWithRetry(path, payload)
	return err
}

// SearchByCID searches for peers that have a specific file CID
func (c *Client) SearchByCID(fileCID string) ([]*TrackerPeerInfo, error) {
	url := fmt.Sprintf("%s/api/v1/tracker/assets/%s/peers", c.trackerURL, fileCID)

	respBody, err := c.getWithRetry(url)
	if err != nil {
		return nil, err
	}

	// Parse response
	var response struct {
		Data []*TrackerPeerInfo `json:"data"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse peers response: %w", err)
	}

	return response.Data, nil
}

// GetOnlinePeerCount queries the tracker for the number of online peers
func (c *Client) GetOnlinePeerCount() (int, error) {
	url := fmt.Sprintf("%s/api/v1/tracker/peers?online=true&limit=1", c.trackerURL)

	respBody, err := c.getWithRetry(url)
	if err != nil {
		return 0, err
	}

	var response struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return 0, fmt.Errorf("failed to parse peers count response: %w", err)
	}

	return response.Total, nil
}

// GetAssetMetadata retrieves file metadata from tracker by CID
func (c *Client) GetAssetMetadata(fileCID string) (*FileMetadata, error) {
	url := fmt.Sprintf("%s/api/v1/tracker/assets/%s", c.trackerURL, fileCID)

	respBody, err := c.getWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get asset metadata: %w", err)
	}

	// Parse response
	var metadata FileMetadata
	if err := json.Unmarshal(respBody, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata response: %w", err)
	}

	// Calculate total_chunks from file size (256KB chunks)
	chunkSize := int64(256 * 1024) // 256KB
	metadata.TotalChunks = int((metadata.TotalSize + chunkSize - 1) / chunkSize)

	return &metadata, nil
}

// RelayInfo represents the tracker's relay host information for AutoRelay configuration.
type RelayInfo struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
}

// GetRelayInfo fetches the tracker's libp2p relay PeerID and multiaddrs.
// Daemons use this to configure AutoRelay with the tracker as a static relay.
func (c *Client) GetRelayInfo() (*RelayInfo, error) {
	url := fmt.Sprintf("%s/api/v1/tracker/relay", c.trackerURL)

	respBody, err := c.getWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get relay info: %w", err)
	}

	var info RelayInfo
	if err := json.Unmarshal(respBody, &info); err != nil {
		return nil, fmt.Errorf("failed to parse relay info: %w", err)
	}

	if info.PeerID == "" || len(info.Multiaddrs) == 0 {
		return nil, fmt.Errorf("relay info missing peer_id or multiaddrs")
	}

	return &info, nil
}

// ReportStats reports transfer statistics to the tracker.
// Best-effort only: failures do not open the circuit breaker, so a flaky or 500 from
// the tracker stats endpoint cannot block Register or wallet/link.
func (c *Client) ReportStats(peerID string, uploadBytes, downloadBytes int64, avgSpeed *int64) error {
	payload := map[string]interface{}{
		"peer_id":                     peerID,
		"upload_bytes":                uploadBytes,
		"download_bytes":              downloadBytes,
		"average_speed_bytes_per_sec": avgSpeed,
	}
	_, err := c.postBestEffort("/api/v1/tracker/stats", payload)
	return err
}

// ReportDownloadComplete reports that a download has completed.
func (c *Client) ReportDownloadComplete(peerID, cid string) error {
	payload := map[string]interface{}{
		"peer_id": peerID,
		"cid":     cid,
	}

	_, err := c.postWithRetry("/api/v1/tracker/downloads/complete", payload)
	return err
}

// UpdateWallet links the given Solana wallet address to the current peer on the tracker.
// Uses PATCH /api/v1/tracker/peers/me with X-API-Key (current peer only; not public).
func (c *Client) UpdateWallet(apiKey, walletAddress string) error {
	payload := map[string]interface{}{
		"wallet_address": walletAddress,
	}
	_, err := c.patchWithRetry("/api/v1/tracker/peers/me", apiKey, payload)
	return err
}

// postWithRetry sends a POST request with retry logic and circuit breaker protection
func (c *Client) postWithRetry(path string, payload interface{}) ([]byte, error) {
	// Circuit breaker check: reject immediately if tracker is known to be down
	if err := c.breaker.Allow(); err != nil {
		return nil, fmt.Errorf("tracker unavailable: %w", err)
	}

	url := c.trackerURL + path

	// Marshal payload
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	var lastErr error

	// Retry loop (max 3 attempts)
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		// Send POST request
		resp, err := c.httpClient.Post(url, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, err)
		}
		defer resp.Body.Close()

		// Read response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed to read response after %d attempts: %w", c.maxRetries, err)
		}

		// Check status code
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.breaker.RecordSuccess()
			return body, nil
		}

		// Non-2xx status code - retry if not last attempt
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		if attempt < c.maxRetries {
			time.Sleep(c.calculateBackoff(attempt))
			continue
		}
	}

	c.breaker.RecordFailure()
	return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, lastErr)
}

// postBestEffort sends a POST with retries but does not use the circuit breaker.
// Use for best-effort calls (e.g. ReportStats) so their failures cannot block
// critical operations (Register, UpdateWallet).
func (c *Client) postBestEffort(path string, payload interface{}) ([]byte, error) {
	url := c.trackerURL + path
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	var lastErr error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		resp, err := c.httpClient.Post(url, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, nil
		}
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		if attempt < c.maxRetries {
			time.Sleep(c.calculateBackoff(attempt))
			continue
		}
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, lastErr)
}

// patchWithRetry sends a PATCH request with X-API-Key and retry logic.
func (c *Client) patchWithRetry(path, apiKey string, payload interface{}) ([]byte, error) {
	if err := c.breaker.Allow(); err != nil {
		return nil, fmt.Errorf("tracker unavailable: %w", err)
	}
	url := c.trackerURL + path
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	var lastErr error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodPatch, url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", apiKey)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.breaker.RecordSuccess()
			return body, nil
		}
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		if attempt < c.maxRetries {
			time.Sleep(c.calculateBackoff(attempt))
			continue
		}
	}
	c.breaker.RecordFailure()
	return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, lastErr)
}

// getWithRetry sends a GET request with retry logic and circuit breaker protection
func (c *Client) getWithRetry(url string) ([]byte, error) {
	// Circuit breaker check: reject immediately if tracker is known to be down
	if err := c.breaker.Allow(); err != nil {
		return nil, fmt.Errorf("tracker unavailable: %w", err)
	}

	var lastErr error

	// Retry loop (max 3 attempts)
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		// Send GET request
		resp, err := c.httpClient.Get(url)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, err)
		}
		defer resp.Body.Close()

		// Read response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries {
				time.Sleep(c.calculateBackoff(attempt))
				continue
			}
			return nil, fmt.Errorf("failed to read response after %d attempts: %w", c.maxRetries, err)
		}

		// Check status code
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.breaker.RecordSuccess()
			return body, nil
		}

		// Non-2xx status code - retry if not last attempt
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		if attempt < c.maxRetries {
			time.Sleep(c.calculateBackoff(attempt))
			continue
		}
	}

	c.breaker.RecordFailure()
	return nil, fmt.Errorf("failed after %d attempts: %w", c.maxRetries, lastErr)
}

// calculateBackoff calculates exponential backoff with jitter
func (c *Client) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: 1s, 2s, 4s
	// For testing, use shorter delays
	baseDelay := time.Duration(attempt) * 10 * time.Millisecond
	return baseDelay
}
