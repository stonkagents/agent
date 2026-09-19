// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Raydium LaunchLab PoolState account decoding + bonding-curve math
//
// Layout source: https://github.com/raydium-io/raydium-sdk-V2/blob/master/src/raydium/launchpad/layout.ts
// (LaunchpadPool) and the Anchor IDL account "PoolState" (discriminator f7 ed e3 f5 d7 c3 de 46).
// Verified against live mainnet pools (space = 429 bytes).

package services

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/stonkagents/agent/tracker/internal/solana/pda"
)

const (
	// LaunchLabProgramMainnet is the Raydium LaunchLab program id on mainnet.
	LaunchLabProgramMainnet = "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj"
	// LaunchLabProgramDevnet is the Raydium LaunchLab program id on devnet.
	LaunchLabProgramDevnet = "DRay6fNdQ5J82H7xV6uq2aV3mNrUZ1J4PgSKsWgptcm6"
	// WrappedSolMint is the native SOL mint used as a LaunchLab quote token.
	WrappedSolMint = "So11111111111111111111111111111111111111112"
	// StonkMint is the $STONK quote mint used by stonkfun launches.
	StonkMint = "6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx"

	launchLabPoolSeed    = "pool"
	launchLabPoolMinSize = 375 // through platformVestingShare; 54 bytes of padding follow

	// Pool status values (IDL PoolStatus): Fund → Migrate → Trade.
	launchLabStatusFund    = 0
	launchLabStatusMigrate = 1
	launchLabStatusTrade   = 2
)

// launchLabPoolDiscriminator is the Anchor discriminator for PoolState.
var launchLabPoolDiscriminator = []byte{247, 237, 227, 245, 215, 195, 222, 70}

// LaunchLabPoolState is the decoded subset of a LaunchLab PoolState account.
// All amounts are raw on-chain integers (not divided by decimals).
type LaunchLabPoolState struct {
	Status            uint8
	MintDecimalsA     uint8
	MintDecimalsB     uint8
	MigrateType       uint8
	Supply            uint64
	TotalSellA        uint64
	VirtualA          uint64
	VirtualB          uint64
	RealA             uint64
	RealB             uint64
	TotalFundRaisingB uint64
	ProtocolFee       uint64
	PlatformFee       uint64
	MigrateFee        uint64
	ConfigID          string
	PlatformID        string
	MintA             string
	MintB             string
	// VaultA / VaultB are the pool's base and quote token accounts; a swap moves quote into
	// VaultB and base out of VaultA (buy) or the reverse (sell).
	VaultA  string
	VaultB  string
	Creator string
}

// DecodeLaunchLabPool parses a raw PoolState account.
func DecodeLaunchLabPool(data []byte) (*LaunchLabPoolState, error) {
	if len(data) < launchLabPoolMinSize {
		return nil, fmt.Errorf("launchlab pool: account too small (%d bytes)", len(data))
	}
	if !bytes.Equal(data[:8], launchLabPoolDiscriminator) {
		return nil, fmt.Errorf("launchlab pool: bad discriminator %x", data[:8])
	}
	le := binary.LittleEndian
	p := &LaunchLabPoolState{}
	// 8: epoch (u64) skipped; 16: bump (u8) skipped
	p.Status = data[17]
	p.MintDecimalsA = data[18]
	p.MintDecimalsB = data[19]
	p.MigrateType = data[20]
	off := 21
	u64 := func() uint64 {
		v := le.Uint64(data[off : off+8])
		off += 8
		return v
	}
	p.Supply = u64()
	p.TotalSellA = u64()
	p.VirtualA = u64()
	p.VirtualB = u64()
	p.RealA = u64()
	p.RealB = u64()
	p.TotalFundRaisingB = u64()
	p.ProtocolFee = u64()
	p.PlatformFee = u64()
	p.MigrateFee = u64()
	off += 40 // vestingSchedule: 5 × u64
	pubkey := func() string {
		var k [32]byte
		copy(k[:], data[off:off+32])
		off += 32
		return pda.EncodePubkey(k)
	}
	p.ConfigID = pubkey()
	p.PlatformID = pubkey()
	p.MintA = pubkey()
	p.MintB = pubkey()
	p.VaultA = pubkey()
	p.VaultB = pubkey()
	p.Creator = pubkey()
	return p, nil
}

// Graduated reports whether funding has ended (Migrate or Trade status).
func (p *LaunchLabPoolState) Graduated() bool {
	return p.Status == launchLabStatusMigrate || p.Status == launchLabStatusTrade
}

// ProgressRatio is realB / totalFundRaisingB in [0, 1].
func (p *LaunchLabPoolState) ProgressRatio() float64 {
	if p.TotalFundRaisingB == 0 {
		return 0
	}
	r := float64(p.RealB) / float64(p.TotalFundRaisingB)
	return math.Max(0, math.Min(1, r))
}

// ProgressPercent is the integer bonding-curve percentage: 100 once graduated,
// otherwise capped at 99 (mirrors the pump.fun convention used by the UI).
func (p *LaunchLabPoolState) ProgressPercent() int {
	if p.Graduated() {
		return 100
	}
	pct := int(math.Round(p.ProgressRatio() * 100))
	if pct > 99 {
		pct = 99
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// QuoteRaisedUI is realB in quote UI units (raw / 10^decimalsB; no Token-2022 UI multiplier).
func (p *LaunchLabPoolState) QuoteRaisedUI() float64 {
	return float64(p.RealB) / math.Pow10(int(p.MintDecimalsB))
}

// QuoteTargetUI is totalFundRaisingB in quote UI units.
func (p *LaunchLabPoolState) QuoteTargetUI() float64 {
	return float64(p.TotalFundRaisingB) / math.Pow10(int(p.MintDecimalsB))
}

// SupplyUI is the base supply in UI units.
func (p *LaunchLabPoolState) SupplyUI() float64 {
	return float64(p.Supply) / math.Pow10(int(p.MintDecimalsA))
}

// PriceQuoteUI returns the spot price of one base token in quote UI units for a
// constant-product curve: (virtualB + realB) / (virtualA - realA), scaled by decimals.
// ok=false when the curve is degenerate (virtualA <= realA).
// Only valid for curveType 0 (constant product); callers must check the config.
func (p *LaunchLabPoolState) PriceQuoteUI() (float64, bool) {
	if p.VirtualA <= p.RealA {
		return 0, false
	}
	raw := (float64(p.VirtualB) + float64(p.RealB)) / (float64(p.VirtualA) - float64(p.RealA))
	price := raw * math.Pow10(int(p.MintDecimalsA)-int(p.MintDecimalsB))
	if math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return 0, false
	}
	return price, true
}

// LaunchLabMarketCapUsd = price (quote per base) × supply (UI) × quote USD price.
func LaunchLabMarketCapUsd(priceQuote, supplyUI, quoteUsd float64) float64 {
	return priceQuote * supplyUI * quoteUsd
}

// DeriveLaunchLabPoolID computes the pool PDA ["pool", mintA, mintB] for program.
func DeriveLaunchLabPoolID(program, mintA, mintB string) (string, error) {
	prog, err := pda.DecodePubkey(program)
	if err != nil {
		return "", fmt.Errorf("launchlab program id: %w", err)
	}
	a, err := pda.DecodePubkey(mintA)
	if err != nil {
		return "", fmt.Errorf("launchlab mintA: %w", err)
	}
	b, err := pda.DecodePubkey(mintB)
	if err != nil {
		return "", fmt.Errorf("launchlab mintB: %w", err)
	}
	addr, _, err := pda.FindProgramAddress([][]byte{[]byte(launchLabPoolSeed), a[:], b[:]}, prog)
	if err != nil {
		return "", err
	}
	return pda.EncodePubkey(addr), nil
}
