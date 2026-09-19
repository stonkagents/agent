// Package: tracker/internal/api
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: HTTP shapes for /api/launch/{mint}/trades, /candles, the 24h fields on launch
//          views and /api/v1/agent-token/burnplan + /payouts.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

var lAgentMint = launchTestKey(40)

// newTradesEnv builds the launch server with a seeded trade repo, a quote-priced trade service
// and (optionally) the network token ledgers.
func newTradesEnv(t *testing.T, withAgent bool) (*launchTestEnv, *repository.MemoryLaunchTradeRepository, *repository.MemoryLaunchBurnRepository) {
	t.Helper()
	env := newLaunchTestEnv(t, false)
	now := env.clock.Now()
	trades := repository.NewMemoryLaunchTradeRepository()
	burns := repository.NewMemoryLaunchBurnRepository()
	_, _ = trades.InsertTrades(context.Background(), []*models.LaunchTrade{
		{Mint: lMint, PoolID: lPool, Signature: "s1", Slot: 1, BlockTime: now.Add(-30 * time.Hour), Side: "buy", Trader: lCreator, BaseAmount: 1000, QuoteAmount: 1, PriceQuote: 0.001, QuoteSymbol: "SOL"},
		{Mint: lMint, PoolID: lPool, Signature: "s2", Slot: 2, BlockTime: now.Add(-3 * time.Hour), Side: "buy", Trader: lOther, BaseAmount: 1000, QuoteAmount: 2, PriceQuote: 0.002, QuoteSymbol: "SOL"},
		{Mint: lMint, PoolID: lPool, Signature: "s3", Slot: 3, BlockTime: now.Add(-time.Hour), Side: "sell", Trader: lOther, BaseAmount: 500, QuoteAmount: 2, PriceQuote: 0.004, QuoteSymbol: "SOL"},
	})
	tradeSvc := services.NewLaunchTradeService(trades, quoteUsdStub{testSTONKMint: 100}, env.clock, nil)
	metrics := &stubLaunchMetrics{
		cached:  map[string]*models.TokenMetrics{lMint: {MarketCapUsd: fptr(9000), PriceQuote: fptr(0.004), PriceUsd: fptr(0.4)}},
		fetched: map[string]*models.TokenMetrics{lMint: {MarketCapUsd: fptr(9000), PriceQuote: fptr(0.004), PriceUsd: fptr(0.4)}},
	}
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: env.launches, Revenue: env.revenue, Tokens: env.tokens, Accounts: env.accounts,
		Wallets: env.wallets, Credits: env.credits, Verifier: env.verifier, Clock: env.clock,
		Metrics: metrics, Trades: tradeSvc, TreasuryAddress: lTreasury, MinFeeLamports: 10_000_000,
	})
	deps := ServerDeps{
		LaunchHandler:       NewLaunchHandler(launchSvc),
		LaunchTradesHandler: NewLaunchTradesHandler(tradeSvc, env.clock),
		TokenHandler:        NewTokenHandler(env.tokens),
		APIKeyRepo:          env.apiKeys,
		Address:             ":7842",
	}
	if withAgent {
		deps.AgentTokenHandler = NewAgentTokenHandler(services.NewAgentTokenService(burns, lAgentMint), env.clock)
	}
	env.srv = NewServer(deps)
	return env, trades, burns
}

type quoteUsdStub map[string]float64

func (q quoteUsdStub) QuoteUsdPrice(_ context.Context, mint string) (float64, bool) {
	p, ok := q[mint]
	return p, ok
}

func TestLaunchTrades_FeedShapeAndCursor(t *testing.T) {
	env, _, _ := newTradesEnv(t, false)
	w := env.do(t, http.MethodGet, "/api/launch/"+lMint+"/trades?limit=2", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			Signature   string    `json:"signature"`
			BlockTime   time.Time `json:"blockTime"`
			Side        string    `json:"side"`
			Trader      string    `json:"trader"`
			BaseAmount  float64   `json:"baseAmount"`
			QuoteAmount float64   `json:"quoteAmount"`
			PriceQuote  float64   `json:"priceQuote"`
		} `json:"data"`
		NextCursor string `json:"next_cursor"`
	}
	mustJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 || resp.Data[0].Signature != "s3" || resp.Data[0].Side != "sell" || resp.Data[0].Trader != lOther ||
		resp.Data[0].BaseAmount != 500 || resp.Data[0].QuoteAmount != 2 || resp.Data[0].PriceQuote != 0.004 || resp.Data[1].Signature != "s2" {
		t.Errorf("page = %+v", resp.Data)
	}
	if resp.NextCursor == "" {
		t.Fatal("next_cursor missing on a full page")
	}
	w = env.do(t, http.MethodGet, "/api/launch/"+lMint+"/trades?limit=2&cursor="+resp.NextCursor, nil, "")
	mustJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Signature != "s1" || resp.NextCursor != "" {
		t.Errorf("page2 = %+v next=%q", resp.Data, resp.NextCursor)
	}
	// Validation and unknown mint.
	if w = env.do(t, http.MethodGet, "/api/launch/"+lMint+"/trades?cursor=nope", nil, ""); w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" {
		t.Errorf("bad cursor = %d %s", w.Code, w.Body.String())
	}
	if w = env.do(t, http.MethodGet, "/api/launch/not-a-mint/trades", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("bad mint = %d", w.Code)
	}
	// A mint with no trades answers an empty page, not an error.
	w = env.do(t, http.MethodGet, "/api/launch/"+lOther+"/trades", nil, "")
	mustJSON(t, w.Body.Bytes(), &resp)
	if w.Code != http.StatusOK || len(resp.Data) != 0 || resp.NextCursor != "" {
		t.Errorf("empty = %d %s", w.Code, w.Body.String())
	}
}

func TestLaunchTrades_CandlesShape(t *testing.T) {
	env, _, _ := newTradesEnv(t, false)
	w := env.do(t, http.MethodGet, "/api/launch/"+lMint+"/candles?interval=1h&limit=5", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			T int64   `json:"t"`
			O float64 `json:"o"`
			H float64 `json:"h"`
			L float64 `json:"l"`
			C float64 `json:"c"`
			V float64 `json:"v"`
		} `json:"data"`
	}
	mustJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 || resp.Data[0].C != 0.002 || resp.Data[1].O != 0.004 || resp.Data[1].V != 2 || resp.Data[0].T >= resp.Data[1].T {
		t.Errorf("candles = %+v", resp.Data)
	}
	if w = env.do(t, http.MethodGet, "/api/launch/"+lMint+"/candles?interval=3h", nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad interval = %d %s", w.Code, w.Body.String())
	}
	// Default interval, empty data for a mint without trades.
	w = env.do(t, http.MethodGet, "/api/launch/"+lOther+"/candles", nil, "")
	if w.Code != http.StatusOK || w.Body.String() != "{\"data\":[]}\n" {
		t.Errorf("empty candles = %d %s", w.Code, w.Body.String())
	}
}

func TestLaunches_MetricsCarry24hTradeFields(t *testing.T) {
	env, _, _ := newTradesEnv(t, false)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record = %d %s", w.Code, w.Body.String())
	}
	check := func(path string, m map[string]interface{}) {
		t.Helper()
		if m["priceChange24hPct"] != 300.0 { // 0.004 now vs 0.001 a day+ ago
			t.Errorf("%s priceChange24hPct = %v", path, m["priceChange24hPct"])
		}
		if m["volume24hQuote"] != 4.0 || m["volume24hUsd"] != 400.0 || m["trades24h"] != 2.0 {
			t.Errorf("%s volume/trades = %v %v %v", path, m["volume24hQuote"], m["volume24hUsd"], m["trades24h"])
		}
		if m["priceQuote"] != 0.004 {
			t.Errorf("%s priceQuote = %v", path, m["priceQuote"])
		}
	}
	w := env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, "")
	metrics, _ := decodeData(t, w)["metrics"].(map[string]interface{})
	check("GET /api/launch/{mint}", metrics)

	w = env.do(t, http.MethodGet, "/api/launches", nil, "")
	var list struct {
		Data []map[string]interface{} `json:"data"`
	}
	mustJSON(t, w.Body.Bytes(), &list)
	if len(list.Data) != 1 {
		t.Fatalf("list = %s", w.Body.String())
	}
	metrics, _ = list.Data[0]["metrics"].(map[string]interface{})
	check("GET /api/launches", metrics)
}

func TestAgentToken_BurnPlanAndPayouts(t *testing.T) {
	// Unset AGENT_TOKEN_MINT: both routes are absent (404).
	env, _, _ := newTradesEnv(t, false)
	if w := env.do(t, http.MethodGet, "/api/v1/agent-token/burnplan", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("burnplan without mint = %d", w.Code)
	}
	if w := env.do(t, http.MethodGet, "/api/v1/agent-token/payouts", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("payouts without mint = %d", w.Code)
	}

	env, _, burns := newTradesEnv(t, true)
	now := env.clock.Now()
	_, _ = burns.InsertBurns(context.Background(), []*models.LaunchBurn{
		{Mint: lAgentMint, Signature: "burn1", Slot: 1, BlockTime: now.Add(-2 * time.Hour), Amount: 1000000, Burner: lCreator},
		{Mint: lAgentMint, Signature: "burn2", Slot: 2, BlockTime: now.Add(-time.Hour), Amount: 250000, Burner: lCreator},
	})
	w := env.do(t, http.MethodGet, "/api/v1/agent-token/burnplan", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("burnplan = %d %s", w.Code, w.Body.String())
	}
	var plan struct {
		Mint      string           `json:"mint"`
		Total     float64          `json:"total"`
		PlanTotal *float64         `json:"planTotal"`
		Burned    float64          `json:"burned"`
		Remaining float64          `json:"remaining"`
		Burns     int              `json:"burns"`
		Next      *json.RawMessage `json:"next"`
		Recent    []struct {
			At     time.Time `json:"at"`
			Amount float64   `json:"amount"`
			Sig    string    `json:"sig"`
		} `json:"recent"`
	}
	mustJSON(t, w.Body.Bytes(), &plan)
	// The plan names its mint, so a portal built for another mint can tell the ledgers apart.
	if plan.Mint != lAgentMint {
		t.Errorf("mint = %q, want %q", plan.Mint, lAgentMint)
	}
	// total is the SUPPLY; planTotal is the plan and is null until AGENT_BURN_TOTAL_PCT is set.
	if plan.Total != 1_000_000_000 || plan.Burned != 1250000 || plan.Remaining != 998750000 || plan.Burns != 2 || plan.PlanTotal != nil {
		t.Errorf("plan = %+v", plan)
	}
	if plan.Next != nil && string(*plan.Next) != "null" {
		t.Errorf("next = %s, want null", string(*plan.Next))
	}
	if len(plan.Recent) != 2 || plan.Recent[0].Sig != "burn2" || plan.Recent[0].Amount != 250000 || !plan.Recent[0].At.Equal(now.Add(-time.Hour)) {
		t.Errorf("recent = %+v", plan.Recent)
	}
	var raw map[string]json.RawMessage
	mustJSON(t, w.Body.Bytes(), &raw)
	for _, k := range []string{"next", "planTotal"} {
		if _, has := raw[k]; !has {
			t.Errorf("%s must be present (null), the panel reads it", k)
		}
	}

	// With a configured plan the figures are filled: 2% of the supply, next = newest burn + interval.
	planned := NewAgentTokenHandler(services.NewAgentTokenServiceWithConfig(burns, services.AgentTokenConfig{
		Mint: lAgentMint, PlanTotalPct: 2, BurnInterval: 24 * time.Hour, BurnAmount: 100000,
	}, env.clock), env.clock)
	env.srv = NewServer(ServerDeps{AgentTokenHandler: planned, Address: ":7842"})
	w = env.do(t, http.MethodGet, "/api/v1/agent-token/burnplan", nil, "")
	var withPlan struct {
		PlanTotal *float64 `json:"planTotal"`
		Next      *struct {
			At     time.Time `json:"at"`
			Amount float64   `json:"amount"`
		} `json:"next"`
	}
	mustJSON(t, w.Body.Bytes(), &withPlan)
	if withPlan.PlanTotal == nil || *withPlan.PlanTotal != 20_000_000 {
		t.Errorf("planTotal = %v, want 20000000", withPlan.PlanTotal)
	}
	if withPlan.Next == nil || !withPlan.Next.At.Equal(now.Add(23*time.Hour)) || withPlan.Next.Amount != 100000 {
		t.Errorf("next = %+v, want %s / 100000", withPlan.Next, now.Add(23*time.Hour))
	}

	w = env.do(t, http.MethodGet, "/api/v1/agent-token/payouts", nil, "")
	if w.Code != http.StatusOK || w.Body.String() != "{\"payouts\":[]}\n" {
		t.Errorf("payouts = %d %s", w.Code, w.Body.String())
	}
}

// The 5s response cache serves repeated reads without touching the repository again.
func TestLaunchTrades_ResponseCached(t *testing.T) {
	env, trades, _ := newTradesEnv(t, false)
	first := env.do(t, http.MethodGet, "/api/launch/"+lMint+"/trades?limit=1", nil, "")
	_, _ = trades.InsertTrades(context.Background(), []*models.LaunchTrade{
		{Mint: lMint, PoolID: lPool, Signature: "s4", Slot: 4, BlockTime: env.clock.Now(), Side: "buy", BaseAmount: 1, QuoteAmount: 1, PriceQuote: 1},
	})
	second := env.do(t, http.MethodGet, "/api/launch/"+lMint+"/trades?limit=1", nil, "")
	if first.Body.String() != second.Body.String() {
		t.Errorf("cached read changed within TTL: %s vs %s", first.Body.String(), second.Body.String())
	}
	env.clock.Advance(6 * time.Second)
	third := env.do(t, http.MethodGet, "/api/launch/"+lMint+"/trades?limit=1", nil, "")
	if third.Body.String() == first.Body.String() {
		t.Error("read after TTL still cached")
	}
}

var _ = clock.RealClock{}
