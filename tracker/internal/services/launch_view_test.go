// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Launch view enrichment — quote token details and market metrics inlined on
//          GET /api/launches, /api/launch/{mint} and /api/launch/pending.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// fakeLaunchMetrics is a LaunchMetricsProvider with a cached set and a fetchable set.
type fakeLaunchMetrics struct {
	mu       sync.Mutex
	cached   map[string]*models.TokenMetrics
	fetched  map[string]*models.TokenMetrics
	fetchErr error
	// hang makes GetMetricsByMint block until the context ends (budget tests).
	hang bool
	// batchCalls / fetchCalls count provider hits so tests can assert the list path never fetches.
	batchCalls int
	fetchCalls int
	lastHint   LaunchLabHint
}

func (f *fakeLaunchMetrics) BatchGetCachedMetrics(_ context.Context, addrs []string) map[string]*models.TokenMetrics {
	f.batchCalls++
	out := map[string]*models.TokenMetrics{}
	for _, a := range addrs {
		if m, ok := f.cached[a]; ok {
			out[a] = m
		}
	}
	return out
}

func (f *fakeLaunchMetrics) GetMetricsByMint(ctx context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error) {
	f.mu.Lock()
	f.fetchCalls++
	f.lastHint = hint
	hang, fetchErr := f.hang, f.fetchErr
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	if m, ok := f.fetched[mint]; ok {
		return m, nil
	}
	return &models.TokenMetrics{}, nil
}

func i32(v int) *int { return &v }

func stonkQuote() *models.LaunchQuote {
	return &models.LaunchQuote{
		QuoteMint: DefaultLaunchQuoteMint, Symbol: "STONK", Name: "STONK", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "custom", Enabled: true,
	}
}

func newViewService(t *testing.T, metrics LaunchMetricsProvider, quotes repository.LaunchQuoteRepository) (*LaunchService, launchTestDeps) {
	t.Helper()
	d := newLaunchTestDeps()
	svc := NewLaunchService(LaunchServiceDeps{
		Launches: d.launches, Revenue: d.revenue, Tokens: d.tokens, Accounts: d.accounts,
		Wallets: d.wallets, Credits: d.credits, Verifier: d.verifier, Clock: d.clock,
		Quotes: quotes, Metrics: metrics,
		TreasuryAddress: tTreasury, PlatformID: tPlatform, MinFeeLamports: 10_000_000,
	})
	// Seeded straight into the repository: these tests exercise the views, and a catalog
	// without $STONK (deliberately, in the degradation tests) would reject Record.
	in := recordInput()
	if err := d.launches.Create(context.Background(), &models.TokenLaunch{
		Mint: in.Mint, PoolID: in.PoolID, CreatorWallet: in.CreatorWallet, QuoteMint: in.QuoteMint,
		Name: in.Name, Symbol: in.Symbol, ImageURL: in.ImageURL, MetadataURI: in.MetadataURI,
		LaunchSignature: in.LaunchSignature, FeeLamports: in.FeeLamports, TransferFeeBps: in.TransferFeeBps,
		PlatformID: tPlatform, Status: models.LaunchStatusConfirmed, CreatedAt: d.clock.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed launch: %v", err)
	}
	return svc, d
}

func TestLaunchView_JSONCarriesCamelCaseAndSnakeCase(t *testing.T) {
	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(stonkQuote())
	metrics := &fakeLaunchMetrics{fetched: map[string]*models.TokenMetrics{
		tMint: {MarketCapUsd: f64(12345.5), Holders: i32(42), BondingCurvePercent: i32(37), Source: "launchlab"},
	}}
	svc, _ := newViewService(t, metrics, quotes)

	view, err := svc.GetView(context.Background(), tMint)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Backward-compatible snake_case (first version of the endpoint) and the camelCase the
	// portal reads must both be present and equal.
	pairs := map[string]string{
		"pool_id": "poolId", "creator_wallet": "creatorWallet", "quote_mint": "quoteMint",
		"image_url": "imageUrl", "launch_signature": "launchSignature", "fee_lamports": "feeLamports",
		"transfer_fee_bps": "transferFeeBps", "created_at": "createdAt",
	}
	for snake, camel := range pairs {
		if got[snake] == nil || got[camel] == nil || got[snake] != got[camel] {
			t.Errorf("%s=%v vs %s=%v, want both present and equal", snake, got[snake], camel, got[camel])
		}
	}
	for _, k := range []string{"mint", "name", "symbol", "status", "agentBound"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	if v, ok := got["imageThumbUrl"]; !ok || v != nil {
		t.Errorf("imageThumbUrl = %v (present %v), want null for a launch without a thumbnail", v, ok)
	}
	if got["agentBound"] != false {
		t.Errorf("agentBound = %v, want false for an unclaimed launch", got["agentBound"])
	}

	q, _ := got["quote"].(map[string]interface{})
	if q["mint"] != DefaultLaunchQuoteMint || q["symbol"] != "STONK" || q["category"] != "custom" || q["decimals"].(float64) != 9 {
		t.Errorf("quote = %v", q)
	}
	m, _ := got["metrics"].(map[string]interface{})
	if m["marketCapUsd"].(float64) != 12345.5 || m["holders"].(float64) != 42 || m["curveProgressPct"].(float64) != 37 {
		t.Errorf("metrics = %v", m)
	}
	if metrics.lastHint.PoolID != tPool || metrics.lastHint.QuoteMint != DefaultLaunchQuoteMint {
		t.Errorf("metrics hint = %+v, want pool/quote from the stored launch", metrics.lastHint)
	}
}

func TestLaunchView_MetricsAndQuoteFailuresDoNotFailTheRequest(t *testing.T) {
	// No quote catalog entry for the launch's quote mint, and the metrics provider errors.
	metrics := &fakeLaunchMetrics{fetchErr: errors.New("rpc down")}
	svc, _ := newViewService(t, metrics, repository.NewMemoryLaunchQuoteRepository())

	view, err := svc.GetView(context.Background(), tMint)
	if err != nil {
		t.Fatalf("GetView must succeed without enrichment: %v", err)
	}
	if view.Quote != nil || view.Metrics != nil {
		t.Errorf("quote=%+v metrics=%+v, want both nil", view.Quote, view.Metrics)
	}

	// Empty metrics (mint unknown to every source) is reported as "no data", not zeros.
	metrics.fetchErr = nil
	view, _ = svc.GetView(context.Background(), tMint)
	if view.Metrics != nil {
		t.Errorf("metrics = %+v, want nil for an all-empty metrics object", view.Metrics)
	}

	// No metrics provider and no quote repo at all still works (older bootstrap wiring).
	bare, _ := newViewService(t, nil, nil)
	pending, err := bare.PendingViews(context.Background(), tCreator, 10)
	if err != nil || len(pending) != 1 || pending[0].Quote != nil || pending[0].Metrics != nil {
		t.Errorf("PendingViews without providers: err=%v len=%d item=%+v", err, len(pending), pending)
	}
}

func TestLaunchView_ListFirstPageFillsCacheMissesLive(t *testing.T) {
	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(stonkQuote())
	// Nothing cached: the first page fetches live within the budget and gets numbers.
	metrics := &fakeLaunchMetrics{fetched: map[string]*models.TokenMetrics{tMint: {Holders: i32(1), MarketCapUsd: f64(500)}}}
	svc, _ := newViewService(t, metrics, quotes)

	views, total, err := svc.ListViews(context.Background(), "", 20, 0)
	if err != nil || total != 1 || len(views) != 1 {
		t.Fatalf("ListViews: err=%v total=%d len=%d", err, total, len(views))
	}
	if metrics.batchCalls != 1 || metrics.fetchCalls != 1 {
		t.Errorf("batch=%d fetch=%d, want 1 cache read and 1 live fill on the first page", metrics.batchCalls, metrics.fetchCalls)
	}
	if views[0].Metrics == nil || views[0].Metrics.Holders == nil || *views[0].Metrics.Holders != 1 || *views[0].Metrics.MarketCapUsd != 500 {
		t.Errorf("metrics = %+v, want the live-fetched values", views[0].Metrics)
	}
	if metrics.lastHint.PoolID != tPool || metrics.lastHint.QuoteMint != DefaultLaunchQuoteMint {
		t.Errorf("live fill hint = %+v", metrics.lastHint)
	}
	if views[0].Quote == nil || views[0].Quote.Symbol != "STONK" {
		t.Errorf("quote = %+v, want STONK", views[0].Quote)
	}

	// Later pages stay cache-only.
	metrics.fetchCalls = 0
	if _, _, err := svc.ListViews(context.Background(), "", 20, 20); err != nil || metrics.fetchCalls != 0 {
		t.Errorf("offset 20: err=%v fetch=%d, want no live fill", err, metrics.fetchCalls)
	}

	// Once cached, the list carries the cached block and does not fetch.
	metrics.cached = map[string]*models.TokenMetrics{tMint: {QuoteRaised: f64(30), QuoteTarget: f64(85)}}
	views, _, _ = svc.ListViews(context.Background(), "", 20, 0)
	if metrics.fetchCalls != 0 {
		t.Errorf("fetch=%d after cache warm, want 0", metrics.fetchCalls)
	}
	if views[0].Metrics == nil || views[0].Metrics.CurveProgressPct == nil {
		t.Fatalf("metrics = %+v, want curve progress from raised/target", views[0].Metrics)
	}
	if pct := *views[0].Metrics.CurveProgressPct; pct < 35.2 || pct > 35.4 {
		t.Errorf("curveProgressPct = %v, want ~35.29 (30/85)", pct)
	}
}

func TestLaunchView_ListLiveFillRespectsBudget(t *testing.T) {
	old := listLiveFillBudget
	listLiveFillBudget = 60 * time.Millisecond
	t.Cleanup(func() { listLiveFillBudget = old })

	// The source hangs until the context ends: the list must still answer, without metrics.
	metrics := &fakeLaunchMetrics{hang: true}
	svc, _ := newViewService(t, metrics, repository.NewMemoryLaunchQuoteRepository())

	start := time.Now()
	views, _, err := svc.ListViews(context.Background(), "", 20, 0)
	if err != nil || len(views) != 1 {
		t.Fatalf("ListViews: err=%v len=%d", err, len(views))
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("list took %v, want to return at the %v budget", el, listLiveFillBudget)
	}
	if views[0].Metrics != nil {
		t.Errorf("metrics = %+v, want nil when the live fill timed out", views[0].Metrics)
	}
	// A failing source degrades the same way.
	metrics.hang, metrics.fetchErr = false, errors.New("rpc down")
	views, _, _ = svc.ListViews(context.Background(), "", 20, 0)
	if views[0].Metrics != nil {
		t.Errorf("metrics = %+v, want nil when the live fill errored", views[0].Metrics)
	}
}
