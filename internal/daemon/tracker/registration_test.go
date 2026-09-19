// Package: internal/daemon/tracker
// Feature: sprint-02
// Story: TD-016 (Daemon registration migration)
// Purpose: Tests for challenge-response registration and authenticated heartbeat client methods

package tracker

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// SignChallenge tests
// ---------------------------------------------------------------------------

func TestSignChallenge_ValidKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	privB64 := base64.StdEncoding.EncodeToString(priv)
	nonceB64 := base64.StdEncoding.EncodeToString([]byte("test-nonce-32-bytes-padding12345"))

	pubB64, sigB64, err := SignChallenge(privB64, nonceB64)
	if err != nil {
		t.Fatalf("SignChallenge() error = %v", err)
	}

	// Verify returned public key matches generated one
	gotPub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("decode pubkey: %v", err)
	}
	if !pub.Equal(ed25519.PublicKey(gotPub)) {
		t.Error("returned public key does not match generated key")
	}

	// Verify signature is valid (same verification as tracker)
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	message := []byte(signDomainPrefix + nonceB64)
	if !ed25519.Verify(pub, message, sig) {
		t.Error("signature verification failed — tracker would reject this")
	}
}

func TestSignChallenge_InvalidBase64Key(t *testing.T) {
	_, _, err := SignChallenge("not-valid-base64!!!", "dGVzdA==")
	if err == nil {
		t.Error("expected error for invalid base64 key")
	}
}

func TestSignChallenge_WrongKeySize(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	_, _, err := SignChallenge(shortKey, "dGVzdA==")
	if err == nil {
		t.Error("expected error for wrong key size (32 instead of 64)")
	}
}

func TestSignChallenge_DomainPrefixMatchesTracker(t *testing.T) {
	// Ensure our domain prefix matches the tracker's SignDomainPrefix constant.
	// If the tracker ever changes this, the test will fail and signal a mismatch.
	if signDomainPrefix != "stonkagents-register-v1:" {
		t.Errorf("signDomainPrefix = %q, want %q", signDomainPrefix, "stonkagents-register-v1:")
	}
}

// ---------------------------------------------------------------------------
// Client.Challenge tests
// ---------------------------------------------------------------------------

func TestClient_Challenge_Success(t *testing.T) {
	wantNonce := base64.StdEncoding.EncodeToString([]byte("random-nonce-data"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/tracker/challenge" {
			t.Errorf("expected /api/v1/tracker/challenge, got %s", r.URL.Path)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["peer_id"] != "12D3KooWPeer1" {
			t.Errorf("peer_id = %q, want %q", body["peer_id"], "12D3KooWPeer1")
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"nonce":      wantNonce,
			"expires_in": 300,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	resp, err := client.Challenge("12D3KooWPeer1")
	if err != nil {
		t.Fatalf("Challenge() error = %v", err)
	}
	if resp.Nonce != wantNonce {
		t.Errorf("Nonce = %q, want %q", resp.Nonce, wantNonce)
	}
	if resp.ExpiresIn != 300 {
		t.Errorf("ExpiresIn = %d, want 300", resp.ExpiresIn)
	}
}

func TestClient_Challenge_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"code":"INTERNAL_ERROR","message":"db down"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Challenge("12D3KooWPeer1")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

// ---------------------------------------------------------------------------
// Client.RegisterIdentity tests
// ---------------------------------------------------------------------------

func TestClient_RegisterIdentity_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/tracker/register/identity" {
			t.Errorf("expected /api/v1/tracker/register/identity, got %s", r.URL.Path)
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		for _, required := range []string{"peer_id", "ed25519_pubkey", "signature", "client_version"} {
			if body[required] == nil {
				t.Errorf("missing required field: %s", required)
			}
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"api_key":    "key-abc123",
			"account_id": "acct-xyz",
			"credits":    map[string]int{"free": 50, "paid": 0},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	resp, err := client.RegisterIdentity(
		"12D3KooWPeer1", "pubB64", "sigB64",
		[]string{"/ip4/127.0.0.1/tcp/4001"}, "1.0.0", "",
	)
	if err != nil {
		t.Fatalf("RegisterIdentity() error = %v", err)
	}
	if resp.APIKey != "key-abc123" {
		t.Errorf("APIKey = %q, want %q", resp.APIKey, "key-abc123")
	}
	if resp.AccountID != "acct-xyz" {
		t.Errorf("AccountID = %q, want %q", resp.AccountID, "acct-xyz")
	}
	if resp.Credits.Free != 50 {
		t.Errorf("Credits.Free = %d, want 50", resp.Credits.Free)
	}
	if resp.Credits.Paid != 0 {
		t.Errorf("Credits.Paid = %d, want 0", resp.Credits.Paid)
	}
}

func TestClient_RegisterIdentity_ReRegistration(t *testing.T) {
	// Re-registration: tracker returns no API key (existing account)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"account_id": "acct-xyz",
			"credits":    map[string]int{"free": 250, "paid": 100},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	resp, err := client.RegisterIdentity(
		"12D3KooWPeer1", "pubB64", "sigB64",
		[]string{"/ip4/127.0.0.1/tcp/4001"}, "1.0.0", "",
	)
	if err != nil {
		t.Fatalf("RegisterIdentity() error = %v", err)
	}
	if resp.APIKey != "" {
		t.Errorf("APIKey = %q, want empty (re-registration)", resp.APIKey)
	}
	if resp.Credits.Free != 250 || resp.Credits.Paid != 100 {
		t.Errorf("Credits = {%d, %d}, want {250, 100}", resp.Credits.Free, resp.Credits.Paid)
	}
}

func TestClient_RegisterIdentity_InvalidSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INVALID_SIGNATURE", "message": "Ed25519 signature verification failed"},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.RegisterIdentity("12D3KooWPeer1", "bad", "bad", nil, "", "")
	if err == nil {
		t.Error("expected error for 401 response")
	}
}

// ---------------------------------------------------------------------------
// Client.Heartbeat tests
// ---------------------------------------------------------------------------

func TestClient_Heartbeat_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/tracker/heartbeat" {
			t.Errorf("expected /api/v1/tracker/heartbeat, got %s", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-api-key" {
			t.Errorf("X-API-Key = %q, want %q", got, "test-api-key")
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_version"] != "1.0.0" {
			t.Errorf("client_version = %v, want 1.0.0", body["client_version"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"status":                 "ok",
				"next_heartbeat_seconds": 120,
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	resp, err := client.Heartbeat("test-api-key", []string{"/ip4/127.0.0.1/tcp/4001"}, "1.0.0", "", nil)
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if resp == nil {
		t.Fatal("Heartbeat() returned nil response")
	}
	if resp.LatestVersion != "" {
		t.Errorf("LatestVersion = %q, want empty (no update)", resp.LatestVersion)
	}
}

func TestClient_Heartbeat_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "UNAUTHORIZED", "message": "API key required"},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Heartbeat("bad-key", nil, "", "", nil)
	if err == nil {
		t.Error("expected error for 401 response")
	}
}

func TestClient_Heartbeat_SendsMultiaddrs(t *testing.T) {
	wantAddrs := []string{"/ip4/192.168.1.1/tcp/4001", "/ip4/10.0.0.1/tcp/4001"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		addrs, ok := body["multiaddrs"].([]interface{})
		if !ok || len(addrs) != 2 {
			t.Errorf("multiaddrs length = %d, want 2", len(addrs))
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"status": "ok", "next_heartbeat_seconds": 120},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Heartbeat("key", wantAddrs, "", "", nil)
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
}

// TestClient_SetDisplayName_AlwaysSendsField covers the identity push: display_name
// is present even when empty (that is how the tracker learns to clear it), the API
// key header is set, and no multiaddrs are sent so the tracker keeps the stored ones.
func TestClient_SetDisplayName_AlwaysSendsField(t *testing.T) {
	var bodies []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tracker/heartbeat" || r.Header.Get("X-API-Key") != "key-1" {
			t.Errorf("unexpected request %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("X-API-Key"))
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"status": "ok"}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	if err := client.SetDisplayName("key-1", "2.2.2", "Atlas"); err != nil {
		t.Fatalf("SetDisplayName() error = %v", err)
	}
	if err := client.SetDisplayName("key-1", "2.2.2", ""); err != nil {
		t.Fatalf("SetDisplayName(clear) error = %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if bodies[0]["display_name"] != "Atlas" || bodies[0]["client_version"] != "2.2.2" {
		t.Errorf("set body = %v", bodies[0])
	}
	if v, ok := bodies[1]["display_name"]; !ok || v != "" {
		t.Errorf("clear body must carry display_name \"\": %v", bodies[1])
	}
	for i, b := range bodies {
		if _, has := b["multiaddrs"]; has {
			t.Errorf("body %d sent multiaddrs: %v", i, b)
		}
	}
}

// --- F-025: Heartbeat response parsing tests ---

func TestClient_Heartbeat_ParsesUpdateFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"status":                 "ok",
				"next_heartbeat_seconds": 120,
				"latest_version":         "1.2.0",
				"release_notes":          "Security fixes and performance improvements",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	resp, err := client.Heartbeat("key-123", []string{"/ip4/127.0.0.1/tcp/4001"}, "1.0.0", "", nil)
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if resp.LatestVersion != "1.2.0" {
		t.Errorf("LatestVersion = %q, want %q", resp.LatestVersion, "1.2.0")
	}
	if resp.ReleaseNotes != "Security fixes and performance improvements" {
		t.Errorf("ReleaseNotes = %q, want %q", resp.ReleaseNotes, "Security fixes and performance improvements")
	}
}

// --- F-032: display_name in registration + heartbeat ---

func TestClient_RegisterIdentity_SendsDisplayName(t *testing.T) {
	var gotDisplayName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if dn, ok := body["display_name"].(string); ok {
			gotDisplayName = dn
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"account_id": "acct-xyz",
			"credits":    map[string]int{"free": 50, "paid": 0},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.RegisterIdentity(
		"12D3KooWPeer1", "pubB64", "sigB64",
		[]string{"/ip4/127.0.0.1/tcp/4001"}, "1.0.0", "MyPeer42",
	)
	if err != nil {
		t.Fatalf("RegisterIdentity() error = %v", err)
	}
	if gotDisplayName != "MyPeer42" {
		t.Errorf("display_name = %q, want %q", gotDisplayName, "MyPeer42")
	}
}

func TestClient_Heartbeat_SendsDisplayName(t *testing.T) {
	var gotDisplayName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if dn, ok := body["display_name"].(string); ok {
			gotDisplayName = dn
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"status": "ok", "next_heartbeat_seconds": 120},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	_, err := client.Heartbeat("test-key", []string{"/ip4/127.0.0.1/tcp/4001"}, "1.0.0", "MyPeer42", nil)
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if gotDisplayName != "MyPeer42" {
		t.Errorf("display_name = %q, want %q", gotDisplayName, "MyPeer42")
	}
}

func TestClient_Heartbeat_OmitsUpdateFieldsWhenAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"status":                 "ok",
				"next_heartbeat_seconds": 120,
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	resp, err := client.Heartbeat("key-123", nil, "1.0.0", "", nil)
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if resp.LatestVersion != "" {
		t.Errorf("LatestVersion = %q, want empty", resp.LatestVersion)
	}
	if resp.ReleaseNotes != "" {
		t.Errorf("ReleaseNotes = %q, want empty", resp.ReleaseNotes)
	}
}

func TestClient_Heartbeat_AutopilotCategories(t *testing.T) {
	var bodies []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"status": "ok", "next_heartbeat_seconds": 120},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "12D3KooWPeer1")
	if _, err := client.Heartbeat("key", nil, "1.0.0", "", []string{"request", "general"}); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if _, err := client.Heartbeat("key", nil, "1.0.0", "", []string{}); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if _, err := client.Heartbeat("key", nil, "1.0.0", "", nil); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	cats, ok := bodies[0]["autopilot_categories"].([]interface{})
	if !ok || len(cats) != 2 || cats[0] != "request" || cats[1] != "general" {
		t.Errorf("autopilot_categories = %v", bodies[0]["autopilot_categories"])
	}
	if cats, ok := bodies[1]["autopilot_categories"].([]interface{}); !ok || len(cats) != 0 {
		t.Errorf("empty list must be sent as []: %v", bodies[1])
	}
	if _, has := bodies[2]["autopilot_categories"]; has {
		t.Errorf("nil must omit the field: %v", bodies[2])
	}
}
