// Package: tracker/internal/services
// Feature: StonkAgents launchpad (token metrics)
// Purpose: In-process fallback for the token metrics cache when REDIS_URL is unset, so the
//          background refresher and the gallery list share a cache in every deployment.

package services

import (
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// metricsMemCache is a TTL map keyed like the Redis cache (contract address, "…:neg" for
// negative entries). It is only consulted when MetricsService has no Redis client.
type metricsMemCache struct {
	mu      sync.RWMutex
	entries map[string]memCacheEntry
}

type memCacheEntry struct {
	metrics *models.TokenMetrics // nil for a negative entry
	expires time.Time
}

func newMetricsMemCache() *metricsMemCache {
	return &metricsMemCache{entries: make(map[string]memCacheEntry)}
}

// get returns a copy of the cached metrics for key, or false when absent or expired.
func (c *metricsMemCache) get(key string, now time.Time) (*models.TokenMetrics, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || now.After(e.expires) {
		return nil, false
	}
	if e.metrics == nil {
		return nil, true
	}
	copied := *e.metrics
	return &copied, true
}

// set stores metrics (nil = negative entry) for key with the given TTL.
func (c *metricsMemCache) set(key string, metrics *models.TokenMetrics, ttl time.Duration, now time.Time) {
	var stored *models.TokenMetrics
	if metrics != nil {
		copied := *metrics
		stored = &copied
	}
	c.mu.Lock()
	c.entries[key] = memCacheEntry{metrics: stored, expires: now.Add(ttl)}
	// Opportunistic sweep so a long-running tracker does not accumulate dead keys.
	if len(c.entries) > 4096 {
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
	}
	c.mu.Unlock()
}
