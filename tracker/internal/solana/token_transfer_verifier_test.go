// Package: tracker/internal/solana
// Purpose: Tests for token offer payment inspection (pre/post token balances by owner, signer,
//          memo) and the GetTransaction call through a fake RPC.

package solana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	txFrom = "PosterWa11etXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	txTo   = "Rep1ierWa11etXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	txMint = "OfferMintXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
)

// tokenTransferFixture mimics a jsonParsed getTransaction result for a Token-2022 transfer of
// 1_000_000 raw units with a 1% fee withheld on the receiving side. The recipient ATA is
// created in the same transaction, so it has no pre balance. A second mint is present to make
// sure only the offer mint is counted.
func tokenTransferFixture(failed bool, memo string) string {
	errField := "null"
	if failed {
		errField = `{"InstructionError":[1,{"Custom":1}]}`
	}
	return `{
	  "slot": 456, "blockTime": 1757592000,
	  "meta": {"err": ` + errField + `, "preBalances": [], "postBalances": [],
	    "logMessages": ["Program MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr invoke [1]", "Program log: Memo (len 60): \"` + memo + `\"", "Program TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb invoke [1]"],
	    "innerInstructions": [],
	    "preTokenBalances": [
	      {"accountIndex": 2, "mint": "` + txMint + `", "owner": "` + txFrom + `", "uiTokenAmount": {"amount": "5000000", "decimals": 6, "uiAmountString": "5"}},
	      {"accountIndex": 4, "mint": "OtherMintXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "owner": "` + txFrom + `", "uiTokenAmount": {"amount": "99", "decimals": 6, "uiAmountString": "0.000099"}}
	    ],
	    "postTokenBalances": [
	      {"accountIndex": 2, "mint": "` + txMint + `", "owner": "` + txFrom + `", "uiTokenAmount": {"amount": "4000000", "decimals": 6, "uiAmountString": "4"}},
	      {"accountIndex": 3, "mint": "` + txMint + `", "owner": "` + txTo + `", "uiTokenAmount": {"amount": "990000", "decimals": 6, "uiAmountString": "0.99"}},
	      {"accountIndex": 4, "mint": "OtherMintXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "owner": "` + txFrom + `", "uiTokenAmount": {"amount": "0", "decimals": 6, "uiAmountString": "0"}}
	    ]},
	  "transaction": {
	    "signatures": ["paysig"],
	    "message": {
	      "accountKeys": [
	        {"pubkey": "` + txFrom + `", "signer": true, "writable": true, "source": "transaction"},
	        {"pubkey": "` + txTo + `", "signer": false, "writable": false, "source": "transaction"},
	        {"pubkey": "FromAtaXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "signer": false, "writable": true, "source": "transaction"},
	        {"pubkey": "ToAtaXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "signer": false, "writable": true, "source": "transaction"},
	        {"pubkey": "OtherAtaXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "signer": false, "writable": true, "source": "transaction"}
	      ],
	      "instructions": []
	    }
	  }
	}`
}

func TestInspectTokenTransfer_ExtractsFacts(t *testing.T) {
	memo := services.TokenOfferMemo("post-1", "reply-1")
	var tx ParsedTransaction
	if err := json.Unmarshal([]byte(tokenTransferFixture(false, memo)), &tx); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	p := services.TokenTransferVerifyParams{Signature: "paysig", Mint: txMint, FromWallet: txFrom, ToWallet: txTo, AmountRaw: 1_000_000, Memo: memo}
	res := InspectTokenTransfer(&tx, p)
	if !res.Found || !res.Succeeded || !res.FromSigner || !res.MemoMatch {
		t.Fatalf("found/succeeded/signer/memo = %v/%v/%v/%v", res.Found, res.Succeeded, res.FromSigner, res.MemoMatch)
	}
	if res.FromDebit != 1_000_000 {
		t.Errorf("from debit = %d, want 1000000 (the other mint must not count)", res.FromDebit)
	}
	if res.ToCredit != 990_000 {
		t.Errorf("to credit = %d, want 990000 (fee withheld, no pre balance)", res.ToCredit)
	}

	// Wrong memo, wrong wallets, wrong mint.
	other := InspectTokenTransfer(&tx, services.TokenTransferVerifyParams{Mint: txMint, FromWallet: txFrom, ToWallet: txTo, Memo: services.TokenOfferMemo("post-2", "reply-1")})
	if other.MemoMatch {
		t.Error("memo for another post must not match")
	}
	other = InspectTokenTransfer(&tx, services.TokenTransferVerifyParams{Mint: txMint, FromWallet: txTo, ToWallet: txFrom, Memo: memo})
	if other.FromSigner || other.FromDebit != 0 || other.ToCredit != 0 {
		t.Errorf("swapped wallets: signer=%v debit=%d credit=%d, want false/0/0", other.FromSigner, other.FromDebit, other.ToCredit)
	}
	other = InspectTokenTransfer(&tx, services.TokenTransferVerifyParams{Mint: "OtherMintXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", FromWallet: txFrom, ToWallet: txTo, Memo: memo})
	if other.FromDebit != 99 || other.ToCredit != 0 {
		t.Errorf("other mint: debit=%d credit=%d, want 99/0", other.FromDebit, other.ToCredit)
	}
}

func TestInspectTokenTransfer_FailedTx(t *testing.T) {
	var tx ParsedTransaction
	if err := json.Unmarshal([]byte(tokenTransferFixture(true, "x")), &tx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := InspectTokenTransfer(&tx, services.TokenTransferVerifyParams{Mint: txMint, FromWallet: txFrom, ToWallet: txTo})
	if !res.Found || res.Succeeded {
		t.Errorf("found/succeeded = %v/%v, want true/false", res.Found, res.Succeeded)
	}
}

func TestTokenTransferVerifier_RPC(t *testing.T) {
	memo := services.TokenOfferMemo("post-1", "reply-1")
	var gotMethod, gotCommitment string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMethod = req.Method
		params, _ := req.Params.([]interface{})
		sig, _ := params[0].(string)
		if opts, ok := params[1].(map[string]interface{}); ok {
			gotCommitment, _ = opts["commitment"].(string)
		}
		w.Header().Set("Content-Type", "application/json")
		if sig == "missing" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + tokenTransferFixture(false, memo) + `}`))
	}))
	defer srv.Close()

	v := NewTokenTransferVerifier(NewClient(srv.URL))
	p := services.TokenTransferVerifyParams{Signature: "paysig", Mint: txMint, FromWallet: txFrom, ToWallet: txTo, AmountRaw: 1_000_000, Memo: memo}
	res, err := v.VerifyTokenTransfer(context.Background(), p)
	if err != nil {
		t.Fatalf("VerifyTokenTransfer: %v", err)
	}
	if gotMethod != "getTransaction" || gotCommitment != "confirmed" {
		t.Errorf("rpc method/commitment = %s/%s, want getTransaction/confirmed", gotMethod, gotCommitment)
	}
	if !res.Found || res.FromDebit != 1_000_000 || res.ToCredit != 990_000 || !res.MemoMatch {
		t.Errorf("result = %+v", res)
	}
	p.Signature = "missing"
	res, err = v.VerifyTokenTransfer(context.Background(), p)
	if err != nil || res.Found {
		t.Errorf("missing: err=%v found=%v, want nil/false", err, res.Found)
	}
}
