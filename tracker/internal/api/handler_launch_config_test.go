// Package: tracker/internal/api
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Tests for GET /api/launch/config

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	testSTONKMint = services.DefaultLaunchQuoteMint
	testSOLMint   = services.SOLMint
)

// newLaunchTestServer builds a full Server (route wiring + rate limiter) with in-memory launch repos.
// priced=false leaves launch_settings empty so the fee is unavailable.
func newLaunchTestServer(t *testing.T, priced bool) (*Server, *services.LaunchConfigService, *clock.MockClock) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))

	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(&models.LaunchQuote{QuoteMint: testSTONKMint, Symbol: "STONK", Name: "STONK", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "custom",
		LaunchLabConfigID: "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW", MinFundRaisingRaw: "1", Enabled: true, SortOrder: 0})
	quotes.Put(&models.LaunchQuote{QuoteMint: testSOLMint, Symbol: "SOL", Name: "Wrapped SOL", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "solana",
		LaunchLabConfigID: "6s1xP3hpbAfFoNtUNF8mfHsjr2Bd97JxFJRWLbL6aHuX", MinFundRaisingRaw: "24000000000", Enabled: true, SortOrder: 1})
	quotes.Put(&models.LaunchQuote{QuoteMint: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", Symbol: "USDC", Name: "USD Coin", Decimals: 6,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "currency",
		LaunchLabConfigID: "8gj14w8vkNZjJTPak6e96Uf1sY438WKheH4s5dHyxiRw", MinFundRaisingRaw: "1", Enabled: false, SortOrder: 5})

	feed := &services.StubPriceFeed{Prices: map[string]services.TokenPrice{
		testSOLMint:   {USDPrice: 102, USDPriceRaw: 102},
		testSTONKMint: {USDPrice: 0.295, USDPriceRaw: 0.295},
	}}
	svc := services.NewLaunchConfigService(services.LaunchConfigServiceDeps{
		Settings: repository.NewMemoryLaunchSettingsRepository(),
		Quotes:   quotes,
		Prices:   feed,
		Clock:    clk,
		Config: services.LaunchConfigSettings{
			ProgramID:      "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj",
			PlatformID:     "PLATFORMtest1111111111111111111111111111111",
			Treasury:       "TREASURYtest1111111111111111111111111111111",
			TransferFeeBps: 100,
			FeeUSD:         0.50,
			MinFeeLamports: 4_000_000,
			MaxFeeLamports: 10_000_000,
		},
	})
	if priced {
		if _, err := svc.RefreshFee(context.Background()); err != nil {
			t.Fatalf("RefreshFee() error = %v", err)
		}
	}

	peerRepo := repository.NewMemoryPeerRepository()
	store := presence.NewMemoryPresenceStore(clk)
	peerSvc := services.NewPeerService(peerRepo, store, nil)
	assetRepo := repository.NewMemoryAssetRepository()
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	srv := NewServer(ServerDeps{
		PeerHandler:         NewPeerHandler(peerSvc),
		AssetHandler:        NewAssetHandlerWithPeers(assetSvc, peerSvc),
		DMCAHandler:         NewDMCAHandler(services.NewDMCAService(assetRepo, repository.NewMemoryDMCARepository())),
		LaunchConfigHandler: NewLaunchConfigHandler(svc),
		Limiter:             ratelimit.NewMemoryLimiter(clk),
		Address:             ":0",
	})
	return srv, svc, clk
}

func getLaunchConfig(t *testing.T, srv *Server, query string) (*httptest.ResponseRecorder, LaunchConfigDTO) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/launch/config"+query, nil)
	req.RemoteAddr = "203.0.113.10:5000"
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	var env struct {
		Data LaunchConfigDTO `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env.Data
}

func TestHandleLaunchConfig_DefaultSTONK(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	w, data := getLaunchConfig(t, srv, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	t.Logf("sample response: %s", w.Body.String())

	if data.ProgramID != "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj" || data.PlatformID != "PLATFORMtest1111111111111111111111111111111" ||
		data.Treasury != "TREASURYtest1111111111111111111111111111111" || data.TransferFeeBps != 100 {
		t.Errorf("static fields = %+v", data)
	}
	if data.Fee.USD != 0.5 || data.Fee.Lamports != 4_901_961 || data.Fee.SolUSD != 102 || data.Fee.Stale || data.Fee.PricedAt != "2026-09-11T12:00:00Z" {
		t.Errorf("fee = %+v", data.Fee)
	}
	if data.Quote.QuoteMint != testSTONKMint || data.Quote.Symbol != "STONK" || data.Quote.LaunchLabConfigID != "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW" {
		t.Errorf("quote = %+v", data.Quote)
	}
	// $STONK only: SOL is an enabled catalog row (metrics lookups) but never a launchable quote.
	if len(data.Quotes) != 1 || data.Quotes[0].QuoteMint != testSTONKMint {
		t.Errorf("quotes = %+v, want exactly [STONK]", data.Quotes)
	}
	if data.DefaultQuoteMint != testSTONKMint {
		t.Errorf("defaultQuoteMint = %q, want %s", data.DefaultQuoteMint, testSTONKMint)
	}
	if data.DevDripEnabled {
		t.Error("devDripEnabled = true, want false until SetDevDripEnabled(true)")
	}
	if data.Raise.Raw != "29389830508475" || data.Raise.MinimumRaw != "1" || data.Raise.Basis != services.LaunchRaiseBasis {
		t.Errorf("raise = %+v", data.Raise)
	}
	if data.Raise.Units < 29389.82 || data.Raise.Units > 29389.84 {
		t.Errorf("raise.units = %v, want ~29389.83", data.Raise.Units)
	}
	if data.Curve.ConfigID != "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW" || data.Curve.CurveType != "ConstantCurve" || data.Curve.MigrateType != "cpmm" ||
		data.Curve.BaseDecimals != 6 || data.Curve.Supply != "1000000000000000" || data.Curve.TotalSellA != "793100000000000" ||
		data.Curve.TotalLockedAmount != "0" || data.Curve.CliffPeriod != "0" || data.Curve.UnlockPeriod != "0" || data.Curve.CpmmCreatorFeeOn != 0 {
		t.Errorf("curve = %+v", data.Curve)
	}

	// Big amounts must be JSON strings, not numbers.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	var dataRaw map[string]json.RawMessage
	_ = json.Unmarshal(raw["data"], &dataRaw)
	var raiseRaw map[string]json.RawMessage
	_ = json.Unmarshal(dataRaw["raise"], &raiseRaw)
	if string(raiseRaw["raw"]) != `"29389830508475"` {
		t.Errorf("raise.raw JSON = %s, want quoted string", raiseRaw["raw"])
	}
}

// A known, enabled quote that is not $STONK is 422 QUOTE_NOT_ALLOWED — distinct from the 404
// of an unknown or disabled mint — so the portal can tell "pick $STONK" from "typo".
func TestHandleLaunchConfig_SOLQuote422NotAllowed(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	w, _ := getLaunchConfig(t, srv, "?quoteMint="+testSOLMint)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body: %s", w.Code, w.Body.String())
	}
	var env ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != LaunchQuoteNotAllowedCode {
		t.Errorf("error code = %q, want %s", env.Error.Code, LaunchQuoteNotAllowedCode)
	}
	// Asking for $STONK explicitly is the default answer.
	w, data := getLaunchConfig(t, srv, "?quoteMint="+testSTONKMint)
	if w.Code != http.StatusOK || data.Quote.QuoteMint != testSTONKMint {
		t.Errorf("explicit STONK = %d %+v", w.Code, data.Quote)
	}
}

func TestHandleLaunchConfig_DevDripEnabledFlag(t *testing.T) {
	srv, svc, _ := newLaunchTestServer(t, true)
	h := NewLaunchConfigHandler(svc)
	h.SetDevDripEnabled(true)
	srv = NewServer(ServerDeps{LaunchConfigHandler: h, Address: ":7842"})
	w, data := getLaunchConfig(t, srv, "")
	if w.Code != http.StatusOK || !data.DevDripEnabled {
		t.Errorf("status=%d devDripEnabled=%v, want 200 true", w.Code, data.DevDripEnabled)
	}
}

func TestHandleLaunchConfig_UnknownQuote404(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	w, _ := getLaunchConfig(t, srv, "?quoteMint=Unknown11111111111111111111111111111111111")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown quote status = %d, want 404, body: %s", w.Code, w.Body.String())
	}
	var env ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", env.Error.Code)
	}
}

func TestHandleLaunchConfig_DisabledQuote404(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	w, _ := getLaunchConfig(t, srv, "?quoteMint=EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v")
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled quote status = %d, want 404, body: %s", w.Code, w.Body.String())
	}
}

func TestHandleLaunchConfig_InvalidMint400(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	w, _ := getLaunchConfig(t, srv, "?quoteMint=not-a-mint")
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid mint status = %d, want 400, body: %s", w.Code, w.Body.String())
	}
}

func TestHandleLaunchConfig_FeeNotPriced503(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, false)
	w, _ := getLaunchConfig(t, srv, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unpriced status = %d, want 503, body: %s", w.Code, w.Body.String())
	}
}

func TestHandleLaunchConfig_StaleFlag(t *testing.T) {
	srv, _, clk := newLaunchTestServer(t, true)
	clk.Advance(16 * time.Minute)
	w, data := getLaunchConfig(t, srv, "")
	if w.Code != http.StatusOK || !data.Fee.Stale {
		t.Errorf("status = %d, fee.stale = %v, want 200 and stale=true", w.Code, data.Fee.Stale)
	}
}

func TestHandleLaunchConfig_RateLimited(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	limit := LaunchConfigRateLimitConfig().Limit
	var last *httptest.ResponseRecorder
	for i := 0; i <= limit; i++ {
		last, _ = getLaunchConfig(t, srv, "")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Errorf("request %d status = %d, want 429", limit+1, last.Code)
	}
}

// Devnet $STONK stand-in (migration 015): Raydium's devnet USDC mint under category "stonk".
const (
	testDevnetSTONKMint   = "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT"
	testDevnetSTONKConfig = "4wHbNkobu7iARU9MbCEqDSAq6JuQreGupG2Jsf2R3DFP"
)

// newDevnetQuoteRepo mirrors launch_quotes on the dev tracker after migration 015: the
// $STONK stand-in at sort_order 0, devnet SOL at 1, mainnet rows invisible.
func newDevnetQuoteRepo() *repository.MemoryLaunchQuoteRepository {
	quotes := repository.NewMemoryLaunchQuoteRepositoryForCluster(models.LaunchClusterDevnet)
	quotes.Put(&models.LaunchQuote{QuoteMint: testSTONKMint, Symbol: "STONK", Enabled: true, SortOrder: 0}) // mainnet only
	quotes.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: testDevnetSTONKMint, Symbol: "STONK",
		Name: "$STONK (devnet stand-in)", Decimals: 6, TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
		Category: models.LaunchQuoteCategorySTONK, LaunchLabConfigID: testDevnetSTONKConfig, MinFundRaisingRaw: "1", Enabled: true, SortOrder: 0})
	quotes.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: testSOLMint, Symbol: "SOL", Name: "Wrapped SOL (devnet)", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "solana",
		LaunchLabConfigID: "7ZR4zD7PYfY2XxoG1Gxcy2EgEeGYrpxrwzPuwdUBssEt", MinFundRaisingRaw: "1", Enabled: true, SortOrder: 1})
	return quotes
}

// TestHandleLaunchConfig_CarriesCluster pins the `cluster` field: mainnet by default, and on a
// devnet tracker the single quote is the migration-015 $STONK stand-in — devnet SOL is 422
// QUOTE_NOT_ALLOWED and a mainnet-only mint is 404.
func TestHandleLaunchConfig_CarriesCluster(t *testing.T) {
	srv, _, _ := newLaunchTestServer(t, true)
	w, data := getLaunchConfig(t, srv, "")
	if w.Code != http.StatusOK || data.Cluster != models.LaunchClusterMainnet || data.Quote.Cluster != models.LaunchClusterMainnet {
		t.Fatalf("mainnet: status=%d cluster=%q quote.cluster=%q body=%s", w.Code, data.Cluster, data.Quote.Cluster, w.Body.String())
	}

	clk := clock.NewMockClock(time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))
	quotes := newDevnetQuoteRepo()
	svc := services.NewLaunchConfigService(services.LaunchConfigServiceDeps{
		Settings: repository.NewMemoryLaunchSettingsRepository(), Quotes: quotes,
		Prices: &services.StubPriceFeed{Prices: map[string]services.TokenPrice{testSOLMint: {USDPrice: 102, USDPriceRaw: 102}}},
		Clock:  clk,
		Config: services.LaunchConfigSettings{
			Cluster: services.ResolveLaunchCluster("", services.LaunchLabProgramDevnet), ProgramID: services.LaunchLabProgramDevnet,
			TransferFeeBps: 100, FeeUSD: 0.50, MinFeeLamports: 4_000_000, MaxFeeLamports: 10_000_000,
		},
	})
	if _, err := svc.RefreshFee(context.Background()); err != nil {
		t.Fatalf("RefreshFee: %v", err)
	}
	dev := NewServer(ServerDeps{LaunchConfigHandler: NewLaunchConfigHandler(svc), Address: ":7842"})

	w, data = getLaunchConfig(t, dev, "")
	if w.Code != http.StatusOK {
		t.Fatalf("devnet default: status = %d body=%s", w.Code, w.Body.String())
	}
	if data.Cluster != models.LaunchClusterDevnet || data.ProgramID != services.LaunchLabProgramDevnet {
		t.Errorf("devnet cluster/program = %q/%q", data.Cluster, data.ProgramID)
	}
	if data.Quote.QuoteMint != testDevnetSTONKMint || data.Quote.Cluster != models.LaunchClusterDevnet || data.Curve.ConfigID != testDevnetSTONKConfig {
		t.Errorf("devnet default quote = %+v curve=%+v, want the devnet $STONK stand-in", data.Quote, data.Curve)
	}
	if len(data.Quotes) != 1 || data.Quotes[0].QuoteMint != testDevnetSTONKMint || data.DefaultQuoteMint != testDevnetSTONKMint {
		t.Errorf("devnet quotes = %+v default=%q, want only the stand-in", data.Quotes, data.DefaultQuoteMint)
	}
	// The stand-in has no market price: the devnet fallback prices it at 1 USD per unit,
	// 85 SOL * 102 USD = 8670 USDC = 8_670_000_000 raw (6 decimals).
	if data.Raise.Raw != "8670000000" {
		t.Errorf("devnet raise.raw = %s, want 8670000000", data.Raise.Raw)
	}
	if w, _ := getLaunchConfig(t, dev, "?quoteMint="+testSOLMint); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("devnet SOL: status = %d, want 422 QUOTE_NOT_ALLOWED", w.Code)
	}
	if w, _ := getLaunchConfig(t, dev, "?quoteMint="+testSTONKMint); w.Code != http.StatusNotFound {
		t.Errorf("devnet mainnet-STONK: status = %d, want 404 (mainnet-only quote)", w.Code)
	}
}
