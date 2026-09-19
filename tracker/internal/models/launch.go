// Package: tracker/internal/models
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Domain models for portal-recorded token launches and the platform revenue ledger

package models

import "time"

// Launch status values.
const (
	LaunchStatusConfirmed = "confirmed" // recorded from the browser, not yet bound to a peer
	LaunchStatusBound     = "bound"     // claimed by the creator's daemon peer
)

// SOLMint is the wrapped SOL mint used as the quote mint for SOL-quoted launches.
const SOLMint = "So11111111111111111111111111111111111111112"

// TokenLaunch is a token launched on Raydium LaunchLab under our platform config.
// Created token-first (wallet only); peer_id is set when the creator claims it.
type TokenLaunch struct {
	Mint            string     `json:"mint"`
	PoolID          string     `json:"pool_id,omitempty"`
	CreatorWallet   string     `json:"creator_wallet"`
	QuoteMint       string     `json:"quote_mint"`
	Name            string     `json:"name"`
	Symbol          string     `json:"symbol"`
	ImageURL        string     `json:"image_url,omitempty"`
	ImageThumbURL   string     `json:"image_thumb_url,omitempty"` // ≤ 128 px copy of ImageURL; empty before thumbnails existed
	MetadataURI     string     `json:"metadata_uri,omitempty"`
	LaunchSignature string     `json:"launch_signature"`
	FeeLamports     int64      `json:"fee_lamports"`
	TransferFeeBps  int        `json:"transfer_fee_bps"`
	PlatformID      string     `json:"platform_id,omitempty"`
	PeerID          string     `json:"peer_id,omitempty"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	BoundAt         *time.Time `json:"bound_at,omitempty"`
}

// IsBound reports whether the launch has been claimed by a peer.
func (l *TokenLaunch) IsBound() bool {
	return l.Status == LaunchStatusBound || l.PeerID != ""
}

// Revenue kinds for the platform_revenue ledger.
const (
	RevenueKindLaunchFee          = "launch_fee"
	RevenueKindPlatformFeeClaim   = "platform_fee_claim"
	RevenueKindBuyback            = "buyback"
	RevenueKindBurn               = "burn"
	RevenueKindNFTYield           = "nft_yield"
	RevenueKindHolderDistribution = "holder_distribution"
)

// RevenueKinds lists every valid ledger kind (order used for stable API output).
var RevenueKinds = []string{
	RevenueKindLaunchFee,
	RevenueKindPlatformFeeClaim,
	RevenueKindBuyback,
	RevenueKindBurn,
	RevenueKindNFTYield,
	RevenueKindHolderDistribution,
}

// IsValidRevenueKind reports whether kind is a known ledger kind.
func IsValidRevenueKind(kind string) bool {
	for _, k := range RevenueKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// PlatformRevenue is one ledger entry. AmountRaw is in the quote mint's base units
// (lamports for SOL). AmountUSD is nil until the pricing hook fills it.
type PlatformRevenue struct {
	ID         int64                  `json:"id"`
	Kind       string                 `json:"kind"`
	QuoteMint  string                 `json:"quote_mint,omitempty"`
	AmountRaw  float64                `json:"amount_raw"`
	AmountUSD  *float64               `json:"amount_usd"`
	Signature  string                 `json:"signature,omitempty"`
	Mint       string                 `json:"mint,omitempty"`
	OccurredAt time.Time              `json:"occurred_at"`
	Meta       map[string]interface{} `json:"meta,omitempty"`
}

// RevenueKindTotal is the aggregate for one ledger kind.
type RevenueKindTotal struct {
	Kind      string  `json:"kind"`
	Count     int     `json:"count"`
	AmountRaw float64 `json:"amount_raw"`
	AmountUSD float64 `json:"amount_usd"`
}

// RevenueDailyRow is the per-day, per-kind aggregate used to build the daily series.
type RevenueDailyRow struct {
	Date      string  `json:"date"` // YYYY-MM-DD (UTC)
	Kind      string  `json:"kind"`
	Count     int     `json:"count"`
	AmountRaw float64 `json:"amount_raw"`
	AmountUSD float64 `json:"amount_usd"`
}
