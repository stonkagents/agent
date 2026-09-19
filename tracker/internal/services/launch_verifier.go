// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: On-chain launch verification contract + offline stub (SOLANA_USE_STUBS / tests)

package services

import (
	"context"
	"time"
)

// LaunchLabProgramID is the Raydium LaunchLab program (mainnet).
const LaunchLabProgramID = "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj"

// LaunchVerifyParams identifies the launch transaction to inspect and what to look for.
type LaunchVerifyParams struct {
	Signature       string
	CreatorWallet   string
	TreasuryAddress string
	// MinFeeLamports is the smallest creator->treasury transfer the service will accept.
	// Verifiers only report the observed amount; the stub uses this as its reported amount.
	MinFeeLamports int64
	// Hints for the stub so offline runs mirror a well-formed launch; real verifiers ignore them.
	Mint, PoolID, QuoteMint, PlatformID string
}

// LaunchVerifyResult reports the facts observed on chain. The LaunchService decides
// whether they satisfy the launch-recording rules.
type LaunchVerifyResult struct {
	Found     bool // transaction exists at the requested commitment
	Succeeded bool // meta.err == null
	// Signers are the account keys flagged as signers.
	Signers []string
	// LaunchLabAccounts is the union of account pubkeys across every instruction
	// (outer or inner) whose program is LaunchLab. Empty = no LaunchLab instruction.
	LaunchLabAccounts []string
	// TreasuryLamports is the sum of System Program transfers from CreatorWallet to TreasuryAddress.
	TreasuryLamports int64
	BlockTime        *time.Time
}

// LaunchVerifier fetches and decodes a launch transaction.
type LaunchVerifier interface {
	VerifyLaunch(ctx context.Context, params LaunchVerifyParams) (*LaunchVerifyResult, error)
}

// StubLaunchVerifier is the offline verifier used with SOLANA_USE_STUBS=true and in tests.
// By default it reports a well-formed launch that pays exactly MinFeeLamports; fields let
// tests break individual checks.
type StubLaunchVerifier struct {
	// Err, when set, is returned as an RPC failure.
	Err error
	// NotFound reports the transaction as missing.
	NotFound bool
	// Failed reports meta.err != null.
	Failed bool
	// CreatorNotSigner omits the creator from the signer list.
	CreatorNotSigner bool
	// NoLaunchLabInstruction reports no LaunchLab instruction.
	NoLaunchLabInstruction bool
	// OmitMint drops the mint from the LaunchLab accounts.
	OmitMint bool
	// FeeLamports overrides the reported treasury transfer when > 0; FeeMissing forces 0.
	FeeLamports int64
	FeeMissing  bool
	// Calls counts invocations (tests assert idempotent re-records skip verification).
	Calls int
}

// VerifyLaunch implements LaunchVerifier.
func (s *StubLaunchVerifier) VerifyLaunch(_ context.Context, p LaunchVerifyParams) (*LaunchVerifyResult, error) {
	s.Calls++
	if s.Err != nil {
		return nil, s.Err
	}
	if s.NotFound {
		return &LaunchVerifyResult{Found: false}, nil
	}
	// BlockTime is left nil so LaunchService falls back to its injected clock; a wall-clock
	// stamp here made tests date-dependent.
	res := &LaunchVerifyResult{Found: true, Succeeded: !s.Failed}
	if !s.CreatorNotSigner {
		res.Signers = []string{p.CreatorWallet}
	}
	if !s.NoLaunchLabInstruction {
		accounts := []string{p.CreatorWallet, p.PoolID, p.QuoteMint, p.PlatformID, p.TreasuryAddress}
		if !s.OmitMint {
			accounts = append(accounts, p.Mint)
		}
		for _, a := range accounts {
			if a != "" {
				res.LaunchLabAccounts = append(res.LaunchLabAccounts, a)
			}
		}
	}
	switch {
	case s.FeeMissing:
		res.TreasuryLamports = 0
	case s.FeeLamports > 0:
		res.TreasuryLamports = s.FeeLamports
	default:
		res.TreasuryLamports = p.MinFeeLamports
	}
	return res, nil
}
