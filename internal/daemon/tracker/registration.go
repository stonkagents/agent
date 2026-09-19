// Package: internal/daemon/tracker
// Feature: sprint-02
// Story: TD-016 (Daemon registration migration)
// Purpose: Challenge-response registration and authenticated heartbeat for daemon↔tracker

package tracker

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// signDomainPrefix must match tracker/internal/services.SignDomainPrefix.
const signDomainPrefix = "stonkagents-register-v1:"

// ChallengeResponseDTO is the JSON shape returned by POST /api/v1/tracker/challenge.
type ChallengeResponseDTO struct {
	Nonce     string `json:"nonce"`
	ExpiresIn int    `json:"expires_in"`
}

// IdentityResponseDTO is the JSON shape returned by POST /api/v1/tracker/register/identity.
type IdentityResponseDTO struct {
	APIKey    string        `json:"api_key,omitempty"`
	AccountID string        `json:"account_id"`
	Credits   CreditSummary `json:"credits"`
}

// CreditSummary shows free/paid credit balances from registration.
type CreditSummary struct {
	Free int `json:"free"`
	Paid int `json:"paid"`
}

// SignChallenge signs a challenge nonce for tracker registration.
// privateKeyB64 is a base64-encoded 64-byte Ed25519 private key.
// nonceB64 is the base64-encoded nonce from the challenge response.
// Returns base64-encoded public key and signature.
func SignChallenge(privateKeyB64, nonceB64 string) (pubkeyB64, signatureB64 string, err error) {
	privKeyBytes, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return "", "", fmt.Errorf("decode private key: %w", err)
	}
	if len(privKeyBytes) != ed25519.PrivateKeySize {
		return "", "", fmt.Errorf("invalid private key size: got %d, want %d", len(privKeyBytes), ed25519.PrivateKeySize)
	}

	privKey := ed25519.PrivateKey(privKeyBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)

	message := []byte(signDomainPrefix + nonceB64)
	signature := ed25519.Sign(privKey, message)

	return base64.StdEncoding.EncodeToString(pubKey),
		base64.StdEncoding.EncodeToString(signature), nil
}

// Challenge requests a registration challenge nonce from the tracker.
// POST /api/v1/tracker/challenge → {nonce, expires_in}
func (c *Client) Challenge(peerID string) (*ChallengeResponseDTO, error) {
	payload := map[string]interface{}{"peer_id": peerID}
	body, err := c.postWithRetry("/api/v1/tracker/challenge", payload)
	if err != nil {
		return nil, err
	}
	var resp ChallengeResponseDTO
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse challenge response: %w", err)
	}
	return &resp, nil
}

// RegisterIdentity completes challenge-response registration with the tracker.
// POST /api/v1/tracker/register/identity → {api_key, account_id, credits}
func (c *Client) RegisterIdentity(peerID, pubkeyB64, signatureB64 string, multiaddrs []string, clientVersion string, displayName string) (*IdentityResponseDTO, error) {
	payload := map[string]interface{}{
		"peer_id":        peerID,
		"ed25519_pubkey": pubkeyB64,
		"signature":      signatureB64,
		"multiaddrs":     multiaddrs,
		"client_version": clientVersion,
	}
	if displayName != "" {
		payload["display_name"] = displayName
	}
	body, err := c.postWithRetry("/api/v1/tracker/register/identity", payload)
	if err != nil {
		return nil, err
	}
	var resp IdentityResponseDTO
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse register identity response: %w", err)
	}
	return &resp, nil
}

// HeartbeatResponse is the parsed response from POST /api/v1/tracker/heartbeat.
// F-025: LatestVersion and ReleaseNotes are conditionally set by the tracker
// when the client's version is older than the latest release.
type HeartbeatResponse struct {
	Status               string `json:"status"`
	NextHeartbeatSeconds int    `json:"next_heartbeat_seconds"`
	LatestVersion        string `json:"latest_version,omitempty"`
	ReleaseNotes         string `json:"release_notes,omitempty"`
	// DisplayName is the name the tracker holds for this peer (F-032), absent on
	// trackers that predate it.
	DisplayName string `json:"display_name,omitempty"`
}

// Heartbeat sends an authenticated heartbeat to the tracker.
// POST /api/v1/tracker/heartbeat with X-API-Key header.
// Returns the parsed response including optional update fields (F-025).
// autopilotCategories is the owner's autopilot trigger list (board phase 2
// request routing); nil leaves the field out, which the tracker reads as
// "autopilot off, no routing bonus".
func (c *Client) Heartbeat(apiKey string, multiaddrs []string, clientVersion string, displayName string, autopilotCategories []string) (*HeartbeatResponse, error) {
	payload := map[string]interface{}{
		"multiaddrs":     multiaddrs,
		"client_version": clientVersion,
	}
	if displayName != "" {
		payload["display_name"] = displayName
	}
	if autopilotCategories != nil {
		payload["autopilot_categories"] = autopilotCategories
	}
	body, err := c.postWithAPIKey("/api/v1/tracker/heartbeat", apiKey, payload)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Data HeartbeatResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse heartbeat response: %w", err)
	}
	return &envelope.Data, nil
}

// SetDisplayName pushes the current display name to the tracker right away (the
// portal's Identity tab saved it) through the heartbeat endpoint. Unlike Heartbeat
// the field is always sent (the daemon never has an empty name: clearing resets to
// the generated placeholder); multiaddrs are omitted, which the tracker treats as
// unchanged. The tracker keeps a name it already holds when the incoming one is a
// placeholder.
func (c *Client) SetDisplayName(apiKey, clientVersion, displayName string) error {
	payload := map[string]interface{}{
		"client_version": clientVersion,
		"display_name":   displayName,
	}
	_, err := c.postWithAPIKey("/api/v1/tracker/heartbeat", apiKey, payload)
	return err
}

// postWithAPIKey sends a POST request with X-API-Key header and retry logic.
// Follows the same circuit-breaker + retry pattern as patchWithRetry.
func (c *Client) postWithAPIKey(path, apiKey string, payload interface{}) ([]byte, error) {
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
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
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
