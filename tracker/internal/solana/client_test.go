// Package: tracker/internal/solana
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: Tests for Solana RPC client — balance queries, wallet age, signature verification (TD-073)

package solana

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mr-tron/base58"
)

func TestClient_GetBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := balanceResult{Value: 2500000000} // 2.5 SOL
		resultBytes, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, string(resultBytes))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	balance, err := client.GetBalance(context.Background(), "SomeWalletAddr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if balance != 2500000000 {
		t.Errorf("balance = %d, want 2500000000", balance)
	}
}

func TestClient_GetBalance_RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid params"}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, err := client.GetBalance(context.Background(), "BadAddr")
	if err == nil {
		t.Fatal("expected error for RPC error, got nil")
	}
}

func TestClient_GetFirstTransactionTime(t *testing.T) {
	blockTime := int64(1700000000) // Nov 2023
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sigs := []SignatureInfo{
			{Signature: "newest", BlockTime: nil},
			{Signature: "oldest", BlockTime: &blockTime},
		}
		resultBytes, _ := json.Marshal(sigs)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, string(resultBytes))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	firstTx, err := client.GetFirstTransactionTime(context.Background(), "WalletAddr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstTx == nil {
		t.Fatal("expected non-nil first transaction time")
	}
	if firstTx.Unix() != blockTime {
		t.Errorf("first tx time = %d, want %d", firstTx.Unix(), blockTime)
	}
}

func TestClient_GetFirstTransactionTime_NoTransactions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	firstTx, err := client.GetFirstTransactionTime(context.Background(), "NewWallet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstTx != nil {
		t.Errorf("expected nil for wallet with no transactions, got %v", firstTx)
	}
}

func TestClient_VerifyWalletSignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	walletAddr := base58.Encode(pub)
	message := []byte("test-message")
	sig := ed25519.Sign(priv, message)

	client := NewClient("unused")
	if !client.VerifyWalletSignature(walletAddr, message, sig) {
		t.Error("expected valid signature to verify")
	}
}

func TestClient_VerifyWalletSignature_WrongMessage(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	walletAddr := base58.Encode(pub)
	sig := ed25519.Sign(priv, []byte("original-message"))

	client := NewClient("unused")
	if client.VerifyWalletSignature(walletAddr, []byte("different-message"), sig) {
		t.Error("expected verification to fail with wrong message")
	}
}

func TestClient_VerifyWalletSignature_InvalidBase58(t *testing.T) {
	client := NewClient("unused")
	if client.VerifyWalletSignature("not-valid-base58!!!", []byte("msg"), []byte("sig")) {
		t.Error("expected false for invalid base58 address")
	}
}

func TestClient_VerifyWalletSignature_WrongKeySize(t *testing.T) {
	// Encode a short byte slice as base58 (not 32 bytes)
	shortKey := base58.Encode([]byte("too-short"))
	client := NewClient("unused")
	if client.VerifyWalletSignature(shortKey, []byte("msg"), make([]byte, ed25519.SignatureSize)) {
		t.Error("expected false for wrong key size")
	}
}

func TestClient_VerifyWalletSignature_WrongSigSize(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	walletAddr := base58.Encode(pub)
	client := NewClient("unused")
	if client.VerifyWalletSignature(walletAddr, []byte("msg"), []byte("short-sig")) {
		t.Error("expected false for wrong signature size")
	}
}

func TestClient_CircuitBreaker_OpensAfterFailures(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "server error")
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	// Make enough failing calls to trip the circuit breaker (cbFailThreshold = 5)
	// Each call retries maxRetries=3 times, so 1 call = 3 HTTP requests = 1 recorded failure
	for i := 0; i < cbFailThreshold; i++ {
		_, _ = client.GetBalance(context.Background(), "wallet")
	}

	// Next call should fail immediately with circuit breaker error
	_, err := client.GetBalance(context.Background(), "wallet")
	if err == nil {
		t.Fatal("expected circuit breaker error, got nil")
	}
	if !containsSubstr(err.Error(), "circuit breaker open") {
		t.Errorf("error = %q, want to contain 'circuit breaker open'", err.Error())
	}
}

func TestClient_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "bad gateway")
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, err := client.GetBalance(context.Background(), "wallet")
	if err == nil {
		t.Fatal("expected error for HTTP 502, got nil")
	}
}
