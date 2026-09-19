// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: RevenueService aggregation tests (totals by kind, zero-filled 30-day series)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

func f64(v float64) *float64 { return &v }

func TestRevenueService_Summary_Aggregates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	clk := clock.NewMockClock(now)
	repo := repository.NewMemoryRevenueRepository()

	entries := []*models.PlatformRevenue{
		{Kind: models.RevenueKindLaunchFee, QuoteMint: models.SOLMint, AmountRaw: 20_000_000, Signature: "sig-a", Mint: "mint-a", OccurredAt: now.Add(-2 * time.Hour)},
		{Kind: models.RevenueKindLaunchFee, QuoteMint: models.SOLMint, AmountRaw: 30_000_000, AmountUSD: f64(6.0), Signature: "sig-b", Mint: "mint-b", OccurredAt: now.AddDate(0, 0, -1)},
		{Kind: models.RevenueKindPlatformFeeClaim, QuoteMint: models.SOLMint, AmountRaw: 1_000_000_000, AmountUSD: f64(200), Signature: "sig-c", OccurredAt: now.AddDate(0, 0, -1)},
		{Kind: models.RevenueKindBuyback, QuoteMint: models.SOLMint, AmountRaw: 500_000_000, AmountUSD: f64(100), Signature: "sig-d", OccurredAt: now},
		// Outside the 30-day window: counted in totals, excluded from the daily series.
		{Kind: models.RevenueKindLaunchFee, QuoteMint: models.SOLMint, AmountRaw: 5_000_000, Signature: "sig-old", Mint: "mint-old", OccurredAt: now.AddDate(0, 0, -45)},
	}
	for _, e := range entries {
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatalf("insert %s: %v", e.Signature, err)
		}
	}
	// Duplicate signature is rejected, keeping the ledger idempotent.
	if err := repo.Insert(ctx, &models.PlatformRevenue{Kind: models.RevenueKindLaunchFee, AmountRaw: 1, Signature: "sig-a", OccurredAt: now}); err != models.ErrAlreadyExists {
		t.Fatalf("duplicate signature err = %v, want ErrAlreadyExists", err)
	}

	sum, err := NewRevenueService(repo, clk).Summary(ctx)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.TotalEntries != 5 || sum.WindowDays != RevenueWindowDays || len(sum.Daily) != RevenueWindowDays {
		t.Fatalf("entries=%d window=%d days=%d", sum.TotalEntries, sum.WindowDays, len(sum.Daily))
	}
	lf := sum.Totals[models.RevenueKindLaunchFee]
	if lf.Count != 3 || lf.AmountRaw != 55_000_000 || lf.AmountUSD != 6.0 {
		t.Errorf("launch_fee totals = %+v, want count 3 raw 55000000 usd 6", lf)
	}
	if sum.Counts[models.RevenueKindBuyback] != 1 || sum.Counts[models.RevenueKindBurn] != 0 {
		t.Errorf("counts = %+v", sum.Counts)
	}
	if _, ok := sum.Totals[models.RevenueKindHolderDistribution]; !ok {
		t.Error("every kind must be present in totals, even when zero")
	}

	last := sum.Daily[len(sum.Daily)-1]
	if last.Date != "2026-09-11" || last.LaunchFeeLamports != 20_000_000 || last.LaunchCount != 1 || last.BuybackUSD != 100 {
		t.Errorf("today = %+v", last)
	}
	yday := sum.Daily[len(sum.Daily)-2]
	if yday.Date != "2026-09-10" || yday.LaunchFeeLamports != 30_000_000 || yday.LaunchFeeUSD != 6.0 || yday.PlatformFeeUSD != 200 {
		t.Errorf("yesterday = %+v", yday)
	}
	if sum.Daily[0].Date != "2026-08-13" || sum.Daily[0].LaunchFeeLamports != 0 {
		t.Errorf("first day = %+v, want 2026-08-13 zero", sum.Daily[0])
	}
	var seriesLamports int64
	for _, p := range sum.Daily {
		seriesLamports += p.LaunchFeeLamports
	}
	if seriesLamports != 50_000_000 {
		t.Errorf("series lamports = %d, want 50000000 (old entry excluded)", seriesLamports)
	}
}

// TestRevenueService_Summary_EntriesAndWallets covers the public-page additions: the newest
// ledger rows with explorer links, and the configured wallet addresses (empty ones omitted).
func TestRevenueService_Summary_EntriesAndWallets(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)
	clk := clock.NewMockClock(now)
	repo := repository.NewMemoryRevenueRepository()

	// More than RevenueRecentLimit rows, oldest first, so the cut and the ordering are both exercised.
	for i := 0; i < RevenueRecentLimit+5; i++ {
		e := &models.PlatformRevenue{
			Kind: models.RevenueKindBuyback, QuoteMint: models.SOLMint, AmountRaw: float64(i),
			Signature: "sig-" + string(rune('a'+i)), OccurredAt: now.Add(time.Duration(i-30) * time.Minute),
		}
		if i == RevenueRecentLimit+4 {
			e.Kind = models.RevenueKindBurn
			e.Mint = "mint-burned"
			e.Meta = map[string]interface{}{"lots": 3}
		}
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	svc := NewRevenueServiceWithConfig(repo, clk, RevenueConfig{
		TreasuryAddress: "Treasury111", BuybackWallet: "Buyback111",
		ExplorerBaseURL: "https://solscan.io/", ExplorerCluster: "devnet",
	})
	sum, err := svc.Summary(ctx)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(sum.Entries) != RevenueRecentLimit {
		t.Fatalf("entries = %d, want %d", len(sum.Entries), RevenueRecentLimit)
	}
	newest := sum.Entries[0]
	if newest.Kind != models.RevenueKindBurn || newest.Mint != "mint-burned" || newest.Meta["lots"] != 3 {
		t.Errorf("newest entry = %+v, want the burn row first", newest)
	}
	if newest.ExplorerURL != "https://solscan.io/tx/"+newest.Signature+"?cluster=devnet" {
		t.Errorf("explorer_url = %q", newest.ExplorerURL)
	}
	if newest.TokenURL != "https://solscan.io/token/mint-burned?cluster=devnet" {
		t.Errorf("token_explorer_url = %q", newest.TokenURL)
	}
	if sum.Entries[1].TokenURL != "" {
		t.Errorf("token_explorer_url for a row without mint = %q, want empty", sum.Entries[1].TokenURL)
	}
	for i := 1; i < len(sum.Entries); i++ {
		if sum.Entries[i].OccurredAt.After(sum.Entries[i-1].OccurredAt) {
			t.Fatalf("entries not newest-first at %d", i)
		}
	}
	if sum.Wallets.Treasury != "Treasury111" || sum.Wallets.Buyback != "Buyback111" || sum.Wallets.TransferFeeAuthority != "" {
		t.Errorf("wallets = %+v", sum.Wallets)
	}

	// Default config: mainnet solscan links, no cluster query, no wallets.
	plain, _ := NewRevenueService(repo, clk).Summary(ctx)
	if plain.Entries[0].ExplorerURL != DefaultExplorerBaseURL+"/tx/"+plain.Entries[0].Signature {
		t.Errorf("default explorer_url = %q", plain.Entries[0].ExplorerURL)
	}
	if plain.Wallets != (RevenueWallets{}) {
		t.Errorf("default wallets = %+v, want empty", plain.Wallets)
	}
}

// TestRevenueService_Record_IdempotentOnSignature pins the keeper write contract.
func TestRevenueService_Record_IdempotentOnSignature(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)
	svc := NewRevenueService(repository.NewMemoryRevenueRepository(), clock.NewMockClock(now))

	created, err := svc.Record(ctx, &models.PlatformRevenue{Kind: models.RevenueKindHolderDistribution, QuoteMint: models.SOLMint, AmountRaw: 5, Signature: "sig-1"})
	if err != nil || !created {
		t.Fatalf("first Record: created=%v err=%v", created, err)
	}
	created, err = svc.Record(ctx, &models.PlatformRevenue{Kind: models.RevenueKindHolderDistribution, QuoteMint: models.SOLMint, AmountRaw: 5, Signature: "sig-1"})
	if err != nil || created {
		t.Fatalf("duplicate Record: created=%v err=%v, want false/nil", created, err)
	}
	if _, err := svc.Record(ctx, &models.PlatformRevenue{Kind: "tip", Signature: "sig-2"}); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
	sum, _ := svc.Summary(ctx)
	if sum.TotalEntries != 1 || !sum.Entries[0].OccurredAt.Equal(now) {
		t.Errorf("total=%d occurred_at=%v, want 1 entry stamped with the clock", sum.TotalEntries, sum.Entries[0].OccurredAt)
	}
}
