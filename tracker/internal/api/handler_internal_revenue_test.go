// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: HTTP tests for POST /api/internal/revenue (keeper ledger write) — shared secret
//          gate, 404 when unconfigured, validation, idempotency on signature.

package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const internalTestToken = "keeper-shared-secret"

// newInternalRevenueEnv wires the launch env plus the internal write handler (token "" = disabled).
func newInternalRevenueEnv(t *testing.T, token string) *launchTestEnv {
	t.Helper()
	env := newLaunchTestEnv(t, false)
	revenueSvc := services.NewRevenueServiceWithConfig(env.revenue, env.clock, services.RevenueConfig{TreasuryAddress: lTreasury})
	env.srv = NewServer(ServerDeps{
		RevenueHandler:         NewRevenueHandler(revenueSvc),
		InternalRevenueHandler: NewInternalRevenueHandler(revenueSvc, token),
		Address:                ":7842",
	})
	return env
}

func revenueBody() map[string]interface{} {
	return map[string]interface{}{
		"kind": "platform_fee_claim", "quoteMint": models.SOLMint, "amountRaw": 1_500_000_000,
		"amountUsd": 300.25, "signature": launchTestSig(70), "mint": lMint,
		"occurredAt": "2026-09-11T10:00:00Z", "meta": map[string]interface{}{"pool": lPool},
	}
}

func (e *launchTestEnv) postInternal(t *testing.T, body interface{}, token string) int {
	t.Helper()
	w := e.doWithHeader(t, http.MethodPost, "/api/internal/revenue", body, InternalTokenHeader, token)
	return w.Code
}

func TestInternalRevenue_404_WhenTokenUnset(t *testing.T) {
	env := newInternalRevenueEnv(t, "")
	if code := env.postInternal(t, revenueBody(), "anything"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when INTERNAL_API_TOKEN is unset", code)
	}
	// nil handler (older bootstrap) behaves the same.
	env.srv = NewServer(ServerDeps{Address: ":7842"})
	if code := env.postInternal(t, revenueBody(), "anything"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 with no handler wired", code)
	}
}

func TestInternalRevenue_401_WrongOrMissingToken(t *testing.T) {
	env := newInternalRevenueEnv(t, internalTestToken)
	if code := env.postInternal(t, revenueBody(), ""); code != http.StatusUnauthorized {
		t.Errorf("missing token status = %d, want 401", code)
	}
	if code := env.postInternal(t, revenueBody(), "nope"); code != http.StatusUnauthorized {
		t.Errorf("wrong token status = %d, want 401", code)
	}
	if n, _ := env.revenue.Recent(context.Background(), 10); len(n) != 0 {
		t.Errorf("ledger has %d rows after unauthorized writes, want 0", len(n))
	}
}

func TestInternalRevenue_201_Then200_IdempotentOnSignature(t *testing.T) {
	env := newInternalRevenueEnv(t, internalTestToken)

	w := env.doWithHeader(t, http.MethodPost, "/api/internal/revenue", revenueBody(), InternalTokenHeader, internalTestToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("first write status = %d body=%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	if data["created"] != true || data["kind"] != "platform_fee_claim" || data["signature"] != launchTestSig(70) || data["id"].(float64) <= 0 {
		t.Errorf("first write data = %v", data)
	}

	w = env.doWithHeader(t, http.MethodPost, "/api/internal/revenue", revenueBody(), InternalTokenHeader, internalTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", w.Code, w.Body.String())
	}
	if decodeData(t, w)["created"] != false {
		t.Errorf("replay data = %v, want created=false", decodeData(t, w))
	}

	// One row in the ledger, and it shows up on the public summary with explorer fields.
	rows, _ := env.revenue.Recent(context.Background(), 10)
	if len(rows) != 1 || rows[0].AmountRaw != 1_500_000_000 || rows[0].AmountUSD == nil || *rows[0].AmountUSD != 300.25 {
		t.Fatalf("ledger rows = %+v", rows)
	}
	if rows[0].OccurredAt.Format("2006-01-02T15:04:05Z") != "2026-09-11T10:00:00Z" || rows[0].Meta["pool"] != lPool {
		t.Errorf("row occurred_at/meta = %v / %v", rows[0].OccurredAt, rows[0].Meta)
	}
	pub := env.do(t, http.MethodGet, "/api/revenue", nil, "")
	summary := decodeData(t, pub)
	entries := summary["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("public entries = %d, want 1", len(entries))
	}
	first := entries[0].(map[string]interface{})
	if first["explorer_url"] != "https://solscan.io/tx/"+launchTestSig(70) || first["token_explorer_url"] != "https://solscan.io/token/"+lMint {
		t.Errorf("explorer fields = %v / %v", first["explorer_url"], first["token_explorer_url"])
	}
	if summary["wallets"].(map[string]interface{})["treasury"] != lTreasury {
		t.Errorf("wallets = %v", summary["wallets"])
	}
}

func TestInternalRevenue_400_Validation(t *testing.T) {
	env := newInternalRevenueEnv(t, internalTestToken)
	cases := map[string]func(m map[string]interface{}){
		"unknown kind":       func(m map[string]interface{}) { m["kind"] = "tip" },
		"bad quote mint":     func(m map[string]interface{}) { m["quoteMint"] = "x" },
		"bad signature":      func(m map[string]interface{}) { m["signature"] = "abc" },
		"bad mint":           func(m map[string]interface{}) { m["mint"] = "not-a-key" },
		"negative amount":    func(m map[string]interface{}) { m["amountRaw"] = -1 },
		"negative usd":       func(m map[string]interface{}) { m["amountUsd"] = -0.5 },
		"bad occurredAt":     func(m map[string]interface{}) { m["occurredAt"] = "yesterday" },
		"future occurredAt":  func(m map[string]interface{}) { m["occurredAt"] = "2999-01-01T00:00:00Z" },
		"missing occurredAt": func(m map[string]interface{}) { delete(m, "occurredAt") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := revenueBody()
			mutate(b)
			w := env.doWithHeader(t, http.MethodPost, "/api/internal/revenue", b, InternalTokenHeader, internalTestToken)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d body=%s, want 400", w.Code, w.Body.String())
			}
		})
	}
	if w := env.doWithHeader(t, http.MethodPost, "/api/internal/revenue", "{not json", InternalTokenHeader, internalTestToken); w.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON status = %d, want 400", w.Code)
	}
	// mint and amountUsd are optional.
	b := revenueBody()
	delete(b, "mint")
	delete(b, "amountUsd")
	b["signature"] = launchTestSig(71)
	if w := env.doWithHeader(t, http.MethodPost, "/api/internal/revenue", b, InternalTokenHeader, internalTestToken); w.Code != http.StatusCreated {
		t.Errorf("optional fields omitted: status = %d body=%s, want 201", w.Code, w.Body.String())
	}
}
