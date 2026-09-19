// Package: tracker/internal/models
// Feature: F-031 (Token Data Persistence)
// Story: US-031-01 (Backend Token Persistence)
// Purpose: Domain model for peer token identity (one token per peer, launched on pump.fun)

package models

import "time"

// PeerToken represents a token launched by a peer on pump.fun.
type PeerToken struct {
	PeerID               string    `json:"peer_id"`
	TokenContractAddress string    `json:"token_contract_address"`
	TokenTicker          string    `json:"token_ticker"`
	TokenName            string    `json:"token_name"`
	TokenImageURL        string    `json:"token_image_url,omitempty"`
	LaunchedAt           time.Time `json:"launched_at"`
}

// TokenSource identifies the launchpad a token's market data is read from.
type TokenSource string

const (
	// TokenSourcePumpFun reads metrics from the pump.fun frontend API.
	TokenSourcePumpFun TokenSource = "pumpfun"
	// TokenSourceLaunchLab reads metrics from Raydium LaunchLab (API + on-chain pool state).
	TokenSourceLaunchLab TokenSource = "launchlab"
)

// ParseTokenSource returns the TokenSource for s, or ok=false if unknown.
func ParseTokenSource(s string) (TokenSource, bool) {
	switch TokenSource(s) {
	case TokenSourcePumpFun:
		return TokenSourcePumpFun, true
	case TokenSourceLaunchLab:
		return TokenSourceLaunchLab, true
	}
	return "", false
}

// TokenMetrics holds aggregated on-chain metrics from the token's launchpad
// (pump.fun or Raydium LaunchLab) plus Moralis. All pointer fields are nullable —
// nil means data not available.
//
// Legacy fields (SolRaised, BondingCurvePercent, Complete) keep their pump.fun
// semantics. For LaunchLab tokens the quote token is not always SOL: QuoteRaised /
// PriceQuote are denominated in the quote mint's *raw UI units* (amount / 10^decimals).
// Token-2022 quotes with the scaled-UI-amount extension (xStocks) are NOT multiplied
// here; the UI must apply the live multiplier. SolRaised is only set when the quote
// mint is wrapped SOL.
type TokenMetrics struct {
	MarketCapUsd        *float64   `json:"marketCapUsd"`
	SolRaised           *float64   `json:"solRaised"`
	BondingCurvePercent *int       `json:"bondingCurvePercent"`
	Complete            *bool      `json:"complete"`
	CreatedAt           *time.Time `json:"createdAt"`
	ImageUrl            *string    `json:"imageUrl"`
	Holders             *int       `json:"holders"`
	PriceUsd            *float64   `json:"priceUsd"`

	// Source is the launchpad these metrics came from ("pumpfun" | "launchlab").
	Source string `json:"source,omitempty"`
	// PoolId is the LaunchLab pool (bonding curve) account, when known.
	PoolId *string `json:"poolId,omitempty"`
	// QuoteMint is the mint the token is priced in (WSOL, $STONK, an xStock, ...).
	QuoteMint *string `json:"quoteMint,omitempty"`
	// QuoteDecimals is the quote mint's on-chain decimals (raw → UI divisor).
	QuoteDecimals *int `json:"quoteDecimals,omitempty"`
	// QuoteRaised is the real quote amount in the bonding curve (realB / 10^QuoteDecimals).
	QuoteRaised *float64 `json:"quoteRaised,omitempty"`
	// QuoteTarget is the graduation threshold (totalFundRaisingB / 10^QuoteDecimals).
	QuoteTarget *float64 `json:"quoteTarget,omitempty"`
	// PriceQuote is the current price of one base token in quote units.
	PriceQuote *float64 `json:"priceQuote,omitempty"`
}
