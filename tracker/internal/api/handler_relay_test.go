package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/relay"
)

func TestHandleGetRelayInfo_ReturnsRelayInfo(t *testing.T) {
	relayHost, err := relay.NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("failed to create relay host: %v", err)
	}
	defer relayHost.Close()

	handler := NewRelayHandler(relayHost)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracker/relay", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetRelayInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var dto RelayInfoDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if dto.PeerID == "" {
		t.Error("expected non-empty peer_id")
	}
	if len(dto.Multiaddrs) == 0 {
		t.Error("expected at least one multiaddr")
	}

	// Verify multiaddrs contain the PeerID
	for _, addr := range dto.Multiaddrs {
		suffix := "/p2p/" + dto.PeerID
		if len(addr) < len(suffix) || addr[len(addr)-len(suffix):] != suffix {
			t.Errorf("multiaddr %q does not end with %q", addr, suffix)
		}
	}
}

func TestHandleGetRelayInfo_NilRelayHost_Returns503(t *testing.T) {
	handler := NewRelayHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracker/relay", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetRelayInfo(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var envelope ErrorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	if envelope.Error.Code != "RELAY_UNAVAILABLE" {
		t.Errorf("expected RELAY_UNAVAILABLE error code, got %q", envelope.Error.Code)
	}
}

func TestHandleGetRelayInfo_ViaRouter(t *testing.T) {
	relayHost, err := relay.NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("failed to create relay host: %v", err)
	}
	defer relayHost.Close()

	srv := NewServer(ServerDeps{
		PeerHandler:  &PeerHandler{},
		AssetHandler: &AssetHandler{},
		DMCAHandler:  &DMCAHandler{},
		RelayHandler: NewRelayHandler(relayHost),
		Address:      ":0",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracker/relay", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var dto RelayInfoDTO
	if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if dto.PeerID == "" {
		t.Error("expected non-empty peer_id in routed response")
	}
}
