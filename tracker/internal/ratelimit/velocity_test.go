// Package: tracker/internal/ratelimit
// Feature: F-013 (Credits & Identity)
// Story: US-013-04 (Rate Limiting)
// Purpose: Tests for velocity-based anomaly detector

package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

func TestVelocityDetector_BlocksIPOverThreshold(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	blocks := repository.NewMemoryBlockRepository()
	ctx := context.Background()

	detector := NewVelocityDetector(VelocityDetectorDeps{
		Limiter: limiter,
		Blocks:  blocks,
		Clock:   clk,
	})

	// Simulate 11 registrations from one IP (threshold = 10)
	for i := 0; i < 11; i++ {
		_, _ = limiter.Allow(ctx, "rl:reg:ip:5.6.7.8", 100, time.Hour)
	}

	blocked, err := detector.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}
	if blocked != 1 {
		t.Errorf("Scan() blocked = %d, want 1", blocked)
	}

	// Verify block exists
	isBlocked, _ := blocks.IsBlocked(ctx, "ip", "5.6.7.8", clk.Now())
	if !isBlocked {
		t.Error("Scan() should have blocked the IP")
	}
}

func TestVelocityDetector_NoBlockUnderThreshold(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	blocks := repository.NewMemoryBlockRepository()
	ctx := context.Background()

	detector := NewVelocityDetector(VelocityDetectorDeps{
		Limiter: limiter,
		Blocks:  blocks,
		Clock:   clk,
	})

	// Only 5 registrations (under threshold of 10)
	for i := 0; i < 5; i++ {
		_, _ = limiter.Allow(ctx, "rl:reg:ip:5.6.7.8", 100, time.Hour)
	}

	blocked, _ := detector.Scan(ctx)
	if blocked != 0 {
		t.Errorf("Scan() blocked = %d, want 0 (under threshold)", blocked)
	}
}

func TestVelocityDetector_BlocksSubnet24OverThreshold(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	blocks := repository.NewMemoryBlockRepository()
	ctx := context.Background()

	detector := NewVelocityDetector(VelocityDetectorDeps{
		Limiter: limiter,
		Blocks:  blocks,
		Clock:   clk,
	})

	// 51 from same /24 (threshold = 50)
	for i := 0; i < 51; i++ {
		_, _ = limiter.Allow(ctx, "rl:reg:s24:10.20.30", 200, time.Hour)
	}

	blocked, _ := detector.Scan(ctx)
	if blocked != 1 {
		t.Errorf("Scan() blocked = %d, want 1", blocked)
	}

	isBlocked, _ := blocks.IsBlocked(ctx, "subnet_24", "10.20.30", clk.Now())
	if !isBlocked {
		t.Error("Scan() should have blocked the /24 subnet")
	}
}

func TestVelocityDetector_BlocksSubnet16OverThreshold(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	blocks := repository.NewMemoryBlockRepository()
	ctx := context.Background()

	detector := NewVelocityDetector(VelocityDetectorDeps{
		Limiter: limiter,
		Blocks:  blocks,
		Clock:   clk,
	})

	// 201 from same /16 (threshold = 200)
	for i := 0; i < 201; i++ {
		_, _ = limiter.Allow(ctx, "rl:reg:s16:10.20", 500, time.Hour)
	}

	blocked, _ := detector.Scan(ctx)
	if blocked != 1 {
		t.Errorf("Scan() blocked = %d, want 1", blocked)
	}

	isBlocked, _ := blocks.IsBlocked(ctx, "subnet_16", "10.20", clk.Now())
	if !isBlocked {
		t.Error("Scan() should have blocked the /16 subnet")
	}
}

func TestVelocityDetector_IdempotentBlockInsert(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	limiter := NewMemoryLimiter(clk)
	blocks := repository.NewMemoryBlockRepository()
	ctx := context.Background()

	detector := NewVelocityDetector(VelocityDetectorDeps{
		Limiter: limiter,
		Blocks:  blocks,
		Clock:   clk,
	})

	for i := 0; i < 11; i++ {
		_, _ = limiter.Allow(ctx, "rl:reg:ip:5.6.7.8", 100, time.Hour)
	}

	// Scan twice — should not error on second
	_, _ = detector.Scan(ctx)
	blocked, err := detector.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan() second call unexpected error: %v", err)
	}
	// Already blocked, so it should not count again
	if blocked != 0 {
		t.Errorf("Scan() second call blocked = %d, want 0 (already blocked)", blocked)
	}
}
