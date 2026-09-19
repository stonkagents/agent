package download

import (
	"context"
	"testing"
	"time"
)

func TestThrottleWait_UnlimitedAndNil(t *testing.T) {
	var nilT *Throttle
	if err := nilT.Wait(context.Background(), 1<<20); err != nil {
		t.Fatalf("nil throttle: %v", err)
	}
	if err := NewThrottle(0).Wait(context.Background(), 1<<20); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
}

func TestThrottleWait_PacesAndHonoursContext(t *testing.T) {
	th := NewThrottle(1000) // 1000 B/s, bucket 1000
	start := time.Now()
	if err := th.Wait(context.Background(), 1000); err != nil { // drains the full bucket
		t.Fatal(err)
	}
	if err := th.Wait(context.Background(), 200); err != nil { // needs ~200ms of refill
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("second wait returned after %v; expected pacing", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := th.Wait(ctx, 5000); err == nil { // oversized: needs a full bucket (~1s)
		t.Error("expected context deadline error")
	}
}

// fakeClock replaces the throttle's time.Now/time.After so pacing can be
// asserted exactly without sleeping. Every After call advances the clock by
// the requested duration and fires immediately.
func fakeClock(th *Throttle) (slept *time.Duration) {
	clock := time.Unix(1_700_000_000, 0)
	slept = new(time.Duration)
	th.lastRefill = clock
	th.now = func() time.Time { return clock }
	th.after = func(d time.Duration) <-chan time.Time {
		*slept += d
		clock = clock.Add(d)
		ch := make(chan time.Time, 1)
		ch <- clock
		return ch
	}
	return slept
}

func TestThrottleWait_OversizedChunkPaysFullSize(t *testing.T) {
	const capBytesPerSec = 62_500 // 0.5 Mbps
	const chunk = 1 << 20         // 1 MiB, ~16.8x the bucket
	th := NewThrottle(capBytesPerSec)
	slept := fakeClock(th)

	if err := th.Wait(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	// The full bucket (62,500 B) is spent at once; the remaining 986,076 B
	// are paid back at 62,500 B/s = 15.777216s. The old behaviour returned
	// after one bucket (~1s), i.e. ~16x the cap.
	want := time.Duration((chunk - capBytesPerSec) * int64(time.Second) / capBytesPerSec)
	if *slept != want {
		t.Errorf("first 1 MiB chunk waited %v, want %v", *slept, want)
	}

	// A second chunk straight after pays its whole size: no burst is left.
	*slept = 0
	if err := th.Wait(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	want = time.Duration(chunk * int64(time.Second) / capBytesPerSec) // 16.777216s
	if *slept != want {
		t.Errorf("second 1 MiB chunk waited %v, want %v", *slept, want)
	}
}

func TestThrottleWait_OversizedChunkRealClock(t *testing.T) {
	th := NewThrottle(100_000) // bucket 100,000 B
	start := time.Now()
	if err := th.Wait(context.Background(), 140_000); err != nil { // 40,000 B over the bucket = 0.4s
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 350*time.Millisecond {
		t.Errorf("oversized chunk returned after %v; it must pay for the bytes beyond the bucket", elapsed)
	}
}

func TestThrottleWait_CancelRefundsDebit(t *testing.T) {
	th := NewThrottle(1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := th.Wait(ctx, 3000); err == nil {
		t.Fatal("expected context error")
	}
	if got := th.GetAvailableTokens(); got < 900 { // debit refunded; bucket still (nearly) full
		t.Errorf("tokens after cancelled wait = %d, want ~1000", got)
	}
}

func TestManagerSetBandwidthLimit(t *testing.T) {
	m := NewManager(t.TempDir(), 1)
	if got := m.BandwidthLimit(); got != 0 {
		t.Errorf("default limit = %d", got)
	}
	m.SetBandwidthLimit(1_250_000)
	if got := m.BandwidthLimit(); got != 1_250_000 {
		t.Errorf("limit = %d", got)
	}
	if err := m.waitBandwidth(context.Background(), 100); err != nil {
		t.Errorf("waitBandwidth: %v", err)
	}
	m.SetBandwidthLimit(0)
	if got := m.BandwidthLimit(); got != 0 {
		t.Errorf("limit after reset = %d", got)
	}
}
