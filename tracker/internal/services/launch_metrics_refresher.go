// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Keep the metrics cache warm for every recorded launch so gallery/trending cards
//          (GET /api/launches, cache-only) show market cap, curve progress and holders without
//          anyone opening a detail page first.

package services

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Refresher defaults (env METRICS_REFRESH_INTERVAL / METRICS_REFRESH_CONCURRENCY override the first two).
const (
	DefaultMetricsRefreshInterval    = 60 * time.Second
	DefaultMetricsRefreshConcurrency = 4
	DefaultMetricsRefreshMintTimeout = 6 * time.Second
	metricsRefreshPageSize           = 100
	metricsRefreshTriggerBuffer      = 256
)

// MetricsRefreshSource re-fetches one mint into the shared cache (implemented by *MetricsService).
type MetricsRefreshSource interface {
	RefreshMint(ctx context.Context, mint string, hint LaunchLabHint) error
}

// LaunchMetricsTrigger requests an immediate, asynchronous refresh of one launch.
// Implemented by *LaunchMetricsRefresher; LaunchService calls it after a launch is recorded.
type LaunchMetricsTrigger interface {
	TriggerRefresh(mint string, hint LaunchLabHint)
}

// LaunchMetricsRefresherConfig tunes the refresher. Zero values take the defaults above.
type LaunchMetricsRefresherConfig struct {
	Interval    time.Duration
	Concurrency int
	MintTimeout time.Duration
	Logger      *slog.Logger
}

// RefreshStats summarises one refresh cycle.
type RefreshStats struct {
	Launches int
	Failures int
	Duration time.Duration
}

// LaunchMetricsRefresher walks every recorded launch on a fixed interval and refreshes its
// LaunchLab metrics with bounded concurrency; single mints can also be refreshed on demand.
type LaunchMetricsRefresher struct {
	launches repository.LaunchRepository
	source   MetricsRefreshSource
	cfg      LaunchMetricsRefresherConfig
	logger   *slog.Logger
	trigger  chan refreshRequest
	// sem bounds in-flight fetches across cycles and on-demand triggers alike.
	sem chan struct{}
}

type refreshRequest struct {
	mint string
	hint LaunchLabHint
}

// NewLaunchMetricsRefresher creates a refresher; call Run to start it.
func NewLaunchMetricsRefresher(launches repository.LaunchRepository, source MetricsRefreshSource, cfg LaunchMetricsRefresherConfig) *LaunchMetricsRefresher {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultMetricsRefreshInterval
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultMetricsRefreshConcurrency
	}
	if cfg.MintTimeout <= 0 {
		cfg.MintTimeout = DefaultMetricsRefreshMintTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &LaunchMetricsRefresher{
		launches: launches, source: source, cfg: cfg, logger: cfg.Logger,
		trigger: make(chan refreshRequest, metricsRefreshTriggerBuffer),
		sem:     make(chan struct{}, cfg.Concurrency),
	}
}

// TriggerRefresh queues an immediate refresh for one mint. Never blocks: when the queue is
// full the mint is simply picked up by the next periodic cycle.
func (r *LaunchMetricsRefresher) TriggerRefresh(mint string, hint LaunchLabHint) {
	if r == nil || mint == "" {
		return
	}
	select {
	case r.trigger <- refreshRequest{mint: mint, hint: hint}:
	default:
		r.logger.Debug("[metrics-refresh] trigger queue full, deferring to next cycle", "mint", mint)
	}
}

// Run blocks until ctx is done: one full cycle immediately, then every Interval, plus
// on-demand single-mint refreshes from TriggerRefresh.
func (r *LaunchMetricsRefresher) Run(ctx context.Context) {
	r.logCycle(r.RefreshAll(ctx))
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.logCycle(r.RefreshAll(ctx))
		case req := <-r.trigger:
			go r.refreshOne(ctx, req.mint, req.hint)
		}
	}
}

// RefreshAll refreshes every recorded launch once, paging through the repository.
func (r *LaunchMetricsRefresher) RefreshAll(ctx context.Context) RefreshStats {
	start := time.Now()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		launches int
		failures int
	)
	for offset := 0; ctx.Err() == nil; offset += metricsRefreshPageSize {
		page, _, err := r.launches.List(ctx, repository.ListLaunchesOptions{Limit: metricsRefreshPageSize, Offset: offset})
		if err != nil {
			r.logger.Warn("[metrics-refresh] listing launches failed", "offset", offset, "error", err)
			break
		}
		for _, l := range page {
			launches++
			wg.Add(1)
			go func(mint string, hint LaunchLabHint) {
				defer wg.Done()
				if !r.refreshOne(ctx, mint, hint) {
					mu.Lock()
					failures++
					mu.Unlock()
				}
			}(l.Mint, LaunchLabHint{PoolID: l.PoolID, QuoteMint: l.QuoteMint})
		}
		if len(page) < metricsRefreshPageSize {
			break
		}
	}
	wg.Wait()
	return RefreshStats{Launches: launches, Failures: failures, Duration: time.Since(start)}
}

// refreshOne refreshes a single mint under the concurrency limit and per-mint timeout.
// Returns false when the refresh failed (or the parent context ended first).
func (r *LaunchMetricsRefresher) refreshOne(ctx context.Context, mint string, hint LaunchLabHint) bool {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-r.sem }()

	mintCtx, cancel := context.WithTimeout(ctx, r.cfg.MintTimeout)
	defer cancel()
	if err := r.source.RefreshMint(mintCtx, mint, hint); err != nil {
		r.logger.Debug("[metrics-refresh] mint refresh failed", "mint", mint, "error", err)
		return false
	}
	return true
}

func (r *LaunchMetricsRefresher) logCycle(st RefreshStats) {
	r.logger.Info("[metrics-refresh] cycle complete",
		"launches", st.Launches, "failures", st.Failures, "duration", st.Duration.Round(time.Millisecond))
}
