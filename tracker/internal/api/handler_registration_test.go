// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: Tests for challenge-response registration HTTP handlers

package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

func newRegistrationTestRouter(t *testing.T) (*mux.Router, *clock.MockClock) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := presence.NewMemoryPresenceStore(clk)
	regSvc := services.NewRegistrationService(services.RegistrationServiceDeps{
		Accounts: repository.NewMemoryAccountRepository(),
		Credits:  repository.NewMemoryCreditRepository(),
		Nonces:   repository.NewMemoryNonceRepository(),
		Blocks:   repository.NewMemoryBlockRepository(),
		Peers:    repository.NewMemoryPeerRepository(),
		APIKeys:  repository.NewMemoryPeerAPIKeyRepository(),
		Presence: store,
		Clock:    clk,
	})
	handler := NewRegistrationHandler(regSvc)
	r := mux.NewRouter()
	r.HandleFunc("/api/v1/tracker/challenge", handler.HandleChallenge).Methods(http.MethodPost)
	r.HandleFunc("/api/v1/tracker/register", handler.HandleRegister).Methods(http.MethodPost)
	return r, clk
}

func TestHandleChallenge_Success(t *testing.T) {
	router, _ := newRegistrationTestRouter(t)

	body, _ := json.Marshal(ChallengeDTO{PeerID: "12D3KooWTestPeer"})
	req := httptest.NewRequest("POST", "/api/v1/tracker/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleChallenge() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ChallengeResponseDTO
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Nonce == "" {
		t.Error("HandleChallenge() nonce is empty")
	}
	if resp.ExpiresIn != 300 {
		t.Errorf("HandleChallenge() ExpiresIn = %d, want 300", resp.ExpiresIn)
	}
}

func TestHandleChallenge_MissingPeerID(t *testing.T) {
	router, _ := newRegistrationTestRouter(t)

	body, _ := json.Marshal(ChallengeDTO{PeerID: ""})
	req := httptest.NewRequest("POST", "/api/v1/tracker/challenge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleChallenge() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_ChallengeResponse_Success(t *testing.T) {
	router, _ := newRegistrationTestRouter(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Step 1: Challenge
	challengeBody, _ := json.Marshal(ChallengeDTO{PeerID: "peer-test"})
	challengeReq := httptest.NewRequest("POST", "/api/v1/tracker/challenge", bytes.NewReader(challengeBody))
	challengeReq.Header.Set("Content-Type", "application/json")
	challengeReq.RemoteAddr = "1.2.3.4:12345"
	challengeW := httptest.NewRecorder()
	router.ServeHTTP(challengeW, challengeReq)

	var challengeResp ChallengeResponseDTO
	_ = json.Unmarshal(challengeW.Body.Bytes(), &challengeResp)

	// Decode nonce
	nonce, _ := base64.StdEncoding.DecodeString(challengeResp.Nonce)

	// Sign with domain prefix
	message := []byte("stonkagents-register-v1:" + challengeResp.Nonce)
	sig := ed25519.Sign(priv, message)

	// Step 2: Register
	regBody, _ := json.Marshal(RegisterChallengeDTO{
		PeerID:        "peer-test",
		Ed25519Pubkey: base64.StdEncoding.EncodeToString(pub),
		Signature:     base64.StdEncoding.EncodeToString(sig),
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
	})
	_ = nonce // used indirectly via challengeResp.Nonce
	regReq := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regReq.RemoteAddr = "1.2.3.4:12345"
	regW := httptest.NewRecorder()
	router.ServeHTTP(regW, regReq)

	if regW.Code != http.StatusCreated {
		t.Fatalf("HandleRegister() status = %d, want %d, body: %s", regW.Code, http.StatusCreated, regW.Body.String())
	}

	var regResp RegisterResponseDTO
	_ = json.Unmarshal(regW.Body.Bytes(), &regResp)
	if regResp.APIKey == "" {
		t.Error("HandleRegister() api_key is empty")
	}
	if regResp.AccountID == "" {
		t.Error("HandleRegister() account_id is empty")
	}
	if regResp.Credits.Free != 150 {
		t.Errorf("HandleRegister() credits.free = %d, want 150", regResp.Credits.Free)
	}
}

func TestHandleRegister_InvalidSignature(t *testing.T) {
	router, _ := newRegistrationTestRouter(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Challenge
	challengeBody, _ := json.Marshal(ChallengeDTO{PeerID: "peer-bad"})
	challengeReq := httptest.NewRequest("POST", "/api/v1/tracker/challenge", bytes.NewReader(challengeBody))
	challengeReq.Header.Set("Content-Type", "application/json")
	challengeReq.RemoteAddr = "1.2.3.4:12345"
	challengeW := httptest.NewRecorder()
	router.ServeHTTP(challengeW, challengeReq)

	// Register with wrong signature
	regBody, _ := json.Marshal(RegisterChallengeDTO{
		PeerID:        "peer-bad",
		Ed25519Pubkey: base64.StdEncoding.EncodeToString(pub),
		Signature:     base64.StdEncoding.EncodeToString(make([]byte, 64)),
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
	})
	regReq := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regReq.RemoteAddr = "1.2.3.4:12345"
	regW := httptest.NewRecorder()
	router.ServeHTTP(regW, regReq)

	if regW.Code != http.StatusUnauthorized {
		t.Errorf("HandleRegister() status = %d, want %d, body: %s", regW.Code, http.StatusUnauthorized, regW.Body.String())
	}
}

func TestSanitizeDisplayName_StripsTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "Alice", "Alice"},
		{"HTML tags stripped", "<script>alert('xss')</script>", "scriptalert('xss')/script"},
		{"angle brackets removed", "a<b>c", "abc"},
		{"whitespace trimmed", "  Alice  ", "Alice"},
		{"empty after sanitize", "   ", ""},
		{"truncated to 50 chars", strings.Repeat("a", 60), strings.Repeat("a", 50)},
		{"exactly 50 chars", strings.Repeat("b", 50), strings.Repeat("b", 50)},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeDisplayName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeDisplayName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHandleRegister_NoChallenge(t *testing.T) {
	router, _ := newRegistrationTestRouter(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Register without challenge
	fakeNonce := make([]byte, 32)
	message := []byte("stonkagents-register-v1:" + base64.StdEncoding.EncodeToString(fakeNonce))
	sig := ed25519.Sign(priv, message)

	regBody, _ := json.Marshal(RegisterChallengeDTO{
		PeerID:        "peer-nochallenge",
		Ed25519Pubkey: base64.StdEncoding.EncodeToString(pub),
		Signature:     base64.StdEncoding.EncodeToString(sig),
		Multiaddrs:    []string{"/ip4/1.2.3.4/tcp/4001"},
		ClientVersion: "0.1.0",
	})
	regReq := httptest.NewRequest("POST", "/api/v1/tracker/register", bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regReq.RemoteAddr = "1.2.3.4:12345"
	regW := httptest.NewRecorder()
	router.ServeHTTP(regW, regReq)

	if regW.Code != http.StatusUnauthorized {
		t.Errorf("HandleRegister() no challenge status = %d, want %d, body: %s", regW.Code, http.StatusUnauthorized, regW.Body.String())
	}
}
