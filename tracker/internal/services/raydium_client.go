// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Raydium LaunchLab market-data client — launch-mint API first, on-chain PoolState fallback
//
// Endpoints (from raydium-sdk-V2-demo/src/launchpad/url.ts, verified live 2026-09-12):
//   GET {RAYDIUM_API_URL}/get/by/mints?ids=<mint,...>
//     → {"id":..,"success":true,"data":{"rows":[{mint,poolId,configId,createAt(ms),imgUrl,
//        decimals,supply,marketCap(USD),finishingRate(0-100, price based),migrateAmmId?,
//        totalSellA,totalFundRaisingB,mintB:{address,decimals,symbol,programId},configInfo:{curveType},...}]}}
//     Unknown mint → success:true, rows:[]. Malformed ids → success:false, msg:"ids type error".
//   Pool state: getMultipleAccounts(poolId) decoded by raydium_pool.go (realB / totalFundRaisingB
//   is the funding progress; the API's finishingRate is price-based and differs).
//
// Metric composition (per fetch):
//   1. API row (metadata, image, createdAt, poolId, quote mint/decimals, curveType, USD market cap).
//   2. Pool account via RPC when available (poolId from hint → API row → PDA ["pool", mintA, mintB]).
//      On-chain numbers win for progress / quote raised / price / graduated.
//   3. Market cap USD = priceQuote × supply × quoteUsd (Jupiter, cached 60s); API marketCap otherwise.
//   If the API errors but RPC succeeds (or vice versa) the result is still produced; only when
//   both fail is an error returned. No pool anywhere → (nil, nil) = not a LaunchLab token.
//
// Quote amounts are raw / 10^decimals. Token-2022 quotes with the scaled-UI-amount extension
// (xStocks) are NOT multiplied here — the UI applies the live multiplier.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

const (
	// RaydiumAPIURLDefault is the LaunchLab launch-mint API host.
	RaydiumAPIURLDefault = "https://launch-mint-v1.raydium.io"
	raydiumTimeout       = 5 * time.Second
	raydiumBodyLimit     = 1 << 20
)

// LaunchLabClient fetches market data for a Raydium LaunchLab token.
type LaunchLabClient interface {
	// GetCoinData returns metrics for mint. Returns (nil, nil) when no LaunchLab pool exists.
	GetCoinData(ctx context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error)
}

// LaunchLabHint carries what the caller already knows about a token (e.g. from a
// launch record). Both fields are optional.
type LaunchLabHint struct {
	// PoolID is the bonding-curve pool account; skips API/PDA discovery for the RPC read.
	PoolID string
	// QuoteMint lets the pool PDA be derived when PoolID is unknown and the API is down.
	QuoteMint string
}

// RPCAccount is a raw Solana account (subset used for pool decoding).
type RPCAccount struct {
	Owner string
	Data  []byte
}

// AccountReader is the RPC surface needed to read LaunchLab pool state.
// Entries for non-existent accounts must be nil.
type AccountReader interface {
	GetMultipleAccounts(ctx context.Context, addresses []string) ([]*RPCAccount, error)
}

// RaydiumClientConfig configures HTTPRaydiumClient.
type RaydiumClientConfig struct {
	APIBaseURL string        // RAYDIUM_API_URL; empty = RaydiumAPIURLDefault
	Timeout    time.Duration // RAYDIUM_TIMEOUT; <= 0 = 5s
	ProgramID  string        // LaunchLab program; empty = mainnet
	QuoteMints []string      // PDA candidates when pool id unknown; empty = [WSOL, STONK]
	Prices     QuotePriceSource
	RPC        AccountReader // nil = API only
	Logger     *slog.Logger
}

// HTTPRaydiumClient is the production LaunchLabClient.
type HTTPRaydiumClient struct {
	client     *http.Client
	baseURL    string
	programID  string
	quoteMints []string
	prices     QuotePriceSource
	rpc        AccountReader
	logger     *slog.Logger
}

// NewRaydiumClient creates a LaunchLab client from cfg.
func NewRaydiumClient(cfg RaydiumClientConfig) *HTTPRaydiumClient {
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = RaydiumAPIURLDefault
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = raydiumTimeout
	}
	if cfg.ProgramID == "" {
		cfg.ProgramID = LaunchLabProgramMainnet
	}
	if len(cfg.QuoteMints) == 0 {
		cfg.QuoteMints = []string{WrappedSolMint, StonkMint}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &HTTPRaydiumClient{
		client:     &http.Client{Timeout: cfg.Timeout},
		baseURL:    strings.TrimSuffix(cfg.APIBaseURL, "/"),
		programID:  cfg.ProgramID,
		quoteMints: cfg.QuoteMints,
		prices:     cfg.Prices,
		rpc:        cfg.RPC,
		logger:     cfg.Logger,
	}
}

// --- API types ---

// flexNumber accepts JSON numbers or numeric strings (Raydium mixes both).
type flexNumber float64

func (f *flexNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("flexNumber %q: %w", s, err)
	}
	*f = flexNumber(v)
	return nil
}

type raydiumMintRow struct {
	Mint              string     `json:"mint"`
	PoolID            string     `json:"poolId"`
	ConfigID          string     `json:"configId"`
	CreateAt          int64      `json:"createAt"`
	ImgURL            string     `json:"imgUrl"`
	Decimals          flexNumber `json:"decimals"`
	Supply            flexNumber `json:"supply"`
	MarketCap         flexNumber `json:"marketCap"`
	FinishingRate     flexNumber `json:"finishingRate"`
	MigrateAmmID      string     `json:"migrateAmmId"`
	TotalFundRaisingB flexNumber `json:"totalFundRaisingB"`
	MintB             struct {
		Address   string     `json:"address"`
		Decimals  flexNumber `json:"decimals"`
		Symbol    string     `json:"symbol"`
		ProgramID string     `json:"programId"`
	} `json:"mintB"`
	ConfigInfo struct {
		CurveType flexNumber `json:"curveType"`
	} `json:"configInfo"`
}

type raydiumMintInfoResponse struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Data    struct {
		Rows []raydiumMintRow `json:"rows"`
	} `json:"data"`
}

// parseRaydiumMintInfo decodes a /get/by/mints body and returns the row for mint (nil if absent).
func parseRaydiumMintInfo(body []byte, mint string) (*raydiumMintRow, error) {
	var resp raydiumMintInfoResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("raydium JSON decode: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("raydium API: %s", resp.Msg)
	}
	for i := range resp.Data.Rows {
		if resp.Data.Rows[i].Mint == mint {
			return &resp.Data.Rows[i], nil
		}
	}
	return nil, nil
}

// --- fetching ---

func (c *HTTPRaydiumClient) fetchMintInfo(ctx context.Context, mint string) (*raydiumMintRow, error) {
	reqURL := c.baseURL + "/get/by/mints?ids=" + url.QueryEscape(mint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("raydium HTTP: %w", err)
	}
	defer resp.Body.Close()
	c.logger.Debug("[RaydiumClient.fetchMintInfo]", "mint", mint, "status", resp.StatusCode, "latency", time.Since(start))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("raydium: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, raydiumBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("raydium read body: %w", err)
	}
	return parseRaydiumMintInfo(body, mint)
}

// fetchPool reads and decodes the pool for mint. Candidates are tried in order:
// hint.PoolID, the API row's poolId, then PDAs derived from hint.QuoteMint + configured
// quote mints. Returns (nil, nil) when no candidate account exists.
func (c *HTTPRaydiumClient) fetchPool(ctx context.Context, mint string, hint LaunchLabHint, row *raydiumMintRow) (*LaunchLabPoolState, string, error) {
	if c.rpc == nil {
		return nil, "", nil
	}
	candidates := c.poolCandidates(mint, hint, row)
	if len(candidates) == 0 {
		return nil, "", nil
	}
	accounts, err := c.rpc.GetMultipleAccounts(ctx, candidates)
	if err != nil {
		return nil, "", err
	}
	for i, acc := range accounts {
		if acc == nil || i >= len(candidates) {
			continue
		}
		if acc.Owner != "" && acc.Owner != c.programID {
			continue
		}
		pool, err := DecodeLaunchLabPool(acc.Data)
		if err != nil {
			c.logger.Warn("[RaydiumClient.fetchPool] decode failed", "pool", candidates[i], "error", err)
			continue
		}
		if pool.MintA != mint {
			continue
		}
		return pool, candidates[i], nil
	}
	return nil, "", nil
}

func (c *HTTPRaydiumClient) poolCandidates(mint string, hint LaunchLabHint, row *raydiumMintRow) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(hint.PoolID)
	if row != nil {
		add(row.PoolID)
	}
	if len(out) > 0 {
		return out
	}
	quotes := c.quoteMints
	if hint.QuoteMint != "" {
		quotes = append([]string{hint.QuoteMint}, quotes...)
	}
	for _, q := range quotes {
		id, err := DeriveLaunchLabPoolID(c.programID, mint, q)
		if err != nil {
			c.logger.Debug("[RaydiumClient.poolCandidates] PDA derivation failed", "mint", mint, "quote", q, "error", err)
			continue
		}
		add(id)
	}
	return out
}

// GetCoinData implements LaunchLabClient.
func (c *HTTPRaydiumClient) GetCoinData(ctx context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	row, apiErr := c.fetchMintInfo(ctx, mint)
	if apiErr != nil {
		c.logger.Warn("[RaydiumClient.GetCoinData] API failed, falling back to RPC", "mint", mint, "error", apiErr)
	}
	pool, poolID, rpcErr := c.fetchPool(ctx, mint, hint, row)
	if rpcErr != nil {
		c.logger.Warn("[RaydiumClient.GetCoinData] pool RPC failed", "mint", mint, "error", rpcErr)
	}
	if row == nil && pool == nil {
		if apiErr != nil {
			// API down and RPC could not confirm a pool: unknown, not "not found".
			return nil, fmt.Errorf("raydium: API failed and no pool found via RPC: %w", errors.Join(apiErr, rpcErr))
		}
		return nil, nil // API answered: not a LaunchLab token
	}
	return c.compose(ctx, mint, row, pool, poolID), nil
}

// compose merges API row + pool state into TokenMetrics (either may be nil, not both).
func (c *HTTPRaydiumClient) compose(ctx context.Context, mint string, row *raydiumMintRow, pool *LaunchLabPoolState, poolID string) *models.TokenMetrics {
	m := &models.TokenMetrics{Source: string(models.TokenSourceLaunchLab)}

	if poolID == "" && row != nil {
		poolID = row.PoolID
	}
	if poolID != "" {
		m.PoolId = &poolID
	}

	var quoteMint string
	var quoteDecimals int
	curveType := 0
	if row != nil {
		quoteMint = row.MintB.Address
		quoteDecimals = int(row.MintB.Decimals)
		curveType = int(row.ConfigInfo.CurveType)
		if row.CreateAt > 0 {
			t := time.UnixMilli(row.CreateAt).UTC()
			m.CreatedAt = &t
		}
		m.ImageUrl = validateImageURL(row.ImgURL)
	}
	if pool != nil {
		quoteMint = pool.MintB
		quoteDecimals = int(pool.MintDecimalsB)
	}
	if quoteMint != "" {
		m.QuoteMint = &quoteMint
		m.QuoteDecimals = &quoteDecimals
	}

	var priceQuote float64
	var havePrice bool
	var supplyUI float64
	graduated := false

	if pool != nil {
		pct := pool.ProgressPercent()
		graduated = pool.Graduated()
		complete := graduated
		raised := pool.QuoteRaisedUI()
		target := pool.QuoteTargetUI()
		m.BondingCurvePercent = &pct
		m.Complete = &complete
		m.QuoteRaised = &raised
		m.QuoteTarget = &target
		if quoteMint == WrappedSolMint {
			m.SolRaised = &raised
		}
		supplyUI = pool.SupplyUI()
		// The curve price is frozen once funding ends (trading moves to CPMM), so it is
		// only meaningful while the pool is still in the Fund state.
		if curveType == 0 && !graduated {
			priceQuote, havePrice = pool.PriceQuoteUI()
		}
	} else {
		// API-only: finishingRate is Raydium's price-based progress (0..100).
		pct := int(math.Round(float64(row.FinishingRate)))
		complete := row.MigrateAmmID != "" || pct >= 100
		if complete {
			pct = 100
		} else if pct > 99 {
			pct = 99
		} else if pct < 0 {
			pct = 0
		}
		m.BondingCurvePercent = &pct
		m.Complete = &complete
		graduated = complete
		if row.TotalFundRaisingB > 0 {
			target := float64(row.TotalFundRaisingB) / math.Pow10(quoteDecimals)
			m.QuoteTarget = &target
		}
		supplyUI = float64(row.Supply)
	}

	// Bonding phase: market cap = curve price × supply × quote USD (Jupiter).
	if havePrice {
		m.PriceQuote = &priceQuote
		if quoteUsd, ok := c.usdPrice(ctx, quoteMint); ok {
			mcap := LaunchLabMarketCapUsd(priceQuote, supplyUI, quoteUsd)
			priceUsd := priceQuote * quoteUsd
			m.MarketCapUsd = &mcap
			m.PriceUsd = &priceUsd
		}
	}
	// API market cap (USD) when the curve price is unavailable or frozen.
	if m.MarketCapUsd == nil && row != nil && row.MarketCap > 0 {
		mcap := float64(row.MarketCap)
		m.MarketCapUsd = &mcap
	}
	// Graduated: Jupiter prices the token itself from the CPMM pool.
	if graduated && m.PriceUsd == nil {
		if tokenUsd, ok := c.usdPrice(ctx, mint); ok {
			m.PriceUsd = &tokenUsd
			if m.MarketCapUsd == nil && supplyUI > 0 {
				mcap := tokenUsd * supplyUI
				m.MarketCapUsd = &mcap
			}
		}
	}
	return m
}

// QuoteUsdPrice implements QuoteUsdSource: the USD price of one whole quote unit, with the
// devnet fallback the market cap uses, so 24h volume in USD agrees with the card's market cap.
func (c *HTTPRaydiumClient) QuoteUsdPrice(ctx context.Context, quoteMint string) (float64, bool) {
	return c.usdPrice(ctx, quoteMint)
}

func (c *HTTPRaydiumClient) usdPrice(ctx context.Context, mintAddr string) (float64, bool) {
	if c.prices == nil || mintAddr == "" {
		return 0, false
	}
	price, ok, err := c.prices.GetUsdPrice(ctx, mintAddr)
	if err != nil {
		c.logger.Warn("[RaydiumClient] USD price failed", "mint", mintAddr, "error", err)
		return 0, false
	}
	if !ok && c.programID == LaunchLabProgramDevnet {
		// Devnet quote mints (the $STONK stand-in) have no market price. Value a whole unit
		// at DevnetQuoteUSDFallback, the same assumption the launch config sizes raises with,
		// so devnet launches get a market cap and USD price instead of nulls.
		return DevnetQuoteUSDFallback, true
	}
	return price, ok
}

// --- Stub (SOLANA_USE_STUBS / tests) ---

// StubRaydiumClient returns deterministic bonding-phase metrics for any mint.
type StubRaydiumClient struct{}

// NewStubRaydiumClient creates a stub LaunchLabClient.
func NewStubRaydiumClient() *StubRaydiumClient { return &StubRaydiumClient{} }

// GetCoinData implements LaunchLabClient with fixed values (STONK-quoted, 42% funded).
func (s *StubRaydiumClient) GetCoinData(_ context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error) {
	poolID := hint.PoolID
	if poolID == "" {
		poolID = "StubPool11111111111111111111111111111111111"
	}
	quote := StonkMint
	if hint.QuoteMint != "" {
		quote = hint.QuoteMint
	}
	dec := 9
	pct := 42
	complete := false
	raised, target := 12328.7, 29351.6
	priceQuote := 0.0000121
	quoteUsd := 0.29
	mcap := LaunchLabMarketCapUsd(priceQuote, 1_000_000_000, quoteUsd)
	priceUsd := priceQuote * quoteUsd
	created := time.Now().Add(-2 * time.Hour).UTC()
	img := "https://gateway.irys.xyz/stub-" + mint
	return &models.TokenMetrics{
		Source:              string(models.TokenSourceLaunchLab),
		PoolId:              &poolID,
		QuoteMint:           &quote,
		QuoteDecimals:       &dec,
		QuoteRaised:         &raised,
		QuoteTarget:         &target,
		BondingCurvePercent: &pct,
		Complete:            &complete,
		PriceQuote:          &priceQuote,
		PriceUsd:            &priceUsd,
		MarketCapUsd:        &mcap,
		CreatedAt:           &created,
		ImageUrl:            &img,
	}, nil
}
