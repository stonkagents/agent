// Package: tracker/internal/services
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: 24h aggregates, USD conversion, cursor paging and candle bucketing over the memory repo.

package services

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type fixedQuoteUsd struct {
	prices map[string]float64
	calls  int
}

func (f *fixedQuoteUsd) QuoteUsdPrice(_ context.Context, mint string) (float64, bool) {
	f.calls++
	p, ok := f.prices[mint]
	return p, ok
}

func seedTrades(t *testing.T, repo *repository.MemoryLaunchTradeRepository, now time.Time) {
	t.Helper()
	trades := []*models.LaunchTrade{
		// 30h ago: the reference for the 24h change.
		{Mint: ixMint, PoolID: ixPool, Signature: "old", Slot: 1, BlockTime: now.Add(-30 * time.Hour), Side: "buy", Trader: "a", BaseAmount: 1000, QuoteAmount: 1, PriceQuote: 0.001, QuoteSymbol: "USDC"},
		// 25h ago: still outside the window, newer reference.
		{Mint: ixMint, PoolID: ixPool, Signature: "ref", Slot: 2, BlockTime: now.Add(-25 * time.Hour), Side: "buy", Trader: "a", BaseAmount: 1000, QuoteAmount: 2, PriceQuote: 0.002, QuoteSymbol: "USDC"},
		// Inside the window.
		{Mint: ixMint, PoolID: ixPool, Signature: "w1", Slot: 3, BlockTime: now.Add(-20 * time.Hour), Side: "buy", Trader: "b", BaseAmount: 1000, QuoteAmount: 3, PriceQuote: 0.003, QuoteSymbol: "USDC"},
		{Mint: ixMint, PoolID: ixPool, Signature: "w2", Slot: 4, BlockTime: now.Add(-2 * time.Hour), Side: "sell", Trader: "c", BaseAmount: 500, QuoteAmount: 2, PriceQuote: 0.004, QuoteSymbol: "USDC"},
		{Mint: ixMint, PoolID: ixPool, Signature: "w3", Slot: 5, BlockTime: now.Add(-time.Minute), Side: "buy", Trader: "d", BaseAmount: 1000, QuoteAmount: 5, PriceQuote: 0.005, QuoteSymbol: "USDC"},
	}
	if n, err := repo.InsertTrades(context.Background(), trades); err != nil || n != 5 {
		t.Fatalf("seed: n=%d err=%v", n, err)
	}
}

func TestLaunchTradeService_Stats24h(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repo := repository.NewMemoryLaunchTradeRepository()
	seedTrades(t, repo, now)
	usd := &fixedQuoteUsd{prices: map[string]float64{ixQuote: 2}}
	svc := NewLaunchTradeService(repo, usd, clock.NewMockClock(now), nil)

	priceNow := 0.006
	stats := svc.Stats24h(context.Background(), []TradeStatsInput{
		{Mint: ixMint, QuoteMint: ixQuote, PriceQuote: &priceNow},
		{Mint: "NoTrades", QuoteMint: ixQuote},
	})
	st := stats[ixMint]
	if st == nil {
		t.Fatal("no stats for traded mint")
	}
	if _, has := stats["NoTrades"]; has {
		t.Error("mint without trades must be omitted")
	}
	if st.Volume24hQuote == nil || *st.Volume24hQuote != 10 {
		t.Errorf("volume quote = %v, want 10 (3+2+5)", st.Volume24hQuote)
	}
	if st.Trades24h == nil || *st.Trades24h != 3 {
		t.Errorf("trades = %v, want 3", st.Trades24h)
	}
	if st.Volume24hUsd == nil || *st.Volume24hUsd != 20 {
		t.Errorf("volume usd = %v, want 20", st.Volume24hUsd)
	}
	// Reference is the last trade at or before 24h ago (0.002), price now is the live curve price.
	if st.PriceChange24hPct == nil || math.Abs(*st.PriceChange24hPct-200) > 1e-9 {
		t.Errorf("change = %v, want 200%%", st.PriceChange24hPct)
	}
	if usd.calls != 1 {
		t.Errorf("quote priced %d times, want once per quote mint", usd.calls)
	}

	// Without a live price the newest trade stands in; without a USD source the ratio implies it.
	priceUsd := 0.010 // 0.005 quote × 2 USD
	lastPrice := 0.005
	svc2 := NewLaunchTradeService(repo, nil, clock.NewMockClock(now), nil)
	st2 := svc2.Stats24h(context.Background(), []TradeStatsInput{{Mint: ixMint, QuoteMint: ixQuote, PriceUsd: &priceUsd, PriceQuote: &lastPrice}})[ixMint]
	if st2 == nil || st2.PriceChange24hPct == nil || math.Abs(*st2.PriceChange24hPct-150) > 1e-9 {
		t.Errorf("implied change = %+v, want 150%%", st2)
	}
	if st2.Volume24hUsd == nil || math.Abs(*st2.Volume24hUsd-20) > 1e-9 {
		t.Errorf("implied usd volume = %v, want 20", st2.Volume24hUsd)
	}
	st3 := svc2.Stats24h(context.Background(), []TradeStatsInput{{Mint: ixMint, QuoteMint: ixQuote}})[ixMint]
	if st3 == nil || st3.PriceChange24hPct == nil || math.Abs(*st3.PriceChange24hPct-150) > 1e-9 || st3.Volume24hUsd != nil {
		t.Errorf("no-price inputs = %+v (change from last trade, usd nil)", st3)
	}
}

func TestLaunchTradeService_PriceChangeNullWithoutOldTrade(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repo := repository.NewMemoryLaunchTradeRepository()
	_, _ = repo.InsertTrades(context.Background(), []*models.LaunchTrade{
		{Mint: ixMint, PoolID: ixPool, Signature: "young", Slot: 1, BlockTime: now.Add(-time.Hour), Side: "buy", BaseAmount: 1, QuoteAmount: 1, PriceQuote: 1},
	})
	svc := NewLaunchTradeService(repo, nil, clock.NewMockClock(now), nil)
	st := svc.Stats24h(context.Background(), []TradeStatsInput{{Mint: ixMint}})[ixMint]
	if st == nil || st.PriceChange24hPct != nil || st.Trades24h == nil || *st.Trades24h != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestLaunchTradeService_TradesPaging(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repo := repository.NewMemoryLaunchTradeRepository()
	seedTrades(t, repo, now)
	svc := NewLaunchTradeService(repo, nil, clock.NewMockClock(now), nil)

	page1, next, err := svc.Trades(context.Background(), ixMint, 2, "")
	if err != nil || len(page1) != 2 || page1[0].Signature != "w3" || page1[1].Signature != "w2" || next == "" {
		t.Fatalf("page1 = %v next=%q err=%v", sigsOf(page1), next, err)
	}
	page2, next2, err := svc.Trades(context.Background(), ixMint, 2, next)
	if err != nil || sigsOf(page2)[0] != "w1" || sigsOf(page2)[1] != "ref" || next2 == "" {
		t.Fatalf("page2 = %v next=%q err=%v", sigsOf(page2), next2, err)
	}
	page3, next3, err := svc.Trades(context.Background(), ixMint, 2, next2)
	if err != nil || len(page3) != 1 || page3[0].Signature != "old" || next3 != "" {
		t.Fatalf("page3 = %v next=%q err=%v", sigsOf(page3), next3, err)
	}
	if _, _, err := svc.Trades(context.Background(), ixMint, 2, "garbage"); err != ErrInvalidTradeCursor {
		t.Errorf("bad cursor err = %v", err)
	}
}

func sigsOf(rows []*models.LaunchTrade) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Signature)
	}
	return out
}

func TestBucketCandles(t *testing.T) {
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	mk := func(sig string, offset time.Duration, slot int64, price, quote float64) *models.LaunchTrade {
		return &models.LaunchTrade{Mint: ixMint, Signature: sig, Slot: slot, BlockTime: base.Add(offset), PriceQuote: price, QuoteAmount: quote}
	}
	trades := []*models.LaunchTrade{
		mk("a", 4*time.Minute, 2, 3, 1),  // bucket 0 (12:00), second by time
		mk("b", 1*time.Minute, 1, 2, 1),  // bucket 0, first → open 2
		mk("c", 4*time.Minute, 3, 1, 1),  // bucket 0, same second as a but later slot → close 1, low 1
		mk("d", 12*time.Minute, 4, 5, 2), // bucket 2 (12:10)
	}
	c := BucketCandles(trades, 5*time.Minute, 10)
	if len(c) != 2 {
		t.Fatalf("candles = %+v", c)
	}
	if c[0].T != base.Unix() || c[0].O != 2 || c[0].H != 3 || c[0].L != 1 || c[0].C != 1 || c[0].V != 3 {
		t.Errorf("bucket0 = %+v", c[0])
	}
	if c[1].T != base.Add(10*time.Minute).Unix() || c[1].O != 5 || c[1].C != 5 || c[1].V != 2 {
		t.Errorf("bucket2 = %+v", c[1])
	}
	if got := BucketCandles(trades, 5*time.Minute, 1); len(got) != 1 || got[0].T != c[1].T {
		t.Errorf("limit keeps newest: %+v", got)
	}
}

func TestLaunchTradeService_Candles(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 30, 0, time.UTC)
	repo := repository.NewMemoryLaunchTradeRepository()
	seedTrades(t, repo, now)
	svc := NewLaunchTradeService(repo, nil, clock.NewMockClock(now), nil)

	// 1h candles, 3 buckets back: 10:00, 11:00, 12:00 → trades at 10:00:30 (w2) and 11:59:30 (w3).
	c, err := svc.Candles(context.Background(), ixMint, "1h", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || c[0].T != now.Add(-2*time.Hour).Truncate(time.Hour).Unix() || c[0].C != 0.004 || c[1].C != 0.005 {
		t.Errorf("candles = %+v", c)
	}
	// Daily buckets align to UTC midnight: yesterday holds old/ref/w1, today holds w2/w3.
	c, err = svc.Candles(context.Background(), ixMint, "1d", 2)
	if err != nil || len(c) != 2 ||
		c[0].O != 0.001 || c[0].H != 0.003 || c[0].L != 0.001 || c[0].C != 0.003 || c[0].V != 6 ||
		c[1].O != 0.004 || c[1].H != 0.005 || c[1].L != 0.004 || c[1].C != 0.005 || c[1].V != 7 {
		t.Errorf("daily = %+v err=%v", c, err)
	}
	if _, err := svc.Candles(context.Background(), ixMint, "2h", 1); err != ErrInvalidCandleInterval {
		t.Errorf("bad interval err = %v", err)
	}
}

func TestAgentTokenService_BurnPlan(t *testing.T) {
	burns := repository.NewMemoryLaunchBurnRepository()
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	var rows []*models.LaunchBurn
	for i := 0; i < 7; i++ {
		rows = append(rows, &models.LaunchBurn{Mint: ixAgent, Signature: "b" + string(rune('a'+i)), Slot: int64(i), BlockTime: base.Add(time.Duration(i) * time.Hour), Amount: 250000, Burner: "k"})
	}
	_, _ = burns.InsertBurns(context.Background(), rows)
	svc := NewAgentTokenService(burns, ixAgent)
	plan, err := svc.BurnPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Total != AgentTokenTotalSupply || plan.Burned != 1750000 || plan.Remaining != AgentTokenTotalSupply-1750000 || plan.Burns != 7 || plan.Next != nil {
		t.Errorf("plan = %+v", plan)
	}
	if len(plan.Recent) != 5 || plan.Recent[0].Sig != "bg" || plan.Recent[4].Sig != "bc" {
		t.Errorf("recent = %+v", plan.Recent)
	}
}
