// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-03 (Asset Registry)
// Purpose: HTTP handlers for asset announcement and search

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/internal/sharing"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// AnnounceAssetDTO is the request body for asset announcement.
type AnnounceAssetDTO struct {
	CID          string `json:"cid"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	PeerID       string `json:"peer_id"`
	ManifestType string `json:"manifest_type"`
	Chunks       []int  `json:"chunks"`
}

// AssetResponseDTO is the response for asset announcement.
type AssetResponseDTO struct {
	CID          string `json:"cid"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	PeerID       string `json:"peer_id"`
	ManifestType string `json:"manifest_type"`
	AnnouncedAt  string `json:"announced_at"`
}

// PeerInfoDTO represents a peer in search results.
type PeerInfoDTO struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
	Chunks     []int    `json:"chunks,omitempty"`
}

// SearchResultDTO is the response shape for search results.
type SearchResultDTO struct {
	CID          string        `json:"cid"`
	Filename     string        `json:"filename"`
	MimeType     string        `json:"mime_type"`
	Size         int64         `json:"size"`
	ManifestType string        `json:"manifest_type"`
	AnnouncedAt  string        `json:"announced_at"`
	Peers        []PeerInfoDTO `json:"peers"`
	Similarity   *float32      `json:"similarity,omitempty"` // F-002, US-002-03: Semantic search similarity score
}

// AssetHandler handles asset-related HTTP endpoints.
type AssetHandler struct {
	assetService    *services.AssetService
	peerService     *services.PeerService
	assetRepo       repository.AssetRepository
	replicationRepo repository.ReplicationRepository
	peerEventRepo   repository.PeerEventRepository
}

// NewAssetHandler creates a new AssetHandler.
func NewAssetHandler(svc *services.AssetService) *AssetHandler {
	return &AssetHandler{assetService: svc}
}

// NewAssetHandlerWithPeers creates an AssetHandler with peer lookup capability.
func NewAssetHandlerWithPeers(svc *services.AssetService, peerSvc *services.PeerService) *AssetHandler {
	return &AssetHandler{assetService: svc, peerService: peerSvc}
}

// SetAssetRepo sets the asset repository (for trending endpoint).
func (h *AssetHandler) SetAssetRepo(repo repository.AssetRepository) {
	h.assetRepo = repo
}

// SetReplicationRepo sets the replication repository.
func (h *AssetHandler) SetReplicationRepo(repo repository.ReplicationRepository) {
	h.replicationRepo = repo
}

// SetPeerEventRepo sets the peer event repository (for activity events).
func (h *AssetHandler) SetPeerEventRepo(repo repository.PeerEventRepository) {
	h.peerEventRepo = repo
}

// HandleAnnounce handles POST /api/v1/tracker/announce.
func (h *AssetHandler) HandleAnnounce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	// Parse request body with size limit (10MB max)
	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto AnnounceAssetDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	// Plain-text rule, enforced here too so an old daemon cannot announce a binary
	if !sharing.AllowedExtension(dto.Filename) || !sharing.AllowedMIME(dto.MimeType) {
		SendError(w, http.StatusBadRequest, sharing.ErrorCode, sharing.RuleMessage)
		return
	}

	asset, err := h.assetService.Announce(r.Context(), services.AnnounceAssetRequest{
		CID:          dto.CID,
		Filename:     dto.Filename,
		MimeType:     dto.MimeType,
		Size:         dto.Size,
		PeerID:       dto.PeerID,
		ManifestType: dto.ManifestType,
		Chunks:       dto.Chunks,
	})
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required fields: cid, peer_id, manifest_type")
		return
	}
	if err == models.ErrAlreadyExists {
		SendError(w, http.StatusConflict, "ALREADY_EXISTS", "Asset with this CID already announced")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to announce asset")
		return
	}

	// Record "share" activity event (F-032, US-032-02)
	if h.peerEventRepo != nil {
		_ = h.peerEventRepo.Insert(r.Context(), &models.PeerEvent{
			PeerID:    dto.PeerID,
			Action:    "share",
			Details:   "Shared " + dto.Filename,
			CreatedAt: time.Now(),
		})
	}

	SendJSON(w, http.StatusCreated, AssetResponseDTO{
		CID:          asset.CID,
		Filename:     asset.Filename,
		MimeType:     asset.MimeType,
		Size:         asset.Size,
		PeerID:       asset.PeerID,
		ManifestType: asset.ManifestType,
		AnnouncedAt:  asset.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// HandleFileSearch handles GET /api/v1/tracker/files/search.
// File search by text (filename/keyword), like normal site search. Requires a non-empty query.
func (h *AssetHandler) HandleFileSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	if query == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Search query is required. Use ?q=... or ?query=...")
		return
	}
	limit, offset := parseLimitOffset(r, 50)
	manifestType := r.URL.Query().Get("type")

	results, total, err := h.assetService.Search(r.Context(), services.SearchAssetsRequest{
		Query:        query,
		ManifestType: manifestType,
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to search files")
		return
	}

	dtos := make([]SearchResultDTO, 0, len(results))
	for _, a := range results {
		peerAvail, _ := h.assetService.PeersForCID(r.Context(), a.CID)
		if len(peerAvail) == 0 {
			continue
		}
		dtos = append(dtos, SearchResultDTO{
			CID:          a.CID,
			Filename:     a.Filename,
			MimeType:     a.MimeType,
			Size:         a.Size,
			ManifestType: a.ManifestType,
			AnnouncedAt:  a.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
			Peers:        h.resolvePeers(r, peerAvail),
		})
	}
	SendList(w, dtos, total)
}

// parseLimitOffset parses limit and offset from request query; defaultLimit is used when limit is missing or invalid.
func parseLimitOffset(r *http.Request, defaultLimit int) (limit, offset int) {
	limit = defaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	return limit, offset
}

// HandleSearch handles GET /api/v1/tracker/search.
// Feature: F-002, Story: US-002-03
// Supports semantic search via ?semantic=true query parameter.
func (h *AssetHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}

	query := r.URL.Query().Get("q")
	manifestType := r.URL.Query().Get("type")
	semantic := r.URL.Query().Get("semantic") == "true" // F-002: Semantic search flag
	limit, offset := parseLimitOffset(r, 50)

	// Branch: Semantic search vs keyword search
	if semantic {
		// Semantic search using embeddings + HNSW (F-002, US-002-03)
		semanticResults, _, err := h.assetService.SearchSemantic(r.Context(), query, limit)
		if err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to perform semantic search")
			return
		}

		dtos := make([]SearchResultDTO, 0, len(semanticResults))
		for _, sr := range semanticResults {
			peerAvail, _ := h.assetService.PeersForCID(r.Context(), sr.Asset.CID)
			if len(peerAvail) == 0 {
				continue
			}
			dtos = append(dtos, SearchResultDTO{
				CID:          sr.Asset.CID,
				Filename:     sr.Asset.Filename,
				MimeType:     sr.Asset.MimeType,
				Size:         sr.Asset.Size,
				ManifestType: sr.Asset.ManifestType,
				AnnouncedAt:  sr.Asset.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
				Peers:        h.resolvePeers(r, peerAvail),
				Similarity:   &sr.Similarity, // Include similarity score
			})
		}

		SendList(w, dtos, len(dtos))
		return
	}

	// Keyword search (backward compatibility)
	results, total, err := h.assetService.Search(r.Context(), services.SearchAssetsRequest{
		Query:        query,
		ManifestType: manifestType,
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to search assets")
		return
	}

	dtos := make([]SearchResultDTO, 0, len(results))
	for _, a := range results {
		peerAvail, _ := h.assetService.PeersForCID(r.Context(), a.CID)
		if len(peerAvail) == 0 {
			continue
		}
		dtos = append(dtos, SearchResultDTO{
			CID:          a.CID,
			Filename:     a.Filename,
			MimeType:     a.MimeType,
			Size:         a.Size,
			ManifestType: a.ManifestType,
			AnnouncedAt:  a.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
			Peers:        h.resolvePeers(r, peerAvail),
			// No similarity field for keyword search (backward compatibility)
		})
	}

	SendList(w, dtos, total)
}

// HandleGetAsset handles GET /api/v1/tracker/assets/{cid}.
func (h *AssetHandler) HandleGetAsset(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID")
		return
	}

	asset, err := h.assetService.FindByCID(r.Context(), cid)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get asset")
		return
	}

	if asset.Quarantined {
		SendError(w, http.StatusUnavailableForLegalReasons, "QUARANTINED", "Asset unavailable due to DMCA takedown")
		return
	}

	peerAvail, _ := h.assetService.PeersForCID(r.Context(), asset.CID)
	SendJSON(w, http.StatusOK, SearchResultDTO{
		CID:          asset.CID,
		Filename:     asset.Filename,
		MimeType:     asset.MimeType,
		Size:         asset.Size,
		ManifestType: asset.ManifestType,
		AnnouncedAt:  asset.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
		Peers:        h.resolvePeers(r, peerAvail),
	})
}

// HandleRelatedAssets handles GET /api/v1/assets/{cid}/related.
// Feature: F-002, Story: US-002-04
// Returns semantically similar assets using embedding-based similarity.
func (h *AssetHandler) HandleRelatedAssets(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID")
		return
	}

	// Parse limit parameter
	limit := 10 // Default limit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	// Find related assets using semantic similarity
	relatedResults, err := h.assetService.RelatedAssets(r.Context(), cid, limit)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return
	}
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "SEMANTIC_SEARCH_UNAVAILABLE", "Semantic search not available")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to find related assets")
		return
	}

	// Convert to DTOs
	dtos := make([]SearchResultDTO, 0, len(relatedResults))
	for _, sr := range relatedResults {
		peerAvail, _ := h.assetService.PeersForCID(r.Context(), sr.Asset.CID)
		if len(peerAvail) == 0 {
			continue
		}
		dtos = append(dtos, SearchResultDTO{
			CID:          sr.Asset.CID,
			Filename:     sr.Asset.Filename,
			MimeType:     sr.Asset.MimeType,
			Size:         sr.Asset.Size,
			ManifestType: sr.Asset.ManifestType,
			AnnouncedAt:  sr.Asset.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
			Peers:        h.resolvePeers(r, peerAvail),
			Similarity:   &sr.Similarity, // Include similarity score
		})
	}

	SendList(w, dtos, len(dtos))
}

func (h *AssetHandler) resolvePeers(r *http.Request, peers []repository.PeerChunkAvailability) []PeerInfoDTO {
	if len(peers) == 0 {
		return []PeerInfoDTO{}
	}
	if h.peerService == nil {
		results := make([]PeerInfoDTO, 0, len(peers))
		for _, p := range peers {
			results = append(results, PeerInfoDTO{PeerID: p.PeerID, Chunks: p.Chunks})
		}
		return results
	}

	results := make([]PeerInfoDTO, 0, len(peers))
	for _, p := range peers {
		peer, err := h.peerService.FindByID(r.Context(), p.PeerID)
		if err != nil {
			results = append(results, PeerInfoDTO{PeerID: p.PeerID, Chunks: p.Chunks})
			continue
		}
		results = append(results, PeerInfoDTO{
			PeerID:     peer.PeerID,
			Multiaddrs: peer.Multiaddrs,
			Chunks:     p.Chunks,
		})
	}
	return results
}

// HandleAssetPeers handles GET /api/v1/tracker/assets/{cid}/peers.
// Returns peers with chunk availability for the given CID.
func (h *AssetHandler) HandleAssetPeers(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID")
		return
	}

	peerAvail, err := h.assetService.PeersForCID(r.Context(), cid)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to resolve peers")
		return
	}

	SendList(w, h.resolvePeers(r, peerAvail), len(peerAvail))
}

// AvailabilityUpdateDTO is the request body for availability updates.
type AvailabilityUpdateDTO struct {
	PeerID string `json:"peer_id"`
	Chunks []int  `json:"chunks"`
}

// HandleAvailabilityUpdate handles POST /api/v1/tracker/assets/{cid}/availability.
func (h *AssetHandler) HandleAvailabilityUpdate(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID")
		return
	}
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto AvailabilityUpdateDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	if dto.PeerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: peer_id")
		return
	}

	_, err := h.assetService.FindByCID(r.Context(), cid)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to find asset")
		return
	}

	_, err = h.assetService.Announce(r.Context(), services.AnnounceAssetRequest{
		CID:          cid,
		PeerID:       dto.PeerID,
		ManifestType: "raw",
		Chunks:       dto.Chunks,
	})
	if err != nil && err != models.ErrAlreadyExists {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update availability")
		return
	}

	SendJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TrendingAssetDTO represents a trending asset in the response.
type TrendingAssetDTO struct {
	CID           string `json:"cid"`
	Filename      string `json:"filename"`
	MimeType      string `json:"mime_type"`
	Size          int64  `json:"size"`
	ManifestType  string `json:"manifest_type"`
	AnnouncedAt   string `json:"announced_at"`
	DownloadCount int64  `json:"download_count"`
	PeerCount     int    `json:"peer_count,omitempty"`
}

// RecentAssetDTO represents a recent announcement feed item for replication workers.
type RecentAssetDTO struct {
	CID          string `json:"cid"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	ManifestType string `json:"manifest_type"`
	PeerID       string `json:"peer_id"`
	AnnouncedAt  string `json:"announced_at"`
}

// HandleRecentAssets handles GET /api/v1/tracker/assets/recent.
// Optional query:
// - limit (default 50, max 200)
// - since (RFC3339): return assets announced strictly after this timestamp.
func (h *AssetHandler) HandleRecentAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	if h.assetRepo == nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Asset repository not configured")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}

	var sinceTime time.Time
	var hasSince bool
	if since := strings.TrimSpace(r.URL.Query().Get("since")); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "since must be RFC3339")
			return
		}
		sinceTime = parsed
		hasSince = true
	}

	assets, err := h.assetRepo.RecentAnnouncements(r.Context(), limit)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list recent assets")
		return
	}

	out := make([]RecentAssetDTO, 0, len(assets))
	for _, a := range assets {
		if hasSince && !a.AnnouncedAt.After(sinceTime) {
			continue
		}
		out = append(out, RecentAssetDTO{
			CID:          a.CID,
			Filename:     a.Filename,
			MimeType:     a.MimeType,
			Size:         a.Size,
			ManifestType: a.ManifestType,
			PeerID:       a.PeerID,
			AnnouncedAt:  a.AnnouncedAt.UTC().Format(time.RFC3339),
		})
	}
	SendList(w, out, len(out))
}

// DownloadRouteDTO returns peer-based download routing (P2P only).
type DownloadRouteDTO struct {
	Mode  string        `json:"mode"`
	Peers []PeerInfoDTO `json:"peers,omitempty"`
}

// ReplicationStatusDTO returns replication metadata for a CID.
type ReplicationStatusDTO struct {
	CID          string `json:"cid"`
	Status       string `json:"status"`
	S3Key        string `json:"s3_key,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	ReplicatedAt string `json:"replicated_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

// HandleDownloadRoute handles GET /api/v1/tracker/assets/{cid}/download.
func (h *AssetHandler) HandleDownloadRoute(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID")
		return
	}

	asset, err := h.assetService.FindByCID(r.Context(), cid)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to resolve asset")
		return
	}
	if asset.Quarantined {
		SendError(w, http.StatusUnavailableForLegalReasons, "QUARANTINED", "Asset unavailable due to DMCA takedown")
		return
	}

	peerAvail, err := h.assetService.PeersForCID(r.Context(), cid)
	if err == nil && len(peerAvail) > 0 {
		downloadRouteTotal.WithLabelValues("p2p").Inc()
		SendJSON(w, http.StatusOK, DownloadRouteDTO{
			Mode:  "p2p",
			Peers: h.resolvePeers(r, peerAvail),
		})
		return
	}

	downloadRouteTotal.WithLabelValues("no_peers").Inc()
	SendError(w, http.StatusServiceUnavailable, "NOT_AVAILABLE", "No online peers with this asset; retry when a seeder is available")
}

// HandleReplicationStatus handles GET /api/v1/tracker/assets/{cid}/replication.
func (h *AssetHandler) HandleReplicationStatus(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID")
		return
	}
	if h.replicationRepo == nil {
		SendError(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "Replication repository not configured")
		return
	}
	replication, err := h.replicationRepo.GetAssetReplication(r.Context(), cid)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Replication record not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to read replication record")
		return
	}
	dto := ReplicationStatusDTO{
		CID:       replication.CID,
		Status:    replication.Status,
		S3Key:     replication.S3Key,
		SizeBytes: replication.SizeBytes,
		LastError: replication.LastError,
		UpdatedAt: replication.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if replication.ReplicatedAt != nil {
		dto.ReplicatedAt = replication.ReplicatedAt.UTC().Format(time.RFC3339)
	}
	SendJSON(w, http.StatusOK, dto)
}

// HandleTrending handles GET /api/v1/tracker/trending.
// Query: period=all (default) = most downloaded all-time; period=24h = trending in last 24 hours.
func (h *AssetHandler) HandleTrending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}

	if h.assetRepo == nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Asset repository not configured")
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

	period := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("period")))
	if period == "" {
		period = "all"
	}

	ctx := r.Context()

	if period == "24h" {
		since := time.Now().Add(-24 * time.Hour)
		rows, total, err := h.assetRepo.ListTrendingSince(ctx, since, limit, offset)
		if err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get trending assets")
			return
		}
		dtos := make([]TrendingAssetDTO, len(rows))
		for i, row := range rows {
			a := row.Asset
			dtos[i] = TrendingAssetDTO{
				CID:           a.CID,
				Filename:      a.Filename,
				MimeType:      a.MimeType,
				Size:          a.Size,
				ManifestType:  a.ManifestType,
				AnnouncedAt:   a.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
				DownloadCount: int64(row.CountInWindow), // downloads in last 24h
			}
			if peerAvail, err := h.assetService.PeersForCID(ctx, a.CID); err == nil {
				dtos[i].PeerCount = len(peerAvail)
			}
		}
		SendList(w, dtos, total)
		return
	}

	// period=all (default): most downloaded all-time
	assets, total, err := h.assetRepo.ListTrending(ctx, limit, offset)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get trending assets")
		return
	}

	dtos := make([]TrendingAssetDTO, len(assets))
	for i, asset := range assets {
		dtos[i] = TrendingAssetDTO{
			CID:           asset.CID,
			Filename:      asset.Filename,
			MimeType:      asset.MimeType,
			Size:          asset.Size,
			ManifestType:  asset.ManifestType,
			AnnouncedAt:   asset.AnnouncedAt.Format("2006-01-02T15:04:05Z"),
			DownloadCount: asset.DownloadCount,
		}
		// Optionally get peer count
		if peerAvail, err := h.assetService.PeersForCID(ctx, asset.CID); err == nil {
			dtos[i].PeerCount = len(peerAvail)
		}
	}

	SendList(w, dtos, total)
}
