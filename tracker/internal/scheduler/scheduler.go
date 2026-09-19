// Package: tracker/internal/scheduler
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: Cron scheduler for daily reputation snapshots + weekly tier bonuses (TD-056)

package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// Weekly bonus amounts by tier (Credit Economy v1.5).
const (
	BonusSilver = 20
	BonusGold   = 50
	BonusOG     = 75
)

// StatsProvider retrieves peer statistics (mirrors reputation.StatsProvider).
type StatsProvider interface {
	GetPeerStats(ctx context.Context, peerID string) (*reputation.PeerStats, error)
	AllPeerIDs(ctx context.Context) ([]string, error)
}

// ReputationStore persists and reads reputation records.
type ReputationStore interface {
	Upsert(ctx context.Context, record *reputation.ReputationRecord) error
	FindByPeerID(ctx context.Context, peerID string) (*reputation.ReputationRecord, error)
}

// SnapshotRepo persists daily reputation snapshots.
type SnapshotRepo interface {
	Upsert(ctx context.Context, snapshot *reputation.ReputationSnapshot) error
}

// AccountRepo looks up accounts by peer ID.
type AccountRepo interface {
	GetByPeerID(ctx context.Context, peerID string) (*models.Account, error)
}

// CreditRepo grants free credits.
type CreditRepo interface {
	CreditFree(ctx context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error
}

// Config holds the scheduler's dependencies.
type Config struct {
	StatsProvider StatsProvider
	RepStore      ReputationStore
	SnapshotRepo  SnapshotRepo
	AccountRepo   AccountRepo
	CreditRepo    CreditRepo
	Clock         clock.Clock
}

// Scheduler runs periodic jobs for reputation and credits.
type Scheduler struct {
	cfg Config
	clk clock.Clock
}

// New creates a new Scheduler.
func New(cfg Config) *Scheduler {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &Scheduler{cfg: cfg, clk: clk}
}

// RunDailySnapshot recalculates all peer reputations and snapshots composite scores.
func (s *Scheduler) RunDailySnapshot(ctx context.Context) error {
	peerIDs, err := s.cfg.StatsProvider.AllPeerIDs(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list peers: %w", err)
	}

	recalc := reputation.NewRecalculator(s.cfg.StatsProvider, s.cfg.RepStore, s.clk)
	if err := recalc.RecalculateAll(ctx); err != nil {
		return fmt.Errorf("scheduler: recalculate: %w", err)
	}

	now := s.clk.Now()
	for _, peerID := range peerIDs {
		rec, err := s.cfg.RepStore.FindByPeerID(ctx, peerID)
		if err != nil {
			slog.Warn("[Scheduler] reputation lookup failed", "peer_id", peerID, "error", err)
			continue
		}
		if rec == nil {
			continue
		}

		snap := &reputation.ReputationSnapshot{
			PeerID:         peerID,
			CompositeScore: rec.CompositeScore,
			SnappedAt:      now,
		}
		if err := s.cfg.SnapshotRepo.Upsert(ctx, snap); err != nil {
			slog.Warn("[Scheduler] snapshot upsert failed", "peer_id", peerID, "error", err)
		}
	}

	slog.Info("[Scheduler] daily snapshot complete", "peers", len(peerIDs))
	return nil
}

// RunWeeklyBonus grants credits to peers with Silver, Gold, or OG tier.
func (s *Scheduler) RunWeeklyBonus(ctx context.Context) error {
	peerIDs, err := s.cfg.StatsProvider.AllPeerIDs(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list peers: %w", err)
	}

	granted := 0
	for _, peerID := range peerIDs {
		rec, err := s.cfg.RepStore.FindByPeerID(ctx, peerID)
		if err != nil {
			slog.Warn("[Scheduler] reputation lookup failed", "peer_id", peerID, "error", err)
			continue
		}
		if rec == nil {
			continue
		}

		bonus := tierBonus(rec.CompositeScore)
		if bonus == 0 {
			continue
		}

		account, err := s.cfg.AccountRepo.GetByPeerID(ctx, peerID)
		if err != nil {
			slog.Warn("[Scheduler] account lookup failed", "peer_id", peerID, "error", err)
			continue
		}
		if account == nil {
			continue
		}

		requestID := fmt.Sprintf("weekly-bonus-%s-%s", peerID, s.clk.Now().Format("2006-01-02"))
		expiresAt := s.clk.Now().Add(30 * 24 * time.Hour)

		if err := s.cfg.CreditRepo.CreditFree(ctx, account.ID, bonus, "weekly_tier_bonus", requestID, expiresAt); err != nil {
			slog.Warn("[Scheduler] credit grant failed", "peer_id", peerID, "amount", bonus, "error", err)
			continue
		}
		granted++
	}

	slog.Info("[Scheduler] weekly bonus complete", "peers_checked", len(peerIDs), "bonuses_granted", granted)
	return nil
}

// tierBonus returns the weekly bonus for a composite score. Returns 0 for Bronze and New.
func tierBonus(composite float64) int {
	switch {
	case composite >= reputation.RankOGThreshold:
		return BonusOG
	case composite >= reputation.RankGoldThreshold:
		return BonusGold
	case composite >= reputation.RankSilverThreshold:
		return BonusSilver
	default:
		return 0
	}
}
