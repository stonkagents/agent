// Package: tracker/internal/services
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Tests for launch fee pricing, staleness, raise sizing and quote lookup

package services

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

const (
	testNVDAxMint = "Xsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh"
	testUSDCMint  = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

func testLaunchSettings() LaunchConfigSettings {
	return LaunchConfigSettings{
		ProgramID:      "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj",
		PlatformID:     "PlatformTest1111111111111111111111111111111",
		Treasury:       "TreasuryTest1111111111111111111111111111111",
		TransferFeeBps: 100,
		FeeUSD:         0.50,
		MinFeeLamports: 4_000_000,
		MaxFeeLamports: 10_000_000,
	}
}

func seedLaunchQuotes(repo *repository.MemoryLaunchQuoteRepository) {
	repo.Put(&models.LaunchQuote{QuoteMint: DefaultLaunchQuoteMint, Symbol: "STONK", Name: "STONK", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "custom",
		LaunchLabConfigID: "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW", MinFundRaisingRaw: "1", Enabled: true, SortOrder: 0})
	repo.Put(&models.LaunchQuote{QuoteMint: SOLMint, Symbol: "SOL", Name: "Wrapped SOL", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "solana",
		LaunchLabConfigID: "6s1xP3hpbAfFoNtUNF8mfHsjr2Bd97JxFJRWLbL6aHuX", MinFundRaisingRaw: "24000000000", Enabled: true, SortOrder: 1})
	repo.Put(&models.LaunchQuote{QuoteMint: testNVDAxMint, Symbol: "NVDAx", Name: "NVIDIA xStock", Decimals: 8,
		TokenProgram: "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb", Category: "xstock",
		LaunchLabConfigID: "2NuVPU5ViAyQsZamVfJ1vU6Cp77WSzret4LtNTMiWFQ4", MinFundRaisingRaw: "1", Enabled: true, SortOrder: 2})
	repo.Put(&models.LaunchQuote{QuoteMint: testUSDCMint, Symbol: "USDC", Name: "USD Coin", Decimals: 6,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "currency",
		LaunchLabConfigID: "8gj14w8vkNZjJTPak6e96Uf1sY438WKheH4s5dHyxiRw", MinFundRaisingRaw: "1", Enabled: false, SortOrder: 5})
}

type launchFixture struct {
	svc      *LaunchConfigService
	clk      *clock.MockClock
	feed     *StubPriceFeed
	settings *repository.MemoryLaunchSettingsRepository
}

func newLaunchFixture(t *testing.T, solUSD, stonkUSD float64) *launchFixture {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))
	feed := &StubPriceFeed{Prices: map[string]TokenPrice{
		SOLMint:                {USDPrice: solUSD, USDPriceRaw: solUSD},
		DefaultLaunchQuoteMint: {USDPrice: stonkUSD, USDPriceRaw: stonkUSD},
		testNVDAxMint:          {USDPrice: 219.58, USDPriceRaw: 219.95},
	}}
	quotes := repository.NewMemoryLaunchQuoteRepository()
	seedLaunchQuotes(quotes)
	settings := repository.NewMemoryLaunchSettingsRepository()
	svc := NewLaunchConfigService(LaunchConfigServiceDeps{
		Settings: settings, Quotes: quotes, Prices: feed, Clock: clk, Config: testLaunchSettings(),
	})
	return &launchFixture{svc: svc, clk: clk, feed: feed, settings: settings}
}

func TestComputeFeeLamports_CeilAndClamp(t *testing.T) {
	cases := []struct {
		name   string
		solUSD float64
		want   int64
	}{
		{"nominal ceil", 102, 4_901_961}, // 0.5/102*1e9 = 4901960.78 -> ceil
		{"clamped to min", 500, 4_000_000},
		{"clamped to max", 20, 10_000_000},
		{"zero price -> max", 0, 10_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeFeeLamports(0.50, tc.solUSD, 4_000_000, 10_000_000); got != tc.want {
				t.Errorf("ComputeFeeLamports(0.5, %v) = %d, want %d", tc.solUSD, got, tc.want)
			}
		})
	}
}

func TestComputeRaiseRaw_STONK(t *testing.T) {
	// 85 * 102 / 0.295 = 29389.8305... STONK; at 9 decimals raw = 29389830508475 (rounded half up).
	raw := ComputeRaiseRaw(102, 0.295, 9)
	if raw.String() != "29389830508475" {
		t.Errorf("ComputeRaiseRaw(102, 0.295, 9) = %s, want 29389830508475", raw)
	}
	// SOL as quote: solUSD == quoteUSD -> exactly 85e9.
	if got := ComputeRaiseRaw(102, 102, 9).String(); got != "85000000000" {
		t.Errorf("ComputeRaiseRaw(SOL) = %s, want 85000000000", got)
	}
	if got := ComputeRaiseRaw(102, 0, 9).Sign(); got != 0 {
		t.Errorf("ComputeRaiseRaw with zero quote price = sign %d, want 0", got)
	}
}

func TestRefreshFee_PersistsPricedFee(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	fee, err := f.svc.RefreshFee(context.Background())
	if err != nil {
		t.Fatalf("RefreshFee() error = %v", err)
	}
	if fee.FeeLamports != 4_901_961 || fee.SolUSD != 102 || fee.FeeUSD != 0.5 || fee.Source != "stub" {
		t.Errorf("RefreshFee() = %+v", fee)
	}
	stored, err := f.settings.GetFee(context.Background())
	if err != nil || stored.FeeLamports != 4_901_961 {
		t.Errorf("persisted fee = %+v, err = %v", stored, err)
	}
}

func TestRefreshFee_FeedFailureKeepsLastValue(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("first RefreshFee() error = %v", err)
	}
	f.feed.Err = errors.New("feed down")
	if _, err := f.svc.RefreshFee(context.Background()); err == nil {
		t.Fatal("RefreshFee() with failing feed error = nil, want error")
	}
	stored, _ := f.settings.GetFee(context.Background())
	if stored == nil || stored.FeeLamports != 4_901_961 {
		t.Errorf("persisted fee after failure = %+v, want unchanged", stored)
	}
}

func TestGetConfig_DefaultQuoteSTONK_RaiseMath(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())

	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}
	if cfg.Quote.Symbol != "STONK" {
		t.Errorf("default quote = %s, want STONK", cfg.Quote.Symbol)
	}
	if cfg.Raise.Raw.String() != "29389830508475" {
		t.Errorf("raise.raw = %s, want 29389830508475", cfg.Raise.Raw)
	}
	if math.Abs(cfg.Raise.Units-29389.83) > 0.01 {
		t.Errorf("raise.units = %v, want ~29389.83", cfg.Raise.Units)
	}
	if cfg.Raise.MinimumRaw.String() != "1" || cfg.Raise.Basis != LaunchRaiseBasis {
		t.Errorf("raise min/basis = %s / %q", cfg.Raise.MinimumRaw, cfg.Raise.Basis)
	}
	if cfg.Fee.Stale {
		t.Error("fee.stale = true right after refresh, want false")
	}
	if cfg.Fee.Lamports != 4_901_961 || cfg.Fee.SolUSD != 102 || cfg.Fee.USD != 0.5 {
		t.Errorf("fee = %+v", cfg.Fee)
	}
	// $STONK only: SOL and NVDAx are enabled catalog rows (kept for metrics lookups) but are
	// not served as launchable.
	if len(cfg.Quotes) != 1 || cfg.Quotes[0].QuoteMint != DefaultLaunchQuoteMint {
		t.Errorf("quotes = %+v, want exactly [$STONK]", cfg.Quotes)
	}
	if cfg.DefaultQuoteMint != DefaultLaunchQuoteMint {
		t.Errorf("DefaultQuoteMint = %q, want $STONK", cfg.DefaultQuoteMint)
	}
	if cfg.Curve.ConfigID != "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW" || cfg.Curve.CurveType != "ConstantCurve" ||
		cfg.Curve.Supply != "1000000000000000" || cfg.Curve.TotalSellA != "793100000000000" || cfg.Curve.BaseDecimals != 6 {
		t.Errorf("curve = %+v", cfg.Curve)
	}
	if cfg.ProgramID != "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj" || cfg.TransferFeeBps != 100 {
		t.Errorf("static config = %+v", cfg)
	}
}

// Launches are quoted in $STONK only: an enabled non-$STONK catalog row (SOL, an xStock) is
// ErrLaunchQuoteNotAllowed, distinct from the 404 of an unknown or disabled mint.
func TestGetConfig_NonSTONKQuoteNotAllowed(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())
	for _, mint := range []string{SOLMint, testNVDAxMint} {
		if _, err := f.svc.GetConfig(context.Background(), mint); !errors.Is(err, ErrLaunchQuoteNotAllowed) {
			t.Errorf("GetConfig(%s) err = %v, want ErrLaunchQuoteNotAllowed", mint, err)
		}
	}
	// Explicitly asking for $STONK is the same as the default.
	cfg, err := f.svc.GetConfig(context.Background(), DefaultLaunchQuoteMint)
	if err != nil || cfg.Quote.QuoteMint != DefaultLaunchQuoteMint {
		t.Errorf("GetConfig($STONK) = %+v, %v", cfg, err)
	}
}

func TestGetConfig_EnforcesMinimumRaise(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())
	// 85 SOL at 102 USD / 0.295 USD sizes to 29389.83 STONK; raise the quote's minimum above
	// that to exercise the floor.
	quotes := repository.NewMemoryLaunchQuoteRepository()
	seedLaunchQuotes(quotes)
	quotes.Put(&models.LaunchQuote{QuoteMint: DefaultLaunchQuoteMint, Symbol: "STONK", Decimals: 9, Enabled: true, MinFundRaisingRaw: "100000000000000"})
	f.svc.quotes = quotes

	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}
	if cfg.Raise.Raw.String() != "100000000000000" {
		t.Errorf("raise.raw = %s, want clamped to minimum 100000000000000", cfg.Raise.Raw)
	}
}

func TestGetConfig_StaleAfter15Minutes(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())

	f.clk.Advance(14 * time.Minute)
	cfg, _ := f.svc.GetConfig(context.Background(), "")
	if cfg.Fee.Stale {
		t.Error("stale at 14m, want fresh")
	}
	f.clk.Advance(2 * time.Minute)
	cfg, _ = f.svc.GetConfig(context.Background(), "")
	if !cfg.Fee.Stale {
		t.Error("not stale at 16m, want stale")
	}
}

func TestGetConfig_UnknownOrDisabledQuote(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())

	if _, err := f.svc.GetConfig(context.Background(), "UnknownMint111111111111111111111111111111"); !errors.Is(err, ErrLaunchQuoteNotFound) {
		t.Errorf("unknown quote error = %v, want ErrLaunchQuoteNotFound", err)
	}
	if _, err := f.svc.GetConfig(context.Background(), testUSDCMint); !errors.Is(err, ErrLaunchQuoteNotFound) {
		t.Errorf("disabled quote error = %v, want ErrLaunchQuoteNotFound", err)
	}
}

func TestGetConfig_FeeNotPricedYet(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	if _, err := f.svc.GetConfig(context.Background(), ""); !errors.Is(err, ErrLaunchFeeUnavailable) {
		t.Errorf("GetConfig() before RefreshFee error = %v, want ErrLaunchFeeUnavailable", err)
	}
}

func TestGetConfig_QuotePriceCacheAndFallback(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())
	first, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}

	// Within TTL: price change in the feed is not observed (cached).
	f.feed.Prices[DefaultLaunchQuoteMint] = TokenPrice{USDPrice: 0.5, USDPriceRaw: 0.5}
	f.clk.Advance(10 * time.Second)
	second, _ := f.svc.GetConfig(context.Background(), "")
	if second.Raise.Raw.Cmp(first.Raise.Raw) != 0 {
		t.Errorf("raise changed within cache TTL: %s -> %s", first.Raise.Raw, second.Raise.Raw)
	}

	// After TTL with feed down: falls back to the stale cached price instead of failing.
	f.feed.Err = errors.New("feed down")
	f.clk.Advance(40 * time.Second)
	third, err := f.svc.GetConfig(context.Background(), "")
	if err != nil || third.Raise.Raw.Cmp(first.Raise.Raw) != 0 {
		t.Errorf("fallback: err = %v, raise = %v", err, third)
	}

	// After TTL with feed up: new price is used.
	f.feed.Err = nil
	f.clk.Advance(40 * time.Second)
	fourth, _ := f.svc.GetConfig(context.Background(), "")
	if fourth.Raise.Raw.String() != "17340000000000" { // 85*102/0.5 = 17340 STONK
		t.Errorf("raise after TTL = %s, want 17340000000000", fourth.Raise.Raw)
	}
}

func TestGetConfig_QuotePriceUnavailable_NoCache(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	_, _ = f.svc.RefreshFee(context.Background())
	f.feed.Err = errors.New("feed down")
	if _, err := f.svc.GetConfig(context.Background(), ""); !errors.Is(err, ErrQuotePriceUnavailable) {
		t.Errorf("GetConfig($STONK) with feed down and no cache error = %v, want ErrQuotePriceUnavailable", err)
	}
}

// The raise is sized from USDPriceRaw (the USD value of 10^decimals raw units), not the UI
// price — the two differ for scaled-UI-amount mints, and the sizing must follow raw units.
func TestGetConfig_UsesPrescaledRawPrice(t *testing.T) {
	f := newLaunchFixture(t, 102, 0.295)
	f.feed.Prices[DefaultLaunchQuoteMint] = TokenPrice{USDPrice: 0.3, USDPriceRaw: 0.295}
	_, _ = f.svc.RefreshFee(context.Background())
	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}
	if cfg.Raise.Raw.String() != "29389830508475" {
		t.Errorf("raise.raw = %s, want 29389830508475 (from USDPriceRaw 0.295, not UI 0.3)", cfg.Raise.Raw)
	}
}

func TestResolveLaunchCluster(t *testing.T) {
	cases := []struct{ explicit, program, want string }{
		{"", LaunchLabProgramDevnet, models.LaunchClusterDevnet},
		{"", LaunchLabProgramMainnet, models.LaunchClusterMainnet},
		{"", "", models.LaunchClusterMainnet},
		{"", "SomeOtherProgram1111111111111111111111111111", models.LaunchClusterMainnet},
		{"devnet", LaunchLabProgramMainnet, models.LaunchClusterDevnet}, // explicit wins
		{" Mainnet ", LaunchLabProgramDevnet, models.LaunchClusterMainnet},
		{"testnet", LaunchLabProgramDevnet, models.LaunchClusterDevnet}, // invalid explicit falls back to derivation
	}
	for _, tc := range cases {
		if got := ResolveLaunchCluster(tc.explicit, tc.program); got != tc.want {
			t.Errorf("ResolveLaunchCluster(%q, %q) = %q, want %q", tc.explicit, tc.program, got, tc.want)
		}
	}
}

// newDevnetFixture builds the service the way the dev tracker runs: devnet program, a quote
// repository scoped to devnet holding the migration-014 devnet SOL row alongside mainnet rows.
func newDevnetFixture(t *testing.T) *launchFixture {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))
	feed := &StubPriceFeed{Prices: map[string]TokenPrice{SOLMint: {USDPrice: 100, USDPriceRaw: 100}}}
	quotes := repository.NewMemoryLaunchQuoteRepositoryForCluster(models.LaunchClusterDevnet)
	seedLaunchQuotes(quotes) // mainnet rows: must be invisible on devnet
	quotes.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: SOLMint, Symbol: "SOL", Name: "Wrapped SOL (devnet)", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "solana",
		LaunchLabConfigID: "7ZR4zD7PYfY2XxoG1Gxcy2EgEeGYrpxrwzPuwdUBssEt", MinFundRaisingRaw: "1", Enabled: true, SortOrder: 0})
	settings := repository.NewMemoryLaunchSettingsRepository()
	cfg := testLaunchSettings()
	cfg.ProgramID = LaunchLabProgramDevnet
	cfg.Cluster = ResolveLaunchCluster("", cfg.ProgramID)
	svc := NewLaunchConfigService(LaunchConfigServiceDeps{Settings: settings, Quotes: quotes, Prices: feed, Clock: clk, Config: cfg})
	return &launchFixture{svc: svc, clk: clk, feed: feed, settings: settings}
}

// A devnet catalog without the migration-015 stand-in has no launchable quote: SOL never
// becomes the default (it is not allowed), and mainnet rows must not leak through.
func TestGetConfig_DevnetWithoutStandInHasNoLaunchableQuote(t *testing.T) {
	f := newDevnetFixture(t)
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}

	if _, err := f.svc.GetConfig(context.Background(), ""); !errors.Is(err, ErrLaunchQuoteNotFound) {
		t.Errorf("GetConfig(default) on devnet without stand-in err = %v, want ErrLaunchQuoteNotFound", err)
	}
	if _, err := f.svc.GetConfig(context.Background(), SOLMint); !errors.Is(err, ErrLaunchQuoteNotAllowed) {
		t.Errorf("GetConfig(SOL) on devnet err = %v, want ErrLaunchQuoteNotAllowed", err)
	}
	// The mainnet $STONK row must not leak through as the devnet quote.
	if _, err := f.svc.GetConfig(context.Background(), DefaultLaunchQuoteMint); !errors.Is(err, ErrLaunchQuoteNotFound) {
		t.Errorf("GetConfig(STONK) on devnet err = %v, want ErrLaunchQuoteNotFound", err)
	}
	if _, err := f.svc.GetConfig(context.Background(), testNVDAxMint); !errors.Is(err, ErrLaunchQuoteNotFound) {
		t.Errorf("GetConfig(NVDAx) on devnet err = %v, want ErrLaunchQuoteNotFound", err)
	}
}

func TestGetConfig_MainnetDefaultStaysSTONK(t *testing.T) {
	f := newLaunchFixture(t, 100, 0.25)
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}
	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Cluster != models.LaunchClusterMainnet || cfg.Quote.QuoteMint != DefaultLaunchQuoteMint {
		t.Errorf("cluster/default quote = %q/%q, want mainnet/$STONK", cfg.Cluster, cfg.Quote.QuoteMint)
	}
}

// Devnet $STONK stand-in (migration 015): Raydium's devnet USDC mint under category "stonk".
const (
	testDevnetSTONKMint   = "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT"
	testDevnetSTONKConfig = "4wHbNkobu7iARU9MbCEqDSAq6JuQreGupG2Jsf2R3DFP"
	testDevnetSOLConfig   = "7ZR4zD7PYfY2XxoG1Gxcy2EgEeGYrpxrwzPuwdUBssEt"
)

// newDevnetStandInFixture is newDevnetFixture after migration 015: the $STONK stand-in row at
// sort_order 0 and the SOL row bumped to 1, exactly as the migration leaves launch_quotes.
func newDevnetStandInFixture(t *testing.T) *launchFixture {
	t.Helper()
	f := newDevnetFixture(t)
	quotes := f.svc.quotes.(*repository.MemoryLaunchQuoteRepository)
	quotes.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: testDevnetSTONKMint, Symbol: "STONK",
		Name: "$STONK (devnet stand-in)", Decimals: 6, TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
		Category: models.LaunchQuoteCategorySTONK, LaunchLabConfigID: testDevnetSTONKConfig, MinFundRaisingRaw: "1", Enabled: true, SortOrder: 0})
	quotes.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: SOLMint, Symbol: "SOL", Name: "Wrapped SOL (devnet)", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "solana",
		LaunchLabConfigID: testDevnetSOLConfig, MinFundRaisingRaw: "1", Enabled: true, SortOrder: 1})
	return f
}

func TestIsSTONKQuote(t *testing.T) {
	cases := []struct {
		name string
		q    *models.LaunchQuote
		want bool
	}{
		{"nil", nil, false},
		{"mainnet mint, category custom", &models.LaunchQuote{QuoteMint: DefaultLaunchQuoteMint, Category: "custom"}, true},
		{"devnet stand-in by category", &models.LaunchQuote{QuoteMint: testDevnetSTONKMint, Category: models.LaunchQuoteCategorySTONK}, true},
		{"SOL", &models.LaunchQuote{QuoteMint: SOLMint, Category: "solana"}, false},
		{"mainnet USDC", &models.LaunchQuote{QuoteMint: testUSDCMint, Category: "currency"}, false},
	}
	for _, tc := range cases {
		if got := IsSTONKQuote(tc.q); got != tc.want {
			t.Errorf("%s: IsSTONKQuote = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGetConfig_DevnetStandInSTONKIsDefault(t *testing.T) {
	f := newDevnetStandInFixture(t)
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}

	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig(default) on devnet error = %v", err)
	}
	if cfg.Cluster != models.LaunchClusterDevnet {
		t.Errorf("Cluster = %q, want devnet", cfg.Cluster)
	}
	if cfg.Quote.QuoteMint != testDevnetSTONKMint || cfg.Quote.Symbol != "STONK" || cfg.Quote.LaunchLabConfigID != testDevnetSTONKConfig {
		t.Errorf("default quote = %+v, want the devnet $STONK stand-in", cfg.Quote)
	}
	if cfg.Curve.ConfigID != testDevnetSTONKConfig {
		t.Errorf("Curve.ConfigID = %q, want %s", cfg.Curve.ConfigID, testDevnetSTONKConfig)
	}
	// SOL stays an enabled catalog row but is not served as launchable; no mainnet row leaks through.
	if len(cfg.Quotes) != 1 || cfg.Quotes[0].QuoteMint != testDevnetSTONKMint || cfg.DefaultQuoteMint != testDevnetSTONKMint {
		t.Errorf("Quotes = %+v default=%q, want exactly [stand-in STONK]", cfg.Quotes, cfg.DefaultQuoteMint)
	}
	// The stand-in is not on Jupiter: the devnet fallback prices it at 1 USD per whole unit, so the
	// raise is 85 SOL * 100 USD/SOL = 8500 USDC = 8_500_000_000 raw (6 decimals).
	if cfg.Raise.Raw.String() != "8500000000" || cfg.Raise.Units != 8500 {
		t.Errorf("Raise = %s raw / %v units, want 8500000000 / 8500", cfg.Raise.Raw, cfg.Raise.Units)
	}

	// Explicit selection: the stand-in works, devnet SOL is a known row but not launchable.
	if _, err := f.svc.GetConfig(context.Background(), SOLMint); !errors.Is(err, ErrLaunchQuoteNotAllowed) {
		t.Errorf("GetConfig(SOL) on devnet err = %v, want ErrLaunchQuoteNotAllowed", err)
	}
	standIn, err := f.svc.GetConfig(context.Background(), testDevnetSTONKMint)
	if err != nil || standIn.Quote.LaunchLabConfigID != testDevnetSTONKConfig {
		t.Errorf("GetConfig(stand-in) on devnet = %+v, %v; want the stand-in row", standIn, err)
	}
	// Mainnet $STONK is still not a devnet quote.
	if _, err := f.svc.GetConfig(context.Background(), DefaultLaunchQuoteMint); !errors.Is(err, ErrLaunchQuoteNotFound) {
		t.Errorf("GetConfig(mainnet STONK) on devnet err = %v, want ErrLaunchQuoteNotFound", err)
	}
}

func TestGetConfig_DevnetUsesFeedPriceWhenAvailable(t *testing.T) {
	f := newDevnetStandInFixture(t)
	f.feed.Prices[testDevnetSTONKMint] = TokenPrice{USDPrice: 2, USDPriceRaw: 2}
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}
	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	// 85 * 100 / 2 = 4250 units -> 4_250_000_000 raw: the feed price wins over the fallback.
	if cfg.Raise.Raw.String() != "4250000000" {
		t.Errorf("Raise.Raw = %s, want 4250000000", cfg.Raise.Raw)
	}
}

func TestGetConfig_PriceFallbackIsDevnetOnly(t *testing.T) {
	// Mainnet: an unpriced non-SOL quote is still an error (no silent 1 USD assumption).
	f := newLaunchFixture(t, 100, 0.25)
	delete(f.feed.Prices, DefaultLaunchQuoteMint)
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}
	if _, err := f.svc.GetConfig(context.Background(), ""); !errors.Is(err, ErrQuotePriceUnavailable) {
		t.Errorf("mainnet GetConfig with unpriced $STONK err = %v, want ErrQuotePriceUnavailable", err)
	}
}

func TestFixedRaiseRaw(t *testing.T) {
	if got := FixedRaiseRaw(25000, 6).String(); got != "25000000000" {
		t.Errorf("FixedRaiseRaw(25000, 6) = %s, want 25000000000", got)
	}
	if got := FixedRaiseRaw(25000, 9).String(); got != "25000000000000" {
		t.Errorf("FixedRaiseRaw(25000, 9) = %s, want 25000000000000", got)
	}
	if got := FixedRaiseRaw(0, 6).Sign(); got != 0 {
		t.Errorf("FixedRaiseRaw(0, 6) should be zero")
	}
	if got := formatUnits(25000); got != "25,000" {
		t.Errorf("formatUnits(25000) = %q", got)
	}
	if got := formatUnits(1234567.5); got != "1,234,567.5" {
		t.Errorf("formatUnits(1234567.5) = %q", got)
	}
}

// LAUNCH_RAISE_UNITS pins the graduation raise regardless of prices.
func TestGetConfig_FixedRaiseUnits(t *testing.T) {
	f := newDevnetStandInFixture(t)
	f.svc.cfg.RaiseUnits = 25000
	if _, err := f.svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}
	cfg, err := f.svc.GetConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Raise.Raw.String() != "25000000000" || cfg.Raise.Units != 25000 {
		t.Errorf("Raise = %s raw / %v units, want 25000000000 / 25000", cfg.Raise.Raw, cfg.Raise.Units)
	}
	if cfg.Raise.Basis != "Fixed raise of 25,000 STONK" {
		t.Errorf("Basis = %q", cfg.Raise.Basis)
	}
}

// LAUNCH_QUOTE_MINT makes another catalog row the one launchable quote (staging: $KNOTS).
func TestPinLaunchQuote(t *testing.T) {
	t.Cleanup(func() { PinLaunchQuote("") })
	knots := &models.LaunchQuote{QuoteMint: "8RVBk8vxLiUHueLUW1f4izFVqN3nWippLhkohKg6EGkS", Symbol: "KNOTS", Category: "custom", Enabled: true}
	stonk := &models.LaunchQuote{QuoteMint: DefaultLaunchQuoteMint, Symbol: "STONK", Category: "custom", Enabled: true}
	if IsSTONKQuote(knots) {
		t.Fatal("KNOTS must not be the launchable quote before it is pinned")
	}
	PinLaunchQuote(knots.QuoteMint)
	if !IsSTONKQuote(knots) || IsSTONKQuote(stonk) {
		t.Errorf("pinned: KNOTS=%v STONK=%v, want true/false", IsSTONKQuote(knots), IsSTONKQuote(stonk))
	}
	PinLaunchQuote("")
	if IsSTONKQuote(knots) || !IsSTONKQuote(stonk) {
		t.Errorf("unpinned: KNOTS=%v STONK=%v, want false/true", IsSTONKQuote(knots), IsSTONKQuote(stonk))
	}
}
