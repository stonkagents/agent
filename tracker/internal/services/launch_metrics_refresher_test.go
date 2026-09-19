// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Background metrics refresher — cycle over every launch with bounded concurrency
//          and per-mint timeout, on-demand refresh after record, and the cache it feeds.

package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// fakeRefreshSource records refresh calls; specific mints can fail or hang.
type fakeRefreshSource struct {
	mu       sync.Mutex
	calls    map[string]int
	hints    map[string]LaunchLabHint
	failing  map[string]bool
	hanging  map[string]bool
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
}

func newFakeRefreshSource() *fakeRefreshSource {
	return &fakeRefreshSource{calls: map[string]int{}, hints: map[string]LaunchLabHint{}, failing: map[string]bool{}, hanging: map[string]bool{}}
}

func (f *fakeRefreshSource) RefreshMint(ctx context.Context, mint string, hint LaunchLabHint) error {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		cur := f.maxSeen.Load()
		if n <= cur || f.maxSeen.CompareAndSwap(cur, n) {
			break
		}
	}
	f.mu.Lock()
	f.calls[mint]++
	f.hints[mint] = hint
	hang, fail := f.hanging[mint], f.failing[mint]
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("rpc down")
	}
	return nil
}

func (f *fakeRefreshSource) count(mint string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[mint]
}

func seedLaunches(t *testing.T, repo *repository.MemoryLaunchRepository, n int) []string {
	t.Helper()
	mints := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l := &models.TokenLaunch{
			Mint: testKey(byte(40 + i)), PoolID: testKey(byte(140 + i)), CreatorWallet: tCreator, QuoteMint: models.SOLMint,
			Name: "L", Symbol: "L", LaunchSignature: testSig(byte(40 + i)), Status: models.LaunchStatusConfirmed,
			CreatedAt: time.Date(2026, 9, 12, 12, 0, 0, i, time.UTC),
		}
		if err := repo.Create(context.Background(), l); err != nil {
			t.Fatalf("seed launch %d: %v", i, err)
		}
		mints = append(mints, l.Mint)
	}
	return mints
}

func TestLaunchMetricsRefresher_CycleRefreshesEveryLaunchWithBoundedConcurrency(t *testing.T) {
	repo := repository.NewMemoryLaunchRepository()
	mints := seedLaunches(t, repo, 9)
	src := newFakeRefreshSource()
	src.delay = 10 * time.Millisecond
	src.failing[mints[3]] = true // one mint fails: counted, does not stop the cycle
	src.hanging[mints[5]] = true // one mint never answers: the per-mint timeout frees the slot

	r := NewLaunchMetricsRefresher(repo, src, LaunchMetricsRefresherConfig{Concurrency: 3, MintTimeout: 50 * time.Millisecond})
	st := r.RefreshAll(context.Background())

	if st.Launches != 9 || st.Failures != 2 {
		t.Errorf("stats = %+v, want 9 launches / 2 failures (one error, one timeout)", st)
	}
	if st.Duration <= 0 || st.Duration > 2*time.Second {
		t.Errorf("duration = %v", st.Duration)
	}
	for _, m := range mints {
		if src.count(m) != 1 {
			t.Errorf("mint %s refreshed %d times, want 1", m, src.count(m))
		}
	}
	if got := src.maxSeen.Load(); got > 3 {
		t.Errorf("max in-flight = %d, want <= 3 (concurrency bound)", got)
	}
	if h := src.hints[mints[0]]; h.PoolID == "" || h.QuoteMint != models.SOLMint {
		t.Errorf("hint for %s = %+v, want pool/quote from the launch row", mints[0], h)
	}
}

func TestLaunchMetricsRefresher_TriggerRefreshRunsImmediately(t *testing.T) {
	repo := repository.NewMemoryLaunchRepository()
	src := newFakeRefreshSource()
	r := NewLaunchMetricsRefresher(repo, src, LaunchMetricsRefresherConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	r.TriggerRefresh(tMint, LaunchLabHint{PoolID: tPool, QuoteMint: models.SOLMint})
	deadline := time.Now().Add(2 * time.Second)
	for src.count(tMint) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if src.count(tMint) != 1 {
		t.Fatalf("triggered mint refreshed %d times, want 1 (without waiting for the interval)", src.count(tMint))
	}
	if src.hints[tMint].PoolID != tPool {
		t.Errorf("trigger hint = %+v", src.hints[tMint])
	}
	// A nil refresher and an empty mint are both no-ops.
	var none *LaunchMetricsRefresher
	none.TriggerRefresh(tMint, LaunchLabHint{})
	r.TriggerRefresh("", LaunchLabHint{})
}

// fakeTrigger records what LaunchService asks to refresh.
type fakeTrigger struct {
	mu    sync.Mutex
	mints []string
	hints []LaunchLabHint
}

func (f *fakeTrigger) TriggerRefresh(mint string, hint LaunchLabHint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, mint)
	f.hints = append(f.hints, hint)
}

func TestLaunchService_RecordTriggersMetricsRefresh(t *testing.T) {
	d := newLaunchTestDeps()
	trig := &fakeTrigger{}
	svc := NewLaunchService(LaunchServiceDeps{
		Launches: d.launches, Revenue: d.revenue, Tokens: d.tokens, Accounts: d.accounts,
		Wallets: d.wallets, Credits: d.credits, Verifier: d.verifier, Clock: d.clock, Refresher: trig,
		TreasuryAddress: tTreasury, PlatformID: tPlatform, MinFeeLamports: 10_000_000,
	})
	if _, created, err := svc.Record(context.Background(), recordInput()); err != nil || !created {
		t.Fatalf("Record: created=%v err=%v", created, err)
	}
	if len(trig.mints) != 1 || trig.mints[0] != tMint || trig.hints[0].PoolID != tPool || trig.hints[0].QuoteMint != DefaultLaunchQuoteMint {
		t.Fatalf("trigger calls = %v / %+v, want one for the new launch", trig.mints, trig.hints)
	}
	// Idempotent replay of the same record does not trigger again.
	if _, created, _ := svc.Record(context.Background(), recordInput()); created || len(trig.mints) != 1 {
		t.Errorf("replay: created=%v triggers=%d, want false/1", created, len(trig.mints))
	}
}

func TestMetricsService_RefreshMintWarmsTheListCache(t *testing.T) {
	ll := &stubLaunchLab{metrics: &models.TokenMetrics{MarketCapUsd: ptrFloat(777), Holders: ptrInt(9), BondingCurvePercent: ptrInt(12)}}
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: ll, TokenRepo: repository.NewMemoryTokenRepository()})
	if !svc.LaunchLabEnabled() {
		t.Fatal("LaunchLabEnabled = false")
	}
	ctx := context.Background()
	if got := svc.BatchGetCachedMetrics(ctx, []string{tMint}); len(got) != 0 {
		t.Fatalf("cache warm before refresh: %v", got)
	}
	if err := svc.RefreshMint(ctx, tMint, LaunchLabHint{PoolID: tPool}); err != nil {
		t.Fatalf("RefreshMint: %v", err)
	}
	got := svc.BatchGetCachedMetrics(ctx, []string{tMint})
	if m := got[tMint]; m == nil || *m.MarketCapUsd != 777 || *m.Holders != 9 {
		t.Fatalf("cache after refresh = %+v", got)
	}
	if ll.lastHint.PoolID != tPool {
		t.Errorf("hint forwarded = %+v", ll.lastHint)
	}
	// A second refresh bypasses the cache and hits the source again; an errored source reports it.
	ll.err = errors.New("raydium down")
	if err := svc.RefreshMint(ctx, tMint, LaunchLabHint{}); err == nil {
		t.Error("RefreshMint with a failing source returned nil error")
	}
	if ll.callCount.Load() != 2 {
		t.Errorf("source calls = %d, want 2", ll.callCount.Load())
	}
}
