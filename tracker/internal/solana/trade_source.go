// Package: tracker/internal/solana
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: services.TradeIndexSource over Solana JSON-RPC — lists an address's signatures and
//          derives LaunchLab swaps (from the pool vaults' token balance deltas) and SPL burns
//          (from parsed burn / burnChecked instructions) out of jsonParsed transactions.

package solana

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// TradeSource implements services.TradeIndexSource.
type TradeSource struct {
	rpc        *Client
	commitment string
}

// NewTradeSource creates a trade source reading at "confirmed" commitment.
func NewTradeSource(rpc *Client) *TradeSource {
	return &TradeSource{rpc: rpc, commitment: "confirmed"}
}

// SignaturesForAddress implements services.TradeIndexSource.
func (s *TradeSource) SignaturesForAddress(ctx context.Context, address string, q services.SignatureQuery) ([]services.IndexedSignature, error) {
	sigs, err := s.rpc.GetSignaturesForAddress(ctx, address, q.Until, q.Before, q.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]services.IndexedSignature, 0, len(sigs))
	for _, si := range sigs {
		is := services.IndexedSignature{Signature: si.Signature, Slot: si.Slot, Failed: si.Failed()}
		if si.BlockTime != nil {
			t := time.Unix(*si.BlockTime, 0).UTC()
			is.BlockTime = &t
		}
		out = append(out, is)
	}
	return out, nil
}

// PoolSwap implements services.TradeIndexSource.
func (s *TradeSource) PoolSwap(ctx context.Context, signature string, vaults services.PoolVaults) (*services.PoolSwap, error) {
	tx, err := s.rpc.GetTransaction(ctx, signature, s.commitment)
	if err != nil || tx == nil {
		return nil, err
	}
	return DeriveLaunchLabSwap(tx, signature, vaults), nil
}

// TokenBurn implements services.TradeIndexSource.
func (s *TradeSource) TokenBurn(ctx context.Context, signature, mint string) (*services.TokenBurn, error) {
	tx, err := s.rpc.GetTransaction(ctx, signature, s.commitment)
	if err != nil || tx == nil {
		return nil, err
	}
	return DeriveTokenBurn(tx, signature, mint), nil
}

// tokenDelta is a token account's balance change within one transaction.
type tokenDelta struct {
	delta    *big.Int
	decimals int
}

// vaultDeltas computes post - pre for every token account in the transaction, keyed by pubkey.
func vaultDeltas(tx *ParsedTransaction) map[string]*tokenDelta {
	keys := tx.Transaction.Message.AccountKeys
	out := make(map[string]*tokenDelta)
	get := func(b ParsedTokenBalance) *tokenDelta {
		if b.AccountIndex < 0 || b.AccountIndex >= len(keys) {
			return nil
		}
		pk := keys[b.AccountIndex].Pubkey
		d, ok := out[pk]
		if !ok {
			d = &tokenDelta{delta: new(big.Int), decimals: b.UITokenAmount.Decimals}
			out[pk] = d
		}
		return d
	}
	for _, b := range tx.Meta.PreTokenBalances {
		if d := get(b); d != nil {
			if v, ok := new(big.Int).SetString(b.UITokenAmount.Amount, 10); ok {
				d.delta.Sub(d.delta, v)
			}
		}
	}
	for _, b := range tx.Meta.PostTokenBalances {
		if d := get(b); d != nil {
			if v, ok := new(big.Int).SetString(b.UITokenAmount.Amount, 10); ok {
				d.delta.Add(d.delta, v)
			}
		}
	}
	return out
}

// rawToWhole converts a raw integer amount to whole tokens.
func rawToWhole(raw *big.Int, decimals int) float64 {
	f := new(big.Float).SetInt(raw)
	div := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	v, _ := new(big.Float).Quo(f, div).Float64()
	return v
}

// feePayer is the first account key (the fee payer, always a signer).
func feePayer(tx *ParsedTransaction) string {
	for _, k := range tx.Transaction.Message.AccountKeys {
		if k.Signer {
			return k.Pubkey
		}
	}
	if len(tx.Transaction.Message.AccountKeys) > 0 {
		return tx.Transaction.Message.AccountKeys[0].Pubkey
	}
	return ""
}

// DeriveLaunchLabSwap reads the pool vaults' balance deltas out of a confirmed transaction:
// quote into VaultB and base out of VaultA is a buy, the reverse a sell. Anything else
// (failed transaction, no vault delta, both vaults growing as on initialize) is nil.
// Exported so tests can feed captured RPC fixtures without a network.
func DeriveLaunchLabSwap(tx *ParsedTransaction, signature string, vaults services.PoolVaults) *services.PoolSwap {
	if tx == nil || tx.Meta == nil || tx.Meta.Err != nil || tx.BlockTime == nil {
		return nil
	}
	deltas := vaultDeltas(tx)
	a, okA := deltas[vaults.VaultA]
	b, okB := deltas[vaults.VaultB]
	if !okA || !okB || a.delta.Sign() == 0 || b.delta.Sign() == 0 {
		return nil
	}
	swap := &services.PoolSwap{
		Signature: signature, Slot: tx.Slot, BlockTime: time.Unix(*tx.BlockTime, 0).UTC(), Trader: feePayer(tx),
	}
	switch {
	case a.delta.Sign() < 0 && b.delta.Sign() > 0:
		swap.Side = models.TradeSideBuy
	case a.delta.Sign() > 0 && b.delta.Sign() < 0:
		swap.Side = models.TradeSideSell
	default:
		return nil
	}
	swap.BaseAmount = rawToWhole(new(big.Int).Abs(a.delta), a.decimals)
	swap.QuoteAmount = rawToWhole(new(big.Int).Abs(b.delta), b.decimals)
	return swap
}

// parsedBurn is the "parsed" body of an spl-token burn / burnChecked instruction.
type parsedBurn struct {
	Type string `json:"type"`
	Info struct {
		Account     string              `json:"account"`
		Authority   string              `json:"authority"`
		Mint        string              `json:"mint"`
		Amount      string              `json:"amount"`      // burn: raw amount
		TokenAmount ParsedUITokenAmount `json:"tokenAmount"` // burnChecked: raw + decimals
	} `json:"info"`
}

// DeriveTokenBurn sums the burn / burnChecked instructions (outer and CPI) of mint in a
// confirmed transaction. A plain burn carries no decimals; they are taken from the burned
// account's token balance entry. Nil when the transaction failed or burns nothing of mint.
func DeriveTokenBurn(tx *ParsedTransaction, signature, mint string) *services.TokenBurn {
	if tx == nil || tx.Meta == nil || tx.Meta.Err != nil || tx.BlockTime == nil {
		return nil
	}
	keys := tx.Transaction.Message.AccountKeys
	decimalsOf := func(account string) (int, bool) {
		for _, list := range [][]ParsedTokenBalance{tx.Meta.PreTokenBalances, tx.Meta.PostTokenBalances} {
			for _, b := range list {
				if b.AccountIndex >= 0 && b.AccountIndex < len(keys) && keys[b.AccountIndex].Pubkey == account {
					return b.UITokenAmount.Decimals, true
				}
			}
		}
		return 0, false
	}
	total := new(big.Int)
	found := false
	burner := ""
	visit := func(ix ParsedInstruction) {
		if (ix.ProgramID != TokenProgramID && ix.ProgramID != Token2022ProgramID) || len(ix.Parsed) == 0 {
			return
		}
		var pb parsedBurn
		if err := json.Unmarshal(ix.Parsed, &pb); err != nil || pb.Info.Mint != mint {
			return
		}
		var raw string
		var decimals int
		switch pb.Type {
		case "burnChecked":
			raw, decimals = pb.Info.TokenAmount.Amount, pb.Info.TokenAmount.Decimals
		case "burn":
			d, ok := decimalsOf(pb.Info.Account)
			if !ok {
				return
			}
			raw, decimals = pb.Info.Amount, d
		default:
			return
		}
		v, ok := new(big.Int).SetString(raw, 10)
		if !ok || v.Sign() <= 0 {
			return
		}
		// Sum in whole units so mixed-decimal entries cannot occur (one mint, one decimals).
		scaled := new(big.Int).Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(burnScale-decimals)), nil))
		total.Add(total, scaled)
		found = true
		if burner == "" {
			burner = pb.Info.Authority
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
	if !found {
		return nil
	}
	if burner == "" {
		burner = feePayer(tx)
	}
	return &services.TokenBurn{
		Signature: signature, Slot: tx.Slot, BlockTime: time.Unix(*tx.BlockTime, 0).UTC(),
		Amount: rawToWhole(total, burnScale), Burner: burner,
	}
}

// burnScale is the fixed decimal scale burn amounts are summed at before conversion to whole tokens.
const burnScale = 18
