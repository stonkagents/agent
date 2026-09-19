// Package: tracker/internal/models
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Domain models for launch fee settings and launch quote tokens

package models

import "time"

// LaunchFee is the live launch fee, priced from SOL/USD and persisted in launch_settings.
type LaunchFee struct {
	FeeUSD      float64
	FeeLamports int64
	SolUSD      float64
	PricedAt    time.Time
	// Source names the price feed that produced SolUSD (e.g. "jupiter", "stub").
	Source string
}

// Solana clusters a launch quote (and its LaunchLab GlobalConfig) can live on.
const (
	LaunchClusterMainnet = "mainnet"
	LaunchClusterDevnet  = "devnet"
)

// LaunchQuoteCategorySTONK marks the quote the launchpad treats as $STONK for its cluster:
// the real mint on mainnet is category "custom" (matched by mint), while devnet carries a
// stand-in row (migration 015) that is only recognisable by this category.
const LaunchQuoteCategorySTONK = "stonk"

// IsValidLaunchCluster reports whether c names a supported cluster.
func IsValidLaunchCluster(c string) bool {
	return c == LaunchClusterMainnet || c == LaunchClusterDevnet
}

// LaunchQuote is a quote token a launch can raise in, bound to a Raydium LaunchLab GlobalConfig PDA.
// The same mint can exist once per cluster with a different GlobalConfig.
type LaunchQuote struct {
	// Cluster is the Solana cluster the LaunchLabConfigID lives on ("mainnet" | "devnet").
	Cluster           string
	QuoteMint         string
	Symbol            string
	Name              string
	Decimals          int
	TokenProgram      string
	Category          string
	LaunchLabConfigID string
	// MinFundRaisingRaw is the LaunchLab config minimum raise in raw (base) units, as a decimal string
	// because raw amounts can exceed 2^63.
	MinFundRaisingRaw string
	Enabled           bool
	SortOrder         int
}
