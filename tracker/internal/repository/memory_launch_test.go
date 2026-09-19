// Package: tracker/internal/repository
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Tests for in-memory launch settings and launch quote repositories

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestMemoryLaunchSettingsRepository_GetFee_NotFoundWhenEmpty(t *testing.T) {
	repo := NewMemoryLaunchSettingsRepository()
	_, err := repo.GetFee(context.Background())
	if !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetFee() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryLaunchSettingsRepository_UpsertThenGet_ReturnsCopy(t *testing.T) {
	repo := NewMemoryLaunchSettingsRepository()
	ctx := context.Background()
	pricedAt := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	fee := &models.LaunchFee{FeeUSD: 0.5, FeeLamports: 4_901_961, SolUSD: 102, PricedAt: pricedAt, Source: "test"}

	if err := repo.UpsertFee(ctx, fee); err != nil {
		t.Fatalf("UpsertFee() error = %v", err)
	}
	fee.FeeLamports = 1 // mutate caller copy; stored value must not change

	got, err := repo.GetFee(ctx)
	if err != nil {
		t.Fatalf("GetFee() error = %v", err)
	}
	if got.FeeLamports != 4_901_961 || got.SolUSD != 102 || !got.PricedAt.Equal(pricedAt) || got.Source != "test" {
		t.Errorf("GetFee() = %+v, want stored fee", got)
	}

	// Second upsert replaces.
	_ = repo.UpsertFee(ctx, &models.LaunchFee{FeeUSD: 0.5, FeeLamports: 5_000_000, SolUSD: 100, PricedAt: pricedAt.Add(time.Minute), Source: "test"})
	got, _ = repo.GetFee(ctx)
	if got.FeeLamports != 5_000_000 {
		t.Errorf("GetFee() after second upsert lamports = %d, want 5000000", got.FeeLamports)
	}
}

func TestMemoryLaunchQuoteRepository_ListEnabled_OrdersAndFilters(t *testing.T) {
	repo := NewMemoryLaunchQuoteRepository()
	repo.Put(&models.LaunchQuote{QuoteMint: "B", Symbol: "B", Enabled: true, SortOrder: 2})
	repo.Put(&models.LaunchQuote{QuoteMint: "A", Symbol: "A", Enabled: true, SortOrder: 0})
	repo.Put(&models.LaunchQuote{QuoteMint: "C", Symbol: "C", Enabled: false, SortOrder: 1})

	list, err := repo.ListEnabled(context.Background())
	if err != nil {
		t.Fatalf("ListEnabled() error = %v", err)
	}
	if len(list) != 2 || list[0].QuoteMint != "A" || list[1].QuoteMint != "B" {
		t.Errorf("ListEnabled() = %v, want [A B]", list)
	}
}

func TestMemoryLaunchQuoteRepository_GetByMint(t *testing.T) {
	repo := NewMemoryLaunchQuoteRepository()
	repo.Put(&models.LaunchQuote{QuoteMint: "M", Symbol: "M", Enabled: false, MinFundRaisingRaw: "24000000000"})

	got, err := repo.GetByMint(context.Background(), "M")
	if err != nil {
		t.Fatalf("GetByMint() error = %v", err)
	}
	if got.Enabled || got.MinFundRaisingRaw != "24000000000" {
		t.Errorf("GetByMint() = %+v, want disabled row with min 24000000000", got)
	}
	if _, err := repo.GetByMint(context.Background(), "missing"); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("GetByMint(missing) error = %v, want ErrNotFound", err)
	}
}

// TestMemoryLaunchQuoteRepository_ScopedToCluster mirrors migration 014: the same mint exists once
// per cluster and a repository only ever returns rows of its own cluster.
func TestMemoryLaunchQuoteRepository_ScopedToCluster(t *testing.T) {
	const sol = "So11111111111111111111111111111111111111112"
	seed := func(repo *MemoryLaunchQuoteRepository) {
		repo.Put(&models.LaunchQuote{QuoteMint: "STONK", Symbol: "STONK", Enabled: true, SortOrder: 0}) // no cluster = mainnet
		repo.Put(&models.LaunchQuote{Cluster: models.LaunchClusterMainnet, QuoteMint: sol, Symbol: "SOL", LaunchLabConfigID: "mainnet-cfg", Enabled: true, SortOrder: 1})
		repo.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: sol, Symbol: "SOL", LaunchLabConfigID: "7ZR4zD7PYfY2XxoG1Gxcy2EgEeGYrpxrwzPuwdUBssEt", Enabled: true, SortOrder: 0})
	}
	ctx := context.Background()

	mainnet := NewMemoryLaunchQuoteRepository()
	seed(mainnet)
	if mainnet.Cluster() != models.LaunchClusterMainnet {
		t.Fatalf("default cluster = %q", mainnet.Cluster())
	}
	list, _ := mainnet.ListEnabled(ctx)
	if len(list) != 2 || list[0].QuoteMint != "STONK" || list[1].LaunchLabConfigID != "mainnet-cfg" || list[0].Cluster != models.LaunchClusterMainnet {
		t.Errorf("mainnet ListEnabled = %+v", list)
	}
	if q, err := mainnet.GetByMint(ctx, sol); err != nil || q.LaunchLabConfigID != "mainnet-cfg" {
		t.Errorf("mainnet GetByMint(SOL) = %+v, %v", q, err)
	}

	devnet := NewMemoryLaunchQuoteRepositoryForCluster(models.LaunchClusterDevnet)
	seed(devnet)
	list, _ = devnet.ListEnabled(ctx)
	if len(list) != 1 || list[0].QuoteMint != sol || list[0].LaunchLabConfigID != "7ZR4zD7PYfY2XxoG1Gxcy2EgEeGYrpxrwzPuwdUBssEt" {
		t.Errorf("devnet ListEnabled = %+v, want only the devnet SOL row", list)
	}
	if _, err := devnet.GetByMint(ctx, "STONK"); err != models.ErrNotFound {
		t.Errorf("devnet GetByMint(STONK) err = %v, want ErrNotFound (mainnet-only row)", err)
	}
	if q, err := devnet.GetByMint(ctx, sol); err != nil || q.Cluster != models.LaunchClusterDevnet {
		t.Errorf("devnet GetByMint(SOL) = %+v, %v", q, err)
	}
}
