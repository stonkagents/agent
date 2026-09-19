// Package: api
// Feature: F-025 (Auto-Update System)
// Story: US-025-03 (Tracker Version Awareness)
// Purpose: Tests for POST /api/v1/tracker/heartbeat including conditional update fields

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/internal/update"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// mockVersionProvider is a test double for VersionProvider.
type mockVersionProvider struct {
	manifest *update.ManifestContent
	err      error
}

func (m *mockVersionProvider) LatestVersion() (*update.ManifestContent, error) {
	return m.manifest, m.err
}

func setupHeartbeatTest(t *testing.T) (*PeerHandler, *mux.Router, *repository.MemoryPeerAPIKeyRepository, *presence.MemoryPresenceStore) {
	t.Helper()
	peerRepo := repository.NewMemoryPeerRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	presenceStore := presence.NewMemoryPresenceStore(clock.RealClock{})
	t.Cleanup(func() { presenceStore.Close() })
	peerSvc := services.NewPeerService(peerRepo, presenceStore, apiKeyRepo)

	handler := NewPeerHandlerWithTrustBlock(peerSvc, nil)
	handler.SetAPIKeyRepo(apiKeyRepo)
	handler.SetPresenceStore(presenceStore)

	r := mux.NewRouter()
	api := r.PathPrefix("/api/v1/tracker").Subrouter()
	api.Handle("/heartbeat", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleHeartbeat))).Methods(http.MethodPost)

	return handler, r, apiKeyRepo, presenceStore
}

func TestHandleHeartbeat_ValidKey(t *testing.T) {
	_, router, apiKeyRepo, _ := setupHeartbeatTest(t)

	// Register a peer with an API key
	apiKeyRepo.Store("test-peer-id", "valid-api-key-123")

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/127.0.0.1/tcp/4001"},
		"client_version": "0.1.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "valid-api-key-123")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleHeartbeat() status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp struct {
		Data struct {
			Status               string `json:"status"`
			NextHeartbeatSeconds int    `json:"next_heartbeat_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if resp.Data.Status != "ok" {
		t.Errorf("status = %q, want %q", resp.Data.Status, "ok")
	}
	if resp.Data.NextHeartbeatSeconds <= 0 {
		t.Errorf("next_heartbeat_seconds = %d, want > 0", resp.Data.NextHeartbeatSeconds)
	}
}

func TestHandleHeartbeat_MissingAPIKey(t *testing.T) {
	_, router, _, _ := setupHeartbeatTest(t)

	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("HandleHeartbeat() status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleHeartbeat_InvalidAPIKey(t *testing.T) {
	_, router, _, _ := setupHeartbeatTest(t)

	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "invalid-key")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("HandleHeartbeat() status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleHeartbeat_UpdatesPresence(t *testing.T) {
	_, router, apiKeyRepo, presenceStore := setupHeartbeatTest(t)

	apiKeyRepo.Store("presence-peer", "presence-key-456")

	// Verify peer is NOT online before heartbeat
	online, _ := presenceStore.IsOnline(nil, "presence-peer")
	if online {
		t.Fatal("peer should be offline before heartbeat")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/10.0.0.1/tcp/4001"},
		"client_version": "0.1.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "presence-key-456")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleHeartbeat() status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify peer IS online after heartbeat
	online, _ = presenceStore.IsOnline(nil, "presence-peer")
	if !online {
		t.Error("peer should be online after heartbeat")
	}
}

func TestHandleHeartbeat_RefreshesPeerMultiaddrs(t *testing.T) {
	peerRepo := repository.NewMemoryPeerRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	presenceStore := presence.NewMemoryPresenceStore(clock.RealClock{})
	t.Cleanup(func() { presenceStore.Close() })
	peerSvc := services.NewPeerService(peerRepo, presenceStore, apiKeyRepo)

	// Seed peer with stale address
	err := peerRepo.Upsert(nil, &models.Peer{
		PeerID:     "refresh-peer",
		PublicKey:  "pubkey-refresh",
		Multiaddrs: []string{"/ip4/10.0.0.9/tcp/4001"},
		FirstSeen:  time.Now().UTC(),
		LastSeen:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	apiKeyRepo.Store("refresh-peer", "refresh-key")

	handler := NewPeerHandlerWithTrustBlock(peerSvc, nil)
	handler.SetAPIKeyRepo(apiKeyRepo)
	handler.SetPresenceStore(presenceStore)

	r := mux.NewRouter()
	api := r.PathPrefix("/api/v1/tracker").Subrouter()
	api.Handle("/heartbeat", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleHeartbeat))).Methods(http.MethodPost)

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/44.55.66.77/tcp/52031/p2p/12D3KooWCLYWBbiKrj5unB3aqkQy41JjyASS1qMc9nPRuCWu9LHQ", "invalid", " /ip4/44.55.66.77/tcp/52031/p2p/12D3KooWCLYWBbiKrj5unB3aqkQy41JjyASS1qMc9nPRuCWu9LHQ "},
		"client_version": "0.1.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "refresh-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	peer, err := peerRepo.FindByID(nil, "refresh-peer")
	if err != nil {
		t.Fatalf("find peer: %v", err)
	}
	if len(peer.Multiaddrs) != 1 {
		t.Fatalf("multiaddrs len = %d, want 1; got=%v", len(peer.Multiaddrs), peer.Multiaddrs)
	}
	if peer.Multiaddrs[0] != "/ip4/44.55.66.77/tcp/52031/p2p/12D3KooWCLYWBbiKrj5unB3aqkQy41JjyASS1qMc9nPRuCWu9LHQ" {
		t.Errorf("multiaddr = %q, want refreshed address", peer.Multiaddrs[0])
	}
}

// --- F-025: Heartbeat update field tests ---

func setupHeartbeatTestWithVersion(t *testing.T, vc VersionProvider) (*PeerHandler, *mux.Router, *repository.MemoryPeerAPIKeyRepository) {
	t.Helper()
	peerRepo := repository.NewMemoryPeerRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	presenceStore := presence.NewMemoryPresenceStore(clock.RealClock{})
	t.Cleanup(func() { presenceStore.Close() })
	peerSvc := services.NewPeerService(peerRepo, presenceStore, apiKeyRepo)

	handler := NewPeerHandlerWithTrustBlock(peerSvc, nil)
	handler.SetAPIKeyRepo(apiKeyRepo)
	handler.SetPresenceStore(presenceStore)
	if vc != nil {
		handler.SetVersionChecker(vc)
	}

	r := mux.NewRouter()
	apiRouter := r.PathPrefix("/api/v1/tracker").Subrouter()
	apiRouter.Handle("/heartbeat", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleHeartbeat))).Methods(http.MethodPost)

	return handler, r, apiKeyRepo
}

func TestHandleHeartbeat_IncludesUpdateFields_WhenClientOlder(t *testing.T) {
	vc := &mockVersionProvider{
		manifest: &update.ManifestContent{
			Version:      "1.0.0",
			ReleaseNotes: "Bug fixes and improvements",
		},
	}
	_, router, apiKeyRepo := setupHeartbeatTestWithVersion(t, vc)
	apiKeyRepo.Store("peer-1", "key-1")

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/127.0.0.1/tcp/4001"},
		"client_version": "0.9.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "key-1")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data := resp["data"].(map[string]interface{})
	if data["latest_version"] != "1.0.0" {
		t.Errorf("latest_version = %v, want %q", data["latest_version"], "1.0.0")
	}
	if data["release_notes"] != "Bug fixes and improvements" {
		t.Errorf("release_notes = %v, want %q", data["release_notes"], "Bug fixes and improvements")
	}
}

func TestHandleHeartbeat_OmitsUpdateFields_WhenClientCurrent(t *testing.T) {
	vc := &mockVersionProvider{
		manifest: &update.ManifestContent{
			Version:      "1.0.0",
			ReleaseNotes: "Bug fixes",
		},
	}
	_, router, apiKeyRepo := setupHeartbeatTestWithVersion(t, vc)
	apiKeyRepo.Store("peer-1", "key-1")

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/127.0.0.1/tcp/4001"},
		"client_version": "1.0.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "key-1")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if _, ok := data["latest_version"]; ok {
		t.Error("latest_version should be omitted when client is current")
	}
	if _, ok := data["release_notes"]; ok {
		t.Error("release_notes should be omitted when client is current")
	}
}

func TestHandleHeartbeat_OmitsUpdateFields_WhenClientVersionEmpty(t *testing.T) {
	vc := &mockVersionProvider{
		manifest: &update.ManifestContent{
			Version:      "1.0.0",
			ReleaseNotes: "Bug fixes",
		},
	}
	_, router, apiKeyRepo := setupHeartbeatTestWithVersion(t, vc)
	apiKeyRepo.Store("peer-1", "key-1")

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs": []string{"/ip4/127.0.0.1/tcp/4001"},
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "key-1")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if _, ok := data["latest_version"]; ok {
		t.Error("latest_version should be omitted when client_version is empty")
	}
}

func TestHandleHeartbeat_OmitsUpdateFields_WhenNoVersionChecker(t *testing.T) {
	// nil version checker — no update information available
	_, router, apiKeyRepo := setupHeartbeatTestWithVersion(t, nil)
	apiKeyRepo.Store("peer-1", "key-1")

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/127.0.0.1/tcp/4001"},
		"client_version": "0.9.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "key-1")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if _, ok := data["latest_version"]; ok {
		t.Error("latest_version should be omitted when no version checker is configured")
	}
}

func TestHandleHeartbeat_OmitsUpdateFields_WhenVersionCheckerFails(t *testing.T) {
	vc := &mockVersionProvider{
		err: fmt.Errorf("fetch failed"),
	}
	_, router, apiKeyRepo := setupHeartbeatTestWithVersion(t, vc)
	apiKeyRepo.Store("peer-1", "key-1")

	body, _ := json.Marshal(map[string]interface{}{
		"multiaddrs":     []string{"/ip4/127.0.0.1/tcp/4001"},
		"client_version": "0.9.0",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "key-1")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; heartbeat should succeed even if version checker fails", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if _, ok := data["latest_version"]; ok {
		t.Error("latest_version should be omitted when version checker fails")
	}
}

// F-032: the response carries the public name so a daemon on its placeholder
// can adopt a name that was set tracker-side.
func TestHandleHeartbeat_EchoesStoredDisplayName(t *testing.T) {
	peerRepo := repository.NewMemoryPeerRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	presenceStore := presence.NewMemoryPresenceStore(clock.RealClock{})
	t.Cleanup(func() { presenceStore.Close() })
	peerSvc := services.NewPeerService(peerRepo, presenceStore, apiKeyRepo)

	if err := peerRepo.Upsert(nil, &models.Peer{
		PeerID:      "named-peer",
		PublicKey:   "pubkey-named",
		DisplayName: "lalala_agent",
		FirstSeen:   time.Now().UTC(),
		LastSeen:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	apiKeyRepo.Store("named-peer", "named-key")

	handler := NewPeerHandlerWithTrustBlock(peerSvc, nil)
	handler.SetAPIKeyRepo(apiKeyRepo)
	handler.SetPresenceStore(presenceStore)
	r := mux.NewRouter()
	api := r.PathPrefix("/api/v1/tracker").Subrouter()
	api.Handle("/heartbeat", RequireAPIKey(apiKeyRepo)(http.HandlerFunc(handler.HandleHeartbeat))).Methods(http.MethodPost)

	beat := func(body map[string]interface{}) map[string]interface{} {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/v1/tracker/heartbeat", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", "named-key")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
		}
		var env struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("parse: %v", err)
		}
		return env.Data
	}

	// A daemon placeholder does not overwrite the stored name, and the stored name is echoed.
	if got := beat(map[string]interface{}{"display_name": "swarm_operator_42"})["display_name"]; got != "lalala_agent" {
		t.Errorf("display_name = %v, want lalala_agent", got)
	}
	// An owner-set name replaces it and is echoed back.
	if got := beat(map[string]interface{}{"display_name": "Atlas"})["display_name"]; got != "Atlas" {
		t.Errorf("display_name = %v, want Atlas", got)
	}
	// An explicit clear leaves the field out.
	if _, ok := beat(map[string]interface{}{"display_name": ""})["display_name"]; ok {
		t.Errorf("display_name present after clear")
	}
}
