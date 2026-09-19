// Package: tracker/internal/solana
// Purpose: Tests for getTokenAccountsByOwner reads (community board room holder checks).

package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tokenAccountsServer(t *testing.T, wantFilter string, accounts string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "getTokenAccountsByOwner" {
			t.Errorf("method = %s, want getTokenAccountsByOwner", req.Method)
		}
		raw, _ := json.Marshal(req.Params)
		if wantFilter != "" && !strings.Contains(string(raw), wantFilter) {
			t.Errorf("params %s lack %s", raw, wantFilter)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","result":{"context":{"slot":1},"value":%s},"id":1}`, accounts)
	}))
}

func TestClient_TokenBalanceByOwner_SumsAccounts(t *testing.T) {
	srv := tokenAccountsServer(t, `{"mint":"Mint1"}`, `[
		{"pubkey":"A","account":{"data":{"parsed":{"info":{"mint":"Mint1","tokenAmount":{"amount":"5"}}}}}},
		{"pubkey":"B","account":{"data":{"parsed":{"info":{"mint":"Mint1","tokenAmount":{"amount":"7"}}}}}}]`)
	defer srv.Close()
	n, err := NewClient(srv.URL).TokenBalanceByOwner(context.Background(), "Owner", "Mint1")
	if err != nil || n != 12 {
		t.Errorf("balance = %d err=%v, want 12", n, err)
	}
}

func TestClient_TokenBalanceByOwner_NoAccounts(t *testing.T) {
	srv := tokenAccountsServer(t, "", `[]`)
	defer srv.Close()
	n, err := NewClient(srv.URL).TokenBalanceByOwner(context.Background(), "Owner", "Mint1")
	if err != nil || n != 0 {
		t.Errorf("balance = %d err=%v, want 0", n, err)
	}
}

func TestClient_TokenMintsByOwner_BothPrograms(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"context":{"slot":1},"value":[
				{"pubkey":"A","account":{"data":{"parsed":{"info":{"mint":"Mint1","tokenAmount":{"amount":"3"}}}}}},
				{"pubkey":"Z","account":{"data":{"parsed":{"info":{"mint":"Empty","tokenAmount":{"amount":"0"}}}}}}]},"id":1}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"context":{"slot":1},"value":[
			{"pubkey":"B","account":{"data":{"parsed":{"info":{"mint":"Mint2022","tokenAmount":{"amount":"9"}}}}}}]},"id":1}`)
	}))
	defer srv.Close()
	mints, err := NewClient(srv.URL).TokenMintsByOwner(context.Background(), "Owner")
	if err != nil {
		t.Fatalf("TokenMintsByOwner: %v", err)
	}
	if calls != 2 || len(mints) != 2 || mints["Mint1"] != 3 || mints["Mint2022"] != 9 {
		t.Errorf("mints = %v after %d calls", mints, calls)
	}
}
