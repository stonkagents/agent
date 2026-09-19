// Package: tracker/internal/solana
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: Swap and burn derivation from captured devnet transactions (jsonParsed, v0).
//
// Fixtures under testdata/ are verbatim getTransaction responses from api.devnet.solana.com
// captured 2026-09-13 for pool D6iVThFiYSg7wu69PhBJ42QU2WqvpFPJkmZX2PRz6hd3 (mint 2161TxLY…,
// quote USDCoct…, both 6 decimals; vaultA J7ii5is…, vaultB GzgvLxR…):
//   launchlab_swap_buy_devnet.json      3HF7nsh… buy: vaultA -17_429_315_825, vaultB +50_403
//   launchlab_initialize_devnet.json    zY4MSrE… the launch itself: both vaults grow, not a swap
//   token2022_burn_checked_devnet.json  4FjDA5S… Token-2022 burnChecked of 250_000 $AGENT

package solana

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	devPool    = "D6iVThFiYSg7wu69PhBJ42QU2WqvpFPJkmZX2PRz6hd3"
	devMint    = "2161TxLYzfZabXowZGHeE5oNMYC3HsQ2SKqRQfdc23ki"
	devQuote   = "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT"
	devVaultA  = "J7ii5isZa32q8H6UU5pY6Tyt4Rv8rKxchV9gpbBQDjsi"
	devVaultB  = "GzgvLxRW2yUTz2XAHsjt6ecEheRVTCiwXf3zL4R9PqEZ"
	devBuySig  = "3HF7nshUWuT3VfAYoChNiQmrTTjv9EecgkG9a45hQgcTvNhCsHSf6m1ahTAZw79pAYkdvVqq1gyxbckhSM12dGHT"
	devBuyer   = "GPZUbZBWkUFzNRDQ2eJvosCwNXWBCvRurdoU52Lspohp"
	devBurnSig = "4FjDA5SehYXRNGXQ6w5FBUM1xK8JuXsegzwfyvuHcYuCbR21YvnKHDdiR1nv2LzDV2wNwpTdWR2GvehomakjnSWk"
	devBurner  = "652nWgxA2vJpWFBtwZVfJt6aQyLhNyTd16ZPZQdxo274"
)

var devVaults = services.PoolVaults{PoolID: devPool, MintA: devMint, MintB: devQuote, VaultA: devVaultA, VaultB: devVaultB}

func loadTxFixture(t *testing.T, name string) *ParsedTransaction {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var resp struct {
		Result *ParsedTransaction `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if resp.Result == nil {
		t.Fatalf("fixture %s has no result", name)
	}
	return resp.Result
}

func near(a, b, rel float64) bool {
	if b == 0 {
		return math.Abs(a) < rel
	}
	return math.Abs(a-b)/math.Abs(b) < rel
}

func TestDeriveLaunchLabSwap_Buy_FromDevnetFixture(t *testing.T) {
	tx := loadTxFixture(t, "launchlab_swap_buy_devnet.json")
	swap := DeriveLaunchLabSwap(tx, devBuySig, devVaults)
	if swap == nil {
		t.Fatal("expected a swap")
	}
	if swap.Side != models.TradeSideBuy {
		t.Errorf("side = %q, want buy", swap.Side)
	}
	if swap.Trader != devBuyer {
		t.Errorf("trader = %q, want fee payer %q", swap.Trader, devBuyer)
	}
	if swap.Signature != devBuySig || swap.Slot != 497927832 || swap.BlockTime.Unix() != 1789337695 {
		t.Errorf("identity = %s/%d/%d", swap.Signature, swap.Slot, swap.BlockTime.Unix())
	}
	// vaultA: 98_965_… → 98_963_… = -17_429_315_825 raw (6 decimals); vaultB +50_403 raw.
	if !near(swap.BaseAmount, 17429.315825, 1e-9) {
		t.Errorf("base = %v, want 17429.315825", swap.BaseAmount)
	}
	if !near(swap.QuoteAmount, 0.050403, 1e-9) {
		t.Errorf("quote = %v, want 0.050403", swap.QuoteAmount)
	}
	price := swap.QuoteAmount / swap.BaseAmount
	if !near(price, 0.050403/17429.315825, 1e-9) || price <= 0 {
		t.Errorf("price = %v", price)
	}
}

// A sell is the mirror image: swap pre and post token balances of the buy fixture.
func TestDeriveLaunchLabSwap_Sell_MirroredFixture(t *testing.T) {
	tx := loadTxFixture(t, "launchlab_swap_buy_devnet.json")
	tx.Meta.PreTokenBalances, tx.Meta.PostTokenBalances = tx.Meta.PostTokenBalances, tx.Meta.PreTokenBalances
	swap := DeriveLaunchLabSwap(tx, devBuySig, devVaults)
	if swap == nil || swap.Side != models.TradeSideSell {
		t.Fatalf("swap = %+v, want sell", swap)
	}
	if !near(swap.BaseAmount, 17429.315825, 1e-9) || !near(swap.QuoteAmount, 0.050403, 1e-9) {
		t.Errorf("amounts = %v / %v", swap.BaseAmount, swap.QuoteAmount)
	}
}

func TestDeriveLaunchLabSwap_SkipsNonSwapAndFailed(t *testing.T) {
	// The initialize transaction funds both vaults: not a swap.
	if s := DeriveLaunchLabSwap(loadTxFixture(t, "launchlab_initialize_devnet.json"), "init", devVaults); s != nil {
		t.Errorf("initialize derived as swap: %+v", s)
	}
	// A failed transaction is never a trade, whatever its balances say.
	tx := loadTxFixture(t, "launchlab_swap_buy_devnet.json")
	tx.Meta.Err = map[string]interface{}{"InstructionError": []interface{}{2.0, map[string]interface{}{"Custom": 6001.0}}}
	if s := DeriveLaunchLabSwap(tx, devBuySig, devVaults); s != nil {
		t.Errorf("failed tx derived as swap: %+v", s)
	}
	// Unrelated vaults: no delta.
	other := services.PoolVaults{VaultA: "VaultAXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", VaultB: "VaultBXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"}
	if s := DeriveLaunchLabSwap(loadTxFixture(t, "launchlab_swap_buy_devnet.json"), devBuySig, other); s != nil {
		t.Errorf("foreign vaults derived as swap: %+v", s)
	}
	// A burn touches no vault.
	if s := DeriveLaunchLabSwap(loadTxFixture(t, "token2022_burn_checked_devnet.json"), devBurnSig, devVaults); s != nil {
		t.Errorf("burn derived as swap: %+v", s)
	}
}

func TestDeriveTokenBurn_BurnChecked_FromDevnetFixture(t *testing.T) {
	tx := loadTxFixture(t, "token2022_burn_checked_devnet.json")
	b := DeriveTokenBurn(tx, devBurnSig, devMint)
	if b == nil {
		t.Fatal("expected a burn")
	}
	if b.Amount != 250000 {
		t.Errorf("amount = %v, want 250000", b.Amount)
	}
	if b.Burner != devBurner {
		t.Errorf("burner = %q, want %q", b.Burner, devBurner)
	}
	if b.Signature != devBurnSig || b.Slot != 497922926 || b.BlockTime.Unix() != 1789336889 {
		t.Errorf("identity = %s/%d/%d", b.Signature, b.Slot, b.BlockTime.Unix())
	}
	// Another mint: nothing.
	if o := DeriveTokenBurn(tx, devBurnSig, devQuote); o != nil {
		t.Errorf("burn of another mint reported: %+v", o)
	}
	// A swap burns nothing.
	if o := DeriveTokenBurn(loadTxFixture(t, "launchlab_swap_buy_devnet.json"), devBuySig, devMint); o != nil {
		t.Errorf("swap reported as burn: %+v", o)
	}
}

// A plain (unchecked) burn carries a raw amount only; decimals come from the token balances.
func TestDeriveTokenBurn_PlainBurn_UsesBalanceDecimals(t *testing.T) {
	tx := loadTxFixture(t, "token2022_burn_checked_devnet.json")
	ix := &tx.Transaction.Message.Instructions[0]
	var pb map[string]interface{}
	if err := json.Unmarshal(ix.Parsed, &pb); err != nil {
		t.Fatal(err)
	}
	info := pb["info"].(map[string]interface{})
	delete(info, "tokenAmount")
	info["amount"] = "1500000" // 1.5 whole at 6 decimals
	pb["type"] = "burn"
	ix.Parsed, _ = json.Marshal(pb)
	b := DeriveTokenBurn(tx, devBurnSig, devMint)
	if b == nil || b.Amount != 1.5 {
		t.Fatalf("burn = %+v, want 1.5", b)
	}
}

// TradeSource over a fake RPC: signatures map to IndexedSignature (failed flag, block time)
// and a swap fetch goes through getTransaction.
func TestTradeSource_SignaturesAndSwap(t *testing.T) {
	buy, _ := os.ReadFile(filepath.Join("testdata", "launchlab_swap_buy_devnet.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "getSignaturesForAddress":
			opts := req.Params[1].(map[string]interface{})
			if opts["until"] != "cursorSig" || opts["limit"].(float64) != 1000 {
				t.Errorf("unexpected opts %v", opts)
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[
				{"signature":"ok","slot":10,"blockTime":1700000000,"err":null},
				{"signature":"bad","slot":9,"blockTime":1699999990,"err":{"InstructionError":[1,{"Custom":1}]}}]}`))
		case "getTransaction":
			w.Write(buy)
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	defer srv.Close()

	src := NewTradeSource(NewClient(srv.URL))
	sigs, err := src.SignaturesForAddress(context.Background(), devPool, services.SignatureQuery{Until: "cursorSig", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 || sigs[0].Failed || !sigs[1].Failed || sigs[0].BlockTime == nil || sigs[0].BlockTime.Unix() != 1700000000 {
		t.Fatalf("sigs = %+v", sigs)
	}
	swap, err := src.PoolSwap(context.Background(), devBuySig, devVaults)
	if err != nil || swap == nil || swap.Side != models.TradeSideBuy {
		t.Fatalf("swap = %+v err = %v", swap, err)
	}
}

// HTTP 429 is surfaced as services.ErrRPCRateLimited so the indexer can back off.
func TestClient_RateLimitedErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	_, err := c.GetSignaturesForAddress(context.Background(), devPool, "", "", 10)
	if err == nil {
		t.Fatal("expected error")
	}
	if !isRateLimitErr(err) {
		t.Errorf("error not typed as rate limited: %v", err)
	}
}

func isRateLimitErr(err error) bool {
	for e := err; e != nil; {
		if e == services.ErrRPCRateLimited {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
