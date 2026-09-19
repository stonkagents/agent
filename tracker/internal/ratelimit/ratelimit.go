// Package: tracker/internal/ratelimit
// Feature: F-013 (Credits & Identity)
// Story: US-013-04 (Rate Limiting)
// Purpose: Rate limiter interface and in-memory implementation for registration abuse prevention

package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

// Result holds the outcome of a rate limit check.
type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Time
	ResetAt    time.Time // When the current window expires
}

// Limiter defines the rate limiting contract. Implementations can be in-memory (testing) or Redis (production).
type Limiter interface {
	// Allow checks if a request is allowed for the given key. Increments the counter if allowed.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
	// GetCount returns the current counter for a key within the given window (does not increment).
	GetCount(ctx context.Context, key string, window time.Duration) (int, error)
	// ListKeys returns all keys matching the given prefix that have counts > 0.
	ListKeys(ctx context.Context, prefix string) ([]string, error)
}

// bucket tracks request counts within a time window.
type bucket struct {
	count     int
	windowEnd time.Time
}

// MemoryLimiter is a thread-safe in-memory rate limiter for testing.
type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	clock   clock.Clock
}

// NewMemoryLimiter creates a new in-memory rate limiter with the given clock.
func NewMemoryLimiter(clk clock.Clock) *MemoryLimiter {
	return &MemoryLimiter{
		buckets: make(map[string]*bucket),
		clock:   clk,
	}
}

func (m *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	b, ok := m.buckets[key]

	// Reset bucket if window expired or doesn't exist
	if !ok || now.After(b.windowEnd) {
		b = &bucket{
			count:     0,
			windowEnd: now.Add(window),
		}
		m.buckets[key] = b
	}

	if b.count >= limit {
		return Result{
			Allowed:    false,
			Remaining:  0,
			RetryAfter: b.windowEnd,
			ResetAt:    b.windowEnd,
		}, nil
	}

	b.count++
	remaining := limit - b.count
	return Result{
		Allowed:   true,
		Remaining: remaining,
		ResetAt:   b.windowEnd,
	}, nil
}

func (m *MemoryLimiter) GetCount(_ context.Context, key string, window time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	b, ok := m.buckets[key]
	if !ok || now.After(b.windowEnd) {
		return 0, nil
	}
	return b.count, nil
}

func (m *MemoryLimiter) ListKeys(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	var keys []string
	for k, b := range m.buckets {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix && b.count > 0 && !now.After(b.windowEnd) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
