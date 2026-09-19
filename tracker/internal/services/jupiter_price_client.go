// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Jupiter Price API v3 client — USD price for quote mints, 60s in-memory cache
//
// Endpoint: GET https://lite-api.jup.ag/price/v3?ids=<mint,...>
// Response (verified 2026-09-12): {"<mint>":{"usdPrice":101.27,"decimals":9,"liquidity":...,
// "blockId":...,"priceChange24h":...,"createdAt":"..."}} — unknown mints are simply omitted.

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// JupiterPriceURLDefault is the public (rate-limited, no key) price endpoint.
	JupiterPriceURLDefault = "https://lite-api.jup.ag/price/v3"
	jupiterPriceCacheTTL   = 60 * time.Second
	jupiterBodyLimit       = 1 << 20
)

// QuotePriceSource returns the USD price of a mint. Implemented by JupiterPriceClient.
type QuotePriceSource interface {
	// GetUsdPrice returns the USD price for mint. ok=false when unknown.
	GetUsdPrice(ctx context.Context, mint string) (price float64, ok bool, err error)
}

// jupiterQuotePriceEntry is one value of the price/v3 map.
type jupiterQuotePriceEntry struct {
	UsdPrice *float64 `json:"usdPrice"`
	Decimals *int     `json:"decimals"`
}

type jupiterCacheEntry struct {
	price     float64
	expiresAt time.Time
}

// JupiterPriceClient fetches USD prices from Jupiter with a per-mint 60s cache.
type JupiterPriceClient struct {
	client  *http.Client
	baseURL string
	logger  *slog.Logger
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]jupiterCacheEntry
}

// NewJupiterPriceClient creates a client. Empty baseURL → JupiterPriceURLDefault.
func NewJupiterPriceClient(baseURL string, timeout time.Duration, logger *slog.Logger) *JupiterPriceClient {
	if baseURL == "" {
		baseURL = JupiterPriceURLDefault
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &JupiterPriceClient{
		client:  &http.Client{Timeout: timeout},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		logger:  logger,
		now:     time.Now,
		cache:   make(map[string]jupiterCacheEntry),
	}
}

// GetUsdPrice returns the cached or freshly fetched USD price for mint.
func (c *JupiterPriceClient) GetUsdPrice(ctx context.Context, mint string) (float64, bool, error) {
	if mint == "" {
		return 0, false, nil
	}
	if p, ok := c.cached(mint); ok {
		return p, true, nil
	}
	prices, err := c.fetch(ctx, []string{mint})
	if err != nil {
		return 0, false, err
	}
	p, ok := prices[mint]
	if !ok {
		return 0, false, nil
	}
	c.store(mint, p)
	return p, true, nil
}

func (c *JupiterPriceClient) cached(mint string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[mint]
	if !ok || c.now().After(e.expiresAt) {
		return 0, false
	}
	return e.price, true
}

func (c *JupiterPriceClient) store(mint string, price float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[mint] = jupiterCacheEntry{price: price, expiresAt: c.now().Add(jupiterPriceCacheTTL)}
}

func (c *JupiterPriceClient) fetch(ctx context.Context, mints []string) (map[string]float64, error) {
	reqURL := c.baseURL + "?ids=" + url.QueryEscape(strings.Join(mints, ","))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jupiter HTTP: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jupiter: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jupiterBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("jupiter read body: %w", err)
	}
	return parseJupiterPrices(body)
}

// parseJupiterPrices decodes a price/v3 body into mint → usdPrice.
func parseJupiterPrices(body []byte) (map[string]float64, error) {
	var raw map[string]*jupiterQuotePriceEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("jupiter JSON decode: %w", err)
	}
	out := make(map[string]float64, len(raw))
	for mint, e := range raw {
		if e == nil || e.UsdPrice == nil || *e.UsdPrice <= 0 {
			continue
		}
		out[mint] = *e.UsdPrice
	}
	return out, nil
}
