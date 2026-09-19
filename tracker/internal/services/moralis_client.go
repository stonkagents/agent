// Package: tracker/internal/services
// Feature: F-031 (Token Data Persistence)
// Story: US-031-02 (Backend Token Metrics Aggregation)
// Purpose: Moralis API client — fetches holders and price for graduated tokens

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// MoralisClient fetches token data from Moralis (holders, price).
type MoralisClient interface {
	// GetTopHolders returns holder count for a contract address. Returns nil on failure.
	GetTopHolders(ctx context.Context, contractAddr string) (*int, error)
	// GetTokenPrice returns USD price for a contract address. Returns nil on failure.
	GetTokenPrice(ctx context.Context, contractAddr string) (*float64, error)
}

const (
	moralisBaseURL   = "https://solana-gateway.moralis.io"
	moralisTimeout   = 5 * time.Second
	moralisBodyLimit = 1 << 20 // 1 MB
)

// HTTPMoralisClient is the HTTP implementation of MoralisClient.
type HTTPMoralisClient struct {
	client  *http.Client
	baseURL string
	apiKey  string
	logger  *slog.Logger
}

// NewMoralisClient creates a new Moralis HTTP client. Returns nil if apiKey is empty (soft-disable).
func NewMoralisClient(apiKey string, logger *slog.Logger) *HTTPMoralisClient {
	if apiKey == "" {
		logger.Warn("[MoralisClient] MORALIS_API_KEY not set, Moralis disabled (holders/price will be null)")
		return nil
	}
	return &HTTPMoralisClient{
		client:  &http.Client{Timeout: moralisTimeout},
		baseURL: moralisBaseURL,
		apiKey:  apiKey,
		logger:  logger,
	}
}

// GetTopHolders returns the number of top holders. Retries once on failure.
func (c *HTTPMoralisClient) GetTopHolders(ctx context.Context, contractAddr string) (*int, error) {
	reqURL := fmt.Sprintf("%s/token/%s/top-holders", c.baseURL, contractAddr)
	body, err := c.doWithRetry(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var holders []json.RawMessage
	if err := json.Unmarshal(body, &holders); err != nil {
		return nil, fmt.Errorf("moralis holders JSON: %w", err)
	}
	count := len(holders)
	return &count, nil
}

// GetTokenPrice returns the USD price. Retries once on failure.
func (c *HTTPMoralisClient) GetTokenPrice(ctx context.Context, contractAddr string) (*float64, error) {
	reqURL := fmt.Sprintf("%s/token/%s/price", c.baseURL, contractAddr)
	body, err := c.doWithRetry(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	// Gate 0 Finding #5 fix: Use *float64 so missing field → nil, not 0
	var priceResp struct {
		UsdPrice *float64 `json:"usdPrice"`
	}
	if err := json.Unmarshal(body, &priceResp); err != nil {
		return nil, fmt.Errorf("moralis price JSON: %w", err)
	}
	return priceResp.UsdPrice, nil
}

func (c *HTTPMoralisClient) doWithRetry(ctx context.Context, reqURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= 1; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			jitter := time.Duration(rand.Int64N(int64(50 * time.Millisecond)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(300*time.Millisecond + jitter):
			}
		}

		body, err := c.doFetch(ctx, reqURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		c.logger.Warn("[MoralisClient] retry",
			"attempt", attempt+1,
			"url", reqURL,
			"error", err,
		)
	}
	return nil, fmt.Errorf("moralis: all retries exhausted: %w", lastErr)
}

func (c *HTTPMoralisClient) doFetch(ctx context.Context, reqURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moralis HTTP: %w", err)
	}
	defer resp.Body.Close()

	c.logger.Debug("[MoralisClient.doFetch]",
		"url", reqURL,
		"status", resp.StatusCode,
		"latency", time.Since(start),
	)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("moralis: unexpected status %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, moralisBodyLimit))
}
