// Package: tracker/internal/api
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Discovery & Enrichment)
// Purpose: Portal peer list and peer asset handlers (split from handler_portal.go, TD-060)

package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// PortalPeer is the frontend Peer shape (F-032: enriched with real data).
type PortalPeer struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	PeerID             string  `json:"peerId"`
	Status             string  `json:"status"`
	Reputation         int     `json:"reputation"`
	Tier               string  `json:"tier"`
	SharedFiles        int     `json:"sharedFiles"`
	Location           string  `json:"location"`
	Country            string  `json:"country,omitempty"`
	City               string  `json:"city,omitempty"`
	Lat                float64 `json:"lat"`
	Lng                float64 `json:"lng"`
	TotalUploadBytes   int64   `json:"totalUploadBytes"`
	TotalDownloadBytes int64   `json:"totalDownloadBytes"`
	LastSeen           string  `json:"lastSeen"`
}

// HandlePortalPeers handles GET /api/peers.
// F-032: Enriched with real reputation, status, rank, shared files via batch queries.
func (h *PortalHandler) HandlePortalPeers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit, offset := parsePagination(r, 20, 100)
	onlineOnly := r.URL.Query().Get("online") == "true"
	searchQuery := strings.TrimSpace(r.URL.Query().Get("q"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))

	// When searching, fetch all peers then filter in-memory (Slice 2: simple approach).
	// Without search, use paginated fetch + repo count for accurate pagination meta.
	var peers []*models.Peer
	var total int

	if searchQuery != "" {
		// Fetch all for in-memory filtering (acceptable at current scale)
		allPeers, err := h.peerService.Discover(ctx, services.DiscoverOptions{Limit: 10000, Offset: 0, OnlineOnly: onlineOnly})
		if err != nil {
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to discover peers")
			return
		}

		// Case-insensitive filter on DisplayName or PeerID
		lowerQ := strings.ToLower(searchQuery)
		filtered := make([]*models.Peer, 0)
		for _, p := range allPeers {
			name := p.DisplayName
			if name == "" {
				name = p.PeerID
			}
			if strings.Contains(strings.ToLower(name), lowerQ) || strings.Contains(strings.ToLower(p.PeerID), lowerQ) {
				filtered = append(filtered, p)
			}
		}

		total = len(filtered)
		// Apply pagination to search results (deferred when status filter is active)
		if statusFilter != "" && isValidStatus(statusFilter) {
			peers = filtered // defer pagination to after status filter
		} else if offset < len(filtered) {
			end := offset + limit
			if end > len(filtered) {
				end = len(filtered)
			}
			peers = filtered[offset:end]
		}
	} else {
		var err error
		// F-032 bugfix: When onlineOnly=true or statusFilter is set, must fetch all peers.
		// onlineOnly: Count() returns all peers, not just online.
		// statusFilter: status is computed from timestamps, can't filter in DB — must enrich all, then filter.
		if onlineOnly || (statusFilter != "" && isValidStatus(statusFilter)) {
			allPeers, err := h.peerService.Discover(ctx, services.DiscoverOptions{Limit: 10000, Offset: 0, OnlineOnly: onlineOnly})
			if err != nil {
				SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to discover peers")
				return
			}
			total = len(allPeers)
			// Defer pagination when status filter is active (paginate after enrichment + filtering)
			if statusFilter != "" && isValidStatus(statusFilter) {
				peers = allPeers
			} else if offset < len(allPeers) {
				end := offset + limit
				if end > len(allPeers) {
					end = len(allPeers)
				}
				peers = allPeers[offset:end]
			}
		} else {
			total, err = h.peerRepo.Count(ctx)
			if err != nil {
				SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to count peers")
				return
			}

			peers, err = h.peerService.Discover(ctx, services.DiscoverOptions{Limit: limit, Offset: offset, OnlineOnly: false})
			if err != nil {
				SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to discover peers")
				return
			}
		}
	}

	if len(peers) == 0 {
		SendDataWithMeta(w, []PortalPeer{}, total, limit, offset)
		return
	}

	// Collect peer IDs for batch queries
	peerIDs := make([]string, len(peers))
	for i, p := range peers {
		peerIDs[i] = p.PeerID
	}

	// Batch: reputation scores
	repMap := make(map[string]*reputation.ReputationRecord)
	if h.reputationRepo != nil {
		repMap, _ = h.reputationRepo.FindByPeerIDs(ctx, peerIDs)
	}

	// Batch: shared file counts
	assetCountMap := make(map[string]int)
	if h.assetRepo != nil {
		assetCountMap, _ = h.assetRepo.CountByPeerIDs(ctx, peerIDs)
	}

	now := time.Now()
	result := make([]PortalPeer, 0, len(peers))
	for _, p := range peers {
		// Name: DisplayName if set, else masked peer ID
		name := p.DisplayName
		if name == "" {
			name = geo.MaskPeerID(p.PeerID)
		}

		// Status: derived from activity timestamps
		status := reputation.DeriveStatus(p.LastSeen, p.LastUploadAt, p.LastDownloadAt, now)

		// Reputation + Rank from batch data
		composite := 0.0
		if rec, ok := repMap[p.PeerID]; ok {
			composite = rec.CompositeScore
		}
		rep := int(composite * 100)
		sharedFiles := assetCountMap[p.PeerID]
		tier := reputation.DeriveRank(composite, p.TotalUploadBytes, p.TotalDownloadBytes, sharedFiles)

		// Location
		location := ""
		if p.City != "" && p.Country != "" {
			location = p.City + ", " + p.Country
		} else if p.Country != "" {
			location = p.Country
		}

		result = append(result, PortalPeer{
			ID:                 p.PeerID,
			Name:               name,
			PeerID:             p.PeerID,
			Status:             status,
			Reputation:         rep,
			Tier:               tier,
			SharedFiles:        sharedFiles,
			Location:           location,
			Country:            p.Country,
			City:               p.City,
			Lat:                p.Lat,
			Lng:                p.Lng,
			TotalUploadBytes:   p.TotalUploadBytes,
			TotalDownloadBytes: p.TotalDownloadBytes,
			LastSeen:           p.LastSeen.Format(time.RFC3339),
		})
	}

	// F-032: Apply ?status= filter (in-memory, since status is computed from timestamps).
	// All peers were fetched (no DB-level pagination) so we can filter accurately, then paginate.
	if statusFilter != "" && isValidStatus(statusFilter) {
		filtered := make([]PortalPeer, 0, len(result))
		for _, peer := range result {
			if peer.Status == statusFilter {
				filtered = append(filtered, peer)
			}
		}
		total = len(filtered)
		// Apply deferred pagination to status-filtered results
		if offset < len(filtered) {
			end := offset + limit
			if end > len(filtered) {
				end = len(filtered)
			}
			result = filtered[offset:end]
		} else {
			result = []PortalPeer{}
		}
	}

	SendDataWithMeta(w, result, total, limit, offset)
}

// isValidStatus returns true if s is a recognized peer status value.
func isValidStatus(s string) bool {
	return s == "online" || s == "seeding" || s == "leeching" || s == "offline"
}

// PeerAssetEntry is a single asset in the peer assets response (F-032, US-032-01).
type PeerAssetEntry struct {
	CID           string `json:"cid"`
	Filename      string `json:"filename"`
	FileType      string `json:"file_type"`
	SizeBytes     int64  `json:"size_bytes"`
	DownloadCount int64  `json:"download_count"`
}

// HandlePeerAssets handles GET /api/peers/:id/assets. Public endpoint.
func (h *PortalHandler) HandlePeerAssets(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	limit := 5
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	assets, err := h.assetRepo.TopByDownloads(r.Context(), peerID, limit)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to fetch peer assets")
		return
	}
	result := make([]PeerAssetEntry, 0, len(assets))
	for _, a := range assets {
		result = append(result, PeerAssetEntry{
			CID:           a.CID,
			Filename:      a.Filename,
			FileType:      a.ManifestType,
			SizeBytes:     a.Size,
			DownloadCount: a.DownloadCount,
		})
	}
	// TD-068: Use SendDataWithMeta for ADR-001 consistency (no offset for top-N endpoint)
	SendDataWithMeta(w, result, len(result), limit, 0)
}
