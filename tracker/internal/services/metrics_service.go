// Package: tracker/internal/services
// Feature: F-031 (Token Data Persistence)
// Story: US-031-02 (Backend Token Metrics Aggregation)
// Purpose: Orchestrates launchpad (pump.fun | Raydium LaunchLab) + Moralis metrics with Redis caching and singleflight dedup

package services

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"golang.org/x/sync/singleflight"
)

const (
	bondingCacheTTL     = 2 * time.Minute
	graduatedCacheTTL   = 15 * time.Minute
	negativeCacheTTL    = 30 * time.Second
	degradedCacheCap    = 60 * time.Second // R13: cap all-nulls cache at 60s
	cacheKeyPrefix      = "token:metrics:"
	negativeCacheSuffix = ":neg"
)

// MetricsService aggregates token metrics from external APIs with caching.
// Each token is routed to a launchpad source (pump.fun or Raydium LaunchLab) by the
// TokenSourceResolver; the other configured source is tried when the primary reports
// "not found" or errors, so legacy rows keep working without a source column.
type MetricsService struct {
	pumpFun   PumpFunClient   // nil = disabled
	launchLab LaunchLabClient // nil = disabled
	resolver  TokenSourceResolver
	moralis   MoralisClient // nil = disabled
	holders   HolderCounter // nil = disabled
	tokenRepo repository.TokenRepository
	redis     *redis.Client // nil = in-process fallback cache (mem)
	mem       *metricsMemCache
	sf        singleflight.Group
	logger    *slog.Logger
}

// Token program ids passed to the holder counter.
const (
	tokenProgramClassic = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	tokenProgram2022    = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

// HolderCounter counts token accounts holding a mint, from chain.
type HolderCounter interface {
	CountTokenHolders(ctx context.Context, mint, tokenProgram string) (int, error)
}

// MetricsServiceDeps holds dependencies for MetricsService.
type MetricsServiceDeps struct {
	PumpFun   PumpFunClient   // nil OK when LaunchLab is set
	LaunchLab LaunchLabClient // nil OK (pump.fun only)
	// Resolver decides the source per token. nil = DefaultTokenSourceResolver(DefaultSource).
	Resolver TokenSourceResolver
	// DefaultSource is the fallback for unrecognised mints (TOKEN_METRICS_DEFAULT_SOURCE).
	// Empty = launchlab.
	DefaultSource models.TokenSource
	Moralis       MoralisClient // nil OK
	// Holders counts a mint's holders on chain when no source served a count (on-curve tokens). nil OK.
	Holders   HolderCounter
	TokenRepo repository.TokenRepository
	Redis     *redis.Client // nil OK
	Logger    *slog.Logger
}

// NewMetricsService creates a new MetricsService.
func NewMetricsService(deps MetricsServiceDeps) *MetricsService {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Resolver == nil {
		deps.Resolver = NewDefaultTokenSourceResolver(deps.DefaultSource)
	}
	return &MetricsService{
		pumpFun:   deps.PumpFun,
		launchLab: deps.LaunchLab,
		resolver:  deps.Resolver,
		moralis:   deps.Moralis,
		holders:   deps.Holders,
		tokenRepo: deps.TokenRepo,
		redis:     deps.Redis,
		mem:       newMetricsMemCache(),
		logger:    deps.Logger,
	}
}

// GetTokenMetrics returns aggregated metrics for a peer's token.
// Returns ErrNotFound if the peer has no token.
func (s *MetricsService) GetTokenMetrics(ctx context.Context, peerID string) (*models.TokenMetrics, error) {
	// Look up the token identity
	token, err := s.tokenRepo.GetByPeerID(ctx, peerID)
	if err != nil {
		return nil, err
	}
	contractAddr := token.TokenContractAddress

	// Check main cache
	if cached, ok := s.readCache(ctx, contractAddr); ok {
		return cached, nil
	}

	// Check negative cache
	if s.readNegativeCache(ctx, contractAddr) {
		return &models.TokenMetrics{}, nil
	}

	// Singleflight: dedupe concurrent fetches for same contract
	key := contractAddr
	result, err, _ := s.sf.Do(key, func() (interface{}, error) {
		return s.fetchAndCache(ctx, peerID, token)
	})
	if err != nil {
		return nil, err
	}
	return result.(*models.TokenMetrics), nil
}

// ResolveSource exposes the routing decision for a token (used by handlers/tests).
func (s *MetricsService) ResolveSource(ctx context.Context, token *models.PeerToken) TokenSourceResolution {
	return s.resolver.Resolve(ctx, token)
}

// sourceOrder returns the configured sources to try, primary first.
func (s *MetricsService) sourceOrder(primary models.TokenSource) []models.TokenSource {
	var order []models.TokenSource
	for _, src := range []models.TokenSource{primary, otherSource(primary)} {
		if s.sourceAvailable(src) {
			order = append(order, src)
		}
	}
	return order
}

func (s *MetricsService) sourceAvailable(src models.TokenSource) bool {
	switch src {
	case models.TokenSourcePumpFun:
		return s.pumpFun != nil
	case models.TokenSourceLaunchLab:
		return s.launchLab != nil
	}
	return false
}

// fetchFrom queries one source. (nil, nil) = not found on that source.
func (s *MetricsService) fetchFrom(ctx context.Context, src models.TokenSource, contractAddr string, hint LaunchLabHint) (*models.TokenMetrics, error) {
	switch src {
	case models.TokenSourcePumpFun:
		return s.pumpFun.GetCoinData(ctx, contractAddr)
	case models.TokenSourceLaunchLab:
		return s.launchLab.GetCoinData(ctx, contractAddr, hint)
	}
	return nil, nil
}

// GetMetricsByMint returns metrics for a mint that may have no peer bound to it yet
// (a recorded launch before its creator claims it). Same cache, same source order and
// same negative caching as the peer path; it just has no peer to write the image back to.
func (s *MetricsService) GetMetricsByMint(ctx context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error) {
	if mint == "" {
		return &models.TokenMetrics{}, nil
	}
	if cached, ok := s.readCache(ctx, mint); ok {
		return cached, nil
	}
	if s.readNegativeCache(ctx, mint) {
		return &models.TokenMetrics{}, nil
	}
	result, err, _ := s.sf.Do(mint, func() (interface{}, error) {
		m, _ := s.fetchMetrics(ctx, mint, models.TokenSourceLaunchLab, hint)
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*models.TokenMetrics), nil
}

func (s *MetricsService) fetchAndCache(ctx context.Context, peerID string, token *models.PeerToken) (*models.TokenMetrics, error) {
	resolution := s.resolver.Resolve(ctx, token)
	metrics, _ := s.fetchMetrics(ctx, token.TokenContractAddress, resolution.Source, resolution.Hint)

	// AC-5B: Populate token_image_url from the launchpad (fire-and-forget)
	if metrics.ImageUrl != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.tokenRepo.UpdateImageURL(bgCtx, peerID, *metrics.ImageUrl); err != nil {
				s.logger.Warn("[MetricsService] image URL update failed", "peerID", peerID, "error", err)
			}
		}()
	}
	return metrics, nil
}

// fetchMetrics queries the launchpad sources (primary first), then Moralis, and caches the
// result. A mint that is unknown to every source is negative-cached and comes back as an
// empty (never nil) TokenMetrics.
func (s *MetricsService) fetchMetrics(ctx context.Context, contractAddr string, primary models.TokenSource, hint LaunchLabHint) (*models.TokenMetrics, error) {
	var metrics *models.TokenMetrics
	var lastErr error
	for _, src := range s.sourceOrder(primary) {
		m, err := s.fetchFrom(ctx, src, contractAddr, hint)
		if err != nil {
			lastErr = err
			s.logger.Warn("[MetricsService.fetchMetrics] source failed",
				"source", src,
				"contractAddr", contractAddr,
				"error", err,
			)
			continue
		}
		if m != nil {
			metrics = m
			metrics.Source = string(src)
			break
		}
		// not found on this source → try the other one
	}

	// Not found everywhere and nothing errored: negative-cache the miss.
	if metrics == nil && lastErr == nil {
		s.writeNegativeCache(ctx, contractAddr)
		return &models.TokenMetrics{}, nil
	}

	sourceFailed := metrics == nil
	if metrics == nil {
		metrics = &models.TokenMetrics{}
	}

	// On the curve no source serves a holder count; read it from chain. Token-2022
	// first (every token we launch), the classic program for legacy mints.
	if metrics.Holders == nil && s.holders != nil && !sourceFailed {
		if n, err := s.countHolders(ctx, contractAddr); err == nil {
			metrics.Holders = &n
		} else {
			s.logger.Warn("[MetricsService] on-chain holder count failed", "contractAddr", contractAddr, "error", err)
		}
	}

	// Moralis: only for graduated tokens, or as fallback when every source failed
	if s.moralis != nil {
		graduated := metrics.Complete != nil && *metrics.Complete

		if graduated {
			// Post-graduation: fetch both holders and price
			if holders, err := s.moralis.GetTopHolders(ctx, contractAddr); err == nil {
				metrics.Holders = holders
			} else {
				s.logger.Warn("[MetricsService] Moralis holders failed", "error", err)
			}
			if price, err := s.moralis.GetTokenPrice(ctx, contractAddr); err == nil {
				if price != nil {
					metrics.PriceUsd = price
				}
			} else {
				s.logger.Warn("[MetricsService] Moralis price failed", "error", err)
			}
		} else if sourceFailed {
			// Fallback: launchpad sources failed entirely, try Moralis for price as marketCap proxy
			if price, err := s.moralis.GetTokenPrice(ctx, contractAddr); err == nil {
				metrics.PriceUsd = price
			}
		}
	}

	// Cache the result
	s.writeCache(ctx, contractAddr, metrics)

	if sourceFailed {
		// Degraded result: callers that only want data ignore this; the refresher counts it.
		return metrics, lastErr
	}
	return metrics, nil
}

// LaunchLabEnabled reports whether a LaunchLab client is configured (the refresher needs one).
func (s *MetricsService) LaunchLabEnabled() bool { return s.launchLab != nil }

// RefreshMint re-fetches LaunchLab metrics for a mint, bypassing the main and negative caches,
// and writes the result into the same cache the gallery list reads. Concurrent refreshes of one
// mint are coalesced. Returns the source error when every launchpad source failed (the
// degraded result is still cached, with the short degraded TTL).
func (s *MetricsService) RefreshMint(ctx context.Context, mint string, hint LaunchLabHint) error {
	if mint == "" {
		return nil
	}
	_, err, _ := s.sf.Do("refresh:"+mint, func() (interface{}, error) {
		return s.fetchMetrics(ctx, mint, models.TokenSourceLaunchLab, hint)
	})
	return err
}

// BatchGetCachedMetrics returns cached metrics for multiple contract addresses (Redis MGET).
// Returns a map of contractAddr → *TokenMetrics. Missing entries are omitted (not nil-valued).
// When Redis is nil, returns an empty map.
func (s *MetricsService) BatchGetCachedMetrics(ctx context.Context, contractAddrs []string) map[string]*models.TokenMetrics {
	result := make(map[string]*models.TokenMetrics)
	if len(contractAddrs) == 0 {
		return result
	}
	if s.redis == nil {
		now := time.Now()
		for _, addr := range contractAddrs {
			if m, ok := s.mem.get(s.cacheKey(addr), now); ok && m != nil {
				result[addr] = m
			}
		}
		return result
	}

	// Build cache keys
	keys := make([]string, len(contractAddrs))
	for i, addr := range contractAddrs {
		keys[i] = s.cacheKey(addr)
	}

	// Redis MGET — returns []interface{} with nil for misses
	vals, err := s.redis.MGet(ctx, keys...).Result()
	if err != nil {
		s.logger.Warn("[MetricsService.BatchGetCachedMetrics] Redis MGET failed", "error", err)
		return result
	}

	for i, val := range vals {
		if val == nil {
			continue
		}
		str, ok := val.(string)
		if !ok {
			continue
		}
		var m models.TokenMetrics
		if err := json.Unmarshal([]byte(str), &m); err != nil {
			continue
		}
		result[contractAddrs[i]] = &m
	}

	return result
}

// --- Cache helpers ---

func (s *MetricsService) cacheKey(contractAddr string) string {
	return cacheKeyPrefix + contractAddr
}

func (s *MetricsService) readCache(ctx context.Context, contractAddr string) (*models.TokenMetrics, bool) {
	if s.redis == nil {
		m, ok := s.mem.get(s.cacheKey(contractAddr), time.Now())
		return m, ok && m != nil
	}
	data, err := s.redis.Get(ctx, s.cacheKey(contractAddr)).Bytes()
	if err != nil {
		return nil, false
	}
	var m models.TokenMetrics
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	return &m, true
}

func (s *MetricsService) readNegativeCache(ctx context.Context, contractAddr string) bool {
	if s.redis == nil {
		_, ok := s.mem.get(s.cacheKey(contractAddr)+negativeCacheSuffix, time.Now())
		return ok
	}
	_, err := s.redis.Get(ctx, s.cacheKey(contractAddr)+negativeCacheSuffix).Result()
	return err == nil
}

func (s *MetricsService) writeCache(ctx context.Context, contractAddr string, metrics *models.TokenMetrics) {
	ttl := bondingCacheTTL
	if metrics.Complete != nil && *metrics.Complete {
		ttl = graduatedCacheTTL
	}

	// R13: Degraded cache — all-nulls result gets capped TTL for faster recovery
	if s.isDegraded(metrics) {
		if ttl > degradedCacheCap {
			ttl = degradedCacheCap
		}
	}

	if s.redis == nil {
		s.mem.set(s.cacheKey(contractAddr), metrics, ttl, time.Now())
		return
	}
	data, err := json.Marshal(metrics)
	if err != nil {
		s.logger.Warn("[MetricsService] cache marshal failed", "error", err)
		return
	}
	if err := s.redis.Set(ctx, s.cacheKey(contractAddr), data, ttl).Err(); err != nil {
		s.logger.Warn("[MetricsService] cache write failed", "error", err)
	}
}

func (s *MetricsService) writeNegativeCache(ctx context.Context, contractAddr string) {
	if s.redis == nil {
		s.mem.set(s.cacheKey(contractAddr)+negativeCacheSuffix, nil, negativeCacheTTL, time.Now())
		return
	}
	_ = s.redis.Set(ctx, s.cacheKey(contractAddr)+negativeCacheSuffix, "1", negativeCacheTTL).Err()
}

// isDegraded returns true if all metric fields are nil (every source failed).
func (s *MetricsService) isDegraded(m *models.TokenMetrics) bool {
	return m.MarketCapUsd == nil && m.SolRaised == nil && m.BondingCurvePercent == nil &&
		m.Complete == nil && m.CreatedAt == nil && m.ImageUrl == nil &&
		m.Holders == nil && m.PriceUsd == nil &&
		m.QuoteRaised == nil && m.PriceQuote == nil && m.PoolId == nil
}

// countHolders tries the Token-2022 program, then the classic token program.
func (s *MetricsService) countHolders(ctx context.Context, mint string) (int, error) {
	n, err := s.holders.CountTokenHolders(ctx, mint, tokenProgram2022)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return n, nil
	}
	return s.holders.CountTokenHolders(ctx, mint, tokenProgramClassic)
}
