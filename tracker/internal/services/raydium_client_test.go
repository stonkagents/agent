// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Tests for Raydium LaunchLab client — fixture parsing, pool decoding, curve math, RPC fallback
//
// Fixtures under testdata/ are verbatim live responses captured 2026-09-12:
//   raydium_mint_info.json            GET launch-mint-v1.raydium.io/get/by/mints?ids=3U11GQ… (OPENTCG, $STONK quote)
//   raydium_mint_info_graduated.json  same for Dz9mQ9…Mbonk (USELESS, graduated, WSOL quote)
//   raydium_pool_account.json         getAccountInfo(F5eCY1…) — OPENTCG pool, status 0
//   raydium_pool_account_partial.json getAccountInfo(Ck7Rre…) — ZEC-quoted pool, ~19.9% funded
//   raydium_pool_account_graduated.json getAccountInfo(GWqWrb…) — USELESS pool, status 2
//   jupiter_price_v3.json             GET lite-api.jup.ag/price/v3?ids=<STONK>,<WSOL>

package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
)

const (
	fxMint     = "3U11GQJQTf94P5gAPdvHFWuFFprWCgZFk3KV6ovxa46V"
	fxPool     = "F5eCY1hG4ohq1JcQQxoJ2VVEgYYvsJzZAEtGQ642qvw3"
	fxGradMint = "Dz9mQ9NzkBcCsuGPFJ3r1bS4wgqKMHBPiVuniW8Mbonk"
	fxGradPool = "GWqWrb44KJ8rmKvytQVUDh9X2pAVUkT8zE5RqTnJ4Dw4"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// fixtureAccount extracts the raw account bytes from a getAccountInfo fixture.
func fixtureAccount(t *testing.T, name string) *RPCAccount {
	t.Helper()
	var resp struct {
		Result struct {
			Value struct {
				Data  []string `json:"data"`
				Owner string   `json:"owner"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(readFixture(t, name), &resp); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	data, err := base64.StdEncoding.DecodeString(resp.Result.Value.Data[0])
	if err != nil {
		t.Fatalf("fixture %s base64: %v", name, err)
	}
	return &RPCAccount{Owner: resp.Result.Value.Owner, Data: data}
}

// --- fakes ---

type fakeAccountReader struct {
	accounts map[string]*RPCAccount
	err      error
	calls    atomic.Int32
	lastReq  []string
}

func (f *fakeAccountReader) GetMultipleAccounts(_ context.Context, addrs []string) ([]*RPCAccount, error) {
	f.calls.Add(1)
	f.lastReq = addrs
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*RPCAccount, len(addrs))
	for i, a := range addrs {
		out[i] = f.accounts[a]
	}
	return out, nil
}

type fakePrices struct {
	prices map[string]float64
	err    error
}

func (f *fakePrices) GetUsdPrice(_ context.Context, mint string) (float64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	p, ok := f.prices[mint]
	return p, ok, nil
}

func approx(a, b, rel float64) bool {
	if b == 0 {
		return math.Abs(a) < rel
	}
	return math.Abs(a-b)/math.Abs(b) < rel
}

// --- API response parsing ---

func TestRaydium_ParseMintInfo_Fixture(t *testing.T) {
	row, err := parseRaydiumMintInfo(readFixture(t, "raydium_mint_info.json"), fxMint)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if row == nil {
		t.Fatal("row nil")
	}
	if row.PoolID != fxPool {
		t.Errorf("poolId = %s, want %s", row.PoolID, fxPool)
	}
	if row.MintB.Address != StonkMint || int(row.MintB.Decimals) != 9 || row.MintB.Symbol != "STONK" {
		t.Errorf("mintB = %+v", row.MintB)
	}
	if int(row.Decimals) != 6 || float64(row.Supply) != 1_000_000_000 {
		t.Errorf("decimals/supply = %v/%v", row.Decimals, row.Supply)
	}
	if float64(row.TotalFundRaisingB) != 29351624398055 {
		t.Errorf("totalFundRaisingB = %v", row.TotalFundRaisingB)
	}
	if row.MarketCap <= 0 || row.CreateAt != 1789143217000 || row.ImgURL == "" {
		t.Errorf("marketCap/createAt/imgUrl = %v/%v/%q", row.MarketCap, row.CreateAt, row.ImgURL)
	}
	if int(row.ConfigInfo.CurveType) != 0 {
		t.Errorf("curveType = %v, want 0 (constant product)", row.ConfigInfo.CurveType)
	}
	if row.MigrateAmmID != "" {
		t.Errorf("migrateAmmId should be empty for bonding token, got %s", row.MigrateAmmID)
	}
}

func TestRaydium_ParseMintInfo_GraduatedFixture(t *testing.T) {
	row, err := parseRaydiumMintInfo(readFixture(t, "raydium_mint_info_graduated.json"), fxGradMint)
	if err != nil || row == nil {
		t.Fatalf("parse: row=%v err=%v", row, err)
	}
	if float64(row.FinishingRate) != 100 || row.MigrateAmmID == "" {
		t.Errorf("finishingRate=%v migrateAmmId=%q, want 100 / non-empty", row.FinishingRate, row.MigrateAmmID)
	}
	if row.MintB.Address != WrappedSolMint {
		t.Errorf("mintB = %s, want WSOL", row.MintB.Address)
	}
}

func TestRaydium_ParseMintInfo_EmptyRowsAndErrors(t *testing.T) {
	row, err := parseRaydiumMintInfo([]byte(`{"id":"x","success":true,"data":{"rows":[]}}`), fxMint)
	if err != nil || row != nil {
		t.Errorf("empty rows: row=%v err=%v, want nil,nil", row, err)
	}
	if _, err := parseRaydiumMintInfo([]byte(`{"id":"x","success":false,"msg":"ids type error 2"}`), fxMint); err == nil {
		t.Error("success=false should error")
	}
	if _, err := parseRaydiumMintInfo([]byte(`<html>`), fxMint); err == nil {
		t.Error("non-JSON should error")
	}
}

func TestJupiter_ParsePrices_Fixture(t *testing.T) {
	prices, err := parseJupiterPrices(readFixture(t, "jupiter_price_v3.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p := prices[StonkMint]; !(p > 0.01 && p < 100) {
		t.Errorf("STONK usdPrice = %v", p)
	}
	if p := prices[WrappedSolMint]; !(p > 1 && p < 100000) {
		t.Errorf("SOL usdPrice = %v", p)
	}
	if _, ok := prices["missing"]; ok {
		t.Error("unknown mint should be absent")
	}
}

// --- Pool decoding + math ---

func TestRaydium_DecodePool_Fixture(t *testing.T) {
	acc := fixtureAccount(t, "raydium_pool_account.json")
	if acc.Owner != LaunchLabProgramMainnet {
		t.Fatalf("owner = %s", acc.Owner)
	}
	p, err := DecodeLaunchLabPool(acc.Data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Status != 0 || p.MintDecimalsA != 6 || p.MintDecimalsB != 9 || p.MigrateType != 1 {
		t.Errorf("header = %+v", p)
	}
	if p.Supply != 1_000_000_000_000_000 || p.TotalSellA != 793_100_000_000_000 || p.TotalFundRaisingB != 29351624398055 {
		t.Errorf("supply/totalSellA/tfb = %d/%d/%d", p.Supply, p.TotalSellA, p.TotalFundRaisingB)
	}
	if p.VirtualA != 1073025605595372 || p.VirtualB != 10359691381708 || p.RealA != 0 || p.RealB != 1 {
		t.Errorf("virtual/real = %d/%d/%d/%d", p.VirtualA, p.VirtualB, p.RealA, p.RealB)
	}
	if p.MintA != fxMint || p.MintB != StonkMint {
		t.Errorf("mintA/mintB = %s/%s", p.MintA, p.MintB)
	}
	if p.ConfigID != "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW" || p.PlatformID != "4E876qZTE9FJMrBzgVtBrSrzz2TLivB5Y5QXPjB4gZL7" {
		t.Errorf("configId/platformId = %s/%s", p.ConfigID, p.PlatformID)
	}
	if p.Creator != "CE7gJhaTZZjf3ewcKYtVLgNyYJEZTHgdaf1kP3j7ZwT" {
		t.Errorf("creator = %s", p.Creator)
	}
	if p.Graduated() || p.ProgressPercent() != 0 {
		t.Errorf("graduated=%v pct=%d, want false/0", p.Graduated(), p.ProgressPercent())
	}
	// initial price = virtualB/virtualA scaled = 9.6546e-6 STONK per token (API initPrice)
	price, ok := p.PriceQuoteUI()
	if !ok || !approx(price, 9.6546543975e-6, 1e-6) {
		t.Errorf("price = %v ok=%v, want ~9.6547e-6", price, ok)
	}
	if !approx(p.SupplyUI(), 1e9, 1e-9) || !approx(p.QuoteTargetUI(), 29351.624398055, 1e-9) {
		t.Errorf("supplyUI=%v targetUI=%v", p.SupplyUI(), p.QuoteTargetUI())
	}
}

func TestRaydium_DecodePool_PartialProgress(t *testing.T) {
	p, err := DecodeLaunchLabPool(fixtureAccount(t, "raydium_pool_account_partial.json").Data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// realB 150896877 / tfb 758901186 = 19.88%
	if !approx(p.ProgressRatio(), 0.19883600, 1e-6) || p.ProgressPercent() != 20 {
		t.Errorf("ratio=%v pct=%d", p.ProgressRatio(), p.ProgressPercent())
	}
	if p.MintDecimalsB != 8 || !approx(p.QuoteRaisedUI(), 1.50896877, 1e-9) {
		t.Errorf("decB=%d raised=%v", p.MintDecimalsB, p.QuoteRaisedUI())
	}
}

func TestRaydium_DecodePool_Graduated(t *testing.T) {
	p, err := DecodeLaunchLabPool(fixtureAccount(t, "raydium_pool_account_graduated.json").Data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Status != 2 || !p.Graduated() || p.ProgressPercent() != 100 {
		t.Errorf("status=%d graduated=%v pct=%d", p.Status, p.Graduated(), p.ProgressPercent())
	}
	if p.MintB != WrappedSolMint || p.MintA != fxGradMint {
		t.Errorf("mints = %s/%s", p.MintA, p.MintB)
	}
	if !approx(p.QuoteRaisedUI(), 85.000000209, 1e-9) || !approx(p.QuoteTargetUI(), 85, 1e-9) {
		t.Errorf("raised=%v target=%v", p.QuoteRaisedUI(), p.QuoteTargetUI())
	}
}

func TestRaydium_DecodePool_Rejects(t *testing.T) {
	if _, err := DecodeLaunchLabPool(make([]byte, 100)); err == nil {
		t.Error("short account should fail")
	}
	bad := make([]byte, 429)
	if _, err := DecodeLaunchLabPool(bad); err == nil {
		t.Error("bad discriminator should fail")
	}
}

func TestRaydium_ProgressPercent_Caps(t *testing.T) {
	p := &LaunchLabPoolState{RealB: 999, TotalFundRaisingB: 1000, Status: 0}
	if got := p.ProgressPercent(); got != 99 {
		t.Errorf("99.9%% funding → %d, want 99 (capped pre-graduation)", got)
	}
	p.RealB = 1000
	if got := p.ProgressPercent(); got != 99 {
		t.Errorf("100%% funded but status Fund → %d, want 99", got)
	}
	p.Status = 1
	if got := p.ProgressPercent(); got != 100 {
		t.Errorf("status Migrate → %d, want 100", got)
	}
	if (&LaunchLabPoolState{}).ProgressPercent() != 0 {
		t.Error("zero target → 0")
	}
}

func TestRaydium_MarketCapMath(t *testing.T) {
	// 1e9 tokens × 9.6546e-6 STONK × $0.2947 ≈ $2845
	mcap := LaunchLabMarketCapUsd(9.6546543975e-6, 1e9, 0.2947026581325139)
	if !approx(mcap, 2845.25, 1e-3) {
		t.Errorf("mcap = %v, want ~2845", mcap)
	}
	if _, ok := (&LaunchLabPoolState{VirtualA: 5, RealA: 5}).PriceQuoteUI(); ok {
		t.Error("degenerate curve should report !ok")
	}
}

func TestRaydium_DeriveLaunchLabPoolID(t *testing.T) {
	id, err := DeriveLaunchLabPoolID(LaunchLabProgramMainnet, fxMint, StonkMint)
	if err != nil || id != fxPool {
		t.Errorf("pool id = %s err=%v, want %s", id, err, fxPool)
	}
	if _, err := DeriveLaunchLabPoolID(LaunchLabProgramMainnet, "not-base58!", StonkMint); err == nil {
		t.Error("bad mint should error")
	}
}

// --- HTTP client composition ---

func newFixtureAPI(t *testing.T, status int, body []byte, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		if r.URL.Path != "/get/by/mints" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("ids") == "" {
			t.Error("missing ids query")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

func TestRaydiumClient_APIAndRPC_ComposesOnChainNumbers(t *testing.T) {
	srv := newFixtureAPI(t, 200, readFixture(t, "raydium_mint_info.json"), nil)
	defer srv.Close()
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{fxPool: fixtureAccount(t, "raydium_pool_account.json")}}
	c := NewRaydiumClient(RaydiumClientConfig{
		APIBaseURL: srv.URL,
		RPC:        rpc,
		Prices:     &fakePrices{prices: map[string]float64{StonkMint: 0.30}},
	})

	m, err := c.GetCoinData(context.Background(), fxMint, LaunchLabHint{})
	if err != nil {
		t.Fatalf("GetCoinData: %v", err)
	}
	if m == nil {
		t.Fatal("metrics nil")
	}
	if m.Source != "launchlab" || m.PoolId == nil || *m.PoolId != fxPool {
		t.Errorf("source/poolId = %s/%v", m.Source, m.PoolId)
	}
	if m.QuoteMint == nil || *m.QuoteMint != StonkMint || m.QuoteDecimals == nil || *m.QuoteDecimals != 9 {
		t.Errorf("quote = %v/%v", m.QuoteMint, m.QuoteDecimals)
	}
	if m.BondingCurvePercent == nil || *m.BondingCurvePercent != 0 || m.Complete == nil || *m.Complete {
		t.Errorf("progress/complete = %v/%v", m.BondingCurvePercent, m.Complete)
	}
	if m.QuoteRaised == nil || !approx(*m.QuoteRaised, 1e-9, 1e-12) || m.SolRaised != nil {
		t.Errorf("quoteRaised=%v solRaised=%v (STONK quote must not set solRaised)", m.QuoteRaised, m.SolRaised)
	}
	if m.PriceQuote == nil || !approx(*m.PriceQuote, 9.6546543975e-6, 1e-6) {
		t.Errorf("priceQuote = %v", m.PriceQuote)
	}
	// mcap = 9.6546e-6 × 1e9 × 0.30 = 2896.4 (on-chain, not the API's 2853)
	if m.MarketCapUsd == nil || !approx(*m.MarketCapUsd, 2896.396, 1e-4) {
		t.Errorf("marketCapUsd = %v, want ~2896.4", m.MarketCapUsd)
	}
	if m.PriceUsd == nil || !approx(*m.PriceUsd, 9.6546543975e-6*0.30, 1e-6) {
		t.Errorf("priceUsd = %v", m.PriceUsd)
	}
	if m.CreatedAt == nil || m.CreatedAt.UnixMilli() != 1789143217000 || m.ImageUrl == nil {
		t.Errorf("createdAt/imageUrl = %v/%v", m.CreatedAt, m.ImageUrl)
	}
	if len(rpc.lastReq) != 1 || rpc.lastReq[0] != fxPool {
		t.Errorf("RPC should read the API's poolId only, got %v", rpc.lastReq)
	}
}

func TestRaydiumClient_APIOnly_UsesFinishingRateAndAPIMarketCap(t *testing.T) {
	srv := newFixtureAPI(t, 200, readFixture(t, "raydium_mint_info_graduated.json"), nil)
	defer srv.Close()
	c := NewRaydiumClient(RaydiumClientConfig{APIBaseURL: srv.URL}) // no RPC, no prices

	m, err := c.GetCoinData(context.Background(), fxGradMint, LaunchLabHint{})
	if err != nil || m == nil {
		t.Fatalf("GetCoinData: m=%v err=%v", m, err)
	}
	if m.BondingCurvePercent == nil || *m.BondingCurvePercent != 100 || m.Complete == nil || !*m.Complete {
		t.Errorf("progress/complete = %v/%v, want 100/true", m.BondingCurvePercent, m.Complete)
	}
	if m.MarketCapUsd == nil || !approx(*m.MarketCapUsd, 224085769.12, 1e-6) {
		t.Errorf("marketCapUsd = %v, want API value", m.MarketCapUsd)
	}
	if m.QuoteTarget == nil || !approx(*m.QuoteTarget, 85, 1e-9) {
		t.Errorf("quoteTarget = %v, want 85 SOL", m.QuoteTarget)
	}
	if m.PoolId == nil || *m.PoolId != fxGradPool || m.PriceQuote != nil {
		t.Errorf("poolId=%v priceQuote=%v", m.PoolId, m.PriceQuote)
	}
}

func TestRaydiumClient_Graduated_PrefersAPIMarketCapOverFrozenCurve(t *testing.T) {
	srv := newFixtureAPI(t, 200, readFixture(t, "raydium_mint_info_graduated.json"), nil)
	defer srv.Close()
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{fxGradPool: fixtureAccount(t, "raydium_pool_account_graduated.json")}}
	c := NewRaydiumClient(RaydiumClientConfig{
		APIBaseURL: srv.URL, RPC: rpc,
		Prices: &fakePrices{prices: map[string]float64{WrappedSolMint: 100, fxGradMint: 0.224}},
	})
	m, err := c.GetCoinData(context.Background(), fxGradMint, LaunchLabHint{})
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if m.Complete == nil || !*m.Complete || *m.BondingCurvePercent != 100 {
		t.Errorf("complete/pct = %v/%v", m.Complete, m.BondingCurvePercent)
	}
	// Curve end price × supply × SOL = ~$41k — must NOT be used; API says $224M.
	if m.MarketCapUsd == nil || !approx(*m.MarketCapUsd, 224085769.12, 1e-6) {
		t.Errorf("marketCapUsd = %v, want API market cap for graduated pool", m.MarketCapUsd)
	}
	if m.PriceQuote != nil {
		t.Error("frozen curve price must not be reported after graduation")
	}
	if m.PriceUsd == nil || *m.PriceUsd != 0.224 {
		t.Errorf("priceUsd = %v, want Jupiter token price post-graduation", m.PriceUsd)
	}
	if m.SolRaised == nil || !approx(*m.SolRaised, 85.000000209, 1e-9) {
		t.Errorf("solRaised = %v (WSOL quote should populate legacy field)", m.SolRaised)
	}
}

func TestRaydiumClient_Graduated_RPCOnly_UsesJupiterTokenPrice(t *testing.T) {
	srv := newFixtureAPI(t, 503, nil, nil)
	defer srv.Close()
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{fxGradPool: fixtureAccount(t, "raydium_pool_account_graduated.json")}}
	c := NewRaydiumClient(RaydiumClientConfig{
		APIBaseURL: srv.URL, RPC: rpc,
		Prices: &fakePrices{prices: map[string]float64{WrappedSolMint: 100, fxGradMint: 0.224}},
	})
	m, err := c.GetCoinData(context.Background(), fxGradMint, LaunchLabHint{PoolID: fxGradPool})
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	// supply 1e9 × $0.224
	if m.MarketCapUsd == nil || !approx(*m.MarketCapUsd, 224_000_000, 1e-9) || m.PriceUsd == nil || *m.PriceUsd != 0.224 {
		t.Errorf("mcap/priceUsd = %v/%v", m.MarketCapUsd, m.PriceUsd)
	}
}

func TestRaydiumClient_APIError_FallsBackToRPCViaPDA(t *testing.T) {
	var apiCalls atomic.Int32
	srv := newFixtureAPI(t, 500, []byte("upstream down"), &apiCalls)
	defer srv.Close()
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{fxPool: fixtureAccount(t, "raydium_pool_account.json")}}
	c := NewRaydiumClient(RaydiumClientConfig{
		APIBaseURL: srv.URL,
		RPC:        rpc,
		Prices:     &fakePrices{prices: map[string]float64{StonkMint: 0.25}},
	})

	m, err := c.GetCoinData(context.Background(), fxMint, LaunchLabHint{})
	if err != nil || m == nil {
		t.Fatalf("GetCoinData: m=%v err=%v", m, err)
	}
	if apiCalls.Load() != 1 || rpc.calls.Load() != 1 {
		t.Errorf("api/rpc calls = %d/%d", apiCalls.Load(), rpc.calls.Load())
	}
	// Without a hint or API row, candidates are PDAs for [WSOL, STONK]; STONK one exists.
	if len(rpc.lastReq) != 2 || rpc.lastReq[1] != fxPool {
		t.Errorf("PDA candidates = %v, want [wsolPDA, %s]", rpc.lastReq, fxPool)
	}
	if m.PoolId == nil || *m.PoolId != fxPool || m.QuoteMint == nil || *m.QuoteMint != StonkMint {
		t.Errorf("poolId/quote = %v/%v", m.PoolId, m.QuoteMint)
	}
	if m.MarketCapUsd == nil || !approx(*m.MarketCapUsd, 9.6546543975e-6*1e9*0.25, 1e-4) {
		t.Errorf("marketCapUsd = %v", m.MarketCapUsd)
	}
	if m.ImageUrl != nil || m.CreatedAt != nil {
		t.Error("RPC-only path has no image/createdAt")
	}
}

func TestRaydiumClient_APIError_PoolHintSkipsDerivation(t *testing.T) {
	srv := newFixtureAPI(t, 502, nil, nil)
	defer srv.Close()
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{fxPool: fixtureAccount(t, "raydium_pool_account.json")}}
	c := NewRaydiumClient(RaydiumClientConfig{APIBaseURL: srv.URL, RPC: rpc})

	m, err := c.GetCoinData(context.Background(), fxMint, LaunchLabHint{PoolID: fxPool})
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if len(rpc.lastReq) != 1 || rpc.lastReq[0] != fxPool {
		t.Errorf("hinted pool id should be the only candidate, got %v", rpc.lastReq)
	}
	// No price source → market cap unknown, but curve numbers present.
	if m.MarketCapUsd != nil || m.PriceQuote == nil || m.BondingCurvePercent == nil {
		t.Errorf("mcap=%v priceQuote=%v pct=%v", m.MarketCapUsd, m.PriceQuote, m.BondingCurvePercent)
	}
}

func TestRaydiumClient_APIError_NoRPC_ReturnsError(t *testing.T) {
	srv := newFixtureAPI(t, 500, nil, nil)
	defer srv.Close()
	c := NewRaydiumClient(RaydiumClientConfig{APIBaseURL: srv.URL})
	if _, err := c.GetCoinData(context.Background(), fxMint, LaunchLabHint{}); err == nil {
		t.Error("expected error when API fails and no RPC configured")
	}
}

func TestRaydiumClient_APIError_RPCError_ReturnsError(t *testing.T) {
	srv := newFixtureAPI(t, 500, nil, nil)
	defer srv.Close()
	c := NewRaydiumClient(RaydiumClientConfig{APIBaseURL: srv.URL, RPC: &fakeAccountReader{err: errors.New("rpc down")}})
	_, err := c.GetCoinData(context.Background(), fxMint, LaunchLabHint{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rpc down") {
		t.Errorf("error should wrap RPC failure: %v", err)
	}
}

func TestRaydiumClient_NotFound_ReturnsNilNil(t *testing.T) {
	srv := newFixtureAPI(t, 200, []byte(`{"id":"x","success":true,"data":{"rows":[]}}`), nil)
	defer srv.Close()
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{}}
	c := NewRaydiumClient(RaydiumClientConfig{APIBaseURL: srv.URL, RPC: rpc})
	m, err := c.GetCoinData(context.Background(), "CE7gJhaTZZjf3ewcKYtVLgNyYJEZTHgdaf1kP3j7ZwT", LaunchLabHint{})
	if err != nil || m != nil {
		t.Errorf("not-found: m=%v err=%v, want nil,nil", m, err)
	}
}

func TestRaydiumClient_RPCAccountOwnedByOtherProgram_Ignored(t *testing.T) {
	srv := newFixtureAPI(t, 200, readFixture(t, "raydium_mint_info.json"), nil)
	defer srv.Close()
	acc := fixtureAccount(t, "raydium_pool_account.json")
	acc.Owner = "SomeOtherProgram1111111111111111111111111111"
	rpc := &fakeAccountReader{accounts: map[string]*RPCAccount{fxPool: acc}}
	c := NewRaydiumClient(RaydiumClientConfig{APIBaseURL: srv.URL, RPC: rpc})
	m, err := c.GetCoinData(context.Background(), fxMint, LaunchLabHint{})
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if m.PriceQuote != nil {
		t.Error("foreign-owned account must not be decoded as pool state")
	}
	if m.MarketCapUsd == nil {
		t.Error("API market cap should still be used")
	}
}

func TestStubRaydiumClient_Deterministic(t *testing.T) {
	m, err := NewStubRaydiumClient().GetCoinData(context.Background(), "AnyMint", LaunchLabHint{PoolID: "P1", QuoteMint: WrappedSolMint})
	if err != nil || m == nil {
		t.Fatalf("stub: %v", err)
	}
	if *m.PoolId != "P1" || *m.QuoteMint != WrappedSolMint || m.Source != "launchlab" || *m.BondingCurvePercent != 42 {
		t.Errorf("unexpected stub metrics: %+v", m)
	}
}

func TestJupiterPriceClient_CachesFor60s(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("ids") != StonkMint {
			t.Errorf("ids = %s", r.URL.Query().Get("ids"))
		}
		fmt.Fprintf(w, `{"%s":{"usdPrice":0.3,"decimals":9}}`, StonkMint)
	}))
	defer srv.Close()
	c := NewJupiterPriceClient(srv.URL, 0, nil)
	for i := 0; i < 3; i++ {
		p, ok, err := c.GetUsdPrice(context.Background(), StonkMint)
		if err != nil || !ok || p != 0.3 {
			t.Fatalf("GetUsdPrice: p=%v ok=%v err=%v", p, ok, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d, want 1 (cached)", calls.Load())
	}
	// Unknown mint → ok=false, no error
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv2.Close()
	if _, ok, err := NewJupiterPriceClient(srv2.URL, 0, nil).GetUsdPrice(context.Background(), "Unknown"); ok || err != nil {
		t.Errorf("unknown mint: ok=%v err=%v", ok, err)
	}
}

// --- Source routing ---

func TestDefaultTokenSourceResolver(t *testing.T) {
	r := NewDefaultTokenSourceResolver("")
	if got := r.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: "ABCpump"}).Source; got != models.TokenSourcePumpFun {
		t.Errorf("pump suffix → %s, want pumpfun", got)
	}
	if got := r.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: fxMint}).Source; got != models.TokenSourceLaunchLab {
		t.Errorf("plain mint → %s, want launchlab (default)", got)
	}
	r2 := NewDefaultTokenSourceResolver(models.TokenSourcePumpFun)
	if got := r2.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: fxMint}).Source; got != models.TokenSourcePumpFun {
		t.Errorf("default=pumpfun → %s", got)
	}
	r2.Overrides = map[string]TokenSourceResolution{fxMint: {Source: models.TokenSourceLaunchLab, Hint: LaunchLabHint{PoolID: fxPool}}}
	res := r2.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: fxMint})
	if res.Source != models.TokenSourceLaunchLab || res.Hint.PoolID != fxPool {
		t.Errorf("override not applied: %+v", res)
	}
	if _, ok := models.ParseTokenSource("bogus"); ok {
		t.Error("ParseTokenSource should reject unknown values")
	}
}
