// Package: tracker/internal/api
// Feature: StonkAgents devnet drip
// Purpose: HTTP tests for POST /api/dev/drip — contract, cooldown 429 with nextAt, drip_empty 503,
//          chain 502, validation, and the route staying unregistered without a handler.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const dripTestWallet = "9hSR6S7WPtxmTojgo6GG3k4yDPecgJY292j7xrsUGWBu"

// fakeDripper scripts the service outcome and records the call.
type fakeDripper struct {
	res    *services.DevDripResult
	err    error
	wallet string
	ip     string
}

func (f *fakeDripper) Drip(_ context.Context, wallet, ip string) (*services.DevDripResult, error) {
	f.wallet, f.ip = wallet, ip
	return f.res, f.err
}

func (f *fakeDripper) Config() services.DevDripConfig { return services.DevDripConfig{Decimals: 6} }

func newDripServer(t *testing.T, d *fakeDripper, withLimiter bool) *Server {
	t.Helper()
	deps := ServerDeps{Address: ":7842"}
	if d != nil {
		deps.DevDripHandler = NewDevDripHandler(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	if withLimiter {
		deps.Limiter = ratelimit.NewMemoryLimiter(clock.NewMockClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)))
	}
	return NewServer(deps)
}

func postDrip(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/dev/drip", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3000")
	req.RemoteAddr = "[::1]:5555"
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestDevDrip_Unregistered(t *testing.T) {
	rec := postDrip(t, newDripServer(t, nil, false), `{"wallet":"`+dripTestWallet+`"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no handler is wired", rec.Code)
	}
}

func TestDevDrip_Success(t *testing.T) {
	d := &fakeDripper{res: &services.DevDripResult{
		Signature: "5sig", SolLamports: 50_000_000, StonkRaw: 25_000_000, SentSol: true, SentStonk: true,
		Explorer: "https://solscan.io/tx/5sig?cluster=devnet",
	}}
	rec := postDrip(t, newDripServer(t, d, true), `{"wallet":" `+dripTestWallet+` "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var resp DevDripResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Signature != "5sig" || resp.Sol != 0.05 || resp.Stonk != 25 || !resp.SentSol || !resp.SentStonk || resp.Explorer == "" {
		t.Errorf("response = %+v", resp)
	}
	if d.wallet != dripTestWallet || d.ip != "::1" {
		t.Errorf("service got wallet=%q ip=%q", d.wallet, d.ip)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the portal origin echoed like the other public routes", got)
	}
}

func TestDevDrip_PartialAndNothing(t *testing.T) {
	d := &fakeDripper{res: &services.DevDripResult{Signature: "sig", StonkRaw: 25_000_000, SentStonk: true, Explorer: "x"}}
	body := decodeBody(t, postDrip(t, newDripServer(t, d, false), `{"wallet":"`+dripTestWallet+`"}`))
	if body["sentSol"] != false || body["sentStonk"] != true || body["sol"] != 0.0 || body["stonk"] != 25.0 {
		t.Errorf("partial body = %v", body)
	}
	d.res = &services.DevDripResult{}
	rec := postDrip(t, newDripServer(t, d, false), `{"wallet":"`+dripTestWallet+`"}`)
	body = decodeBody(t, rec)
	if rec.Code != http.StatusOK || body["signature"] != "" || body["sentSol"] != false || body["sentStonk"] != false {
		t.Errorf("nothing-to-send: %d %v", rec.Code, body)
	}
}

func TestDevDrip_Limited429WithNextAt(t *testing.T) {
	next := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ scope, code string }{{"wallet", "ALREADY_DRIPPED"}, {"ip", "IP_LIMITED"}} {
		d := &fakeDripper{err: &services.DripLimitedError{Scope: tc.scope, NextAt: next}}
		rec := postDrip(t, newDripServer(t, d, false), `{"wallet":"`+dripTestWallet+`"}`)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: status = %d", tc.scope, rec.Code)
		}
		body := decodeBody(t, rec)
		if body["nextAt"] != "2026-09-15T12:00:00Z" {
			t.Errorf("%s: nextAt = %v", tc.scope, body["nextAt"])
		}
		if errObj, _ := body["error"].(map[string]interface{}); errObj["code"] != tc.code {
			t.Errorf("%s: error = %v", tc.scope, body["error"])
		}
	}
}

func TestDevDrip_ErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{services.ErrDripEmpty, http.StatusServiceUnavailable, DevDripEmptyCode},
		{services.ErrDripInvalidWallet, http.StatusBadRequest, "VALIDATION_ERROR"},
		{services.ErrDripSelf, http.StatusBadRequest, "VALIDATION_ERROR"},
		{errors.Join(services.ErrDripChain, errors.New("rpc")), http.StatusBadGateway, "CHAIN_ERROR"},
		{services.ErrDripFailed, http.StatusBadGateway, "CHAIN_ERROR"},
		{services.ErrDripUnconfirmed, http.StatusBadGateway, "CHAIN_ERROR"},
		{errors.New("boom"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		rec := postDrip(t, newDripServer(t, &fakeDripper{err: tc.err}, false), `{"wallet":"`+dripTestWallet+`"}`)
		if rec.Code != tc.status {
			t.Errorf("%v: status = %d, want %d", tc.err, rec.Code, tc.status)
		}
		if errObj, _ := decodeBody(t, rec)["error"].(map[string]interface{}); errObj["code"] != tc.code {
			t.Errorf("%v: code = %v, want %s", tc.err, errObj["code"], tc.code)
		}
	}
}

func TestDevDrip_Validation(t *testing.T) {
	d := &fakeDripper{res: &services.DevDripResult{}}
	srv := newDripServer(t, d, false)
	for _, body := range []string{`not json`, `{}`, `{"wallet":"abc"}`, `{"wallet":"0OIl"}`} {
		rec := postDrip(t, srv, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", body, rec.Code)
		}
	}
	if d.wallet != "" {
		t.Error("service must not be called for an invalid body")
	}
}

func TestDevDrip_RateLimited(t *testing.T) {
	d := &fakeDripper{res: &services.DevDripResult{}}
	srv := newDripServer(t, d, true)
	var last int
	for i := 0; i < 11; i++ {
		last = postDrip(t, srv, `{"wallet":"`+dripTestWallet+`"}`).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("11th request status = %d, want 429 from the IP middleware", last)
	}
}
