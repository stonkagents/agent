// Package: tracker/internal/api
// Feature: Peer and Asset Analytics
// Purpose: HTTP handler for download completion reporting

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// DownloadsHandler handles download completion endpoints.
type DownloadsHandler struct {
	assetRepo     repository.AssetRepository
	peerRepo      repository.PeerRepository
	peerEventRepo repository.PeerEventRepository
}

// NewDownloadsHandler creates a new DownloadsHandler.
func NewDownloadsHandler(assetRepo repository.AssetRepository, peerRepo repository.PeerRepository) *DownloadsHandler {
	return &DownloadsHandler{
		assetRepo: assetRepo,
		peerRepo:  peerRepo,
	}
}

// SetPeerEventRepo sets the peer event repository (for activity events).
func (h *DownloadsHandler) SetPeerEventRepo(repo repository.PeerEventRepository) {
	h.peerEventRepo = repo
}

// DownloadCompleteDTO is the request body for download completion.
type DownloadCompleteDTO struct {
	PeerID string `json:"peer_id"`
	CID    string `json:"cid"`
}

// HandleDownloadComplete handles POST /api/v1/tracker/downloads/complete.
func (h *DownloadsHandler) HandleDownloadComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto DownloadCompleteDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	if dto.PeerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: peer_id")
		return
	}
	if dto.CID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: cid")
		return
	}

	ctx := r.Context()

	// Verify peer exists
	_, err := h.peerRepo.FindByID(ctx, dto.PeerID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to find peer")
		return
	}

	// Verify asset exists
	asset, err := h.assetRepo.FindByCID(ctx, dto.CID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to find asset")
		return
	}

	// Increment download count (all-time)
	if err := h.assetRepo.IncrementDownloadCount(ctx, dto.CID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to increment download count")
		return
	}
	// Record event for time-windowed trending (e.g. last 24h)
	_ = h.assetRepo.RecordDownloadEvent(ctx, dto.CID)

	// Record "download" activity event (F-032, US-032-02)
	if h.peerEventRepo != nil {
		_ = h.peerEventRepo.Insert(ctx, &models.PeerEvent{
			PeerID:    dto.PeerID,
			Action:    "download",
			Details:   "Downloaded " + asset.Filename,
			CreatedAt: time.Now(),
		})
	}

	SendJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":        dto.PeerID,
		"cid":            dto.CID,
		"download_count": asset.DownloadCount + 1,
	})
}
