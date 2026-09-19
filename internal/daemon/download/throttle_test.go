// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: TDD tests for bandwidth throttling (token bucket algorithm)

package download

import (
	"testing"
	"time"
)

// TestThrottle_UnlimitedBandwidth - RED test
// Acceptance Criterion: Bandwidth limit 0 = unlimited (no throttling)
func TestThrottle_UnlimitedBandwidth(t *testing.T) {
	// Arrange - unlimited bandwidth (0 = no limit)
	throttle := NewThrottle(0)

	// Act - request 1 MB instantly
	canProceed := throttle.Allow(1048576)

	// Assert - should allow immediately
	if !canProceed {
		t.Error("Unlimited bandwidth should allow any amount instantly")
	}
}

// TestThrottle_EnforcesRateLimit - RED test
// Acceptance Criterion: Bandwidth throttling enforces max bytes per second
func TestThrottle_EnforcesRateLimit(t *testing.T) {
	// Arrange - 1 MB/s limit (1048576 bytes/sec)
	throttle := NewThrottle(1048576)

	// Act - request 512 KB (half the limit)
	allowed := throttle.Allow(524288)

	// Assert - should allow (tokens available)
	if !allowed {
		t.Error("First request should be allowed within rate limit")
	}

	// Act - immediately request another 512 KB (would exceed 1 MB/s)
	allowed = throttle.Allow(524288)

	// Assert - should allow (total 1 MB in bucket)
	if !allowed {
		t.Error("Second request should be allowed (total = 1 MB)")
	}

	// Act - immediately request another 1 KB (exceeds bucket)
	allowed = throttle.Allow(1024)

	// Assert - should deny (bucket depleted)
	if allowed {
		t.Error("Third request should be denied (bucket empty)")
	}
}

// TestThrottle_RefillsTokens - RED test
// Acceptance Criterion: Token bucket refills at configured rate
func TestThrottle_RefillsTokens(t *testing.T) {
	// Arrange - 1 MB/s limit
	throttle := NewThrottle(1048576)

	// Act - consume all tokens
	throttle.Allow(1048576)

	// Immediately try to get more tokens (should fail)
	if throttle.Allow(1024) {
		t.Error("Should deny immediately after depleting bucket")
	}

	// Wait 100ms (refill should add ~102 KB of tokens)
	time.Sleep(100 * time.Millisecond)

	// Try to get 50 KB (should succeed after refill)
	if !throttle.Allow(51200) {
		t.Error("Should allow request after token refill")
	}
}

// TestThrottle_BurstAllowance - RED test
// Acceptance Criterion: Bucket size = 1 second worth of bandwidth (burst allowance)
func TestThrottle_BurstAllowance(t *testing.T) {
	// Arrange - 100 KB/s limit → bucket capacity = 100 KB
	// Using lower rate to avoid flakiness from sub-millisecond refills
	throttle := NewThrottle(102400)

	// Act - request entire bucket at once (burst)
	allowed := throttle.Allow(102400)

	// Assert - should allow burst up to bucket capacity
	if !allowed {
		t.Error("Burst up to bucket capacity should be allowed")
	}

	// Act - request more (exceeds capacity)
	allowed = throttle.Allow(1024)

	// Assert - should deny (bucket empty)
	if allowed {
		t.Error("Request exceeding bucket capacity should be denied")
	}
}

// TestThrottle_GetAvailableTokens - RED test
// Acceptance Criterion: Report how many bytes can be downloaded without waiting
func TestThrottle_GetAvailableTokens(t *testing.T) {
	// Arrange - 100 KB/s limit (using lower rate to avoid flakiness)
	throttle := NewThrottle(102400)

	// Assert - initially full bucket (100 KB available)
	available := throttle.GetAvailableTokens()
	if available != 102400 {
		t.Errorf("Expected 102400 available tokens, got %d", available)
	}

	// Act - consume 50 KB
	throttle.Allow(51200)

	// Assert - approximately 50 KB remaining (allow small refill)
	available = throttle.GetAvailableTokens()
	expectedMin := int64(51200 - 100) // Allow 100 byte tolerance for refill
	expectedMax := int64(51200 + 100)
	if available < expectedMin || available > expectedMax {
		t.Errorf("Expected ~51200 available tokens, got %d", available)
	}
}

// TestThrottle_WaitDuration - RED test
// Acceptance Criterion: Calculate how long to wait for N bytes to become available
func TestThrottle_WaitDuration(t *testing.T) {
	// Arrange - 1 MB/s limit
	throttle := NewThrottle(1048576)

	// Deplete bucket
	throttle.Allow(1048576)

	// Act - calculate wait time for 524 KB (half the limit)
	// At 1 MB/s, 524 KB requires 0.5 seconds
	waitTime := throttle.WaitDuration(524288)

	// Assert - should be approximately 500ms
	expectedMin := 400 * time.Millisecond // Allow 100ms tolerance
	expectedMax := 600 * time.Millisecond
	if waitTime < expectedMin || waitTime > expectedMax {
		t.Errorf("Expected wait time ~500ms, got %v", waitTime)
	}
}

// TestThrottle_ConcurrentAccess - RED test
// Acceptance Criterion: Thread-safe for concurrent downloads
func TestThrottle_ConcurrentAccess(t *testing.T) {
	// Arrange - 1 MB/s limit
	throttle := NewThrottle(1048576)

	// Act - 10 goroutines each request 100 KB concurrently
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			throttle.Allow(102400) // 100 KB
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Assert - should not panic or race (primary goal)
	// Check available tokens is less than full bucket (tokens were consumed)
	available := throttle.GetAvailableTokens()
	if available >= 1048576 {
		t.Errorf("Expected tokens to be consumed, but %d tokens available (full bucket)", available)
	}

	// Tokens should be significantly depleted (less than 50% remaining)
	// Note: Small refill during concurrent execution is expected
	if available > 524288 {
		t.Errorf("Expected significant token consumption, but %d tokens still available", available)
	}
}
