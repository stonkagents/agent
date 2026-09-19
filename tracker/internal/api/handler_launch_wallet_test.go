// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: HTTP tests for one agent per wallet — GET /api/launch/by-wallet and the
//          409 LAUNCH_EXISTS answer of POST /api/launch/record under LAUNCH_ONE_PER_WALLET.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

var (
	lMint2 = launchTestKey(40)
	lPool2 = launchTestKey(41)
	lSig2  = launchTestSig(42)
)

// secondRecordBody is a second launch by the same creator (new mint, pool and signature).
func secondRecordBody() map[string]interface{} {
	b := recordBody()
	b["mint"], b["poolId"], b["launchSignature"], b["symbol"] = lMint2, lPool2, lSig2, "STNK2"
	return b
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) []map[string]interface{} {
	t.Helper()
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return resp.Data
}

func TestLaunchRecord_409_LaunchExists_OnePerWallet(t *testing.T) {
	env := newLaunchTestEnvOpts(t, true, true)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("first record: %d %s", w.Code, w.Body.String())
	}
	verified := env.verifier.Calls

	w := env.do(t, http.MethodPost, "/api/launch/record", secondRecordBody(), "")
	if w.Code != http.StatusConflict {
		t.Fatalf("second record status = %d body=%s, want 409", w.Code, w.Body.String())
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Mint    string `json:"mint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode 409: %v (%s)", err, w.Body.String())
	}
	if resp.Error.Code != "LAUNCH_EXISTS" || resp.Error.Mint != lMint || resp.Error.Message == "" {
		t.Errorf("409 body = %s, want code LAUNCH_EXISTS with mint %s", w.Body.String(), lMint)
	}
	if env.verifier.Calls != verified {
		t.Errorf("verifier called for the rejected launch (calls %d -> %d)", verified, env.verifier.Calls)
	}
	// The second launch was not stored; the first is still the wallet's only launch.
	if items := decodeList(t, env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet="+lCreator, nil, "")); len(items) != 1 || items[0]["mint"] != lMint {
		t.Errorf("by-wallet after 409 = %v, want only %s", items, lMint)
	}
	// Re-posting the original launch stays idempotent (200), not 409.
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusOK {
		t.Errorf("re-post of the recorded launch status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	// A different wallet can still launch.
	other := recordBody()
	other["mint"], other["poolId"], other["launchSignature"], other["creatorWallet"] = launchTestKey(50), launchTestKey(51), launchTestSig(52), lOther
	if w := env.do(t, http.MethodPost, "/api/launch/record", other, ""); w.Code != http.StatusCreated {
		t.Errorf("other wallet record status = %d body=%s, want 201", w.Code, w.Body.String())
	}
}

func TestLaunchRecord_OnePerWalletDisabled_AllowsSecondLaunch(t *testing.T) {
	env := newLaunchTestEnvOpts(t, false, false)
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("first record: %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodPost, "/api/launch/record", secondRecordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("second record with LAUNCH_ONE_PER_WALLET=false: %d %s, want 201", w.Code, w.Body.String())
	}
	items := decodeList(t, env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet="+lCreator, nil, ""))
	if len(items) != 2 {
		t.Errorf("by-wallet = %v, want both launches", items)
	}
}

func TestLaunchByWallet_ReturnsClaimedAndUnclaimed(t *testing.T) {
	env := newLaunchTestEnvOpts(t, true, true)

	// Unknown wallet: empty list, not an error; bad wallet: 400.
	if w := env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet="+lOther, nil, ""); w.Code != http.StatusOK || w.Body.String() != "{\"data\":[]}\n" {
		t.Errorf("unknown wallet = %d %q, want 200 {\"data\":[]}", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet=nope", nil, ""); w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" {
		t.Errorf("bad wallet = %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodGet, "/api/launch/by-wallet", nil, ""); w.Code != http.StatusBadRequest {
		t.Errorf("missing wallet = %d %s", w.Code, w.Body.String())
	}

	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	// Unclaimed: visible on both endpoints.
	if items := decodeList(t, env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet="+lCreator, nil, "")); len(items) != 1 || items[0]["status"] != "confirmed" {
		t.Fatalf("by-wallet before claim = %v", items)
	}
	if items := decodeList(t, env.do(t, http.MethodGet, "/api/launch/pending?wallet="+lCreator, nil, "")); len(items) != 1 {
		t.Fatalf("pending before claim = %v", items)
	}

	env.linkPeer(t, "peer-1", "key-1", lCreator)
	if w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-1"); w.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	// Claimed: gone from pending, still on by-wallet with the bound peer.
	if items := decodeList(t, env.do(t, http.MethodGet, "/api/launch/pending?wallet="+lCreator, nil, "")); len(items) != 0 {
		t.Errorf("pending after claim = %v, want empty", items)
	}
	items := decodeList(t, env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet="+lCreator, nil, ""))
	if len(items) != 1 || items[0]["mint"] != lMint || items[0]["status"] != "bound" || items[0]["peer_id"] != "peer-1" {
		t.Errorf("by-wallet after claim = %v, want the bound launch", items)
	}
	// And the claimed launch still blocks a second one for this wallet.
	if w := env.do(t, http.MethodPost, "/api/launch/record", secondRecordBody(), ""); w.Code != http.StatusConflict {
		t.Errorf("second record after claim = %d %s, want 409", w.Code, w.Body.String())
	}
}
