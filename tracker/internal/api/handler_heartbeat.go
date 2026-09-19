// Package: tracker/internal/api
// Feature: F-025 (Auto-Update System)
// Story: US-025-03 (Tracker Version Awareness)
// Purpose: POST /api/v1/tracker/heartbeat — daemon sends periodic heartbeats to keep presence alive;
//          conditionally includes update notification fields when a newer version is available

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/stonkagents/agent/internal/update"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// VersionProvider returns the latest cached release manifest.
// Implemented by services.VersionChecker; abstracted for testability.
type VersionProvider interface {
	LatestVersion() (*update.ManifestContent, error)
}

const (
	// heartbeatPresenceTTL is how long a peer stays "online" after a heartbeat.
	heartbeatPresenceTTL = 5 * time.Minute

	// heartbeatNextSeconds tells the client when to send the next heartbeat.
	// Set to TTL/2.5 so two missed heartbeats still keep the peer online.
	heartbeatNextSeconds = 120
)

// HeartbeatDTO is the request body for POST /api/v1/tracker/heartbeat.
type HeartbeatDTO struct {
	Multiaddrs    []string `json:"multiaddrs"`
	ClientVersion string   `json:"client_version"`
	// DisplayName is the daemon's current name (F-032). Absent = unchanged; present and
	// empty = explicit clear (the daemon's identity reset sends it); a daemon placeholder
	// (swarm_operator_42) never overwrites a stored real name (models.ResolveDisplayName).
	DisplayName *string `json:"display_name,omitempty"`
	// AutopilotCategories are the post categories the daemon's Autopilot answers (phase 2
	// request routing). Absent = the peer gets no category bonus (its stored list is cleared);
	// present = stored as sent (unknown categories dropped, at most MaxAutopilotCategories).
	AutopilotCategories *[]string `json:"autopilot_categories,omitempty"`
}

// sanitizeAutopilotCategories lowercases, trims, drops unknown and duplicate categories and
// caps the list at models.MaxAutopilotCategories.
func sanitizeAutopilotCategories(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] || !boardCategoryValid(c) {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) == models.MaxAutopilotCategories {
			break
		}
	}
	return out
}

// storeAutopilotCategories records (or clears) the peer's autopilot categories; failures are
// logged, the heartbeat stands. A heartbeat without the field clears the row once: the
// handler remembers which peers are known to have no row (autopilotRows), so daemons that
// never send the field (older than 2.4.0, or autopilot off) cost one DELETE per tracker
// process, not one every two minutes.
func (h *PeerHandler) storeAutopilotCategories(r *http.Request, peerID string, categories *[]string) {
	if h.autopilotRepo == nil {
		return
	}
	if categories == nil {
		if hasRow, known := h.autopilotRows.Load(peerID); known && !hasRow.(bool) {
			return
		}
		if err := h.autopilotRepo.Clear(r.Context(), peerID); err != nil {
			slog.Warn("[heartbeat] autopilot categories not cleared", "peer", peerID, "error", err)
			return
		}
		h.autopilotRows.Store(peerID, false)
		return
	}
	if err := h.autopilotRepo.Set(r.Context(), peerID, sanitizeAutopilotCategories(*categories), time.Now()); err != nil {
		slog.Warn("[heartbeat] autopilot categories not stored", "peer", peerID, "error", err)
		return
	}
	h.autopilotRows.Store(peerID, true)
}

// HandleHeartbeat handles POST /api/v1/tracker/heartbeat. Requires X-API-Key.
// Updates the peer's presence (last_seen) and returns the next heartbeat interval.
func (h *PeerHandler) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto HeartbeatDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	// Sanitize display_name: strip HTML, trim whitespace, truncate to 50 chars
	if dto.DisplayName != nil {
		clean := sanitizeDisplayName(*dto.DisplayName)
		dto.DisplayName = &clean
	}
	dto.Multiaddrs = normalizeHeartbeatMultiaddrs(dto.Multiaddrs)
	// Inject observed public IP so NAT'd peers stay reachable across heartbeats
	dto.Multiaddrs = injectObservedAddr(r, peerID, dto.Multiaddrs)

	storedName, err := h.service.HeartbeatWithMetadata(r.Context(), peerID, dto.Multiaddrs, dto.DisplayName)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update peer heartbeat")
		return
	}

	h.storeAutopilotCategories(r, peerID, dto.AutopilotCategories)

	// Backward-safe fallback if service was configured without presence wiring.
	if h.presenceStore != nil {
		if err := h.presenceStore.Heartbeat(r.Context(), peerID, heartbeatPresenceTTL); err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update presence")
			return
		}
	}

	respData := map[string]interface{}{
		"status":                 "ok",
		"next_heartbeat_seconds": heartbeatNextSeconds,
	}
	// F-032: the public name after this heartbeat, so a daemon still on its
	// placeholder can adopt a name that was set tracker-side.
	if storedName != "" {
		respData["display_name"] = storedName
	}

	// F-025: Conditionally include update fields when a newer version is available.
	// Only included when: (1) client reported its version, (2) version checker is configured,
	// (3) version checker returns a manifest, and (4) client is older than latest.
	if dto.ClientVersion != "" && h.versionChecker != nil {
		if manifest, err := h.versionChecker.LatestVersion(); err == nil && manifest != nil {
			if update.VersionEligible(dto.ClientVersion, manifest.Version) {
				respData["latest_version"] = manifest.Version
				respData["release_notes"] = manifest.ReleaseNotes
			}
		}
	}

	SendJSON(w, http.StatusOK, DataEnvelope{Data: respData})
}

func normalizeHeartbeatMultiaddrs(addrs []string) []string {
	if len(addrs) == 0 {
		return nil
	}
	const maxAddrs = 64
	out := make([]string, 0, len(addrs))
	seen := make(map[string]bool, len(addrs))
	for _, raw := range addrs {
		addr := strings.TrimSpace(raw)
		if addr == "" || seen[addr] {
			continue
		}
		if _, err := multiaddr.NewMultiaddr(addr); err != nil {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
		if len(out) >= maxAddrs {
			break
		}
	}
	return out
}
