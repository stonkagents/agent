// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: Bandwidth throttling using token bucket algorithm

package download

import (
	"context"
	"sync"
	"time"
)

// Throttle implements bandwidth throttling using token bucket algorithm
type Throttle struct {
	rateLimit      int64     // Bytes per second (0 = unlimited)
	bucketCapacity int64     // Maximum tokens in bucket (= rateLimit for 1 second burst)
	tokens         int64     // Available tokens; negative while a Wait debt is being paid back
	lastRefill     time.Time // Last time bucket was refilled
	mu             sync.Mutex

	// Clock seams (time.Now / time.After) so Wait's pacing is testable
	// without sleeping.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

// NewThrottle creates a new bandwidth throttle
// rateLimit: bytes per second (0 = unlimited)
func NewThrottle(rateLimit int64) *Throttle {
	t := &Throttle{now: time.Now, after: time.After}
	t.lastRefill = t.now()
	if rateLimit <= 0 {
		// Unlimited bandwidth - no throttling
		return t
	}
	t.rateLimit = rateLimit
	t.bucketCapacity = rateLimit // 1 second burst allowance
	t.tokens = rateLimit         // Start with full bucket
	return t
}

// Allow checks if N bytes can be downloaded without exceeding rate limit
// Returns true if allowed, false if bucket has insufficient tokens
func (t *Throttle) Allow(bytes int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Unlimited bandwidth - always allow
	if t.rateLimit == 0 {
		return true
	}

	// Refill tokens based on time elapsed
	t.refillLocked()

	// Check if enough tokens available
	if t.tokens >= bytes {
		t.tokens -= bytes
		return true
	}

	return false
}

// GetAvailableTokens returns how many bytes can be downloaded without waiting
func (t *Throttle) GetAvailableTokens() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Unlimited bandwidth
	if t.rateLimit == 0 {
		return 1 << 60 // Return very large number for unlimited
	}

	// Refill before reporting
	t.refillLocked()

	return t.tokens
}

// WaitDuration calculates how long to wait for N bytes to become available
func (t *Throttle) WaitDuration(bytes int64) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Unlimited bandwidth - no wait
	if t.rateLimit == 0 {
		return 0
	}

	// Refill first
	t.refillLocked()

	// If enough tokens available, no wait needed
	if t.tokens >= bytes {
		return 0
	}

	// Calculate shortfall
	shortfall := bytes - t.tokens

	// Calculate wait time: shortfall / rate
	waitNanoseconds := (shortfall * int64(time.Second)) / t.rateLimit

	return time.Duration(waitNanoseconds)
}

// refillLocked refills tokens based on time elapsed (caller must hold lock)
func (t *Throttle) refillLocked() {
	now := t.now()
	elapsed := now.Sub(t.lastRefill)

	// Calculate tokens to add based on elapsed time
	tokensToAdd := (int64(elapsed) * t.rateLimit) / int64(time.Second)

	if tokensToAdd > 0 {
		t.tokens += tokensToAdd

		// Cap at bucket capacity (prevent infinite accumulation)
		if t.tokens > t.bucketCapacity {
			t.tokens = t.bucketCapacity
		}

		t.lastRefill = now
	}
}

// Wait debits bytes tokens and blocks until the bucket has paid the debt back
// at the configured rate (or ctx ends, in which case the debit is refunded).
// The full size is always charged, so a chunk larger than the bucket costs
// size/rate seconds instead of a single bucket: the effective rate never
// exceeds the cap regardless of chunk size.
func (t *Throttle) Wait(ctx context.Context, bytes int64) error {
	if t == nil || bytes <= 0 {
		return nil
	}
	t.mu.Lock()
	if t.rateLimit == 0 {
		t.mu.Unlock()
		return nil
	}
	t.refillLocked()
	t.tokens -= bytes
	deficit := -t.tokens
	rate := t.rateLimit
	t.mu.Unlock()
	if deficit <= 0 {
		return nil
	}
	wait := time.Duration((deficit * int64(time.Second)) / rate)
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	select {
	case <-ctx.Done():
		t.mu.Lock()
		t.tokens += bytes // not sent; give the tokens back
		t.mu.Unlock()
		return ctx.Err()
	case <-t.after(wait):
		return nil
	}
}
