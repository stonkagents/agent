// Package: tracker/internal/api
// Feature: Peer and Asset Analytics
// Purpose: HTTP handlers for peer stats reporting and dashboard stats

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// DefaultOnlineTTL is the default window for "online" (used for by-country and dashboard).
const DefaultOnlineTTL = 5 * time.Minute

// StatsHandler handles peer stats and dashboard endpoints.
type StatsHandler struct {
	peerService      *services.PeerService
	assetService     *services.AssetService
	peerRepo         repository.PeerRepository
	assetRepo        repository.AssetRepository
	presence         presence.PresenceStore
	onlineTTL        time.Duration // window for "online" (last_seen within this)
	leaderboardStore interface {
		UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64) error
	} // optional: nil ok
}

// NewStatsHandler creates a new StatsHandler. leaderboardStore is optional (e.g. Redis cache); pass nil to skip cache updates.
// onlineTTL is the window for "online" peers (e.g. 5*time.Minute); if 0, DefaultOnlineTTL is used.
func NewStatsHandler(peerSvc *services.PeerService, assetSvc *services.AssetService, peerRepo repository.PeerRepository, assetRepo repository.AssetRepository, pres presence.PresenceStore, onlineTTL time.Duration, leaderboardStore interface {
	UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64) error
}) *StatsHandler {
	if onlineTTL <= 0 {
		onlineTTL = DefaultOnlineTTL
	}
	return &StatsHandler{
		peerService:      peerSvc,
		assetService:     assetSvc,
		peerRepo:         peerRepo,
		assetRepo:        assetRepo,
		presence:         pres,
		onlineTTL:        onlineTTL,
		leaderboardStore: leaderboardStore,
	}
}

// ReportStatsDTO is the request body for reporting transfer stats.
type ReportStatsDTO struct {
	PeerID                  string `json:"peer_id"`
	UploadBytes             int64  `json:"upload_bytes"`
	DownloadBytes           int64  `json:"download_bytes"`
	AverageSpeedBytesPerSec *int64 `json:"average_speed_bytes_per_sec,omitempty"`
}

// HandleReportStats handles POST /api/v1/tracker/stats.
func (h *StatsHandler) HandleReportStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto ReportStatsDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	if dto.PeerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: peer_id")
		return
	}

	// Verify peer exists (e.g. must have registered first; race or replica lag can yield not found)
	_, err := h.peerRepo.FindByID(r.Context(), dto.PeerID)
	if err != nil {
		if err == models.ErrNotFound {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
			return
		}
		// DB or other transient error: return 404 so clients treat as "peer not found" and don't trip circuit breakers
		slog.Warn("[StatsHandler.ReportStats] FindByID failed", "peer_id", dto.PeerID, "error", err)
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
		return
	}

	// Update transfer stats
	if err := h.peerRepo.UpdateTransferStats(r.Context(), dto.PeerID, dto.UploadBytes, dto.DownloadBytes, dto.AverageSpeedBytesPerSec); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update stats")
		return
	}
	// Update leaderboard cache (e.g. Redis) when present
	if h.leaderboardStore != nil {
		_ = h.leaderboardStore.UpdateTransferStats(r.Context(), dto.PeerID, dto.UploadBytes, dto.DownloadBytes)
	}

	SendJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":                     dto.PeerID,
		"upload_bytes":                dto.UploadBytes,
		"download_bytes":              dto.DownloadBytes,
		"average_speed_bytes_per_sec": dto.AverageSpeedBytesPerSec,
	})
}

// DashboardStatsResponse is the response for dashboard stats.
type DashboardStatsResponse struct {
	TotalPeers         int   `json:"total_peers"`
	OnlinePeers        int   `json:"online_peers"`
	OfflinePeers       int   `json:"offline_peers"`
	TotalSeeders       int   `json:"total_seeders"`
	TotalLeechers      int   `json:"total_leechers"`
	TotalAssets        int   `json:"total_assets"`
	TotalUploadBytes   int64 `json:"total_upload_bytes"`
	TotalDownloadBytes int64 `json:"total_download_bytes"`
	TrendingCount      int   `json:"trending_count"`
}

// ByCountryEntry is one row for GET /stats/by-country (online peer count by country).
type ByCountryEntry struct {
	Country string `json:"country"`
	Count   int    `json:"count"`
}

// HandleStatsByCountry handles GET /api/v1/tracker/stats/by-country.
// Returns online peer counts grouped by country (ISO 3166-1 alpha-2) for world map display.
func (h *StatsHandler) HandleStatsByCountry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	ctx := r.Context()
	cutoff := time.Now().Add(-h.onlineTTL)
	counts, err := h.peerRepo.CountOnlineByCountry(ctx, cutoff)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get counts by country")
		return
	}
	entries := make([]ByCountryEntry, 0, len(counts))
	for _, c := range counts {
		entries = append(entries, ByCountryEntry{Country: c.Country, Count: c.Count})
	}
	// Also return a map for key lookup (e.g. map[countryCode]count for map libraries)
	byCountry := make(map[string]int, len(counts))
	for _, c := range counts {
		byCountry[c.Country] = c.Count
	}
	SendJSON(w, http.StatusOK, map[string]interface{}{
		"data":       entries,
		"by_country": byCountry,
	})
}

// HandleDashboardStats handles GET /api/v1/tracker/stats.
func (h *StatsHandler) HandleDashboardStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}

	ctx := r.Context()

	// Get total peers
	totalPeers, err := h.peerRepo.Count(ctx)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to count peers")
		return
	}

	// Get online peers
	onlineIDs, err := h.presence.OnlinePeerIDs(ctx)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get online peers")
		return
	}
	onlinePeers := len(onlineIDs)
	offlinePeers := totalPeers - onlinePeers

	// Get total assets
	totalAssets, err := h.assetRepo.Count(ctx)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to count assets")
		return
	}

	// Count seeders: online peers with at least one asset
	onlineSet := make(map[string]bool, len(onlineIDs))
	for _, id := range onlineIDs {
		onlineSet[id] = true
	}

	totalSeeders := 0
	totalUploadBytes := int64(0)
	totalDownloadBytes := int64(0)
	trendingCount := 0

	// Get all peers to calculate stats
	allPeers, err := h.peerRepo.List(ctx, repository.ListPeersOptions{Limit: 0})
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list peers")
		return
	}

	for _, peer := range allPeers {
		if onlineSet[peer.PeerID] {
			// Check if peer has assets (is a seeder)
			assetCount, err := h.assetRepo.CountByPeerID(ctx, peer.PeerID)
			if err == nil && assetCount > 0 {
				totalSeeders++
			}
		}
		totalUploadBytes += peer.TotalUploadBytes
		totalDownloadBytes += peer.TotalDownloadBytes
	}

	totalLeechers := onlinePeers - totalSeeders

	// Count trending assets (download_count > 0)
	trending, _, err := h.assetRepo.ListTrending(ctx, 0, 0)
	if err == nil {
		for _, asset := range trending {
			if asset.DownloadCount > 0 {
				trendingCount++
			}
		}
	}

	SendData(w, DashboardStatsResponse{
		TotalPeers:         totalPeers,
		OnlinePeers:        onlinePeers,
		OfflinePeers:       offlinePeers,
		TotalSeeders:       totalSeeders,
		TotalLeechers:      totalLeechers,
		TotalAssets:        totalAssets,
		TotalUploadBytes:   totalUploadBytes,
		TotalDownloadBytes: totalDownloadBytes,
		TrendingCount:      trendingCount,
	})
}
