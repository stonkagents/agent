// Package: tracker/internal/solana
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: Real Solana RPC client for balance queries, wallet age checks, and Ed25519 signature verification

package solana

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mr-tron/base58"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// RPC request/response types for Solana JSON-RPC.

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// getBalance response.
type balanceResult struct {
	Context struct {
		Slot int64 `json:"slot"`
	} `json:"context"`
	Value int64 `json:"value"`
}

// SignatureInfo is a getSignaturesForAddress response item. Err is null for a successful
// transaction and an error object otherwise.
type SignatureInfo struct {
	Signature string          `json:"signature"`
	Slot      int64           `json:"slot"`
	BlockTime *int64          `json:"blockTime"`
	Err       json.RawMessage `json:"err"`
}

// Failed reports whether the transaction errored on chain.
func (s SignatureInfo) Failed() bool {
	return len(s.Err) > 0 && string(s.Err) != "null"
}

// Circuit breaker states.
const (
	cbClosed   = 0
	cbOpen     = 1
	cbHalfOpen = 2
)

// Resilience defaults.
const (
	maxRetries       = 3
	baseBackoff      = 500 * time.Millisecond
	jitterRange      = 200 * time.Millisecond
	cbFailThreshold  = 5
	cbOpenDuration   = 30 * time.Second
	rpcTimeout       = 5 * time.Second
	maxResponseBytes = 1 << 20 // 1 MB
)

// circuitBreaker tracks consecutive failures and opens the circuit.
type circuitBreaker struct {
	mu           sync.Mutex
	state        int
	failures     int
	lastFailedAt time.Time
}

func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case cbClosed:
		return true
	case cbOpen:
		if time.Since(cb.lastFailedAt) >= cbOpenDuration {
			cb.state = cbHalfOpen
			return true
		}
		return false
	case cbHalfOpen:
		return true
	}
	return false
}

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.state = cbClosed
}

func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastFailedAt = time.Now()
	if cb.failures >= cbFailThreshold {
		cb.state = cbOpen
	}
}

// Client implements services.SolanaClient using Solana JSON-RPC.
type Client struct {
	rpcURL string
	http   *http.Client
	cb     *circuitBreaker
}

// NewClient creates a real Solana RPC client.
func NewClient(rpcURL string) *Client {
	return &Client{
		rpcURL: rpcURL,
		http: &http.Client{
			Timeout: rpcTimeout,
		},
		cb: &circuitBreaker{},
	}
}

// GetBalance returns the SOL balance in lamports for the given wallet address.
func (c *Client) GetBalance(ctx context.Context, walletAddress string) (int64, error) {
	params := []interface{}{
		walletAddress,
		map[string]string{"commitment": "finalized"},
	}
	raw, err := c.callRPC(ctx, "getBalance", params)
	if err != nil {
		return 0, fmt.Errorf("getBalance RPC: %w", err)
	}
	var result balanceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, fmt.Errorf("getBalance unmarshal: %w", err)
	}
	return result.Value, nil
}

// GetFirstTransactionTime returns the timestamp of the earliest transaction
// for the given wallet. Returns nil if the wallet has no transactions.
func (c *Client) GetFirstTransactionTime(ctx context.Context, walletAddress string) (*time.Time, error) {
	// Solana returns newest-first. We request a large window and check the last
	// element — wallets with >1000 txs are definitely old enough for the 7-day check.
	params := []interface{}{
		walletAddress,
		map[string]interface{}{
			"limit":      1000,
			"commitment": "finalized",
		},
	}
	raw, err := c.callRPC(ctx, "getSignaturesForAddress", params)
	if err != nil {
		return nil, fmt.Errorf("getSignaturesForAddress RPC: %w", err)
	}
	var sigs []SignatureInfo
	if err := json.Unmarshal(raw, &sigs); err != nil {
		return nil, fmt.Errorf("getSignaturesForAddress unmarshal: %w", err)
	}
	if len(sigs) == 0 {
		return nil, nil
	}
	// The last element in the array is the oldest (Solana returns newest-first).
	oldest := sigs[len(sigs)-1]
	if oldest.BlockTime == nil {
		return nil, nil
	}
	t := time.Unix(*oldest.BlockTime, 0)
	return &t, nil
}

// GetSignaturesForAddress lists confirmed signatures touching address, newest first, at most
// limit (1..1000). until / before bound the walk (both exclusive) when set.
func (c *Client) GetSignaturesForAddress(ctx context.Context, address, until, before string, limit int) ([]SignatureInfo, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	opts := map[string]interface{}{"limit": limit, "commitment": "confirmed"}
	if until != "" {
		opts["until"] = until
	}
	if before != "" {
		opts["before"] = before
	}
	raw, err := c.callRPC(ctx, "getSignaturesForAddress", []interface{}{address, opts})
	if err != nil {
		return nil, fmt.Errorf("getSignaturesForAddress RPC: %w", err)
	}
	var sigs []SignatureInfo
	if err := json.Unmarshal(raw, &sigs); err != nil {
		return nil, fmt.Errorf("getSignaturesForAddress unmarshal: %w", err)
	}
	return sigs, nil
}

// GetTransaction fetches a confirmed transaction with jsonParsed encoding
// (maxSupportedTransactionVersion 0). Returns nil, nil when the transaction is not
// found at the requested commitment ("confirmed" or "finalized"; empty = "confirmed").
func (c *Client) GetTransaction(ctx context.Context, signature, commitment string) (*ParsedTransaction, error) {
	if commitment == "" {
		commitment = "confirmed"
	}
	params := []interface{}{
		signature,
		map[string]interface{}{
			"encoding":                       "jsonParsed",
			"commitment":                     commitment,
			"maxSupportedTransactionVersion": 0,
		},
	}
	raw, err := c.callRPC(ctx, "getTransaction", params)
	if err != nil {
		return nil, fmt.Errorf("getTransaction RPC: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var tx ParsedTransaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("getTransaction unmarshal: %w", err)
	}
	return &tx, nil
}

// VerifyWalletSignature verifies that the given signature was produced by the
// wallet's Ed25519 private key over the message. The walletAddr is a base58-encoded
// Ed25519 public key (Solana wallet address).
func (c *Client) VerifyWalletSignature(walletAddr string, message, signature []byte) bool {
	pubKeyBytes, err := base58.Decode(walletAddr)
	if err != nil {
		slog.Warn("[Solana.VerifyWalletSignature] base58 decode failed", "error", err)
		return false
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		slog.Warn("[Solana.VerifyWalletSignature] invalid pubkey size", "got", len(pubKeyBytes), "want", ed25519.PublicKeySize)
		return false
	}
	if len(signature) != ed25519.SignatureSize {
		slog.Warn("[Solana.VerifyWalletSignature] invalid signature size", "got", len(signature), "want", ed25519.SignatureSize)
		return false
	}
	return ed25519.Verify(pubKeyBytes, message, signature)
}

// callRPC sends a JSON-RPC request with retries and circuit breaker.
func (c *Client) callRPC(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if !c.cb.allow() {
		return nil, fmt.Errorf("solana RPC circuit breaker open")
	}

	var lastErr error
	for attempt := range maxRetries {
		result, err := c.doRPCCall(ctx, method, params)
		if err == nil {
			c.cb.recordSuccess()
			return result, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			break
		}

		// Exponential backoff with jitter before next retry.
		if attempt < maxRetries-1 {
			backoff := baseBackoff * time.Duration(1<<uint(attempt))
			jitter := time.Duration(rand.Int64N(int64(jitterRange))) - jitterRange/2
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff + jitter):
			}
		}
	}

	c.cb.recordFailure()
	return nil, fmt.Errorf("solana RPC %s failed after %d retries: %w", method, maxRetries, lastErr)
}

// doRPCCall executes a single JSON-RPC call.
func (c *Client) doRPCCall(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: HTTP 429: %s", services.ErrRPCRateLimited, string(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// --- Account reads (StonkAgents: Raydium LaunchLab pool state) ---

// AccountInfo is a decoded getAccountInfo / getMultipleAccounts value.
// Data holds the raw account bytes (base64-decoded).
type AccountInfo struct {
	Owner    string
	Lamports uint64
	Data     []byte
}

// rawAccountValue is the JSON shape of an account value with encoding=base64.
type rawAccountValue struct {
	Data     []string `json:"data"`
	Owner    string   `json:"owner"`
	Lamports uint64   `json:"lamports"`
}

func (v *rawAccountValue) toAccountInfo() (*AccountInfo, error) {
	if v == nil {
		return nil, nil
	}
	if len(v.Data) < 1 {
		return nil, fmt.Errorf("account data missing")
	}
	if len(v.Data) > 1 && v.Data[1] != "base64" {
		return nil, fmt.Errorf("unexpected account encoding %q", v.Data[1])
	}
	raw, err := base64.StdEncoding.DecodeString(v.Data[0])
	if err != nil {
		return nil, fmt.Errorf("account data base64: %w", err)
	}
	return &AccountInfo{Owner: v.Owner, Lamports: v.Lamports, Data: raw}, nil
}

// GetAccountInfo returns the account at address, or (nil, nil) if it does not exist.
func (c *Client) GetAccountInfo(ctx context.Context, address string) (*AccountInfo, error) {
	params := []interface{}{
		address,
		map[string]string{"encoding": "base64", "commitment": "confirmed"},
	}
	raw, err := c.callRPC(ctx, "getAccountInfo", params)
	if err != nil {
		return nil, fmt.Errorf("getAccountInfo RPC: %w", err)
	}
	var result struct {
		Value *rawAccountValue `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("getAccountInfo unmarshal: %w", err)
	}
	return result.Value.toAccountInfo()
}

// GetMultipleAccounts returns the accounts for addresses, in order; entries for
// non-existent accounts are nil. At most 100 addresses per call (RPC limit).
func (c *Client) GetMultipleAccounts(ctx context.Context, addresses []string) ([]*AccountInfo, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	if len(addresses) > 100 {
		return nil, fmt.Errorf("getMultipleAccounts: too many addresses (%d > 100)", len(addresses))
	}
	params := []interface{}{
		addresses,
		map[string]string{"encoding": "base64", "commitment": "confirmed"},
	}
	raw, err := c.callRPC(ctx, "getMultipleAccounts", params)
	if err != nil {
		return nil, fmt.Errorf("getMultipleAccounts RPC: %w", err)
	}
	var result struct {
		Value []*rawAccountValue `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("getMultipleAccounts unmarshal: %w", err)
	}
	out := make([]*AccountInfo, len(result.Value))
	for i, v := range result.Value {
		info, err := v.toAccountInfo()
		if err != nil {
			return nil, fmt.Errorf("getMultipleAccounts[%d]: %w", i, err)
		}
		out[i] = info
	}
	return out, nil
}

// Token program ids, for holder counts.
const (
	TokenProgramID     = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	Token2022ProgramID = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

// tokenAccountAmountOffset is where the u64 amount sits in a token account (mint 32 + owner 32).
const tokenAccountAmountOffset = 64

// CountTokenHolders counts token accounts of mint that hold a positive balance.
// The largest-accounts call answers in milliseconds and is complete below twenty
// accounts; only when it comes back full does the program-account scan run, and
// it reads eight bytes per account so even a large holder set stays cheap.
func (c *Client) CountTokenHolders(ctx context.Context, mint, tokenProgram string) (int, error) {
	raw, err := c.callRPC(ctx, "getTokenLargestAccounts", []interface{}{mint, map[string]string{"commitment": "confirmed"}})
	if err != nil {
		return 0, fmt.Errorf("getTokenLargestAccounts RPC: %w", err)
	}
	var largest struct {
		Value []struct {
			Amount string `json:"amount"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &largest); err != nil {
		return 0, fmt.Errorf("getTokenLargestAccounts unmarshal: %w", err)
	}
	count := 0
	for _, v := range largest.Value {
		if v.Amount != "" && v.Amount != "0" {
			count++
		}
	}
	if len(largest.Value) < 20 {
		return count, nil
	}

	params := []interface{}{
		tokenProgram,
		map[string]interface{}{
			"encoding":   "base64",
			"commitment": "confirmed",
			"dataSlice":  map[string]int{"offset": tokenAccountAmountOffset, "length": 8},
			"filters":    []interface{}{map[string]interface{}{"memcmp": map[string]interface{}{"offset": 0, "bytes": mint}}},
		},
	}
	raw, err = c.callRPC(ctx, "getProgramAccounts", params)
	if err != nil {
		// The fast path already gave a floor; report it rather than nothing.
		return count, nil
	}
	var accounts []struct {
		Account struct {
			Data []string `json:"data"`
		} `json:"account"`
	}
	if err := json.Unmarshal(raw, &accounts); err != nil {
		return count, nil
	}
	total := 0
	for _, a := range accounts {
		if len(a.Account.Data) == 0 {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(a.Account.Data[0])
		if err != nil || len(b) < 8 {
			continue
		}
		if binary.LittleEndian.Uint64(b) > 0 {
			total++
		}
	}
	if total < count {
		return count, nil
	}
	return total, nil
}

// tokenAccountsByOwnerResult is the getTokenAccountsByOwner value with jsonParsed encoding
// (only the fields the balance sums need).
type tokenAccountsByOwnerResult struct {
	Value []struct {
		Account struct {
			Data struct {
				Parsed struct {
					Info struct {
						Mint        string `json:"mint"`
						TokenAmount struct {
							Amount string `json:"amount"`
						} `json:"tokenAmount"`
					} `json:"info"`
				} `json:"parsed"`
			} `json:"data"`
		} `json:"account"`
	} `json:"value"`
}

// tokenAccountsByOwner runs getTokenAccountsByOwner with the given filter (mint or programId).
func (c *Client) tokenAccountsByOwner(ctx context.Context, owner string, filter map[string]string) (*tokenAccountsByOwnerResult, error) {
	raw, err := c.callRPC(ctx, "getTokenAccountsByOwner", []interface{}{
		owner, filter, map[string]string{"encoding": "jsonParsed", "commitment": "confirmed"},
	})
	if err != nil {
		return nil, fmt.Errorf("getTokenAccountsByOwner RPC: %w", err)
	}
	var result tokenAccountsByOwnerResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("getTokenAccountsByOwner unmarshal: %w", err)
	}
	return &result, nil
}

// TokenBalanceByOwner returns the raw amount of mint held across the owner's token accounts
// (0 when the owner has none). The mint filter covers both token programs.
func (c *Client) TokenBalanceByOwner(ctx context.Context, owner, mint string) (uint64, error) {
	result, err := c.tokenAccountsByOwner(ctx, owner, map[string]string{"mint": mint})
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, v := range result.Value {
		n, err := strconv.ParseUint(v.Account.Data.Parsed.Info.TokenAmount.Amount, 10, 64)
		if err != nil {
			continue
		}
		total += n
	}
	return total, nil
}

// TokenMintsByOwner returns mint -> raw amount for every mint the owner holds with a positive
// balance, across the classic token program and Token-2022 (two calls).
func (c *Client) TokenMintsByOwner(ctx context.Context, owner string) (map[string]uint64, error) {
	out := map[string]uint64{}
	for _, program := range []string{TokenProgramID, Token2022ProgramID} {
		result, err := c.tokenAccountsByOwner(ctx, owner, map[string]string{"programId": program})
		if err != nil {
			return nil, err
		}
		for _, v := range result.Value {
			info := v.Account.Data.Parsed.Info
			n, err := strconv.ParseUint(info.TokenAmount.Amount, 10, 64)
			if err != nil || n == 0 || info.Mint == "" {
				continue
			}
			out[info.Mint] += n
		}
	}
	return out, nil
}

// --- Transaction submission (StonkAgents devnet drip) ---

// SignatureStatus is one getSignatureStatuses value. Err is null on success.
type SignatureStatus struct {
	Slot               int64           `json:"slot"`
	ConfirmationStatus string          `json:"confirmationStatus"`
	Err                json.RawMessage `json:"err"`
}

// Failed reports whether the transaction errored on chain.
func (s SignatureStatus) Failed() bool {
	return len(s.Err) > 0 && string(s.Err) != "null"
}

// Confirmed reports whether the transaction reached confirmed or finalized commitment.
func (s SignatureStatus) Confirmed() bool {
	return s.ConfirmationStatus == "confirmed" || s.ConfirmationStatus == "finalized"
}

// GetLatestBlockhash returns the latest confirmed blockhash (base58).
func (c *Client) GetLatestBlockhash(ctx context.Context) (string, error) {
	raw, err := c.callRPC(ctx, "getLatestBlockhash", []interface{}{map[string]string{"commitment": "confirmed"}})
	if err != nil {
		return "", fmt.Errorf("getLatestBlockhash RPC: %w", err)
	}
	var result struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("getLatestBlockhash unmarshal: %w", err)
	}
	if result.Value.Blockhash == "" {
		return "", fmt.Errorf("getLatestBlockhash: empty blockhash")
	}
	return result.Value.Blockhash, nil
}

// SendTransaction submits a base64-encoded signed transaction with preflight enabled and
// returns its signature.
func (c *Client) SendTransaction(ctx context.Context, base64Tx string) (string, error) {
	params := []interface{}{
		base64Tx,
		map[string]interface{}{"encoding": "base64", "skipPreflight": false, "preflightCommitment": "confirmed"},
	}
	raw, err := c.callRPC(ctx, "sendTransaction", params)
	if err != nil {
		return "", fmt.Errorf("sendTransaction RPC: %w", err)
	}
	var sig string
	if err := json.Unmarshal(raw, &sig); err != nil {
		return "", fmt.Errorf("sendTransaction unmarshal: %w", err)
	}
	return sig, nil
}

// GetSignatureStatus returns the status of signature, or (nil, nil) when the cluster has not
// seen it yet.
func (c *Client) GetSignatureStatus(ctx context.Context, signature string) (*SignatureStatus, error) {
	params := []interface{}{[]string{signature}, map[string]bool{"searchTransactionHistory": false}}
	raw, err := c.callRPC(ctx, "getSignatureStatuses", params)
	if err != nil {
		return nil, fmt.Errorf("getSignatureStatuses RPC: %w", err)
	}
	var result struct {
		Value []*SignatureStatus `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("getSignatureStatuses unmarshal: %w", err)
	}
	if len(result.Value) == 0 {
		return nil, nil
	}
	return result.Value[0], nil
}

// GetTokenAccountBalance returns the raw amount held by the token account at address and
// whether the account exists (a missing ATA is (0, false, nil)).
func (c *Client) GetTokenAccountBalance(ctx context.Context, address string) (uint64, bool, error) {
	info, err := c.GetAccountInfo(ctx, address)
	if err != nil {
		return 0, false, err
	}
	if info == nil {
		return 0, false, nil
	}
	if len(info.Data) < tokenAccountAmountOffset+8 {
		return 0, false, fmt.Errorf("token account %s: data too short (%d bytes)", address, len(info.Data))
	}
	return binary.LittleEndian.Uint64(info.Data[tokenAccountAmountOffset:]), true, nil
}
