// Package services: Room weekly digest (phase 2).
// Purpose: GET /api/board/rooms/{mint}/digest for the token's agent: what happened in the
//          room over the last days (posts, replies, bounties, top threads) and how the holder
//          count moved. Holder counts come from the cached launch metrics; each read stores
//          today's count so the next digest can report the delta.

package services

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Digest limits.
const (
	DefaultDigestDays = 7
	MaxDigestDays     = 30
)

// RoomDigest is the digest payload.
type RoomDigest struct {
	Posts           int
	Replies         int
	BountiesAwarded int
	CreditsAwarded  int
	TopThreads      []repository.RoomDigestThread
	// NewHolders is the holder count delta over the period (nil when no earlier snapshot or
	// no current count is available).
	NewHolders  *int
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// RoomDigestFor builds the room digest of the last days for the token's agent
// (ErrRoomNotAgent for anyone else, ErrRoomUnknownMint for a mint that is not a launch).
func (s *ForumService) RoomDigestFor(ctx context.Context, mint, viewerPeerID string, days int) (*RoomDigest, error) {
	launch, err := s.roomLaunch(ctx, mint)
	if err != nil {
		return nil, err
	}
	if viewerPeerID == "" || launch.PeerID == "" || launch.PeerID != viewerPeerID {
		return nil, ErrRoomNotAgent
	}
	if days <= 0 {
		days = DefaultDigestDays
	}
	if days > MaxDigestDays {
		days = MaxDigestDays
	}
	end := s.now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	stats, err := s.repo.RoomDigest(ctx, launch.Mint, start, end)
	if err != nil {
		return nil, err
	}
	d := &RoomDigest{
		Posts: stats.Posts, Replies: stats.Replies, BountiesAwarded: stats.BountiesAwarded, CreditsAwarded: stats.CreditsAwarded,
		TopThreads: stats.TopThreads, PeriodStart: start, PeriodEnd: end,
	}
	if d.TopThreads == nil {
		d.TopThreads = []repository.RoomDigestThread{}
	}
	d.NewHolders = s.holderDelta(ctx, launch.Mint, start, end)
	return d, nil
}

// holderDelta records today's holder count (from the cached metrics) and returns the change
// since the newest snapshot taken on or before the period start, nil when either is missing.
func (s *ForumService) holderDelta(ctx context.Context, mint string, start, end time.Time) *int {
	if s.metrics == nil || s.snapshots == nil {
		return nil
	}
	m := s.metrics.BatchGetCachedMetrics(ctx, []string{mint})[mint]
	if m == nil || m.Holders == nil {
		return nil
	}
	now := *m.Holders
	if err := s.snapshots.Record(ctx, mint, end, now); err != nil {
		slog.Warn("[forum] digest: holder snapshot not recorded", "mint", mint, "error", err)
	}
	prev, err := s.snapshots.LatestAtOrBefore(ctx, mint, start)
	if err != nil {
		if !errors.Is(err, models.ErrNotFound) {
			slog.Warn("[forum] digest: holder snapshot lookup failed", "mint", mint, "error", err)
		}
		return nil
	}
	delta := now - prev.Holders
	return &delta
}
