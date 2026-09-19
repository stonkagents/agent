// Package: tracker/internal/api
// Feature: Peer and Asset Analytics
// Purpose: HTTP handlers for leaderboard endpoints

package api

import (
	"net/http"
	"strconv"

	"github.com/stonkagents/agent/tracker/internal/leaderboard"
)

// LeaderboardHandler handles leaderboard endpoints.
type LeaderboardHandler struct {
	store leaderboard.Store
}

// NewLeaderboardHandler creates a new LeaderboardHandler.
func NewLeaderboardHandler(store leaderboard.Store) *LeaderboardHandler {
	return &LeaderboardHandler{store: store}
}

// LeaderboardEntry represents a single leaderboard entry (API response).
type LeaderboardEntry struct {
	Rank         int    `json:"rank"`
	MaskedPeerID string `json:"masked_peer_id"`
	// DisplayName is the owner-set agent name; omitted when unset.
	DisplayName             string `json:"display_name,omitempty"`
	Country                 string `json:"country,omitempty"`
	Region                  string `json:"region,omitempty"`
	TotalUploadBytes        int64  `json:"total_upload_bytes,omitempty"`
	TotalDownloadBytes      int64  `json:"total_download_bytes,omitempty"`
	AverageSpeedBytesPerSec *int64 `json:"average_speed_bytes_per_sec,omitempty"`
	FirstSeen               string `json:"first_seen,omitempty"`
	LastSeen                string `json:"last_seen,omitempty"`
}

// HandleTopSeeders handles GET /api/v1/tracker/leaderboard/seeders.
func (h *LeaderboardHandler) HandleTopSeeders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}

	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	ctx := r.Context()

	list, err := h.store.TopSeeders(ctx, limit, offset)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get seeders")
		return
	}
	entries := make([]LeaderboardEntry, len(list))
	for i, e := range list {
		entries[i] = LeaderboardEntry{
			Rank:                    e.Rank,
			MaskedPeerID:            e.MaskedPeerID,
			DisplayName:             e.DisplayName,
			Country:                 e.Country,
			Region:                  e.Region,
			TotalUploadBytes:        e.TotalUploadBytes,
			AverageSpeedBytesPerSec: e.AverageSpeedBytesPerSec,
			FirstSeen:               e.FirstSeen,
			LastSeen:                e.LastSeen,
		}
	}
	SendList(w, entries, len(entries))
}

// HandleTopLeechers handles GET /api/v1/tracker/leaderboard/leechers.
func (h *LeaderboardHandler) HandleTopLeechers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}

	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	ctx := r.Context()

	list, err := h.store.TopLeechers(ctx, limit, offset)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get leechers")
		return
	}
	entries := make([]LeaderboardEntry, len(list))
	for i, e := range list {
		entries[i] = LeaderboardEntry{
			Rank:                    e.Rank,
			MaskedPeerID:            e.MaskedPeerID,
			DisplayName:             e.DisplayName,
			Country:                 e.Country,
			Region:                  e.Region,
			TotalDownloadBytes:      e.TotalDownloadBytes,
			AverageSpeedBytesPerSec: e.AverageSpeedBytesPerSec,
			FirstSeen:               e.FirstSeen,
			LastSeen:                e.LastSeen,
		}
	}
	SendList(w, entries, len(entries))
}
