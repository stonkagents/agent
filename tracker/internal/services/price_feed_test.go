// Package: tracker/internal/services
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Tests for the Jupiter price feed client (response shape verified against lite-api.jup.ag/price/v3)

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// jupiterSampleBody is a trimmed real response from GET https://lite-api.jup.ag/price/v3?ids=SOL,STONK,NVDAx (2026-09-11).
const jupiterSampleBody = `{
  "6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx":{"usdPrice":0.29530842375741795,"blockId":446224845,"decimals":9,"priceChange24h":52.83},
  "So11111111111111111111111111111111111111112":{"usdPrice":101.28401125485574,"blockId":446224853,"decimals":9},
  "Xsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh":{"usdPrice":219.58181757649163,"decimals":8,
     "scaledUiConfig":{"multiplier":1.0009180758490996,"newMultiplier":1.001701196801074,"usdPricePrescaled":219.95536946212678}},
  "UnknownMint":null
}`

func TestJupiterPriceFeed_GetPrices_ParsesShape(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jupiterSampleBody))
	}))
	defer srv.Close()

	feed := NewJupiterPriceFeed(srv.URL + "/")
	if feed.Name() != "jupiter" {
		t.Errorf("Name() = %q", feed.Name())
	}
	prices, err := feed.GetPrices(context.Background(), []string{SOLMint, DefaultLaunchQuoteMint, "Xsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh"})
	if err != nil {
		t.Fatalf("GetPrices() error = %v", err)
	}
	if gotQuery != "ids=So11111111111111111111111111111111111111112%2C6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx%2CXsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh" {
		t.Errorf("query = %q", gotQuery)
	}
	if p := prices[SOLMint]; p.USDPrice != 101.28401125485574 || p.USDPriceRaw != p.USDPrice {
		t.Errorf("SOL price = %+v", p)
	}
	if p := prices[DefaultLaunchQuoteMint]; p.USDPrice != 0.29530842375741795 {
		t.Errorf("STONK price = %+v", p)
	}
	// xStock: raw math must use the pre-scaled price.
	if p := prices["Xsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh"]; p.USDPrice != 219.58181757649163 || p.USDPriceRaw != 219.95536946212678 {
		t.Errorf("NVDAx price = %+v", p)
	}
	if _, ok := prices["UnknownMint"]; ok {
		t.Error("null entry should be absent from result")
	}
}

func TestJupiterPriceFeed_GetPrices_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	if _, err := NewJupiterPriceFeed(srv.URL).GetPrices(context.Background(), []string{SOLMint}); err == nil {
		t.Fatal("GetPrices() with 429 error = nil, want error")
	}
}

func TestJupiterPriceFeed_GetPrices_EmptyMints(t *testing.T) {
	feed := NewJupiterPriceFeed("http://127.0.0.1:1") // must not be called
	prices, err := feed.GetPrices(context.Background(), nil)
	if err != nil || len(prices) != 0 {
		t.Errorf("GetPrices(nil) = %v, %v", prices, err)
	}
}
