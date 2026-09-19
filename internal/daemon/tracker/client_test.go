// Package: internal/daemon/tracker
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-07 (Upload Queue & Seeding)
// Purpose: TDD tests for tracker HTTP client

package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClient_Register - RED test
// Acceptance Criterion: Register peer with tracker (POST /api/v1/tracker/register)
func TestClient_Register(t *testing.T) {
	// Arrange - mock tracker server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/tracker/register" {
			t.Errorf("Expected /api/v1/tracker/register path, got %s", r.URL.Path)
		}

		// Parse request body
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)

		// Verify required fields
		if req["peer_id"] == nil || req["ed25519_pubkey"] == nil {
			t.Error("Missing required fields in request")
		}

		// Return success response
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "registered",
		})
	}))
	defer server.Close()

	// Act
	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Register("12D3KooWPeer1", "pubkey", []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWPeer1"})

	// Assert
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}
}

// TestClient_Register_ReturnsAPIKeyOnFirstRegister verifies that when the tracker
// includes api_key in the response (first register), Register returns it.
func TestClient_Register_ReturnsAPIKeyOnFirstRegister(t *testing.T) {
	wantKey := "a1b2c3d4e5f6"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"peer_id":    "12D3KooWPeer1",
			"first_seen": "2025-01-01T00:00:00Z",
			"last_seen":  "2025-01-01T00:00:00Z",
			"api_key":    wantKey,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	apiKey, err := client.Register("12D3KooWPeer1", "pubkey", []string{"/ip4/127.0.0.1/tcp/4001"})
	if err != nil {
		t.Fatalf("Register() failed: %v", err)
	}
	if apiKey != wantKey {
		t.Errorf("Register() api_key = %q, want %q", apiKey, wantKey)
	}
}

// TestClient_Announce - RED test
// Acceptance Criterion: Announce file availability (POST /api/v1/tracker/announce)
func TestClient_Announce(t *testing.T) {
	// Arrange - mock tracker server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/tracker/announce" {
			t.Errorf("Expected /api/v1/tracker/announce path, got %s", r.URL.Path)
		}

		// Parse request
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)

		// Verify required fields
		if req["peer_id"] == nil {
			t.Error("Missing peer_id")
		}
		if req["cid"] == nil || req["chunks"] == nil {
			t.Error("Missing cid or chunks")
		}

		// Return success
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "announced"})
	}))
	defer server.Close()

	// Act
	client := NewClient(server.URL, "12D3KooWPeer1")
	err := client.Announce("bafybeigdyrzt5sfp123", "file.txt", "text/plain", "raw", 12, []int{0, 1, 2, 3})

	// Assert
	if err != nil {
		t.Fatalf("Announce() failed: %v", err)
	}
}

// TestClient_SearchByCID - RED test
// Acceptance Criterion: Search for peers by file CID (GET /api/v1/tracker/assets/{cid}/peers)
func TestClient_SearchByCID(t *testing.T) {
	// Arrange - mock tracker server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/tracker/assets/bafybeigdyrzt5sfp123/peers" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
		}

		// Return peer list
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"peer_id": "12D3KooWPeer2", "chunks": []int{0, 1, 2, 3}, "multiaddrs": []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWPeer2"}},
			},
			"total": 1,
		})
	}))
	defer server.Close()

	// Act
	client := NewClient(server.URL, "12D3KooWPeer1")
	peers, err := client.SearchByCID("bafybeigdyrzt5sfp123")

	// Assert
	if err != nil {
		t.Fatalf("SearchByCID() failed: %v", err)
	}

	if len(peers) != 1 {
		t.Errorf("Expected 1 peer, got %d", len(peers))
	}

	if peers[0].PeerID != "12D3KooWPeer2" {
		t.Errorf("Expected peer 12D3KooWPeer2, got %s", peers[0].PeerID)
	}
}

// TestClient_RetryLogic - RED test
// Acceptance Criterion: Retry failed requests with exponential backoff (max 3 retries)
func TestClient_RetryLogic(t *testing.T) {
	// Arrange - server that fails twice then succeeds
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Fail first 2 attempts
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Succeed on 3rd attempt
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
	}))
	defer server.Close()

	// Act
	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Register("12D3KooWPeer1", "pubkey", []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWPeer1"})

	// Assert
	if err != nil {
		t.Fatalf("Register() failed after retries: %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts (2 retries), got %d", attempts)
	}
}

// TestClient_MaxRetriesExceeded - RED test
// Acceptance Criterion: Give up after 3 retries
func TestClient_MaxRetriesExceeded(t *testing.T) {
	// Arrange - server that always fails
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Act
	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Register("12D3KooWPeer1", "pubkey", []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWPeer1"})

	// Assert - should fail after max retries
	if err == nil {
		t.Error("Register() should fail after max retries exceeded")
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts (max retries), got %d", attempts)
	}
}
