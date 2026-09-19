// Package: tracker/internal/solana
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: Real Solana transaction verifier — 6-field on-chain verification for purchase flow

package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// Verification constants.
const (
	txVerifyWindow = 15 * time.Minute
)

// Verifier implements services.TransactionVerifier using Solana JSON-RPC.
type Verifier struct {
	rpc *Client
}

// NewVerifier creates a transaction verifier backed by the given RPC client.
func NewVerifier(rpc *Client) *Verifier {
	return &Verifier{rpc: rpc}
}

// getTransaction response types.
type txResponse struct {
	Slot        int64       `json:"slot"`
	BlockTime   *int64      `json:"blockTime"`
	Meta        *txMeta     `json:"meta"`
	Transaction interface{} `json:"transaction"` // can be JSON or base64; we use JSON encoding
}

type txMeta struct {
	Err               interface{} `json:"err"`
	PreBalances       []int64     `json:"preBalances"`
	PostBalances      []int64     `json:"postBalances"`
	LogMessages       []string    `json:"logMessages"`
	InnerInstructions interface{} `json:"innerInstructions"`
}

// parsedTransaction represents a parsed Solana transaction.
type parsedTransaction struct {
	Message parsedMessage `json:"message"`
}

type parsedMessage struct {
	AccountKeys []accountKey `json:"accountKeys"`
}

type accountKey struct {
	Pubkey string `json:"pubkey"`
}

// VerifyTransaction performs 6-field on-chain verification:
//  1. Transaction confirmed (not failed)
//  2. Treasury address in account list
//  3. Balance delta matches expected lamports (within dust tolerance)
//  4. Memo matches expected string
//  5. Transaction is recent (within 15-minute window)
//  6. Transaction was finalized
func (v *Verifier) VerifyTransaction(ctx context.Context, txSignature string, params services.VerifyParams) (*services.TxVerifyResult, error) {
	// Fetch transaction with jsonParsed encoding and finalized commitment.
	rpcParams := []interface{}{
		txSignature,
		map[string]interface{}{
			"encoding":                       "jsonParsed",
			"commitment":                     "finalized",
			"maxSupportedTransactionVersion": 0,
		},
	}
	raw, err := v.rpc.callRPC(ctx, "getTransaction", rpcParams)
	if err != nil {
		return nil, fmt.Errorf("getTransaction RPC: %w", err)
	}

	// A null result means the transaction was not found (not yet finalized or doesn't exist).
	if string(raw) == "null" {
		return &services.TxVerifyResult{Success: false}, nil
	}

	var tx txResponse
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("getTransaction unmarshal: %w", err)
	}

	// Check 1: Transaction must not have failed.
	if tx.Meta == nil {
		return &services.TxVerifyResult{Success: false}, nil
	}
	if tx.Meta.Err != nil {
		return &services.TxVerifyResult{Success: false}, nil
	}

	// Check 5: Transaction must be recent (within verification window).
	if tx.BlockTime == nil {
		return &services.TxVerifyResult{Success: false}, nil
	}
	txTime := time.Unix(*tx.BlockTime, 0)
	if time.Since(txTime) > txVerifyWindow {
		return nil, fmt.Errorf("transaction too old: %s ago (max %s)", time.Since(txTime).Round(time.Second), txVerifyWindow)
	}

	// Parse the transaction to get account keys.
	txJSON, err := json.Marshal(tx.Transaction)
	if err != nil {
		return nil, fmt.Errorf("re-marshal transaction: %w", err)
	}
	var parsed parsedTransaction
	if err := json.Unmarshal(txJSON, &parsed); err != nil {
		return nil, fmt.Errorf("parse transaction message: %w", err)
	}

	// Check 2: Treasury address must be in the account list.
	treasuryIdx := -1
	for i, key := range parsed.Message.AccountKeys {
		if key.Pubkey == params.TreasuryAddress {
			treasuryIdx = i
			break
		}
	}
	if treasuryIdx < 0 {
		return &services.TxVerifyResult{Success: false}, nil
	}

	// Check 3: Balance delta on the treasury account.
	var actualLamports int64
	if treasuryIdx < len(tx.Meta.PreBalances) && treasuryIdx < len(tx.Meta.PostBalances) {
		actualLamports = tx.Meta.PostBalances[treasuryIdx] - tx.Meta.PreBalances[treasuryIdx]
	}

	// Check 4: Memo match — search log messages for the expected memo, or for any
	// accepted alternative (the legacy purchase memo prefix from installed clients).
	memoMatch := false
	if params.ExpectedMemo != "" || len(params.AcceptedMemos) > 0 {
		for _, logMsg := range tx.Meta.LogMessages {
			if params.MemoMatches(logMsg) {
				memoMatch = true
				break
			}
		}
	}

	return &services.TxVerifyResult{
		Success:        true,
		ActualLamports: actualLamports,
		MemoMatch:      memoMatch,
	}, nil
}
