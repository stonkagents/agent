// Package: tracker/internal/services
// Feature: F-031 (Token Data Persistence)
// Story: US-031-02 (Backend Token Metrics Aggregation)
// Purpose: pump.fun API client — fetches coin data with retry, imageUrl HTTPS validation

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// PumpFunClient fetches token data from pump.fun.
type PumpFunClient interface {
	// GetCoinData returns metrics for a contract address. Returns nil (not error) on 404.
	GetCoinData(ctx context.Context, contractAddr string) (*models.TokenMetrics, error)
}

// pumpFunResponse is the subset of pump.fun /coins/{addr} JSON we care about.
type pumpFunResponse struct {
	UsdMarketCap       float64 `json:"usd_market_cap"`
	VirtualSolReserves float64 `json:"virtual_sol_reserves"`
	Complete           bool    `json:"complete"`
	CreatedTimestamp   int64   `json:"created_timestamp"`
	ImageURI           string  `json:"image_uri"`
}

const (
	// Mainnet and devnet pump.fun API base URLs (GET /coins/{mint}).
	PumpFunBaseURLMainnet = "https://frontend-api-v3.pump.fun"
	PumpFunBaseURLDevnet  = "https://frontend-api-v3.testnetpump.fun"
	pumpFunTimeout        = 5 * time.Second
	pumpFunMaxRetries     = 2
	pumpFunBodyLimit      = 1 << 20 // 1 MB
	pumpFunInitialSolRes  = 30.0    // pump.fun initial virtual SOL reserve
	pumpFunGradThreshold  = 85.0    // SOL needed for bonding curve graduation
)

// HTTPPumpFunClient is the HTTP implementation of PumpFunClient.
type HTTPPumpFunClient struct {
	client  *http.Client
	baseURL string
	logger  *slog.Logger
}

// NewPumpFunClient creates a new pump.fun HTTP client for mainnet.
func NewPumpFunClient(logger *slog.Logger) *HTTPPumpFunClient {
	return NewPumpFunClientWithBaseURL(logger, PumpFunBaseURLMainnet)
}

// NewPumpFunClientWithBaseURL creates a new pump.fun HTTP client with the given API base URL.
// Use PumpFunBaseURLMainnet or PumpFunBaseURLDevnet, or a custom URL (e.g. from PUMP_FUN_API_URL).
func NewPumpFunClientWithBaseURL(logger *slog.Logger, baseURL string) *HTTPPumpFunClient {
	if baseURL == "" {
		baseURL = PumpFunBaseURLMainnet
	}
	return &HTTPPumpFunClient{
		client:  &http.Client{Timeout: pumpFunTimeout},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		logger:  logger,
	}
}

// GetCoinData fetches coin data from pump.fun with retry. Returns nil on 404 (not indexed).
func (c *HTTPPumpFunClient) GetCoinData(ctx context.Context, contractAddr string) (*models.TokenMetrics, error) {
	var lastErr error
	delays := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}

	for attempt := 0; attempt <= pumpFunMaxRetries; attempt++ {
		// Check context before each attempt (R13)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if attempt > 0 {
			jitter := time.Duration(rand.Int64N(int64(50 * time.Millisecond)))
			delay := delays[attempt-1] + jitter
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		metrics, err := c.doFetch(ctx, contractAddr)
		if err == nil {
			return metrics, nil
		}
		// nil metrics + nil error = 404 (not indexed)
		if err == errPumpFunNotFound {
			return nil, nil
		}
		lastErr = err
		c.logger.Warn("[PumpFunClient.GetCoinData] retry",
			"attempt", attempt+1,
			"contractAddr", contractAddr,
			"error", err,
		)
	}
	return nil, fmt.Errorf("pump.fun: all retries exhausted: %w", lastErr)
}

var errPumpFunNotFound = fmt.Errorf("pump.fun: coin not found (404)")

func (c *HTTPPumpFunClient) doFetch(ctx context.Context, contractAddr string) (*models.TokenMetrics, error) {
	reqURL := fmt.Sprintf("%s/coins/%s", c.baseURL, contractAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pump.fun HTTP: %w", err)
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	c.logger.Debug("[PumpFunClient.doFetch]",
		"contractAddr", contractAddr,
		"status", resp.StatusCode,
		"latency", latency,
	)

	if resp.StatusCode == http.StatusNotFound {
		return nil, errPumpFunNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pump.fun: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, pumpFunBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("pump.fun read body: %w", err)
	}
	if len(body) == 0 {
		// pump.fun sometimes returns 200 with empty body for unknown/not-indexed coins
		return nil, errPumpFunNotFound
	}

	var data pumpFunResponse
	if err := json.Unmarshal(body, &data); err != nil {
		// Log first 200 bytes for debugging when API returns non-JSON (e.g. HTML, empty)
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		if c.logger != nil {
			c.logger.Debug("[PumpFunClient.doFetch] JSON decode failed", "contractAddr", contractAddr, "bodyPreview", snippet)
		}
		return nil, fmt.Errorf("pump.fun JSON decode: %w", err)
	}

	return c.toMetrics(&data), nil
}

func (c *HTTPPumpFunClient) toMetrics(data *pumpFunResponse) *models.TokenMetrics {
	mcap := data.UsdMarketCap
	solRaised := math.Max(0, (data.VirtualSolReserves/1e9)-pumpFunInitialSolRes)
	bondingPct := int(math.Min(99, math.Round((solRaised/pumpFunGradThreshold)*100)))
	complete := data.Complete

	var createdAt *time.Time
	if data.CreatedTimestamp > 0 {
		t := time.UnixMilli(data.CreatedTimestamp).UTC()
		createdAt = &t
	}

	imageUrl := validateImageURL(data.ImageURI)

	return &models.TokenMetrics{
		MarketCapUsd:        &mcap,
		SolRaised:           &solRaised,
		BondingCurvePercent: &bondingPct,
		Complete:            &complete,
		CreatedAt:           createdAt,
		ImageUrl:            imageUrl,
	}
}

// validateImageURL returns the URL only if it's a valid HTTPS URL (R13 security).
func validateImageURL(raw string) *string {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return nil
	}
	return &raw
}
