// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: HTTP tests for /api/launch/* and /api/revenue with memory repos + stub verifier

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mr-tron/base58"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

func launchTestKey(b byte) string {
	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = b + byte(i)
	}
	return base58.Encode(buf)
}

func launchTestSig(b byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = b ^ byte(i)
	}
	return base58.Encode(buf)
}

var (
	lMint     = launchTestKey(30)
	lPool     = launchTestKey(31)
	lCreator  = launchTestKey(32)
	lTreasury = launchTestKey(33)
	lOther    = launchTestKey(34)
	lSig      = launchTestSig(35)
)

type launchTestEnv struct {
	srv      *Server
	launches *repository.MemoryLaunchRepository
	revenue  *repository.MemoryRevenueRepository
	tokens   *repository.MemoryTokenRepository
	accounts *repository.MemoryAccountRepository
	wallets  *repository.MemoryWalletRepository
	credits  *repository.MemoryCreditRepository
	apiKeys  *repository.MemoryPeerAPIKeyRepository
	verifier *services.StubLaunchVerifier
	clock    *clock.MockClock
}

// newLaunchTestEnv builds the launch server with LAUNCH_ONE_PER_WALLET off (the pre-existing
// tests record freely); newLaunchTestEnvOpts turns the one-launch-per-wallet rule on.
func newLaunchTestEnv(t *testing.T, withLimiter bool) *launchTestEnv {
	t.Helper()
	return newLaunchTestEnvOpts(t, withLimiter, false)
}

func newLaunchTestEnvOpts(t *testing.T, withLimiter, onePerWallet bool) *launchTestEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))
	env := &launchTestEnv{
		launches: repository.NewMemoryLaunchRepository(),
		revenue:  repository.NewMemoryRevenueRepository(),
		tokens:   repository.NewMemoryTokenRepository(),
		accounts: repository.NewMemoryAccountRepository(),
		wallets:  repository.NewMemoryWalletRepository(),
		credits:  repository.NewMemoryCreditRepositoryWithClock(clk),
		apiKeys:  repository.NewMemoryPeerAPIKeyRepository(),
		verifier: &services.StubLaunchVerifier{},
		clock:    clk,
	}
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: env.launches, Revenue: env.revenue, Tokens: env.tokens, Accounts: env.accounts,
		Wallets: env.wallets, Credits: env.credits, Verifier: env.verifier, Clock: clk,
		TreasuryAddress: lTreasury, MinFeeLamports: 10_000_000, OnePerWallet: onePerWallet,
	})
	deps := ServerDeps{
		LaunchHandler:  NewLaunchHandler(launchSvc),
		RevenueHandler: NewRevenueHandler(services.NewRevenueService(env.revenue, clk)),
		TokenHandler:   NewTokenHandler(env.tokens),
		APIKeyRepo:     env.apiKeys,
		Address:        ":7842",
	}
	if withLimiter {
		deps.Limiter = ratelimit.NewMemoryLimiter(clk)
	}
	env.srv = NewServer(deps)
	return env
}

func (e *launchTestEnv) do(t *testing.T, method, path string, body interface{}, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	return e.doWithHeader(t, method, path, body, "X-API-Key", apiKey)
}

// doWithHeader is do with an arbitrary auth header (empty value = header not sent).
func (e *launchTestEnv) doWithHeader(t *testing.T, method, path string, body interface{}, header, value string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.10:5555"
	if value != "" {
		req.Header.Set(header, value)
	}
	w := httptest.NewRecorder()
	e.srv.Router().ServeHTTP(w, req)
	return w
}

func recordBody() map[string]interface{} {
	return map[string]interface{}{
		"mint": lMint, "poolId": lPool, "creatorWallet": lCreator, "quoteMint": testSTONKMint,
		"name": "Stonk Agent", "symbol": "STNK", "imageUrl": "https://cdn.example/a.png",
		"metadataUri": "ipfs://QmMeta", "launchSignature": lSig, "feeLamports": 20_000_000, "transferFeeBps": 100,
	}
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	data, _ := resp["data"].(map[string]interface{})
	return data
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v (body %s)", err, w.Body.String())
	}
	return resp.Error.Code
}

func (e *launchTestEnv) linkPeer(t *testing.T, peerID, apiKey, wallet string) string {
	t.Helper()
	ctx := context.Background()
	e.apiKeys.Store(peerID, apiKey)
	acc, _, _ := e.accounts.GetOrCreateForPeer(ctx, peerID)
	_ = e.credits.CreateBalance(ctx, &models.CreditBalance{AccountID: acc.ID, UpdatedAt: e.clock.Now()})
	if err := e.wallets.UpsertWalletForDisplay(ctx, acc.ID, wallet, "solana"); err != nil {
		t.Fatalf("link wallet: %v", err)
	}
	return acc.ID
}

func TestLaunchRecord_201_Then200_Idempotent(t *testing.T) {
	env := newLaunchTestEnv(t, false)

	w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), "")
	if w.Code != http.StatusCreated {
		t.Fatalf("first record status = %d body=%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	if data["mint"] != lMint || data["status"] != "confirmed" || data["fee_lamports"].(float64) != 20_000_000 {
		t.Errorf("record data = %v", data)
	}
	if _, has := data["peer_id"]; has {
		t.Error("peer_id must be omitted on an unbound launch")
	}

	w = env.do(t, http.MethodPost, "/api/launch/record", recordBody(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("re-record status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if decodeData(t, w)["mint"] != lMint {
		t.Error("re-record must return the existing record")
	}
	if env.verifier.Calls != 1 {
		t.Errorf("verifier calls = %d, want 1", env.verifier.Calls)
	}

	// Public reads.
	w = env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, "")
	if w.Code != http.StatusOK || decodeData(t, w)["launch_signature"] != lSig {
		t.Errorf("GET /api/launch/{mint} = %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, http.MethodGet, "/api/launches?creator="+lCreator+"&limit=5&cursor=0", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/launches = %d %s", w.Code, w.Body.String())
	}
	var list PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if list.Meta.Total != 1 || list.Meta.Limit != 5 || len(list.Data.([]interface{})) != 1 {
		t.Errorf("list = %+v", list)
	}
	w = env.do(t, http.MethodGet, "/api/launch/pending?wallet="+lCreator, nil, "")
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(lMint)) {
		t.Errorf("pending = %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, http.MethodGet, "/api/launch/"+lOther, nil, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown mint status = %d, want 404", w.Code)
	}
	// A path that cannot be a mint is also a launch that does not exist (404, not 400).
	w = env.do(t, http.MethodGet, "/api/launch/tRqrTWVmyZD7pqgu8jkBJodLyCKYJhC7Wbgi1MnT9gSr", nil, "")
	if w.Code != http.StatusNotFound || errorCode(t, w) != "NOT_FOUND" {
		t.Errorf("malformed mint status = %d %s, want 404 NOT_FOUND", w.Code, w.Body.String())
	}
}

func TestLaunchRecord_422_WhenFeeTransferMissing(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	env.verifier.FeeMissing = true

	w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), "")
	if w.Code != http.StatusUnprocessableEntity || errorCode(t, w) != "FEE_TRANSFER_MISSING" {
		t.Fatalf("status=%d body=%s, want 422 FEE_TRANSFER_MISSING", w.Code, w.Body.String())
	}
	if _, err := env.launches.GetByMint(context.Background(), lMint); err == nil {
		t.Error("launch must not be stored")
	}

	env2 := newLaunchTestEnv(t, false)
	env2.verifier.CreatorNotSigner = true
	if w := env2.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusUnprocessableEntity || errorCode(t, w) != "CREATOR_NOT_SIGNER" {
		t.Errorf("not-signer status=%d body=%s", w.Code, w.Body.String())
	}
}

// Launches are quoted in $STONK only: any other quoteMint is 422 QUOTE_NOT_ALLOWED, checked
// before the on-chain verification. With a devnet catalog the migration-015 stand-in passes.
func TestLaunchRecord_422_QuoteNotAllowed(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	b := recordBody()
	b["quoteMint"] = models.SOLMint
	w := env.do(t, http.MethodPost, "/api/launch/record", b, "")
	if w.Code != http.StatusUnprocessableEntity || errorCode(t, w) != LaunchQuoteNotAllowedCode {
		t.Fatalf("status=%d body=%s, want 422 %s", w.Code, w.Body.String(), LaunchQuoteNotAllowedCode)
	}
	if env.verifier.Calls != 0 {
		t.Errorf("verifier calls = %d, want 0 (rejected before RPC)", env.verifier.Calls)
	}
	if _, err := env.launches.GetByMint(context.Background(), lMint); err == nil {
		t.Error("launch must not be stored")
	}

	// Devnet catalog: the stand-in is the $STONK quote there; devnet SOL is still not.
	dev := newLaunchTestEnv(t, false)
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: dev.launches, Revenue: dev.revenue, Verifier: dev.verifier, Clock: dev.clock,
		Quotes: newDevnetQuoteRepo(), TreasuryAddress: lTreasury, MinFeeLamports: 10_000_000,
	})
	dev.srv = NewServer(ServerDeps{LaunchHandler: NewLaunchHandler(launchSvc), Address: ":7842"})
	b = recordBody()
	b["quoteMint"] = models.SOLMint
	if w := dev.do(t, http.MethodPost, "/api/launch/record", b, ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("devnet SOL status=%d body=%s, want 422", w.Code, w.Body.String())
	}
	b = recordBody()
	b["quoteMint"] = testDevnetSTONKMint
	if w := dev.do(t, http.MethodPost, "/api/launch/record", b, ""); w.Code != http.StatusCreated {
		t.Errorf("devnet stand-in status=%d body=%s, want 201", w.Code, w.Body.String())
	}
}

func TestLaunchRecord_400_Validation(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	cases := []struct {
		name string
		mut  func(map[string]interface{})
	}{
		{"bad mint", func(b map[string]interface{}) { b["mint"] = "not-base58!" }},
		{"short pubkey", func(b map[string]interface{}) { b["creatorWallet"] = base58.Encode([]byte{1, 2, 3}) }},
		{"bad signature", func(b map[string]interface{}) { b["launchSignature"] = lMint }},
		{"symbol too long", func(b map[string]interface{}) { b["symbol"] = "ABCDEFGHIJK" }},
		{"name too long", func(b map[string]interface{}) { b["name"] = "123456789012345678901234567890123" }},
		{"bad image url", func(b map[string]interface{}) { b["imageUrl"] = "javascript:alert(1)" }},
		{"negative fee", func(b map[string]interface{}) { b["feeLamports"] = -1 }},
		{"bps out of range", func(b map[string]interface{}) { b["transferFeeBps"] = 10001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := recordBody()
			tc.mut(b)
			w := env.do(t, http.MethodPost, "/api/launch/record", b, "")
			if w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" {
				t.Fatalf("status=%d body=%s, want 400 VALIDATION_ERROR", w.Code, w.Body.String())
			}
		})
	}
	if w := env.do(t, http.MethodPost, "/api/launch/record", "{not json", ""); w.Code != http.StatusBadRequest {
		t.Errorf("invalid json status = %d", w.Code)
	}
	if w := env.do(t, http.MethodGet, "/api/launch/pending?wallet=nope", nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("pending bad wallet status = %d", w.Code)
	}
	if env.verifier.Calls != 0 {
		t.Errorf("verifier must not be called on invalid input, calls=%d", env.verifier.Calls)
	}
}

func TestLaunchClaim_HappyPath_GrantsOnce(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	accID := env.linkPeer(t, "peer-1", "key-1", lCreator)

	w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-1")
	if w.Code != http.StatusOK {
		t.Fatalf("claim status = %d body=%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	if data["credits_granted"].(float64) != 250 || data["already_bound"] != false {
		t.Errorf("claim data = %v", data)
	}
	launch := data["launch"].(map[string]interface{})
	if launch["status"] != "bound" || launch["peer_id"] != "peer-1" {
		t.Errorf("claimed launch = %v", launch)
	}
	bal, _ := env.credits.GetBalance(context.Background(), accID)
	if bal.FreeBalance != 250 {
		t.Errorf("balance = %d, want 250", bal.FreeBalance)
	}

	// Existing gallery endpoint now serves the mirrored token.
	w = env.do(t, http.MethodGet, "/api/peers/peer-1/token", nil, "")
	if w.Code != http.StatusOK || decodeData(t, w)["token_contract_address"] != lMint {
		t.Errorf("GET /api/peers/{id}/token = %d %s", w.Code, w.Body.String())
	}

	// Re-claim is idempotent: 200, already_bound, no extra credits.
	w = env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-1")
	if w.Code != http.StatusOK || decodeData(t, w)["already_bound"] != true {
		t.Fatalf("re-claim = %d %s", w.Code, w.Body.String())
	}
	bal, _ = env.credits.GetBalance(context.Background(), accID)
	if bal.FreeBalance != 250 {
		t.Errorf("balance after re-claim = %d, want 250", bal.FreeBalance)
	}

	// Pending is now empty for the creator.
	w = env.do(t, http.MethodGet, "/api/launch/pending?wallet="+lCreator, nil, "")
	if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte(lMint)) {
		t.Errorf("pending after claim = %d %s", w.Code, w.Body.String())
	}

	// Unauthenticated claim is rejected.
	if w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no key status = %d, want 401", w.Code)
	}
}

func TestLaunchClaim_403_WalletMismatch(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	accID := env.linkPeer(t, "peer-2", "key-2", lOther)

	w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-2")
	if w.Code != http.StatusForbidden || errorCode(t, w) != "WALLET_MISMATCH" {
		t.Fatalf("status=%d body=%s, want 403 WALLET_MISMATCH", w.Code, w.Body.String())
	}
	bal, _ := env.credits.GetBalance(context.Background(), accID)
	if bal.FreeBalance != 0 {
		t.Errorf("balance = %d, want 0", bal.FreeBalance)
	}

	// Peer without any linked wallet.
	env.apiKeys.Store("peer-3", "key-3")
	w = env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-3")
	if w.Code != http.StatusConflict || errorCode(t, w) != "WALLET_NOT_LINKED" {
		t.Errorf("no-wallet status=%d body=%s, want 409 WALLET_NOT_LINKED", w.Code, w.Body.String())
	}
	// Unknown mint.
	w = env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lOther}, "key-2")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown mint status = %d, want 404", w.Code)
	}
}

func TestRevenue_Summary(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	usd := 50.0
	_ = env.revenue.Insert(context.Background(), &models.PlatformRevenue{
		Kind: models.RevenueKindBuyback, QuoteMint: models.SOLMint, AmountRaw: 250_000_000, AmountUSD: &usd,
		Signature: launchTestSig(99), OccurredAt: env.clock.Now(),
	})

	w := env.do(t, http.MethodGet, "/api/revenue", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("revenue status = %d body=%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	totals := data["totals"].(map[string]interface{})
	lf := totals["launch_fee"].(map[string]interface{})
	if lf["count"].(float64) != 1 || lf["amount_raw"].(float64) != 20_000_000 {
		t.Errorf("launch_fee totals = %v", lf)
	}
	if data["counts"].(map[string]interface{})["buyback"].(float64) != 1 || data["total_entries"].(float64) != 2 {
		t.Errorf("counts = %v total=%v", data["counts"], data["total_entries"])
	}
	daily := data["daily"].([]interface{})
	if len(daily) != 30 {
		t.Fatalf("daily len = %d, want 30", len(daily))
	}
	today := daily[29].(map[string]interface{})
	if today["date"] != "2026-09-11" || today["launch_fee_lamports"].(float64) != 20_000_000 || today["buyback_usd"].(float64) != 50 {
		t.Errorf("today = %v", today)
	}
	for _, k := range []string{"launch_fee_usd", "platform_fee_usd", "buyback_usd", "launch_count"} {
		if _, ok := today[k]; !ok {
			t.Errorf("daily point missing %s", k)
		}
	}
}

func TestLaunchRoutes_RateLimited(t *testing.T) {
	env := newLaunchTestEnv(t, true)
	cfg := LaunchRecordRateLimitConfig()
	var last int
	for i := 0; i <= cfg.Limit; i++ {
		b := recordBody()
		b["mint"] = "x" // fails validation, but the limiter runs first
		last = env.do(t, http.MethodPost, "/api/launch/record", b, "").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("request %d status = %d, want 429", cfg.Limit+1, last)
	}
	if w := env.do(t, http.MethodGet, "/api/revenue", nil, ""); w.Code != http.StatusOK {
		t.Errorf("revenue under separate read limit = %d", w.Code)
	}
}
