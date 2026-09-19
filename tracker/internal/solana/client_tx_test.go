// Package: tracker/internal/solana
// Feature: StonkAgents devnet drip
// Purpose: Tests for the transaction-submission RPC methods.

package solana

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rpcStub answers each JSON-RPC method with the canned result (or error) in results.
func rpcStub(t *testing.T, results map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		body, ok := results[req.Method]
		if !ok {
			t.Errorf("unexpected RPC method %s", req.Method)
			body = `null`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,%s}`, body)
	}))
}

func TestClient_GetLatestBlockhash(t *testing.T) {
	srv := rpcStub(t, map[string]string{"getLatestBlockhash": `"result":{"context":{"slot":1},"value":{"blockhash":"CktRuQ2mttgRGkXJtyksdKHjUdc2C4TgDzyB98oEzy8","lastValidBlockHeight":10}}`})
	defer srv.Close()
	bh, err := NewClient(srv.URL).GetLatestBlockhash(context.Background())
	if err != nil || bh != "CktRuQ2mttgRGkXJtyksdKHjUdc2C4TgDzyB98oEzy8" {
		t.Fatalf("got %q, %v", bh, err)
	}
}

func TestClient_SendTransaction(t *testing.T) {
	srv := rpcStub(t, map[string]string{"sendTransaction": `"result":"5VERv8NMvzbJMEkV8xnrLkEaWRtSz9CosKDYjCJjBRnbJLgp8uirBgmQpjKhoR4tjF3ZpRzrFmBV6UjKdiSZkQUW"`})
	defer srv.Close()
	sig, err := NewClient(srv.URL).SendTransaction(context.Background(), "AQ==")
	if err != nil || sig == "" {
		t.Fatalf("got %q, %v", sig, err)
	}
}

func TestClient_GetSignatureStatus(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		srv := rpcStub(t, map[string]string{"getSignatureStatuses": `"result":{"context":{"slot":1},"value":[null]}`})
		defer srv.Close()
		st, err := NewClient(srv.URL).GetSignatureStatus(context.Background(), "sig")
		if err != nil || st != nil {
			t.Fatalf("got %+v, %v", st, err)
		}
	})
	t.Run("confirmed", func(t *testing.T) {
		srv := rpcStub(t, map[string]string{"getSignatureStatuses": `"result":{"context":{"slot":1},"value":[{"slot":5,"confirmations":3,"err":null,"confirmationStatus":"confirmed"}]}`})
		defer srv.Close()
		st, err := NewClient(srv.URL).GetSignatureStatus(context.Background(), "sig")
		if err != nil || st == nil || !st.Confirmed() || st.Failed() {
			t.Fatalf("got %+v, %v", st, err)
		}
	})
	t.Run("failed", func(t *testing.T) {
		srv := rpcStub(t, map[string]string{"getSignatureStatuses": `"result":{"context":{"slot":1},"value":[{"slot":5,"err":{"InstructionError":[0,"Custom"]},"confirmationStatus":"processed"}]}`})
		defer srv.Close()
		st, err := NewClient(srv.URL).GetSignatureStatus(context.Background(), "sig")
		if err != nil || st == nil || !st.Failed() || st.Confirmed() {
			t.Fatalf("got %+v, %v", st, err)
		}
	})
}

func TestClient_GetTokenAccountBalance(t *testing.T) {
	data := make([]byte, 165)
	binary.LittleEndian.PutUint64(data[tokenAccountAmountOffset:], 25_000_000)
	enc := base64.StdEncoding.EncodeToString(data)
	t.Run("exists", func(t *testing.T) {
		srv := rpcStub(t, map[string]string{"getAccountInfo": fmt.Sprintf(`"result":{"context":{"slot":1},"value":{"data":["%s","base64"],"owner":"%s","lamports":2039280}}`, enc, TokenProgramID)})
		defer srv.Close()
		amt, ok, err := NewClient(srv.URL).GetTokenAccountBalance(context.Background(), "ata")
		if err != nil || !ok || amt != 25_000_000 {
			t.Fatalf("got %d, %v, %v", amt, ok, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		srv := rpcStub(t, map[string]string{"getAccountInfo": `"result":{"context":{"slot":1},"value":null}`})
		defer srv.Close()
		amt, ok, err := NewClient(srv.URL).GetTokenAccountBalance(context.Background(), "ata")
		if err != nil || ok || amt != 0 {
			t.Fatalf("got %d, %v, %v", amt, ok, err)
		}
	})
}
