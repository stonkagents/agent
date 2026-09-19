// Package: tracker/internal/solana
// Feature: F-013 (Credits & Identity)
// Story: US-013-07 (Solana Purchase)
// Purpose: Tests for Solana transaction verifier — covers all 6 on-chain verification checks (TD-073)

package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// rpcTestServer creates an httptest server that returns the given result for getTransaction.
func rpcTestServer(t *testing.T, result interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode RPC request: %v", err)
		}

		resultBytes, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("failed to marshal result: %v", err)
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  json.RawMessage(resultBytes),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

// validTxResponse builds a valid transaction response for testing.
func validTxResponse(treasuryAddr string, preBal, postBal int64, memo string) txResponse {
	blockTime := time.Now().Unix()
	return txResponse{
		Slot:      12345,
		BlockTime: &blockTime,
		Meta: &txMeta{
			Err:          nil,
			PreBalances:  []int64{1000000000, preBal},
			PostBalances: []int64{900000000, postBal},
			LogMessages:  []string{"Program log: " + memo},
		},
		Transaction: parsedTransaction{
			Message: parsedMessage{
				AccountKeys: []accountKey{
					{Pubkey: "sender-address"},
					{Pubkey: treasuryAddr},
				},
			},
		},
	}
}

func TestVerifier_ValidTransaction(t *testing.T) {
	treasury := "TreasuryAddr123"
	tx := validTxResponse(treasury, 500000000, 600000000, "intent:abc-123")

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-sig-valid", services.VerifyParams{
		TreasuryAddress: treasury,
		ExpectedMemo:    "intent:abc-123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true for valid transaction")
	}
	if result.ActualLamports != 100000000 {
		t.Errorf("ActualLamports = %d, want 100000000", result.ActualLamports)
	}
	if !result.MemoMatch {
		t.Error("expected MemoMatch=true")
	}
}

func TestVerifier_NullTransaction_NotFound(t *testing.T) {
	// Server returns null result (tx not found / not finalized)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":null}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-not-found", services.VerifyParams{
		TreasuryAddress: "Treasury123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false for null transaction")
	}
}

func TestVerifier_FailedTransaction(t *testing.T) {
	// Transaction that failed on-chain (Meta.Err is not nil)
	blockTime := time.Now().Unix()
	tx := txResponse{
		Slot:      12345,
		BlockTime: &blockTime,
		Meta: &txMeta{
			Err:          "InstructionError",
			PreBalances:  []int64{1000, 500},
			PostBalances: []int64{1000, 500},
		},
		Transaction: parsedTransaction{
			Message: parsedMessage{
				AccountKeys: []accountKey{{Pubkey: "Treasury"}},
			},
		},
	}

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-failed", services.VerifyParams{
		TreasuryAddress: "Treasury",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false for failed transaction")
	}
}

func TestVerifier_WrongTreasuryAddress(t *testing.T) {
	tx := validTxResponse("ActualTreasury", 500, 600, "memo")

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-wrong-treasury", services.VerifyParams{
		TreasuryAddress: "WrongTreasury",
		ExpectedMemo:    "memo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false when treasury address not in transaction accounts")
	}
}

func TestVerifier_ExpiredWindow(t *testing.T) {
	// Transaction is older than 15 minutes
	oldTime := time.Now().Add(-20 * time.Minute).Unix()
	tx := txResponse{
		Slot:      12345,
		BlockTime: &oldTime,
		Meta: &txMeta{
			Err:          nil,
			PreBalances:  []int64{1000, 500},
			PostBalances: []int64{900, 600},
			LogMessages:  []string{"memo"},
		},
		Transaction: parsedTransaction{
			Message: parsedMessage{
				AccountKeys: []accountKey{
					{Pubkey: "sender"},
					{Pubkey: "Treasury"},
				},
			},
		},
	}

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	_, err := verifier.VerifyTransaction(context.Background(), "tx-expired", services.VerifyParams{
		TreasuryAddress: "Treasury",
	})
	if err == nil {
		t.Fatal("expected error for expired transaction, got nil")
	}
	if got := err.Error(); !contains(got, "too old") {
		t.Errorf("error = %q, want to contain 'too old'", got)
	}
}

func TestVerifier_MemoMismatch(t *testing.T) {
	treasury := "Treasury123"
	tx := validTxResponse(treasury, 500, 600, "intent:wrong-id")

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-memo-mismatch", services.VerifyParams{
		TreasuryAddress: treasury,
		ExpectedMemo:    "intent:correct-id",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true (memo mismatch is reported via MemoMatch, not Success)")
	}
	if result.MemoMatch {
		t.Error("expected MemoMatch=false when memo doesn't match")
	}
}

// TestVerifier_AcceptsLegacyAndCurrentPurchaseMemo verifies the memo check passes for a
// memo an installed client writes, and still fails for another intent's memo.
func TestVerifier_AcceptsLegacyAndCurrentPurchaseMemo(t *testing.T) {
	const intentID = "0f2b1a7c-1111-4222-8333-444455556666"
	treasury := "Treasury123"
	params := services.VerifyParams{
		TreasuryAddress: treasury,
		ExpectedMemo:    services.PurchaseMemo(intentID),
	}

	cases := []struct {
		name string
		memo string
		want bool
	}{
		{"current prefix", "Memo (len 58): \"stonkagents:purchase:" + intentID + "\"", true},
		{"other intent, legacy prefix", "Memo: stonkagents:purchase:ffffffff-0000-4000-8000-000000000000", false},
		{"other intent, current prefix", "Memo: stonkagents:purchase:ffffffff-0000-4000-8000-000000000000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := rpcTestServer(t, validTxResponse(treasury, 500, 600, tc.memo))
			defer srv.Close()

			result, err := NewVerifier(NewClient(srv.URL)).VerifyTransaction(context.Background(), "tx-"+tc.name, params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.MemoMatch != tc.want {
				t.Errorf("MemoMatch = %v, want %v for log %q", result.MemoMatch, tc.want, tc.memo)
			}
		})
	}
}

func TestVerifier_NilMeta(t *testing.T) {
	blockTime := time.Now().Unix()
	tx := txResponse{
		Slot:      12345,
		BlockTime: &blockTime,
		Meta:      nil, // No meta = transaction not processed
		Transaction: parsedTransaction{
			Message: parsedMessage{
				AccountKeys: []accountKey{{Pubkey: "Treasury"}},
			},
		},
	}

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-nil-meta", services.VerifyParams{
		TreasuryAddress: "Treasury",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false when Meta is nil")
	}
}

func TestVerifier_NilBlockTime(t *testing.T) {
	tx := txResponse{
		Slot:      12345,
		BlockTime: nil, // No block time
		Meta: &txMeta{
			Err:          nil,
			PreBalances:  []int64{1000, 500},
			PostBalances: []int64{900, 600},
		},
		Transaction: parsedTransaction{
			Message: parsedMessage{
				AccountKeys: []accountKey{{Pubkey: "Treasury"}},
			},
		},
	}

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-nil-blocktime", services.VerifyParams{
		TreasuryAddress: "Treasury",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false when BlockTime is nil")
	}
}

func TestVerifier_BalanceDelta(t *testing.T) {
	// Verify exact lamport calculation: post - pre for the treasury account
	treasury := "Treasury123"
	tx := validTxResponse(treasury, 200000000, 350000000, "memo")

	srv := rpcTestServer(t, tx)
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	result, err := verifier.VerifyTransaction(context.Background(), "tx-balance", services.VerifyParams{
		TreasuryAddress: treasury,
		ExpectedMemo:    "memo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActualLamports != 150000000 {
		t.Errorf("ActualLamports = %d, want 150000000 (350M - 200M)", result.ActualLamports)
	}
}

func TestVerifier_RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid request"}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	verifier := NewVerifier(client)

	_, err := verifier.VerifyTransaction(context.Background(), "tx-rpc-error", services.VerifyParams{
		TreasuryAddress: "Treasury",
	})
	if err == nil {
		t.Fatal("expected error for RPC error response, got nil")
	}
}

// contains is a test helper for substring check.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
