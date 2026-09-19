// Package daemon: tests for POST /api/v1/agent/chat (agent-to-agent communication via LLM).

package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleAgentChat_RequiresPOST(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/chat", nil)
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestHandleAgentChat_InvalidJSON(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	code := getErrorCode(t, errBody)
	if code != "INVALID_REQUEST" {
		t.Errorf("expected code INVALID_REQUEST, got %v", code)
	}
}

func getErrorCode(t *testing.T, errBody map[string]interface{}) string {
	t.Helper()
	errObj, ok := errBody["error"].(map[string]interface{})
	if !ok {
		return ""
	}
	c, _ := errObj["code"].(string)
	return c
}

func TestHandleAgentChat_MissingMessages(t *testing.T) {
	server := NewServer()
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if getErrorCode(t, errBody) != "MISSING_MESSAGES" {
		t.Errorf("expected code MISSING_MESSAGES, got %v", getErrorCode(t, errBody))
	}
}

func TestHandleAgentChat_EmptyMessagesArray(t *testing.T) {
	server := NewServer()
	body := []byte(`{"messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if getErrorCode(t, errBody) != "MISSING_MESSAGES" {
		t.Errorf("expected code MISSING_MESSAGES, got %v", getErrorCode(t, errBody))
	}
}

func TestHandleAgentChat_TooManyMessages(t *testing.T) {
	server := NewServer()
	msgs := make([]AgentChatMessage, maxAgentChatMessages+1)
	for i := range msgs {
		msgs[i] = AgentChatMessage{Role: "user", Content: "hi"}
	}
	payload := AgentChatRequest{Messages: msgs}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if getErrorCode(t, errBody) != "TOO_MANY_MESSAGES" {
		t.Errorf("expected code TOO_MANY_MESSAGES, got %v", getErrorCode(t, errBody))
	}
}

func TestHandleAgentChat_AllEmptyContent(t *testing.T) {
	server := NewServer()
	body := []byte(`{"messages":[{"role":"user","content":"  "},{"role":"assistant","content":""}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if getErrorCode(t, errBody) != "MISSING_CONTENT" {
		t.Errorf("expected code MISSING_CONTENT, got %v", getErrorCode(t, errBody))
	}
}

// TestHandleAgentChat_AskUseTrackerRequiresAPIKey verifies the local gateway is not used when ask_use_tracker is on but no tracker API key is loaded.
func TestHandleAgentChat_AskUseTrackerRequiresAPIKey(t *testing.T) {
	mockGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("gateway must not be called when ask_use_tracker is set and tracker API key is missing")
	}))
	defer mockGateway.Close()

	server := NewServer()
	server.config.AskUseTracker = true
	server.config.TrackerURL = "http://127.0.0.1:1"
	server.config.GatewayURL = mockGateway.URL

	body := []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if getErrorCode(t, errBody) != "NOT_REGISTERED" {
		t.Fatalf("expected NOT_REGISTERED, got %v", getErrorCode(t, errBody))
	}
}

// TestHandleAgentChat_NotConfigured verifies 503 when no gateway and no tracker (default server).
func TestHandleAgentChat_NotConfigured(t *testing.T) {
	server := NewServer()
	body := []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if getErrorCode(t, errBody) != "ASK_NOT_CONFIGURED" {
		t.Errorf("expected code ASK_NOT_CONFIGURED, got %v", getErrorCode(t, errBody))
	}
}

func TestBuildLLMMessages_SystemPromptAndMessages(t *testing.T) {
	req := AgentChatRequest{
		SystemPrompt: "You are a mediator.",
		Messages: []AgentChatMessage{
			{Role: "user", Content: "Hi", AgentID: "agent_a"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "Bye", AgentID: "agent_b"},
		},
	}
	out := buildLLMMessages(req)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages (1 system + 3 conv), got %d", len(out))
	}
	if out[0]["role"] != "system" || out[0]["content"] != "You are a mediator." {
		t.Errorf("first message should be system: %v", out[0])
	}
	if out[1]["content"] != "[agent_a]: Hi" {
		t.Errorf("agent_id should be prefixed: %v", out[1])
	}
	if out[2]["content"] != "Hello!" {
		t.Errorf("no agent_id: %v", out[2])
	}
	if out[3]["content"] != "[agent_b]: Bye" {
		t.Errorf("agent_id should be prefixed: %v", out[3])
	}
}

func TestBuildLLMMessages_InvalidRoleDefaultsToUser(t *testing.T) {
	req := AgentChatRequest{
		Messages: []AgentChatMessage{
			{Role: "unknown", Content: "x"},
		},
	}
	out := buildLLMMessages(req)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0]["role"] != "user" {
		t.Errorf("invalid role should default to user, got %q", out[0]["role"])
	}
}

func TestBuildLLMMessages_SkipsEmptyContent(t *testing.T) {
	req := AgentChatRequest{
		Messages: []AgentChatMessage{
			{Role: "user", Content: ""},
			{Role: "user", Content: "only this"},
		},
	}
	out := buildLLMMessages(req)
	if len(out) != 1 {
		t.Fatalf("expected 1 message (empty skipped), got %d", len(out))
	}
	if out[0]["content"] != "only this" {
		t.Errorf("unexpected content: %v", out[0])
	}
}

// TestHandleAgentChat_WithPersistence verifies session creation and persistence when agentChatRepo is set and gateway responds.
func TestHandleAgentChat_WithPersistence(t *testing.T) {
	mockContent := "Mock LLM reply"
	mockGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": mockContent}}},
		})
	}))
	defer mockGateway.Close()

	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("InitializeManagers: %v", err)
	}
	defer server.Shutdown()
	server.config.GatewayURL = mockGateway.URL

	body := []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var out AgentChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Response != mockContent {
		t.Errorf("response: got %q", out.Response)
	}
	if out.SessionID == "" {
		t.Error("expected non-empty session_id when persistence enabled")
	}
	// Persisted: 1 user message + 1 assistant message
	if server.agentChatRepo != nil {
		msgs, err := server.agentChatRepo.ListMessages(out.SessionID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if len(msgs) != 2 {
			t.Errorf("expected 2 messages in session, got %d", len(msgs))
		}
	}
}

// TestHandleAgentChat_PersonalStableSession verifies two POSTs with user_id and no session_id/context_id share one session and accumulate history.
func TestHandleAgentChat_PersonalStableSession(t *testing.T) {
	mockContent := "Mock reply"
	mockGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": mockContent}}},
		})
	}))
	defer mockGateway.Close()

	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("InitializeManagers: %v", err)
	}
	defer server.Shutdown()
	server.config.GatewayURL = mockGateway.URL

	wallet := "SolWallet111PersonalStable"
	post := func(userContent string) AgentChatResponse {
		t.Helper()
		body, _ := json.Marshal(map[string]interface{}{
			"user_id":  wallet,
			"messages": []map[string]string{{"role": "user", "content": userContent}},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.handleAgentChat(w, req)
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var out AgentChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	first := post("First turn")
	second := post("Second turn")
	if first.SessionID == "" || first.SessionID != second.SessionID {
		t.Fatalf("expected same non-empty session_id, got %q and %q", first.SessionID, second.SessionID)
	}
	if server.agentChatRepo == nil {
		t.Fatal("expected agentChatRepo")
	}
	msgs, err := server.agentChatRepo.ListMessages(first.SessionID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (2 user + 2 assistant), got %d", len(msgs))
	}
}

// TestHandleAgentChat_UnknownSessionID returns 404 when session_id is provided but not found.
func TestHandleAgentChat_UnknownSessionID(t *testing.T) {
	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("InitializeManagers: %v", err)
	}
	defer server.Shutdown()
	// No gateway/tracker so we'll fail later; but we first resolve session and should get 404 for unknown id
	server.config.GatewayURL = "http://invalid.local"
	body := []byte(`{"session_id":"nonexistent-session-id","messages":[{"role":"user","content":"Hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleAgentChat(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown session_id, got %d", resp.StatusCode)
	}
}

func TestEnsureAgentChatSession_UserTokenMappingIsStable(t *testing.T) {
	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("InitializeManagers: %v", err)
	}
	defer server.Shutdown()

	req := &AgentChatRequest{
		UserID:    "holder-wallet-1",
		ContextID: "token-address-1",
	}
	first := server.ensureAgentChatSession("", req)
	second := server.ensureAgentChatSession("", req)
	if first == "" || second == "" {
		t.Fatal("expected non-empty mapped session IDs")
	}
	if first != second {
		t.Fatalf("expected stable mapped session id, got %q and %q", first, second)
	}
}

func TestHandleAgentChatSessions_NotAvailableWhenNoRepo(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/chat/sessions?user_id=test-wallet", nil)
	w := httptest.NewRecorder()
	server.handleAgentChatSessions(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when agent chat DB not initialized, got %d", resp.StatusCode)
	}
	var errBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if getErrorCode(t, errBody) != "NOT_AVAILABLE" {
		t.Fatalf("expected NOT_AVAILABLE, got %v", getErrorCode(t, errBody))
	}
}
