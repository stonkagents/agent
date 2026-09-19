// Package: tracker/internal/solana
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Tests for jsonParsed launch transaction inspection and the GetTransaction RPC call

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
	fxCreator  = "CreatorWa11etXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	fxTreasury = "TreasuryWa11etXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	fxMint     = "MintXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	fxPool     = "Poo1XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	fxQuote    = "So11111111111111111111111111111111111111112"
	fxPlatform = "P1atformXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
)

// launchTxFixture mimics a jsonParsed getTransaction result for a LaunchLab initialize
// bundled with the platform fee transfer (outer) and a second small transfer via CPI (inner).
func launchTxFixture(failed bool) string {
	errField := "null"
	if failed {
		errField = `{"InstructionError":[1,{"Custom":6000}]}`
	}
	return `{
	  "slot": 123, "blockTime": 1757592000,
	  "meta": {"err": ` + errField + `, "preBalances": [], "postBalances": [], "logMessages": [],
	    "innerInstructions": [{"index": 1, "instructions": [
	      {"program": "system", "programId": "11111111111111111111111111111111",
	       "parsed": {"type": "transfer", "info": {"source": "` + fxCreator + `", "destination": "` + fxTreasury + `", "lamports": 500}}}
	    ]}]},
	  "transaction": {
	    "signatures": ["sig"],
	    "message": {
	      "accountKeys": [
	        {"pubkey": "` + fxCreator + `", "signer": true, "writable": true, "source": "transaction"},
	        {"pubkey": "` + fxMint + `", "signer": true, "writable": true, "source": "transaction"},
	        {"pubkey": "` + fxTreasury + `", "signer": false, "writable": true, "source": "transaction"},
	        {"pubkey": "` + fxPool + `", "signer": false, "writable": true, "source": "lookupTable"}
	      ],
	      "instructions": [
	        {"program": "system", "programId": "11111111111111111111111111111111",
	         "parsed": {"type": "transfer", "info": {"source": "` + fxCreator + `", "destination": "` + fxTreasury + `", "lamports": 15000000}}},
	        {"programId": "` + services.LaunchLabProgramID + `",
	         "accounts": ["` + fxCreator + `", "` + fxPlatform + `", "` + fxPool + `", "` + fxMint + `", "` + fxQuote + `"],
	         "data": "base58data"},
	        {"program": "system", "programId": "11111111111111111111111111111111",
	         "parsed": {"type": "transfer", "info": {"source": "` + fxCreator + `", "destination": "SomeoneE1seXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", "lamports": 999999}}}
	      ]
	    }
	  }
	}`
}

func TestInspectLaunchTransaction_ExtractsFacts(t *testing.T) {
	var tx ParsedTransaction
	if err := json.Unmarshal([]byte(launchTxFixture(false)), &tx); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	res := InspectLaunchTransaction(&tx, fxCreator, fxTreasury)

	if !res.Found || !res.Succeeded {
		t.Fatalf("found/succeeded = %v/%v", res.Found, res.Succeeded)
	}
	if len(res.Signers) != 2 || res.Signers[0] != fxCreator {
		t.Errorf("signers = %v", res.Signers)
	}
	for _, want := range []string{fxMint, fxPool, fxQuote, fxPlatform, fxCreator} {
		found := false
		for _, a := range res.LaunchLabAccounts {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("LaunchLab accounts missing %s: %v", want, res.LaunchLabAccounts)
		}
	}
	// Outer 15_000_000 + inner 500 to treasury; the transfer to someone else is ignored.
	if res.TreasuryLamports != 15_000_500 {
		t.Errorf("treasury lamports = %d, want 15000500", res.TreasuryLamports)
	}
	if res.BlockTime == nil || res.BlockTime.Unix() != 1757592000 {
		t.Errorf("blockTime = %v", res.BlockTime)
	}

	// Different treasury → no fee observed.
	if other := InspectLaunchTransaction(&tx, fxCreator, "Other"); other.TreasuryLamports != 0 {
		t.Errorf("wrong treasury lamports = %d, want 0", other.TreasuryLamports)
	}
	// Different creator → no fee observed (transfer source must be the creator).
	if other := InspectLaunchTransaction(&tx, "Other", fxTreasury); other.TreasuryLamports != 0 {
		t.Errorf("wrong creator lamports = %d, want 0", other.TreasuryLamports)
	}
}

func TestInspectLaunchTransaction_FailedTx(t *testing.T) {
	var tx ParsedTransaction
	if err := json.Unmarshal([]byte(launchTxFixture(true)), &tx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := InspectLaunchTransaction(&tx, fxCreator, fxTreasury)
	if !res.Found || res.Succeeded {
		t.Errorf("found/succeeded = %v/%v, want true/false", res.Found, res.Succeeded)
	}
}

func TestLaunchVerifier_VerifyLaunch_RPC(t *testing.T) {
	var gotMethod string
	var gotParams []interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMethod = req.Method
		gotParams, _ = req.Params.([]interface{})
		sig, _ := gotParams[0].(string)
		w.Header().Set("Content-Type", "application/json")
		if sig == "missing" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + launchTxFixture(false) + `}`))
	}))
	defer srv.Close()

	v := NewLaunchVerifier(NewClient(srv.URL))
	res, err := v.VerifyLaunch(context.Background(), services.LaunchVerifyParams{
		Signature: "sig", CreatorWallet: fxCreator, TreasuryAddress: fxTreasury,
	})
	if err != nil {
		t.Fatalf("VerifyLaunch: %v", err)
	}
	if gotMethod != "getTransaction" {
		t.Errorf("method = %q", gotMethod)
	}
	opts, _ := gotParams[1].(map[string]interface{})
	if opts["encoding"] != "jsonParsed" || opts["commitment"] != "confirmed" || opts["maxSupportedTransactionVersion"].(float64) != 0 {
		t.Errorf("rpc options = %v", opts)
	}
	if !res.Found || res.TreasuryLamports != 15_000_500 {
		t.Errorf("result = %+v", res)
	}

	missing, err := v.VerifyLaunch(context.Background(), services.LaunchVerifyParams{Signature: "missing"})
	if err != nil || missing.Found {
		t.Errorf("missing tx: res=%+v err=%v, want Found=false", missing, err)
	}
}
