// Package: tracker/internal/ratelimit
// Feature: F-013 (Credits & Identity)
// Story: US-013-04 (Rate Limiting)
// Purpose: Tests for rate limiter interface and in-memory implementation

package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

func TestMemoryLimiter_AllowUnderLimit(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	result, err := limiter.Allow(ctx, "rl:test:1.2.3.4", 5, time.Hour)
	if err != nil {
		t.Fatalf("Allow() unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Error("Allow() should be allowed under limit")
	}
	if result.Remaining != 4 {
		t.Errorf("Allow() Remaining = %d, want 4", result.Remaining)
	}
}

func TestMemoryLimiter_AllowExactlyAtLimit(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	// Use 4 of 5 allowed
	for i := 0; i < 4; i++ {
		_, _ = limiter.Allow(ctx, "rl:test:key", 5, time.Hour)
	}

	// 5th should be allowed (exactly at limit)
	result, _ := limiter.Allow(ctx, "rl:test:key", 5, time.Hour)
	if !result.Allowed {
		t.Error("Allow() 5th request should be allowed (at limit)")
	}
	if result.Remaining != 0 {
		t.Errorf("Allow() Remaining = %d, want 0", result.Remaining)
	}
}

func TestMemoryLimiter_DenyOverLimit(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	// Exhaust limit
	for i := 0; i < 5; i++ {
		_, _ = limiter.Allow(ctx, "rl:test:key", 5, time.Hour)
	}

	// 6th should be denied
	result, _ := limiter.Allow(ctx, "rl:test:key", 5, time.Hour)
	if result.Allowed {
		t.Error("Allow() 6th request should be denied (over limit)")
	}
	if result.Remaining != 0 {
		t.Errorf("Allow() Remaining = %d, want 0", result.Remaining)
	}
	if result.RetryAfter.IsZero() {
		t.Error("Allow() RetryAfter should be non-zero when denied")
	}
}

func TestMemoryLimiter_WindowExpiry(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	// Exhaust limit
	for i := 0; i < 5; i++ {
		_, _ = limiter.Allow(ctx, "rl:test:key", 5, time.Hour)
	}

	// Advance past the window
	clk.Advance(61 * time.Minute)

	// Should be allowed again
	result, _ := limiter.Allow(ctx, "rl:test:key", 5, time.Hour)
	if !result.Allowed {
		t.Error("Allow() should be allowed after window expiry")
	}
	if result.Remaining != 4 {
		t.Errorf("Allow() Remaining = %d, want 4 (fresh window)", result.Remaining)
	}
}

func TestMemoryLimiter_IndependentKeys(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	// Exhaust limit for key1
	for i := 0; i < 5; i++ {
		_, _ = limiter.Allow(ctx, "rl:test:key1", 5, time.Hour)
	}

	// Key2 should still be allowed
	result, _ := limiter.Allow(ctx, "rl:test:key2", 5, time.Hour)
	if !result.Allowed {
		t.Error("Allow() different key should be allowed independently")
	}
}

func TestMemoryLimiter_GetCount(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	// Make 3 requests
	for i := 0; i < 3; i++ {
		_, _ = limiter.Allow(ctx, "rl:test:key", 10, time.Hour)
	}

	count, err := limiter.GetCount(ctx, "rl:test:key", time.Hour)
	if err != nil {
		t.Fatalf("GetCount() unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("GetCount() = %d, want 3", count)
	}
}

func TestMemoryLimiter_GetCountAfterExpiry(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _ = limiter.Allow(ctx, "rl:test:key", 10, time.Hour)
	}

	clk.Advance(61 * time.Minute)

	count, _ := limiter.GetCount(ctx, "rl:test:key", time.Hour)
	if count != 0 {
		t.Errorf("GetCount() = %d, want 0 after window expiry", count)
	}
}
