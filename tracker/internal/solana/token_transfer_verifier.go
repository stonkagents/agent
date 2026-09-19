// Package: tracker/internal/solana
// Feature: Community board token offers (phase 1)
// Purpose: Real token transfer verifier for token offer payments. Same RPC client and
//          jsonParsed getTransaction decoding as the launch verifier; the transfer is read from
//          the pre/post token balances by owner and mint, which covers both the Token and the
//          Token-2022 program (a fee-withholding transfer shows as a smaller credit) and a
//          recipient ATA created in the same transaction (no pre balance).

package solana

import (
	"context"
	"strconv"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// TokenTransferVerifier implements services.TokenTransferVerifier against Solana JSON-RPC.
type TokenTransferVerifier struct {
	rpc        *Client
	commitment string
}

// NewTokenTransferVerifier creates a verifier. The portal posts right after the transaction
// reaches "confirmed", so that is the commitment used.
func NewTokenTransferVerifier(rpc *Client) *TokenTransferVerifier {
	return &TokenTransferVerifier{rpc: rpc, commitment: "confirmed"}
}

// VerifyTokenTransfer fetches the transaction and reports what it moved.
func (v *TokenTransferVerifier) VerifyTokenTransfer(ctx context.Context, p services.TokenTransferVerifyParams) (*services.TokenTransferVerifyResult, error) {
	tx, err := v.rpc.GetTransaction(ctx, p.Signature, v.commitment)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return &services.TokenTransferVerifyResult{Found: false}, nil
	}
	return InspectTokenTransfer(tx, p), nil
}

// InspectTokenTransfer derives the result from a decoded transaction. Exported so tests can
// feed captured RPC fixtures without a network.
func InspectTokenTransfer(tx *ParsedTransaction, p services.TokenTransferVerifyParams) *services.TokenTransferVerifyResult {
	res := &services.TokenTransferVerifyResult{Found: true}
	if tx.Meta == nil {
		return res
	}
	res.Succeeded = tx.Meta.Err == nil
	for _, k := range tx.Transaction.Message.AccountKeys {
		if k.Signer && k.Pubkey == p.FromWallet {
			res.FromSigner = true
			break
		}
	}
	// Net movement per owner for the mint: post minus pre over every token account.
	pre := tokenBalancesByOwner(tx.Meta.PreTokenBalances, p.Mint)
	post := tokenBalancesByOwner(tx.Meta.PostTokenBalances, p.Mint)
	if d := pre[p.FromWallet] - post[p.FromWallet]; d > 0 {
		res.FromDebit = d
	}
	if d := post[p.ToWallet] - pre[p.ToWallet]; d > 0 {
		res.ToCredit = d
	}
	if p.Memo != "" {
		for _, line := range tx.Meta.LogMessages {
			if strings.Contains(line, p.Memo) {
				res.MemoMatch = true
				break
			}
		}
	}
	return res
}

// tokenBalancesByOwner sums the raw balances of mint per owner wallet.
func tokenBalancesByOwner(balances []ParsedTokenBalance, mint string) map[string]int64 {
	out := map[string]int64{}
	for _, b := range balances {
		if b.Mint != mint || b.Owner == "" {
			continue
		}
		n, err := strconv.ParseInt(b.UITokenAmount.Amount, 10, 64)
		if err != nil {
			continue
		}
		out[b.Owner] += n
	}
	return out
}
