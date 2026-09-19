// Package: tracker/internal/api
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Peer Activity & Reputation)
// Purpose: Peer reputation, activity, and recent activity feed handlers (split from handler_portal.go, TD-060)

package api

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// sanitizeEventDetails strips HTML angle brackets and limits length for defense-in-depth (TD-065).
// Details should only contain plain text (filenames, masked peer IDs, tier transitions).
func sanitizeEventDetails(s string) string {
	s = strings.NewReplacer("<", "", ">", "").Replace(s)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// PeerReputationResponse is the peer reputation detail response (F-032, US-032-01 + US-032-02).
type PeerReputationResponse struct {
	CompositeScore   float64                 `json:"composite_score"`
	BandwidthScore   float64                 `json:"bandwidth_score"`
	QualityScore     float64                 `json:"quality_score"`
	SecurityScore    float64                 `json:"security_score"`
	CitizenshipScore float64                 `json:"citizenship_score"`
	Tier             string                  `json:"tier"`
	Badges           []reputation.BadgeEntry `json:"badges"`
	WeeklyBonus      int                     `json:"weekly_bonus"`
	Trend            *float64                `json:"trend"`

	// Community board reputation (phase 1). ReputationTier is the board tier (new, active,
	// trusted, top), distinct from Tier (the P2P rank); Score is only set for the peer itself.
	ReputationTier  string  `json:"reputation_tier"`
	Score           *int    `json:"score,omitempty"`
	BountiesWon     int     `json:"bounties_won"`
	AnswersAccepted int     `json:"answers_accepted"`
	UpvotesReceived int     `json:"upvotes_received"`
	ComputedAt      *string `json:"computed_at"`
}

// HandlePeerReputation handles GET /api/peers/:id/reputation. Public endpoint.
func (h *PortalHandler) HandlePeerReputation(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	ctx := r.Context()

	// Look up reputation record (may not exist for new peers)
	rec, err := h.reputationRepo.FindByPeerID(ctx, peerID)
	if err != nil {
		rec = &reputation.ReputationRecord{}
	}

	// Look up peer for transfer stats (needed for DeriveRank bootstrap rule + badges)
	peer, err := h.peerRepo.FindByID(ctx, peerID)
	if err != nil {
		peer = &models.Peer{PeerID: peerID} // zero-value peer for soft-degrade
	}
	totalUpload := peer.TotalUploadBytes
	totalDownload := peer.TotalDownloadBytes

	// Count shared files (for DeriveRank bootstrap rule + badges)
	sharedFiles := 0
	if h.assetRepo != nil {
		sharedFiles, _ = h.assetRepo.CountByPeerID(ctx, peerID)
	}

	// Count trusts received (for trusted_network badge)
	trustsReceived := 0
	if h.trustBlockRepo != nil {
		trustsReceived, _ = h.trustBlockRepo.CountTrustsReceived(ctx, peerID)
	}

	tier := reputation.DeriveRank(rec.CompositeScore, totalUpload, totalDownload, sharedFiles)
	badges := reputation.EvaluateBadges(peer, rec.CompositeScore, sharedFiles, trustsReceived, LaunchDate)

	// Compute trend from previous snapshot (nil if no snapshot)
	var trend *float64
	if h.snapshotRepo != nil {
		prev, err := h.snapshotRepo.FindPrevious(ctx, peerID, time.Now())
		if err != nil {
			slog.Warn("[PortalHandler] snapshot lookup degraded", "peer_id", peerID, "error", err)
		} else if prev != nil {
			delta := rec.CompositeScore - prev.CompositeScore
			trend = &delta
		}
	}

	resp := PeerReputationResponse{
		CompositeScore:   rec.CompositeScore,
		BandwidthScore:   rec.BandwidthScore,
		QualityScore:     rec.QualityScore,
		SecurityScore:    rec.SecurityScore,
		CitizenshipScore: rec.CitizenshipScore,
		Tier:             tier,
		Badges:           badges,
		WeeklyBonus:      reputation.TierToWeeklyBonus(tier),
		Trend:            trend,
		ReputationTier:   models.ReputationTierNew,
	}
	h.addBoardReputation(ctx, &resp, peerID, h.viewerPeerID(r))
	SendData(w, resp)
}

// addBoardReputation fills the board reputation fields of the peer detail; score only for the
// peer itself.
func (h *PortalHandler) addBoardReputation(ctx context.Context, resp *PeerReputationResponse, peerID, viewer string) {
	if h.forumService == nil || h.forumService.Reputation() == nil {
		return
	}
	rep, err := h.forumService.Reputation().Get(ctx, peerID)
	if err != nil {
		return
	}
	resp.ReputationTier = rep.Tier
	resp.BountiesWon, resp.AnswersAccepted, resp.UpvotesReceived = rep.BountiesWon, rep.AnswersAccepted, rep.UpvotesReceived
	if !rep.ComputedAt.IsZero() {
		resp.ComputedAt = rfc3339Ptr(&rep.ComputedAt)
	}
	if viewer != "" && viewer == peerID {
		score := rep.Score
		resp.Score = &score
	}
}

// PeerActivityDTO is a single event in a peer's activity timeline (F-032, US-032-02).
type PeerActivityDTO struct {
	Action  string `json:"action"`
	Details string `json:"details"`
	Time    string `json:"time"`
}

// HandlePeerActivity handles GET /api/peers/{id}/activity. Public endpoint, paginated.
func (h *PortalHandler) HandlePeerActivity(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}

	limit := 10
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	if h.peerEventRepo == nil {
		SendDataWithMeta(w, []PeerActivityDTO{}, 0, limit, offset)
		return
	}

	events, total, err := h.peerEventRepo.ListByPeerID(r.Context(), peerID, limit, offset)
	if err != nil {
		slog.Warn("[PortalHandler] peer activity degraded", "peer_id", peerID, "error", err)
		SendDataWithMeta(w, []PeerActivityDTO{}, 0, limit, offset)
		return
	}

	dtos := make([]PeerActivityDTO, 0, len(events))
	for _, e := range events {
		dtos = append(dtos, PeerActivityDTO{
			Action:  e.Action,
			Details: sanitizeEventDetails(e.Details),
			Time:    e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	SendDataWithMeta(w, dtos, total, limit, offset)
}

// ActivityEntry is a single item in the activity feed (F-027, US-027-06).
type ActivityEntry struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	TimeAgo    string `json:"time_ago"`
	OccurredAt string `json:"occurred_at"`
	Color      string `json:"color"`
	PeerID     string `json:"peer_id"`
}

// HandlePortalActivityRecent handles GET /api/activity/recent.
// Merges 3 sources into a unified feed: shares, installs, and new peers.
func (h *PortalHandler) HandlePortalActivityRecent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()

	// Parse limit directly — this endpoint has no offset (single merged feed).
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	// Gather from all 3 sources — track errors for fail-closed check.
	announces, errA := h.assetRepo.RecentAnnouncements(ctx, limit)
	downloads, errD := h.assetRepo.RecentDownloadEvents(ctx, limit)

	var joins []*models.Peer
	var errJ error
	if h.peerRepo != nil {
		joins, errJ = h.peerRepo.RecentlyJoined(ctx, limit)
	}

	// Fail closed: if ALL sources error, return 500.
	allFailed := errA != nil && errD != nil && (h.peerRepo == nil || errJ != nil)
	if allFailed {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to fetch activity")
		return
	}

	type rawEntry struct {
		Type   string
		Title  string
		Color  string
		PeerID string
		At     time.Time
	}
	raw := make([]rawEntry, 0, len(announces)+len(downloads)+len(joins))
	for _, a := range announces {
		raw = append(raw, rawEntry{
			Type:   "share",
			Title:  agentName(a.PeerID) + " shared " + a.Filename,
			Color:  "green",
			PeerID: a.PeerID,
			At:     a.AnnouncedAt,
		})
	}
	for _, d := range downloads {
		raw = append(raw, rawEntry{
			Type:  "install",
			Title: d.Filename + " installed",
			Color: "blue",
			At:    d.At,
		})
	}
	for _, p := range joins {
		raw = append(raw, rawEntry{
			Type:   "join",
			Title:  agentName(p.PeerID) + " joined",
			Color:  "purple",
			PeerID: p.PeerID,
			At:     p.FirstSeen,
		})
	}

	sort.Slice(raw, func(i, j int) bool { return raw[i].At.After(raw[j].At) })
	if len(raw) > limit {
		raw = raw[:limit]
	}

	entries := make([]ActivityEntry, len(raw))
	for i, e := range raw {
		entries[i] = ActivityEntry{
			Type:       e.Type,
			Title:      e.Title,
			TimeAgo:    formatTimeAgo(e.At, now),
			OccurredAt: e.At.UTC().Format(time.RFC3339),
			Color:      e.Color,
			PeerID:     e.PeerID,
		}
	}
	SendData(w, entries)
}

// AgentNamePrefix is the display prefix for anonymous peers in user-facing copy
// ("agent-XXXX"). Single source of truth: change it here, nowhere else.
const AgentNamePrefix = "agent-"

// AgentAnonName is the display name used when no peer ID is known.
const AgentAnonName = AgentNamePrefix + "anon"

// agentName produces an "agent-XXXX" short display name from a libp2p peer ID.
func agentName(peerID string) string {
	if peerID == "" {
		return AgentAnonName
	}
	const prefix = "12D3KooW"
	if strings.HasPrefix(peerID, prefix) && len(peerID) > len(prefix) {
		suffix := peerID[len(prefix):]
		if len(suffix) > 4 {
			suffix = suffix[:4]
		}
		return AgentNamePrefix + suffix
	}
	name := peerID
	if len(name) > 4 {
		name = name[:4]
	}
	return AgentNamePrefix + name
}

// formatTimeAgo converts a timestamp to a human-readable relative time string.
// Accepts now for testability (no dependency on wall clock).
func formatTimeAgo(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return strconv.Itoa(m) + " mins ago"
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return strconv.Itoa(h) + " hours ago"
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return strconv.Itoa(days) + " days ago"
	}
}
