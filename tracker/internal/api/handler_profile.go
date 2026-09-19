// Package: tracker/internal/api
// Feature: F-026 (Profile Endpoint)
// Story: US-026-01 (Profile Aggregation)
// Purpose: Handler for GET /api/profile/me — aggregates peer, reputation, assets, presence

package api

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"time"

	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

var LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

// ── Named Constants ──

const (
	topDropsLimit       = 5
	recentActivityLimit = 5
	cloutScale          = 100
	degradedTopPercent  = 99
)

// ── Minimal Interfaces (satisfied by Postgres repos via structural typing) ──

type profileReputationFinder interface {
	FindByPeerID(ctx context.Context, peerID string) (*reputation.ReputationRecord, error)
	CountAboveScore(ctx context.Context, score float64) (above int, total int, err error)
}

type profilePeerFinder interface {
	FindByID(ctx context.Context, peerID string) (*models.Peer, error)
}

type profileAssetSearcher interface {
	Search(ctx context.Context, opts repository.SearchAssetsOptions) ([]*models.Asset, int, error)
	CountByPeerID(ctx context.Context, peerID string) (int, error)
	TopByDownloads(ctx context.Context, peerID string, limit int) ([]*models.Asset, error)
}

type profilePresenceChecker interface {
	IsOnline(ctx context.Context, peerID string) (bool, error)
}

type profileTrustCounter interface {
	CountTrustsReceived(ctx context.Context, targetPeerID string) (int, error)
}

type profileLibraryCounter interface {
	CountByPeerIDAndAction(ctx context.Context, peerID, action string) (int, error)
}

// ── DTOs ──

type ProfileResponseDTO struct {
	PeerID         string                  `json:"peer_id"`
	MaskedPeerID   string                  `json:"masked_peer_id"`
	Rank           string                  `json:"rank"`
	IsOnline       bool                    `json:"is_online"`
	Stats          ProfileStatsDTO         `json:"stats"`
	EigenTrust     EigenTrustDTO           `json:"eigen_trust"`
	Badges         []reputation.BadgeEntry `json:"badges"`
	TopDrops       []TopDropDTO            `json:"top_drops"`
	RecentActivity []ActivityDTO           `json:"recent_activity"`
}

type ProfileStatsDTO struct {
	Clout         int   `json:"clout"`
	TopPercent    int   `json:"top_percent"`
	Drops         int   `json:"drops"`
	Library       int   `json:"library"`
	UptimeSeconds int64 `json:"uptime_seconds"`
}

type EigenTrustDTO struct {
	BandwidthScore   float64    `json:"bandwidth_score"`
	QualityScore     float64    `json:"quality_score"`
	SecurityScore    float64    `json:"security_score"`
	CitizenshipScore float64    `json:"citizenship_score"`
	CompositeScore   float64    `json:"composite_score"`
	Weights          WeightsDTO `json:"weights"`
}

type WeightsDTO struct {
	Bandwidth   float64 `json:"bandwidth"`
	Quality     float64 `json:"quality"`
	Security    float64 `json:"security"`
	Citizenship float64 `json:"citizenship"`
}

// BadgeDTO is kept as an alias for backward compatibility with profile tests. Use reputation.BadgeEntry directly.
type BadgeDTO = reputation.BadgeEntry

type TopDropDTO struct {
	Filename      string `json:"filename"`
	FileType      string `json:"file_type"`
	DownloadCount int64  `json:"download_count"`
	SizeBytes     int64  `json:"size_bytes"`
}

type ActivityDTO struct {
	Filename    string `json:"filename"`
	AnnouncedAt string `json:"announced_at"`
}

// ── ProfileHandler ──

type ProfileHandler struct {
	reputation profileReputationFinder
	peers      profilePeerFinder
	assets     profileAssetSearcher
	presence   profilePresenceChecker
	trusts     profileTrustCounter
	library    profileLibraryCounter
}

func NewProfileHandler(
	rep profileReputationFinder,
	peers profilePeerFinder,
	assets profileAssetSearcher,
	presence profilePresenceChecker,
	trusts profileTrustCounter,
) *ProfileHandler {
	return &ProfileHandler{
		reputation: rep,
		peers:      peers,
		assets:     assets,
		presence:   presence,
		trusts:     trusts,
	}
}

// SetLibraryCounter sets the library counter (peer event repository for download counts).
func (h *ProfileHandler) SetLibraryCounter(lc profileLibraryCounter) {
	h.library = lc
}

// ── Pure Helpers ──

// NOTE: Rank logic now delegated to reputation.DeriveRank() for consistency with F-032 portal endpoints.
// OG rank is automatic at composite >= 0.8 per the reputation system rules.
// No manual flag required — the tier system rewards contribution, not status.
// Founders are distinguished by badge #6 (OG Status: firstSeen within +/-7d of LaunchDate),
// which is cosmetic prestige handled by evaluateBadges, not rank.

func computeTopPercent(above, total int) int {
	if total == 0 {
		return degradedTopPercent
	}
	pct := int(math.Ceil(float64(above) / float64(total) * cloutScale))
	if pct < 1 {
		pct = 1
	}
	if pct > degradedTopPercent {
		pct = degradedTopPercent
	}
	return pct
}

// handleProfileTimeout is the handler-level context timeout.
// Leaves 3s margin for JSON marshaling + response write relative to the server's 15s WriteTimeout.
const handleProfileTimeout = 12 * time.Second

// HandleGetMyProfile handles GET /api/profile/me (requires X-API-Key via RequireAPIKey middleware).
//
// Security note (Gate 0 review): Uses context.WithTimeout to bound the 6 sequential
// DB calls below the server's WriteTimeout (15s). Without this, sequential retries or
// slow queries could exceed WriteTimeout, causing silent connection drops.
// See backend-architecture.md rule: "Handler Context Timeouts".
func (h *ProfileHandler) HandleGetMyProfile(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), handleProfileTimeout)
	defer cancel()

	profile, err := h.buildProfile(ctx, peerID)
	if err != nil {
		if err == models.ErrNotFound {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
			return
		}
		slog.Error("[ProfileHandler] buildProfile failed", "peer_id", peerID, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to build profile")
		return
	}

	// Cache-Control: per-user, 60s browser cache. EigenTrust recalculates hourly;
	// 60s staleness is acceptable. Private prevents CDN caching.
	// NOTE: When accessed via daemon proxy (:7841), this header is stripped — daemon's
	// portal_proxy.go only forwards Content-Type and Content-Length (api.go:744-750).
	// Direct tracker access (:7842) delivers Cache-Control correctly.
	// TODO(TD-023): Expand daemon proxy header allowlist to include Cache-Control,
	// X-RateLimit-*, and Retry-After headers.
	w.Header().Set("Cache-Control", "private, max-age=60")
	SendData(w, profile)
}

// buildEigenTrust fetches reputation data and computes the EigenTrust DTO + composite score + top percent.
// Soft-degrades: returns zero-value EigenTrust and degradedTopPercent on repo failure.
func (h *ProfileHandler) buildEigenTrust(ctx context.Context, peerID string) (EigenTrustDTO, float64, int) {
	eigenTrust := EigenTrustDTO{
		Weights: WeightsDTO{
			Bandwidth:   reputation.WeightBandwidth,
			Quality:     reputation.WeightQuality,
			Security:    reputation.WeightSecurity,
			Citizenship: reputation.WeightCitizenship,
		},
	}
	var composite float64

	rep, err := h.reputation.FindByPeerID(ctx, peerID)
	if err != nil {
		if err != models.ErrNotFound {
			slog.Warn("[ProfileHandler] reputation lookup degraded", "peer_id", peerID, "error", err)
			profileDegradationTotal.WithLabelValues("reputation").Inc()
		}
		return eigenTrust, 0, degradedTopPercent
	}

	composite = rep.CompositeScore
	eigenTrust.BandwidthScore = rep.BandwidthScore
	eigenTrust.QualityScore = rep.QualityScore
	eigenTrust.SecurityScore = rep.SecurityScore
	eigenTrust.CitizenshipScore = rep.CitizenshipScore
	eigenTrust.CompositeScore = rep.CompositeScore

	topPercent := degradedTopPercent
	above, total, err := h.reputation.CountAboveScore(ctx, composite)
	if err != nil {
		slog.Warn("[ProfileHandler] count above score degraded", "peer_id", peerID, "error", err)
		profileDegradationTotal.WithLabelValues("reputation").Inc()
	} else {
		topPercent = computeTopPercent(above, total)
	}

	return eigenTrust, composite, topPercent
}

// buildTopDrops fetches the topDropsLimit drops by download count for a peer.
// Soft-degrades: returns empty slice on repo failure.
func (h *ProfileHandler) buildTopDrops(ctx context.Context, peerID string) []TopDropDTO {
	topAssets, err := h.assets.TopByDownloads(ctx, peerID, topDropsLimit)
	if err != nil {
		slog.Warn("[ProfileHandler] top downloads degraded", "peer_id", peerID, "error", err)
		profileDegradationTotal.WithLabelValues("top_downloads").Inc()
		return []TopDropDTO{}
	}
	drops := make([]TopDropDTO, 0, len(topAssets))
	for _, a := range topAssets {
		drops = append(drops, TopDropDTO{
			Filename:      a.Filename,
			FileType:      filepath.Ext(a.Filename),
			DownloadCount: a.DownloadCount,
			SizeBytes:     a.Size,
		})
	}
	return drops
}

// buildRecentActivity fetches the most recent drops for the activity timeline.
// Soft-degrades: returns empty slice on repo failure.
func (h *ProfileHandler) buildRecentActivity(ctx context.Context, peerID string) []ActivityDTO {
	searchAssets, _, err := h.assets.Search(ctx, repository.SearchAssetsOptions{
		PeerIDs: []string{peerID},
		Limit:   recentActivityLimit,
	})
	if err != nil {
		slog.Warn("[ProfileHandler] recent activity degraded", "peer_id", peerID, "error", err)
		profileDegradationTotal.WithLabelValues("recent_activity").Inc()
		return []ActivityDTO{}
	}
	activity := make([]ActivityDTO, 0, len(searchAssets))
	for _, a := range searchAssets {
		activity = append(activity, ActivityDTO{
			Filename:    a.Filename,
			AnnouncedAt: a.AnnouncedAt.UTC().Format(time.RFC3339),
		})
	}
	return activity
}

// resolveMaskedPeerID returns the stored masked ID, or falls back to geo.MaskPeerID.
func resolveMaskedPeerID(peer *models.Peer) string {
	if peer.MaskedPeerID != "" {
		return peer.MaskedPeerID
	}
	return geo.MaskPeerID(peer.PeerID)
}

// buildProfile aggregates peer, reputation, assets, and presence into a single DTO.
// Only peers.FindByID is hard-required (returns error). All other deps soft-degrade.
func (h *ProfileHandler) buildProfile(ctx context.Context, peerID string) (*ProfileResponseDTO, error) {
	peer, err := h.peers.FindByID(ctx, peerID)
	if err != nil {
		return nil, err
	}

	eigenTrust, composite, topPercent := h.buildEigenTrust(ctx, peerID)

	isOnline := false
	online, err := h.presence.IsOnline(ctx, peerID)
	if err != nil {
		slog.Warn("[ProfileHandler] presence check degraded", "peer_id", peerID, "error", err)
		profileDegradationTotal.WithLabelValues("presence").Inc()
	} else {
		isOnline = online
	}

	drops := 0
	assetCount, err := h.assets.CountByPeerID(ctx, peerID)
	if err != nil {
		slog.Warn("[ProfileHandler] asset count degraded", "peer_id", peerID, "error", err)
		profileDegradationTotal.WithLabelValues("assets").Inc()
	} else {
		drops = assetCount
	}

	trustsReceived := 0
	trustCount, err := h.trusts.CountTrustsReceived(ctx, peerID)
	if err != nil {
		slog.Warn("[ProfileHandler] trust count degraded", "peer_id", peerID, "error", err)
		profileDegradationTotal.WithLabelValues("trusts").Inc()
	} else {
		trustsReceived = trustCount
	}

	library := 0
	if h.library != nil {
		if lc, err := h.library.CountByPeerIDAndAction(ctx, peerID, "download"); err == nil {
			library = lc
		}
	}

	return &ProfileResponseDTO{
		PeerID:       peerID,
		MaskedPeerID: resolveMaskedPeerID(peer),
		Rank:         reputation.DeriveRank(composite, peer.TotalUploadBytes, peer.TotalDownloadBytes, drops),
		IsOnline:     isOnline,
		Stats: ProfileStatsDTO{
			Clout:         int(math.Round(composite * cloutScale)),
			TopPercent:    topPercent,
			Drops:         drops,
			Library:       library,
			UptimeSeconds: peer.TotalUptimeSeconds,
		},
		EigenTrust:     eigenTrust,
		Badges:         reputation.EvaluateBadges(peer, composite, drops, trustsReceived, LaunchDate),
		TopDrops:       h.buildTopDrops(ctx, peerID),
		RecentActivity: h.buildRecentActivity(ctx, peerID),
	}, nil
}
