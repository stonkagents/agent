// Package: tracker/internal/models
// Feature: StonkAgents devnet drip
// Purpose: Domain model for test-SOL / test-$STONK drips sent to wallets connecting to the dev
//          portal (table dev_drips, migration 020). Devnet only.

package models

import "time"

// DevDrip records the latest drip a wallet received. Amounts are raw chain units
// (lamports, token base units). IPHash is a SHA-256 hex digest of the client IP, never
// the address itself.
type DevDrip struct {
	Wallet      string    `json:"wallet"`
	IPHash      string    `json:"-"`
	Signature   string    `json:"signature"`
	AmountSol   uint64    `json:"amount_sol"`
	AmountStonk uint64    `json:"amount_stonk"`
	DrippedAt   time.Time `json:"dripped_at"`
}
