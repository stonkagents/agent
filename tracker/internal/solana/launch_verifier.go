// Package: tracker/internal/solana
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Real launch verifier — decodes a jsonParsed getTransaction response and reports
//          signers, LaunchLab instruction accounts, and creator->treasury system transfers.

package solana

import (
	"context"
	"encoding/json"
	"time"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// SystemProgramID is the Solana System Program.
const SystemProgramID = "11111111111111111111111111111111"

// ParsedTransaction is the subset of a jsonParsed getTransaction result we inspect.
type ParsedTransaction struct {
	Slot        int64                 `json:"slot"`
	BlockTime   *int64                `json:"blockTime"`
	Meta        *ParsedMeta           `json:"meta"`
	Transaction ParsedTransactionBody `json:"transaction"`
}

// ParsedTransactionBody is the "transaction" object (message + signatures).
type ParsedTransactionBody struct {
	Message    ParsedMessage `json:"message"`
	Signatures []string      `json:"signatures"`
}

// ParsedMeta is the transaction meta we inspect.
type ParsedMeta struct {
	Err               interface{}              `json:"err"`
	PreBalances       []int64                  `json:"preBalances"`
	PostBalances      []int64                  `json:"postBalances"`
	LogMessages       []string                 `json:"logMessages"`
	InnerInstructions []ParsedInnerInstruction `json:"innerInstructions"`
	PreTokenBalances  []ParsedTokenBalance     `json:"preTokenBalances"`
	PostTokenBalances []ParsedTokenBalance     `json:"postTokenBalances"`
}

// ParsedTokenBalance is one pre/postTokenBalances entry (token account by message index).
type ParsedTokenBalance struct {
	AccountIndex  int                 `json:"accountIndex"`
	Mint          string              `json:"mint"`
	Owner         string              `json:"owner"`
	UITokenAmount ParsedUITokenAmount `json:"uiTokenAmount"`
}

// ParsedUITokenAmount is a token amount as the RPC renders it (raw string + decimals).
type ParsedUITokenAmount struct {
	Amount         string `json:"amount"`
	Decimals       int    `json:"decimals"`
	UIAmountString string `json:"uiAmountString"`
}

// ParsedInnerInstruction groups CPI instructions under an outer instruction index.
type ParsedInnerInstruction struct {
	Index        int                 `json:"index"`
	Instructions []ParsedInstruction `json:"instructions"`
}

// ParsedMessage holds account keys and top-level instructions.
type ParsedMessage struct {
	AccountKeys  []ParsedAccountKey  `json:"accountKeys"`
	Instructions []ParsedInstruction `json:"instructions"`
}

// ParsedAccountKey is one resolved account key (static or from a lookup table).
type ParsedAccountKey struct {
	Pubkey   string `json:"pubkey"`
	Signer   bool   `json:"signer"`
	Writable bool   `json:"writable"`
	Source   string `json:"source"`
}

// ParsedInstruction is either a decoded ("parsed") instruction for known programs
// (System, Token, ...) or a raw instruction with an accounts pubkey list.
type ParsedInstruction struct {
	Program   string          `json:"program"`
	ProgramID string          `json:"programId"`
	Parsed    json.RawMessage `json:"parsed"`
	Accounts  []string        `json:"accounts"`
	Data      string          `json:"data"`
}

// parsedSystemTransfer is the "parsed" body of a System Program transfer.
type parsedSystemTransfer struct {
	Type string `json:"type"`
	Info struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Lamports    int64  `json:"lamports"`
	} `json:"info"`
}

// LaunchVerifier implements services.LaunchVerifier against Solana JSON-RPC.
type LaunchVerifier struct {
	rpc        *Client
	commitment string
	programID  string
}

// NewLaunchVerifier creates a launch verifier. The browser posts right after the
// transaction reaches "confirmed", so that is the commitment used by default.
func NewLaunchVerifier(rpc *Client) *LaunchVerifier {
	return &LaunchVerifier{rpc: rpc, commitment: "confirmed", programID: services.LaunchLabProgramID}
}

// WithProgramID overrides the LaunchLab program id (devnet uses a different deployment).
// An empty id keeps the mainnet default.
func (v *LaunchVerifier) WithProgramID(programID string) *LaunchVerifier {
	if programID != "" {
		v.programID = programID
	}
	return v
}

// VerifyLaunch fetches the transaction and extracts the facts the LaunchService checks.
func (v *LaunchVerifier) VerifyLaunch(ctx context.Context, p services.LaunchVerifyParams) (*services.LaunchVerifyResult, error) {
	tx, err := v.rpc.GetTransaction(ctx, p.Signature, v.commitment)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return &services.LaunchVerifyResult{Found: false}, nil
	}
	return InspectLaunchTransactionFor(tx, v.programID, p.CreatorWallet, p.TreasuryAddress), nil
}

// InspectLaunchTransaction derives the LaunchVerifyResult from a decoded transaction.
// Exported so tests can feed captured RPC fixtures without a network.
func InspectLaunchTransaction(tx *ParsedTransaction, creator, treasury string) *services.LaunchVerifyResult {
	return InspectLaunchTransactionFor(tx, services.LaunchLabProgramID, creator, treasury)
}

// InspectLaunchTransactionFor is InspectLaunchTransaction for a specific LaunchLab program id.
func InspectLaunchTransactionFor(tx *ParsedTransaction, programID, creator, treasury string) *services.LaunchVerifyResult {
	if programID == "" {
		programID = services.LaunchLabProgramID
	}
	res := &services.LaunchVerifyResult{Found: true}
	if tx.Meta == nil {
		return res
	}
	res.Succeeded = tx.Meta.Err == nil
	if tx.BlockTime != nil {
		t := time.Unix(*tx.BlockTime, 0).UTC()
		res.BlockTime = &t
	}
	for _, k := range tx.Transaction.Message.AccountKeys {
		if k.Signer {
			res.Signers = append(res.Signers, k.Pubkey)
		}
	}

	seen := make(map[string]struct{})
	visit := func(ix ParsedInstruction) {
		switch {
		case ix.ProgramID == programID:
			for _, a := range ix.Accounts {
				if _, dup := seen[a]; !dup {
					seen[a] = struct{}{}
					res.LaunchLabAccounts = append(res.LaunchLabAccounts, a)
				}
			}
		case ix.ProgramID == SystemProgramID && len(ix.Parsed) > 0:
			var xfer parsedSystemTransfer
			if err := json.Unmarshal(ix.Parsed, &xfer); err != nil {
				return
			}
			if xfer.Type == "transfer" && xfer.Info.Source == creator && xfer.Info.Destination == treasury {
				res.TreasuryLamports += xfer.Info.Lamports
			}
		}
	}
	for _, ix := range tx.Transaction.Message.Instructions {
		visit(ix)
	}
	for _, inner := range tx.Meta.InnerInstructions {
		for _, ix := range inner.Instructions {
			visit(ix)
		}
	}
	return res
}
