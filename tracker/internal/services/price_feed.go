// Package: tracker/internal/services
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: USD price feed abstraction — Jupiter lite price API (v3) HTTP client plus a dev stub

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SOLMint is the wrapped SOL mint used as the price-feed key for SOL/USD.
const SOLMint = "So11111111111111111111111111111111111111112"

// DefaultPriceFeedURL is the Jupiter public price API (no key). GET ?ids=<mint>,<mint>
// returns {"<mint>":{"usdPrice":..., "decimals":..., "scaledUiConfig":{"usdPricePrescaled":...}}}.
const DefaultPriceFeedURL = "https://lite-api.jup.ag/price/v3"

const (
	priceFeedTimeout   = 8 * time.Second
	priceFeedBodyLimit = 1 << 20 // 1 MB
)

// TokenPrice is the USD price of one whole token (10^decimals raw units).
type TokenPrice struct {
	// USDPrice is the UI price per token as quoted by the feed.
	USDPrice float64
	// USDPriceRaw is the USD value of 10^decimals RAW units. For Token-2022 mints with the
	// scaled-UI-amount extension (xStocks) the feed reports a separate pre-scaled price; for
	// everything else it equals USDPrice. Raw-amount math (raise sizing) must use this field.
	USDPriceRaw float64
}

// PriceFeed returns USD prices for token mints.
type PriceFeed interface {
	// GetPrices returns prices keyed by mint. Mints the feed does not know are absent from the map.
	GetPrices(ctx context.Context, mints []string) (map[string]TokenPrice, error)
	// Name identifies the feed for launch_settings.source (e.g. "jupiter", "stub").
	Name() string
}

// jupiterPriceEntry is the subset of the Jupiter price v3 response we read.
type jupiterPriceEntry struct {
	USDPrice       float64 `json:"usdPrice"`
	ScaledUIConfig *struct {
		USDPricePrescaled float64 `json:"usdPricePrescaled"`
	} `json:"scaledUiConfig,omitempty"`
}

// JupiterPriceFeed fetches prices from the Jupiter lite price API.
type JupiterPriceFeed struct {
	client  *http.Client
	baseURL string
}

// NewJupiterPriceFeed creates a Jupiter price feed client. Empty baseURL uses DefaultPriceFeedURL.
func NewJupiterPriceFeed(baseURL string) *JupiterPriceFeed {
	if baseURL == "" {
		baseURL = DefaultPriceFeedURL
	}
	return &JupiterPriceFeed{client: &http.Client{Timeout: priceFeedTimeout}, baseURL: strings.TrimRight(baseURL, "/")}
}

// NewJupiterPriceFeedWithClient creates a Jupiter price feed with a custom HTTP client (tests).
func NewJupiterPriceFeedWithClient(baseURL string, client *http.Client) *JupiterPriceFeed {
	f := NewJupiterPriceFeed(baseURL)
	if client != nil {
		f.client = client
	}
	return f
}

func (f *JupiterPriceFeed) Name() string { return "jupiter" }

// GetPrices fetches USD prices for the given mints in a single request.
func (f *JupiterPriceFeed) GetPrices(ctx context.Context, mints []string) (map[string]TokenPrice, error) {
	if len(mints) == 0 {
		return map[string]TokenPrice{}, nil
	}
	reqURL := f.baseURL + "?ids=" + url.QueryEscape(strings.Join(mints, ","))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("price feed: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("price feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("price feed: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, priceFeedBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("price feed: read body: %w", err)
	}
	var raw map[string]*jupiterPriceEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("price feed: decode: %w", err)
	}

	out := make(map[string]TokenPrice, len(raw))
	for mint, entry := range raw {
		if entry == nil || entry.USDPrice <= 0 {
			continue
		}
		p := TokenPrice{USDPrice: entry.USDPrice, USDPriceRaw: entry.USDPrice}
		if entry.ScaledUIConfig != nil && entry.ScaledUIConfig.USDPricePrescaled > 0 {
			p.USDPriceRaw = entry.ScaledUIConfig.USDPricePrescaled
		}
		out[mint] = p
	}
	return out, nil
}

// StubPriceFeed serves fixed prices for local development (SOLANA_USE_STUBS=true) and tests.
type StubPriceFeed struct {
	Prices map[string]TokenPrice
	// Err, when set, is returned from every GetPrices call (simulates feed outage).
	Err error
}

// NewStubPriceFeed returns a stub feed with a fixed SOL price and prices for the seeded quotes.
func NewStubPriceFeed() *StubPriceFeed {
	return &StubPriceFeed{Prices: map[string]TokenPrice{
		SOLMint: {USDPrice: 100, USDPriceRaw: 100},
		"6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx": {USDPrice: 0.30, USDPriceRaw: 0.30}, // STONK
		"Xsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh":  {USDPrice: 220, USDPriceRaw: 220},   // NVDAx
		"XsoCS1TfEyfFhfvj8EtZ528L3CaKBDBRqRapnBbDF2W":  {USDPrice: 650, USDPriceRaw: 650},   // SPYx
		"XsueG8BtpquVJX9LVLLEGuViXUungE6WmK5YZ3p3bd1":  {USDPrice: 150, USDPriceRaw: 150},   // CRCLx
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": {USDPrice: 1, USDPriceRaw: 1},       // USDC
		"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": {USDPrice: 1, USDPriceRaw: 1},       // USDT
	}}
}

func (s *StubPriceFeed) Name() string { return "stub" }

func (s *StubPriceFeed) GetPrices(_ context.Context, mints []string) (map[string]TokenPrice, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	out := make(map[string]TokenPrice, len(mints))
	for _, m := range mints {
		if p, ok := s.Prices[m]; ok {
			out[m] = p
		}
	}
	return out, nil
}
