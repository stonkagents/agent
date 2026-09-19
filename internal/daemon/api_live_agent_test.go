package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveAgentAuthOK(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if !liveAgentAuthOK("", r) {
		t.Fatal("empty secret should allow")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set(headerLiveAgentKey, "abc")
	if liveAgentAuthOK("different", r2) {
		t.Fatal("wrong secret should deny")
	}
	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	r3.Header.Set(headerLiveAgentKey, "matchme")
	if !liveAgentAuthOK("matchme", r3) {
		t.Fatal("matching X-Live-Agent-Key should allow")
	}
	r4 := httptest.NewRequest(http.MethodGet, "/", nil)
	r4.Header.Set("Authorization", "Bearer matchme")
	if !liveAgentAuthOK("matchme", r4) {
		t.Fatal("matching Bearer should allow")
	}
}

func TestHandleLiveAgentDownload_UnauthorizedWhenKeySet(t *testing.T) {
	s := NewServer()
	s.config = &Config{LiveAgentDownloadAPIKey: "expected-secret"}

	body, _ := json.Marshal(map[string]string{"cid": "bafybeigdyrzt5sfp7ug3edqh7ovj5k67lvsfydqkd5qvd4yjiq5jbtwzf"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/live-agent/download", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleLiveAgentDownload(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestHandleLiveAgentDownload_MethodNotAllowed(t *testing.T) {
	s := NewServer()
	s.config = &Config{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/live-agent/download", nil)
	rec := httptest.NewRecorder()
	s.handleLiveAgentDownload(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}
