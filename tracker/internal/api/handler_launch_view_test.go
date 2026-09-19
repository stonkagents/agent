// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: HTTP tests for the enriched launch shape on GET /api/launches, /api/launch/{mint}
//          and /api/launch/pending — quote details + metrics inlined for gallery cards.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// stubLaunchMetrics serves one cached and one fetchable metrics object.
type stubLaunchMetrics struct {
	cached  map[string]*models.TokenMetrics
	fetched map[string]*models.TokenMetrics
}

func (s *stubLaunchMetrics) BatchGetCachedMetrics(_ context.Context, addrs []string) map[string]*models.TokenMetrics {
	out := map[string]*models.TokenMetrics{}
	for _, a := range addrs {
		if m, ok := s.cached[a]; ok {
			out[a] = m
		}
	}
	return out
}

func (s *stubLaunchMetrics) GetMetricsByMint(_ context.Context, mint string, _ services.LaunchLabHint) (*models.TokenMetrics, error) {
	if m, ok := s.fetched[mint]; ok {
		return m, nil
	}
	return &models.TokenMetrics{}, nil
}

func fptr(v float64) *float64 { return &v }
func iptr(v int) *int         { return &v }

func mustJSON(t *testing.T, raw []byte, into interface{}) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode: %v (body %s)", err, raw)
	}
}

// newLaunchViewEnv rebuilds the launch env with a quote catalog and metrics provider.
func newLaunchViewEnv(t *testing.T, metrics services.LaunchMetricsProvider) *launchTestEnv {
	t.Helper()
	env := newLaunchTestEnv(t, false)
	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(&models.LaunchQuote{
		QuoteMint: testSTONKMint, Symbol: "STONK", Name: "STONK", Decimals: 9, Category: "custom", Enabled: true,
	})
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: env.launches, Revenue: env.revenue, Tokens: env.tokens, Accounts: env.accounts,
		Wallets: env.wallets, Credits: env.credits, Verifier: env.verifier, Clock: env.clock,
		Quotes: quotes, Metrics: metrics,
		TreasuryAddress: lTreasury, MinFeeLamports: 10_000_000,
	})
	env.srv = NewServer(ServerDeps{
		LaunchHandler: NewLaunchHandler(launchSvc),
		TokenHandler:  NewTokenHandler(env.tokens),
		APIKeyRepo:    env.apiKeys,
		Address:       ":7842",
	})
	return env
}

func TestLaunches_ListAndGet_CarryQuoteAndMetrics(t *testing.T) {
	metrics := &stubLaunchMetrics{
		cached:  map[string]*models.TokenMetrics{lMint: {MarketCapUsd: fptr(9000), Holders: iptr(12), BondingCurvePercent: iptr(48)}},
		fetched: map[string]*models.TokenMetrics{lMint: {MarketCapUsd: fptr(9500), Holders: iptr(13), QuoteRaised: fptr(42.5), QuoteTarget: fptr(85)}},
	}
	env := newLaunchViewEnv(t, metrics)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}

	// List: cached metrics, quote inlined, pagination meta intact.
	w := env.do(t, http.MethodGet, "/api/launches", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	var list map[string]interface{}
	mustJSON(t, w.Body.Bytes(), &list)
	items := list["data"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("list items = %d, want 1", len(items))
	}
	meta := list["meta"].(map[string]interface{})
	if meta["total"].(float64) != 1 || meta["limit"].(float64) != 20 {
		t.Errorf("meta = %v", meta)
	}
	card := items[0].(map[string]interface{})
	for _, k := range []string{"mint", "poolId", "name", "symbol", "imageUrl", "transferFeeBps", "status", "createdAt", "creatorWallet", "agentBound", "quoteMint"} {
		if _, ok := card[k]; !ok {
			t.Errorf("gallery card missing %q: %v", k, card)
		}
	}
	if card["poolId"] != lPool || card["creatorWallet"] != lCreator || card["transferFeeBps"].(float64) != 100 {
		t.Errorf("card = %v", card)
	}
	q := card["quote"].(map[string]interface{})
	if q["mint"] != testSTONKMint || q["symbol"] != "STONK" || q["category"] != "custom" || q["decimals"].(float64) != 9 {
		t.Errorf("card quote = %v", q)
	}
	m := card["metrics"].(map[string]interface{})
	if m["marketCapUsd"].(float64) != 9000 || m["holders"].(float64) != 12 || m["curveProgressPct"].(float64) != 48 {
		t.Errorf("card metrics = %v", m)
	}

	// Get: fresh metrics via the fetch path, same quote block.
	w = env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", w.Code, w.Body.String())
	}
	one := decodeData(t, w)
	m = one["metrics"].(map[string]interface{})
	if m["marketCapUsd"].(float64) != 9500 || m["holders"].(float64) != 13 || m["curveProgressPct"].(float64) != 50 {
		t.Errorf("get metrics = %v", m)
	}
	if one["quote"].(map[string]interface{})["symbol"] != "STONK" {
		t.Errorf("get quote = %v", one["quote"])
	}
	if one["peerId"] != nil || one["agentBound"] != false {
		t.Errorf("unclaimed launch: peerId=%v agentBound=%v", one["peerId"], one["agentBound"])
	}

	// Pending keeps the same enriched shape.
	w = env.do(t, http.MethodGet, "/api/launch/pending?wallet="+lCreator, nil, "")
	var pending map[string]interface{}
	mustJSON(t, w.Body.Bytes(), &pending)
	pitems := pending["data"].([]interface{})
	if len(pitems) != 1 || pitems[0].(map[string]interface{})["quote"] == nil {
		t.Errorf("pending = %v", pending["data"])
	}
}

func TestLaunches_MetricsAbsentIsOmitted(t *testing.T) {
	env := newLaunchViewEnv(t, &stubLaunchMetrics{})
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	one := decodeData(t, env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, ""))
	if _, ok := one["metrics"]; ok {
		t.Errorf("metrics key present = %v, want omitted when nothing is known", one["metrics"])
	}
	if one["quote"] == nil {
		t.Error("quote should still be inlined without metrics")
	}
}
