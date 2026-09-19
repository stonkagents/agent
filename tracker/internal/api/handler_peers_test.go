// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: Tests for peer HTTP handlers

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

func newTestServer(t *testing.T) (*Server, *clock.MockClock) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	dmcaRepo := repository.NewMemoryDMCARepository()
	store := presence.NewMemoryPresenceStore(clk)

	peerSvc := services.NewPeerService(peerRepo, store, nil)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	dmcaSvc := services.NewDMCAService(assetRepo, dmcaRepo)

	srv := NewServer(ServerDeps{
		PeerHandler:  NewPeerHandler(peerSvc),
		AssetHandler: NewAssetHandlerWithPeers(assetSvc, peerSvc),
		DMCAHandler:  NewDMCAHandler(dmcaSvc),
		Address:      ":7842",
	})
	return srv, clk
}

func TestHandleRegister_Success(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(RegisterPeerDTO{
		PeerID:     "peer-1",
		PublicKey:  "pubkey-1",
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("HandleRegister() status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp PeerResponseDTO
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.PeerID != "peer-1" {
		t.Errorf("HandleRegister() peer_id = %q, want %q", resp.PeerID, "peer-1")
	}
}

func TestHandleRegister_InvalidJSON(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleRegister() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_MissingFields(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(RegisterPeerDTO{PeerID: "peer-1"}) // missing pubkey
	req := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleRegister() status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var errResp ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("HandleRegister() error code = %q, want %q", errResp.Error.Code, "VALIDATION_ERROR")
	}
}

func TestHandleDiscover_Success(t *testing.T) {
	srv, _ := newTestServer(t)

	// Register 2 peers
	for _, id := range []string{"peer-a", "peer-b"} {
		body, _ := json.Marshal(RegisterPeerDTO{PeerID: id, PublicKey: "pk-" + id, Multiaddrs: []string{"/ip4/1.1.1.1"}})
		req := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
	}

	// Discover
	req := httptest.NewRequest("GET", "/api/v1/tracker/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleDiscover() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp ListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("HandleDiscover() total = %d, want 2", resp.Total)
	}
}

func TestHandleDiscover_Empty(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/tracker/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleDiscover() status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp ListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("HandleDiscover() total = %d, want 0", resp.Total)
	}
}

func TestHandleDiscover_OnlineParam(t *testing.T) {
	srv, clk := newTestServer(t)

	// Register 2 peers
	for _, id := range []string{"peer-a", "peer-b"} {
		body, _ := json.Marshal(RegisterPeerDTO{PeerID: id, PublicKey: "pk-" + id, Multiaddrs: []string{"/ip4/1.1.1.1"}})
		req := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
	}

	// Expire peer-b
	clk.Advance(6 * time.Minute)

	// ?online=true should return only online peers (possibly 0 since both expired without heartbeat)
	req := httptest.NewRequest("GET", "/api/v1/tracker/peers?online=true", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleDiscover(?online=true) status = %d, want %d", w.Code, http.StatusOK)
	}

	var onlineResp ListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &onlineResp)

	// Without ?online (default = all registered peers)
	req2 := httptest.NewRequest("GET", "/api/v1/tracker/peers", nil)
	w2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(w2, req2)

	var allResp ListResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &allResp)

	// All peers should include both registered peers
	if allResp.Total != 2 {
		t.Errorf("HandleDiscover() total = %d, want 2 (all registered)", allResp.Total)
	}

	// Online should be less (both expired after 6min without heartbeat)
	if onlineResp.Total != 0 {
		t.Errorf("HandleDiscover(?online=true) total = %d, want 0 (all expired)", onlineResp.Total)
	}
}

func TestInjectObservedAddr_SynthesizesFromPrivateOnlyPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register", nil)
	req.RemoteAddr = "34.120.10.20:34567"

	in := []string{
		"/ip4/192.168.1.10/tcp/52031/p2p/12D3KooWPrivate",
		"/ip4/172.25.176.1/tcp/52031/p2p/12D3KooWPrivate",
	}
	out := injectObservedAddr(req, "12D3KooWPrivate", in)
	// Should inject observed public IP using port from private addr (peer binds 0.0.0.0)
	if len(out) != len(in)+1 {
		t.Fatalf("len(out) = %d, want %d; out=%v", len(out), len(in)+1, out)
	}
	expected := "/ip4/34.120.10.20/tcp/52031/p2p/12D3KooWPrivate"
	if out[len(out)-1] != expected {
		t.Fatalf("last addr = %q, want %q", out[len(out)-1], expected)
	}
}

func TestInjectObservedAddr_SkipsWhenRelayCircuitPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register", nil)
	req.RemoteAddr = "34.120.10.20:34567"

	in := []string{
		"/ip4/10.0.0.5/tcp/52031/p2p/12D3KooWPrivate",
		"/ip4/52.1.2.3/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWPrivate",
	}
	out := injectObservedAddr(req, "12D3KooWPrivate", in)
	if len(out) != len(in) {
		t.Fatalf("len(out) = %d, want %d; out=%v", len(out), len(in), out)
	}
}
