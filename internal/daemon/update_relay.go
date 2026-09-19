// Package: internal/daemon
// Feature: F-025 (Auto-Update System)
// Story: US-025-04 (Daemon Update Relay)
// Purpose: Daemon → controller update notification relay.
//          When the tracker heartbeat reports a newer version, the daemon
//          POSTs to the controller's /update/notify endpoint so the
//          controller can begin its update flow.

package daemon

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// notifyControllerUpdate sends a POST to the controller with the latest version
// and release notes. It debounces by version (skips if same version already notified),
// skips if latestVersion is empty, and retries once on failure.
func (s *Server) notifyControllerUpdate(latestVersion, releaseNotes string) {
	if latestVersion == "" {
		return
	}

	// Debounce: skip if we already notified for this version.
	if s.lastNotifiedVersion == latestVersion {
		return
	}

	url := s.controllerURL + "/update/notify"
	payload, _ := json.Marshal(map[string]string{
		"latest_version": latestVersion,
		"release_notes":  releaseNotes,
	})

	const maxAttempts = 2
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if s.doControllerNotify(url, payload) {
			s.lastNotifiedVersion = latestVersion
			return
		}
		if attempt < maxAttempts {
			time.Sleep(50 * time.Millisecond) // brief pause before retry
		}
	}
	// Both attempts failed — do NOT record version so next heartbeat retries.
}

// doControllerNotify sends a single POST to the controller. Returns true on 2xx.
func (s *Server) doControllerNotify(url string, payload []byte) bool {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", generateRequestID())

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// generateRequestID creates a short random hex string for correlation.
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
