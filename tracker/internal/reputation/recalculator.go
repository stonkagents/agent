// Package: tracker/internal/reputation
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: Background reputation recalculation job

package reputation

import (
	"context"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

// DefaultRecalcInterval is the default interval for background recalculation.
const DefaultRecalcInterval = 1 * time.Hour

// StatsProvider retrieves peer statistics for reputation calculation.
type StatsProvider interface {
	GetPeerStats(ctx context.Context, peerID string) (*PeerStats, error)
	AllPeerIDs(ctx context.Context) ([]string, error)
}

// ReputationStore persists reputation records.
type ReputationStore interface {
	Upsert(ctx context.Context, record *ReputationRecord) error
	FindByPeerID(ctx context.Context, peerID string) (*ReputationRecord, error)
}

// Recalculator handles periodic reputation recalculation for all peers.
type Recalculator struct {
	stats StatsProvider
	store ReputationStore
	clk   clock.Clock

	// OnRecalculate is an optional callback invoked after each recalculation cycle.
	// Used for testing to count recalculation runs.
	OnRecalculate func()
}

// NewRecalculator creates a new Recalculator.
func NewRecalculator(stats StatsProvider, store ReputationStore, clk clock.Clock) *Recalculator {
	return &Recalculator{
		stats: stats,
		store: store,
		clk:   clk,
	}
}

// RecalculateAll recalculates reputation for all known peers.
func (r *Recalculator) RecalculateAll(ctx context.Context) error {
	peerIDs, err := r.stats.AllPeerIDs(ctx)
	if err != nil {
		return err
	}

	for _, peerID := range peerIDs {
		if err := r.RecalculatePeer(ctx, peerID); err != nil {
			return err
		}
	}

	return nil
}

// RecalculatePeer recalculates reputation for a single peer.
func (r *Recalculator) RecalculatePeer(ctx context.Context, peerID string) error {
	ps, err := r.stats.GetPeerStats(ctx, peerID)
	if err != nil {
		return err
	}

	record := CalculateReputation(ps, r.clk)
	return r.store.Upsert(ctx, record)
}

// Start begins the background recalculation loop with the given interval.
// The loop runs until the context is cancelled.
func (r *Recalculator) Start(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = r.RecalculateAll(ctx)
				if r.OnRecalculate != nil {
					r.OnRecalculate()
				}
			}
		}
	}()
}
