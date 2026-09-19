// Serves frontend at /api/* with { data: T } envelope.
// TD-060: Split into domain files — peers, community, trust, activity.

package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// DefaultGuestCredits is the initial credit for a new guest key (installer).
const DefaultGuestCredits = 1000

type PortalHandler struct {
	assetService   *services.AssetService
	assetRepo      repository.AssetRepository
	peerService    *services.PeerService
	peerRepo       repository.PeerRepository
	forumService   *services.ForumService
	trustBlockRepo repository.PeerTrustBlockRepository
	apiKeyRepo     repository.PeerAPIKeyRepository
	guestKeyRepo   repository.GuestKeyRepository
	reputationRepo repository.ReputationRepository         // F-032: batch reputation lookups for enriched peer list
	geoResolver    geo.Resolver                            // F-032: GeoIP lookup (nil-safe, falls back to empty)
	peerEventRepo  repository.PeerEventRepository          // F-032: per-peer activity timeline (nil-safe)
	snapshotRepo   repository.ReputationSnapshotRepository // F-032: trend computation (nil-safe)
	limiter        ratelimit.Limiter                       // nil-safe: view count always increments if nil
}

// NewPortalHandler creates a new PortalHandler. limiter is optional (nil-safe).
func NewPortalHandler(
	assetService *services.AssetService,
	assetRepo repository.AssetRepository,
	peerService *services.PeerService,
	peerRepo repository.PeerRepository,
	forumService *services.ForumService,
	trustBlockRepo repository.PeerTrustBlockRepository,
	apiKeyRepo repository.PeerAPIKeyRepository,
	guestKeyRepo repository.GuestKeyRepository,
	reputationRepo repository.ReputationRepository,
	geoResolver geo.Resolver,
	peerEventRepo repository.PeerEventRepository,
	snapshotRepo repository.ReputationSnapshotRepository,
	limiter ratelimit.Limiter,
) *PortalHandler {
	return &PortalHandler{
		assetService:   assetService,
		assetRepo:      assetRepo,
		peerService:    peerService,
		peerRepo:       peerRepo,
		forumService:   forumService,
		trustBlockRepo: trustBlockRepo,
		apiKeyRepo:     apiKeyRepo,
		guestKeyRepo:   guestKeyRepo,
		reputationRepo: reputationRepo,
		geoResolver:    geoResolver,
		peerEventRepo:  peerEventRepo,
		snapshotRepo:   snapshotRepo,
		limiter:        limiter,
	}
}

// HomeResponse is the frontend HomeResponse shape.
type HomeResponse struct {
	VisionStats    []VisionStat     `json:"visionStats"`
	TrendingAssets []TrendingAsset  `json:"trendingAssets"`
	MostInstalled  []TrendingAsset  `json:"mostInstalled"`
	RecentlyShared []RecentlyShared `json:"recentlyShared"`
}

type VisionStat struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Trend string `json:"trend"`
}

type TrendingAsset struct {
	Rank        int    `json:"rank"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Author      string `json:"author"`
	Metric      int64  `json:"metric"`
	MetricLabel string `json:"metricLabel"`
	IsFlagged   bool   `json:"isFlagged,omitempty"`
}

type RecentlyShared struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Author   string `json:"author"`
	Size     int64  `json:"size"`
	Time     string `json:"time"`
	Agents   int    `json:"agents"`
	Verified bool   `json:"verified"`
}

// HandleHome handles GET /api/home.
func (h *PortalHandler) HandleHome(w http.ResponseWriter, r *http.Request) {
	if h.assetRepo == nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Asset repo not configured")
		return
	}
	ctx := r.Context()

	assets, _, err := h.assetRepo.ListTrending(ctx, 10, 0)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to fetch trending")
		return
	}

	trendingAssets := make([]TrendingAsset, 0, len(assets))
	for i, a := range assets {
		trendingAssets = append(trendingAssets, TrendingAsset{
			Rank:        i + 1,
			Name:        a.Filename,
			Type:        a.ManifestType,
			Author:      a.PeerID,
			Metric:      a.DownloadCount,
			MetricLabel: "downloads",
		})
	}

	recentlyShared := make([]RecentlyShared, 0, 5)
	for i, a := range assets {
		if i >= 5 {
			break
		}
		recentlyShared = append(recentlyShared, RecentlyShared{
			Name:     a.Filename,
			Type:     a.ManifestType,
			Author:   a.PeerID,
			Size:     a.Size,
			Time:     a.AnnouncedAt.Format(time.RFC3339),
			Agents:   0,
			Verified: false,
		})
	}

	totalPeers := 0
	if h.peerRepo != nil {
		if n, err := h.peerRepo.Count(ctx); err == nil {
			totalPeers = n
		}
	}

	totalAssets := 0
	if h.assetRepo != nil {
		if n, err := h.assetRepo.Count(ctx); err == nil {
			totalAssets = n
		}
	}

	avgReputation := 0.0
	if h.reputationRepo != nil {
		if avg, err := h.reputationRepo.AverageCompositeScore(ctx); err == nil {
			avgReputation = avg
		}
	}

	home := HomeResponse{
		VisionStats: []VisionStat{
			{Value: strconv.Itoa(totalPeers), Label: "Total Peers", Trend: "-"},
			{Value: "0", Label: "Active Transfers", Trend: "-"},
			{Value: strconv.Itoa(totalAssets), Label: "Shared Assets", Trend: "-"},
			{Value: fmt.Sprintf("%.1f", avgReputation*100), Label: "Avg Reputation", Trend: "-"},
		},
		TrendingAssets: trendingAssets,
		MostInstalled:  trendingAssets,
		RecentlyShared: recentlyShared,
	}
	SendData(w, home)
}

// GuestKeyResponse is the response shape for POST /api/v1/portal/guest-key (installer). The body carries no product name.
type GuestKeyResponse struct {
	APIKey string `json:"apiKey"`
}

// HandleGuestKey handles POST /api/v1/portal/guest-key. Issues a guest agent API key with default credit.
func (h *PortalHandler) HandleGuestKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}
	if h.guestKeyRepo == nil {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Guest key issuance not configured")
		return
	}
	key, err := h.guestKeyRepo.CreateGuestKey(r.Context(), DefaultGuestCredits)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create guest key")
		return
	}
	SendData(w, GuestKeyResponse{APIKey: key})
}
