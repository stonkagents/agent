// Package services: Community board token offers (phase 1).
// Purpose: On-chain verification contract for a wallet-to-wallet SPL transfer that pays a reply
//          its token offer, plus the offline stub (SOLANA_USE_STUBS / tests). Mirrors the launch
//          verifier: the verifier reports facts, the ForumService decides.

package services

import (
	"context"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// Token program ids a launch's mint can live under.
const (
	TokenProgramClassic = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	TokenProgram2022    = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
	// LaunchTokenDecimals is the base decimals of every LaunchLab launch (DefaultLaunchCurve).
	LaunchTokenDecimals = 6
	// TokenOfferMemoPrefix starts the memo the portal writes on a token offer payment:
	// stonkagents:offer:<post id>:<reply id>.
	TokenOfferMemoPrefix = "stonkagents:offer:"
)

// LaunchTokenProgram returns the token program of a launch's mint: Token-2022 when the launch
// carries a transfer fee (the fee extension is what flips the mint to Token-2022), classic otherwise.
func LaunchTokenProgram(l *models.TokenLaunch) string {
	if l != nil && l.TransferFeeBps > 0 {
		return TokenProgram2022
	}
	return TokenProgramClassic
}

// TokenOfferMemo returns the memo expected on the payment of replyID on postID.
func TokenOfferMemo(postID, replyID string) string {
	return TokenOfferMemoPrefix + postID + ":" + replyID
}

// TokenTransferVerifyParams identifies the payment transaction and the transfer to look for.
type TokenTransferVerifyParams struct {
	Signature  string
	Mint       string
	FromWallet string
	ToWallet   string
	// AmountRaw is the per-reply amount in raw token units the sender must have paid.
	AmountRaw int64
	// Memo is the expected memo (TokenOfferMemo).
	Memo string
}

// TokenTransferVerifyResult reports what the transaction did. The ForumService decides whether
// it satisfies the offer.
type TokenTransferVerifyResult struct {
	Found     bool // transaction exists at the requested commitment
	Succeeded bool // meta.err == null
	// FromSigner is true when FromWallet signed the transaction.
	FromSigner bool
	// FromDebit is how many raw units of Mint left FromWallet's token accounts (>= 0).
	FromDebit int64
	// ToCredit is how many raw units of Mint landed in ToWallet's token accounts (>= 0).
	// With a Token-2022 transfer fee it is the amount minus the withheld fee.
	ToCredit int64
	// MemoMatch is true when a log line carries the expected memo.
	MemoMatch bool
}

// TokenTransferVerifier fetches and decodes a token offer payment transaction.
type TokenTransferVerifier interface {
	VerifyTokenTransfer(ctx context.Context, params TokenTransferVerifyParams) (*TokenTransferVerifyResult, error)
}

// StubTokenTransferVerifier is the offline verifier (SOLANA_USE_STUBS=true and tests). By
// default it reports a well-formed payment of exactly AmountRaw; fields break single checks.
type StubTokenTransferVerifier struct {
	Err       error
	NotFound  bool
	Failed    bool
	NotSigner bool
	NoMemo    bool
	// FromDebit / ToCredit override the reported amounts when > 0.
	FromDebit int64
	ToCredit  int64
	// Calls counts invocations.
	Calls int
	// Last is the last params seen.
	Last TokenTransferVerifyParams
}

// VerifyTokenTransfer implements TokenTransferVerifier.
func (s *StubTokenTransferVerifier) VerifyTokenTransfer(_ context.Context, p TokenTransferVerifyParams) (*TokenTransferVerifyResult, error) {
	s.Calls++
	s.Last = p
	if s.Err != nil {
		return nil, s.Err
	}
	if s.NotFound {
		return &TokenTransferVerifyResult{Found: false}, nil
	}
	res := &TokenTransferVerifyResult{
		Found: true, Succeeded: !s.Failed, FromSigner: !s.NotSigner, MemoMatch: !s.NoMemo,
		FromDebit: p.AmountRaw, ToCredit: p.AmountRaw,
	}
	if s.FromDebit > 0 {
		res.FromDebit = s.FromDebit
	}
	if s.ToCredit > 0 {
		res.ToCredit = s.ToCredit
	}
	return res, nil
}
