// Package: tracker/internal/services
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: RPC error sentinels shared between the Solana client and the services that back off on them

package services

import "errors"

// ErrRPCRateLimited is wrapped by RPC clients when the node answers HTTP 429; the trade
// indexer backs off on it instead of counting it as a hard failure right away.
var ErrRPCRateLimited = errors.New("solana RPC rate limited")
