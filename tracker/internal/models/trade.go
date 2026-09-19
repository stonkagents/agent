// Package: tracker/internal/models
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: Domain models for indexed pool swaps, the per-address index cursor and token burns

package models

import "time"

// Trade sides.
const (
	TradeSideBuy  = "buy"  // quote into the pool, base out
	TradeSideSell = "sell" // base into the pool, quote out
)

// LaunchTrade is one swap against a launch's LaunchLab bonding-curve pool.
// Amounts are whole tokens (raw / 10^decimals); PriceQuote is quote per base.
type LaunchTrade struct {
	Mint        string    `json:"mint"`
	PoolID      string    `json:"pool_id"`
	Signature   string    `json:"signature"`
	Slot        int64     `json:"slot"`
	BlockTime   time.Time `json:"block_time"`
	Side        string    `json:"side"`
	Trader      string    `json:"trader"`
	BaseAmount  float64   `json:"base_amount"`
	QuoteAmount float64   `json:"quote_amount"`
	PriceQuote  float64   `json:"price_quote"`
	QuoteSymbol string    `json:"quote_symbol,omitempty"`
}

// LaunchIndexCursor remembers, per indexed address, the newest signature already stored.
// Key is a pool id for trades and "burns:<mint>" for the burn ledger.
type LaunchIndexCursor struct {
	Key           string
	LastSignature string
	LastBlockTime *time.Time
	UpdatedAt     time.Time
}

// LaunchTradeWindow is the per-mint aggregate over one time window (from, to].
type LaunchTradeWindow struct {
	Mint        string
	VolumeQuote float64 // sum of quote_amount in the window
	Trades      int     // number of trades in the window
	QuoteSymbol string  // quote symbol of the newest trade seen
	// PriceAtFrom is the price of the last trade at or before the window start; nil when
	// no trade is that old. PriceAtTo is the price of the last trade at or before the end.
	PriceAtFrom *float64
	PriceAtTo   *float64
}

// LaunchBurn is one burn / burnChecked of the network token, amount in whole tokens.
type LaunchBurn struct {
	Mint      string    `json:"mint"`
	Signature string    `json:"signature"`
	Slot      int64     `json:"slot"`
	BlockTime time.Time `json:"block_time"`
	Amount    float64   `json:"amount"`
	Burner    string    `json:"burner"`
}

// LaunchBurnSummary is the total burned and the burn count for one mint.
type LaunchBurnSummary struct {
	Burned float64
	Burns  int
}
