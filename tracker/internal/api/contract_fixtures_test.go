// Package: tracker/internal/api
// Feature: StonkAgents portal contract fixtures
// Purpose: Writes canonical JSON responses of the portal-facing endpoints to
//          tracker/testdata/contracts/*.json, produced by the real handlers over memory
//          repositories with fixed clocks (never hand-written), so the portal's fetch mocks
//          use shapes the tracker actually sends.
//
//	Regenerate:  go test ./tracker/internal/api -run TestWriteContractFixtures -update
//	         or  CONTRACT_FIXTURES=1 go test ./tracker/internal/api -run TestWriteContractFixtures
//	Otherwise the test compares the live output with the files and fails on drift.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

var updateFixtures = flag.Bool("update", false, "rewrite tracker/testdata/contracts/*.json from the handlers")

const contractFixtureDir = "../../testdata/contracts"

// contractFixture is one file: the request that produced it and the exact response.
type contractFixture struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Body   json.RawMessage   `json:"requestBody,omitempty"`
	Header map[string]string `json:"requestHeaders,omitempty"`
	Status int               `json:"status"`
	// Response is the body verbatim (re-indented); envelopes and nulls are the handler's own.
	Response json.RawMessage `json:"response"`
}

type fixtureCall struct {
	name   string
	srv    *Server
	method string
	path   string
	body   interface{}
	header map[string]string
}

func runFixtureCall(t *testing.T, c fixtureCall) []byte {
	t.Helper()
	var buf bytes.Buffer
	if c.body != nil {
		if err := json.NewEncoder(&buf).Encode(c.body); err != nil {
			t.Fatalf("%s: encode body: %v", c.name, err)
		}
	}
	req := httptest.NewRequest(c.method, c.path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "portal-contract/1.0")
	req.RemoteAddr = "203.0.113.10:5555"
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c.srv.Router().ServeHTTP(w, req)

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, w.Body.Bytes(), "  ", "  "); err != nil {
		t.Fatalf("%s: response is not JSON: %v (%s)", c.name, err, w.Body.String())
	}
	fx := contractFixture{Method: c.method, Path: c.path, Header: c.header, Status: w.Code, Response: pretty.Bytes()}
	if c.body != nil {
		var reqPretty bytes.Buffer
		_ = json.Indent(&reqPretty, buf.Bytes(), "  ", "  ")
		fx.Body = reqPretty.Bytes()
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // keep "&" and "<" readable in paths and headers
	enc.SetIndent("", "  ")
	if err := enc.Encode(fx); err != nil {
		t.Fatalf("%s: marshal fixture: %v", c.name, err)
	}
	return out.Bytes()
}

// contractLaunchEnv is the launch server as the portal sees it: two launches (one claimed,
// one with a thumbnail), a quote catalog, live + cached metrics, indexed trades for the 24h
// block, one-launch-per-wallet on, and the network token burn plan.
func contractLaunchEnv(t *testing.T) *launchTestEnv {
	t.Helper()
	env := newLaunchTestEnvOpts(t, false, true)
	now := env.clock.Now()
	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(&models.LaunchQuote{QuoteMint: testSTONKMint, Symbol: "STONK", Name: "STONK", Decimals: 9,
		TokenProgram: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Category: "custom",
		LaunchLabConfigID: "4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW", MinFundRaisingRaw: "1", Enabled: true})
	trades := repository.NewMemoryLaunchTradeRepository()
	_, _ = trades.InsertTrades(context.Background(), []*models.LaunchTrade{
		{Mint: lMint, PoolID: lPool, Signature: launchTestSig(50), Slot: 1, BlockTime: now.Add(-30 * time.Hour), Side: "buy", Trader: lCreator, BaseAmount: 1000, QuoteAmount: 1, PriceQuote: 0.001, QuoteSymbol: "STONK"},
		{Mint: lMint, PoolID: lPool, Signature: launchTestSig(51), Slot: 2, BlockTime: now.Add(-3 * time.Hour), Side: "buy", Trader: lOther, BaseAmount: 1000, QuoteAmount: 2, PriceQuote: 0.002, QuoteSymbol: "STONK"},
		{Mint: lMint, PoolID: lPool, Signature: launchTestSig(52), Slot: 3, BlockTime: now.Add(-time.Hour), Side: "sell", Trader: lOther, BaseAmount: 500, QuoteAmount: 2, PriceQuote: 0.004, QuoteSymbol: "STONK"},
	})
	tradeSvc := services.NewLaunchTradeService(trades, quoteUsdStub{testSTONKMint: 0.3}, env.clock, nil)
	full := func(mcap float64) *models.TokenMetrics {
		return &models.TokenMetrics{
			MarketCapUsd: fptr(mcap), BondingCurvePercent: iptr(48), Holders: iptr(12), PriceUsd: fptr(0.0012),
			QuoteRaised: fptr(14100.5), QuoteTarget: fptr(29389.83), Complete: func() *bool { b := false; return &b }(),
			Source: "launchlab", PriceQuote: fptr(0.004),
		}
	}
	metrics := &stubLaunchMetrics{
		cached:  map[string]*models.TokenMetrics{lMint: full(9000.5), lMint2: full(1200)},
		fetched: map[string]*models.TokenMetrics{lMint: full(9000.5), lMint2: full(1200)},
	}
	// The claiming peer has an owner-set display name so the fixtures carry peer_display_name.
	peers := repository.NewMemoryPeerRepository()
	_ = peers.Create(context.Background(), &models.Peer{PeerID: "12D3KooWContractPeer1111111111111111111111111111111", DisplayName: "Second Agent Bot", FirstSeen: now, LastSeen: now})
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: env.launches, Revenue: env.revenue, Tokens: env.tokens, Accounts: env.accounts,
		Wallets: env.wallets, Credits: env.credits, Verifier: env.verifier, Clock: env.clock,
		Quotes: quotes, Metrics: metrics, Trades: tradeSvc, Peers: peers,
		TreasuryAddress: lTreasury, PlatformID: "PLATFORMtest1111111111111111111111111111111", MinFeeLamports: 10_000_000, OnePerWallet: true,
	})
	burns := repository.NewMemoryLaunchBurnRepository()
	_, _ = burns.InsertBurns(context.Background(), []*models.LaunchBurn{
		{Mint: lAgentMint, Signature: launchTestSig(60), Slot: 1, BlockTime: now.Add(-50 * time.Hour), Amount: 1_000_000, Burner: lCreator},
		{Mint: lAgentMint, Signature: launchTestSig(61), Slot: 2, BlockTime: now.Add(-2 * time.Hour), Amount: 250_000, Burner: lCreator},
	})
	agentSvc := services.NewAgentTokenServiceWithConfig(burns, services.AgentTokenConfig{
		Mint: lAgentMint, PlanTotalPct: 5, BurnInterval: 7 * 24 * time.Hour, BurnAmount: 500_000,
	}, env.clock)
	env.srv = NewServer(ServerDeps{
		LaunchHandler:       NewLaunchHandler(launchSvc),
		LaunchTradesHandler: NewLaunchTradesHandler(tradeSvc, env.clock),
		AgentTokenHandler:   NewAgentTokenHandler(agentSvc, env.clock),
		TokenHandler:        NewTokenHandler(env.tokens),
		APIKeyRepo:          env.apiKeys,
		Address:             ":7842",
	})

	// Launch A: unclaimed, with a thumbnail. Launch B: another creator, claimed by peer-1.
	a := recordBody()
	a["imageThumbUrl"] = "https://cdn.example/a-thumb.png"
	if w := env.do(t, http.MethodPost, "/api/launch/record", a, ""); w.Code != http.StatusCreated {
		t.Fatalf("record A: %d %s", w.Code, w.Body.String())
	}
	env.clock.Advance(90 * time.Second)
	b := secondRecordBody()
	b["creatorWallet"], b["name"] = lOther, "Second Agent"
	if w := env.do(t, http.MethodPost, "/api/launch/record", b, ""); w.Code != http.StatusCreated {
		t.Fatalf("record B: %d %s", w.Code, w.Body.String())
	}
	env.linkPeer(t, "12D3KooWContractPeer1111111111111111111111111111111", "key-1", lOther)
	if w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint2}, "key-1"); w.Code != http.StatusOK {
		t.Fatalf("claim B: %d %s", w.Code, w.Body.String())
	}
	env.clock.Advance(30 * time.Second)
	return env
}

func TestWriteContractFixtures(t *testing.T) {
	update := *updateFixtures || os.Getenv("CONTRACT_FIXTURES") == "1"

	launch := contractLaunchEnv(t)
	cfgSrv, cfgSvc, _ := newLaunchTestServer(t, true)
	cfgHandler := NewLaunchConfigHandler(cfgSvc)
	cfgHandler.SetDevDripEnabled(true)
	cfgSrv = NewServer(ServerDeps{LaunchConfigHandler: cfgHandler, Address: ":7842"})

	dripOK := newDripServer(t, &fakeDripper{res: &services.DevDripResult{
		Signature: launchTestSig(70), SolLamports: 50_000_000, StonkRaw: 25_000_000, SentSol: true, SentStonk: true,
		Explorer: "https://solscan.io/tx/" + launchTestSig(70) + "?cluster=devnet",
	}}, false)
	dripLimited := newDripServer(t, &fakeDripper{err: &services.DripLimitedError{Scope: "wallet", NextAt: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}}, false)
	dripEmpty := newDripServer(t, &fakeDripper{err: services.ErrDripEmpty}, false)
	noDrip := newDripServer(t, nil, false)

	feedback := newFeedbackTestEnv(t, "", false)
	feedback.handler = NewFeedbackHandler(FeedbackHandlerDeps{Repo: feedback.repo, Clock: feedback.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	feedback.srv = NewServer(ServerDeps{FeedbackHandler: feedback.handler, Address: ":7842"})
	interest := newInterestTestEnv(t, "", false)
	interest.handler = NewInterestHandler(InterestHandlerDeps{Repo: interest.repo, Clock: interest.clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	interest.srv = NewServer(ServerDeps{InterestHandler: interest.handler, Address: ":7842"})

	third := recordBody()
	third["mint"], third["poolId"], third["launchSignature"] = launchTestKey(45), launchTestKey(46), launchTestSig(47)
	notAllowed := recordBody()
	notAllowed["mint"], notAllowed["poolId"], notAllowed["launchSignature"] = launchTestKey(48), launchTestKey(49), launchTestSig(53)
	notAllowed["creatorWallet"], notAllowed["quoteMint"] = launchTestKey(54), models.SOLMint

	calls := []fixtureCall{
		{name: "launch-config", srv: cfgSrv, method: http.MethodGet, path: "/api/launch/config"},
		{name: "launch-config-quote-not-allowed-422", srv: cfgSrv, method: http.MethodGet, path: "/api/launch/config?quoteMint=" + models.SOLMint},
		{name: "launch-get", srv: launch.srv, method: http.MethodGet, path: "/api/launch/" + lMint},
		{name: "launch-get-not-found-404", srv: launch.srv, method: http.MethodGet, path: "/api/launch/" + launchTestKey(90)},
		{name: "launches-list", srv: launch.srv, method: http.MethodGet, path: "/api/launches?limit=20&offset=0"},
		{name: "launch-by-wallet", srv: launch.srv, method: http.MethodGet, path: "/api/launch/by-wallet?wallet=" + lOther},
		{name: "launch-trades", srv: launch.srv, method: http.MethodGet, path: "/api/launch/" + lMint + "/trades?limit=50"},
		{name: "launch-candles", srv: launch.srv, method: http.MethodGet, path: "/api/launch/" + lMint + "/candles?interval=1h&limit=100"},
		{name: "launch-record-launch-exists-409", srv: launch.srv, method: http.MethodPost, path: "/api/launch/record", body: third},
		{name: "launch-record-quote-not-allowed-422", srv: launch.srv, method: http.MethodPost, path: "/api/launch/record", body: notAllowed},
		{name: "agent-token-burnplan", srv: launch.srv, method: http.MethodGet, path: "/api/v1/agent-token/burnplan"},
		{name: "dev-drip-ok", srv: dripOK, method: http.MethodPost, path: "/api/dev/drip", body: map[string]string{"wallet": dripTestWallet}},
		{name: "dev-drip-limited-429", srv: dripLimited, method: http.MethodPost, path: "/api/dev/drip", body: map[string]string{"wallet": dripTestWallet}},
		{name: "dev-drip-empty-503", srv: dripEmpty, method: http.MethodPost, path: "/api/dev/drip", body: map[string]string{"wallet": dripTestWallet}},
		{name: "dev-drip-unregistered-404", srv: noDrip, method: http.MethodPost, path: "/api/dev/drip", body: map[string]string{"wallet": dripTestWallet}},
		{name: "feedback", srv: feedback.srv, method: http.MethodPost, path: "/api/v1/feedback", body: feedbackBody(), header: map[string]string{TurnstileTokenHeader: "<widget response>"}},
		{name: "interest", srv: interest.srv, method: http.MethodPost, path: "/api/v1/interest", body: interestBody()},
	}

	if update {
		if err := os.MkdirAll(contractFixtureDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	var drift []string
	for _, c := range calls {
		got := runFixtureCall(t, c)
		path := filepath.Join(contractFixtureDir, c.name+".json")
		if update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			t.Logf("wrote %s (%d bytes)", path, len(got))
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				drift = append(drift, c.name+" (missing)")
				continue
			}
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
			drift = append(drift, c.name)
		}
	}
	if len(drift) > 0 {
		t.Fatalf("contract fixtures out of date: %v\nregenerate with: go test ./tracker/internal/api -run TestWriteContractFixtures -update", drift)
	}
	feedback.handler.WaitForwards()
	interest.handler.WaitForwards()
}
