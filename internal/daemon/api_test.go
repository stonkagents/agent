// Package: internal/daemon
// Feature: F-010 (Go Core Daemon), F-029 (Transfer Page E2E)
// Story: US-010-03, US-029-04, US-029-06, US-029-07, US-029-08, US-029-09, US-029-26
// Purpose: TDD tests for REST API endpoints

package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/history"
	"github.com/stonkagents/agent/internal/daemon/stats"
	"github.com/stonkagents/agent/internal/daemon/storage"
	"github.com/stonkagents/agent/internal/daemon/tracker"
)

// setupTestDownloadManager creates a download.Manager backed by a temp dir.
func setupTestDownloadManager(t *testing.T) *download.Manager {
	t.Helper()
	return download.NewManager(t.TempDir(), 3)
}

// TestShareEndpointAcceptsManifest - DEPRECATED
// Note: handleShare now accepts multipart/form-data file uploads instead of JSON
// See api_share_integration_test.go for updated integration tests
func TestShareEndpointAcceptsManifest(t *testing.T) {
	t.Skip("Deprecated: handleShare now accepts multipart/form-data. See api_share_integration_test.go")
}

// TestSearchEndpointReturnsResults - RED test
// Acceptance Criterion: GET /api/v1/search returns asset search results
func TestSearchEndpointReturnsResults(t *testing.T) {
	server := NewServer()
	err := server.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	req, _ := http.NewRequest("GET", "http://127.0.0.1:7841/api/v1/search?q=test&limit=10", nil)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Assert
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Should return data array (Audit C1: aligned with SDK expected shape)
	if response["data"] == nil {
		t.Error("Response should contain 'data' field")
	}
}

// TestDownloadEndpointAcceptsCID - RED test
// Acceptance Criterion: POST /api/v1/download initiates asset download
func TestDownloadEndpointAcceptsCID(t *testing.T) {
	// SKIP: Integration test requires running tracker (tracker client verified working)
	t.Skip("Requires tracker at localhost:7842 - tracker integration verified")

	server := NewServer()
	server.config.DataDir = t.TempDir()

	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	downloadRequest := map[string]interface{}{
		"cid": "bafkreibta6xflzzucvm2kprttjkx4uiypwka26fyzcsedrihxwdg6le5fy",
	}
	body, _ := json.Marshal(downloadRequest)
	req, _ := http.NewRequest("POST", "http://127.0.0.1:7841/api/v1/download", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	// Act
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Assert
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Errorf("Expected status 200 or 202, got %d", resp.StatusCode)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Should return download status or message
	if response["status"] == nil && response["message"] == nil {
		t.Error("Response should contain 'status' or 'message' field")
	}
}

// TestStatusEndpointReturnsSystemStatus - RED test
// Acceptance Criterion: GET /api/v1/status returns daemon status
func TestStatusEndpointReturnsSystemStatus(t *testing.T) {
	server := NewServer()
	err := server.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	req, _ := http.NewRequest("GET", "http://127.0.0.1:7841/api/v1/status", nil)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Assert
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Should return daemon status fields
	if response["daemon"] == nil {
		t.Error("Response should contain 'daemon' field")
	}
}

// TestAPI_NoAuthRequired - Status works without Authorization (BitTorrent-style: no login)
func TestAPI_NoAuthRequired(t *testing.T) {
	server := NewServer()
	err := server.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	req, _ := http.NewRequest("GET", "http://127.0.0.1:7841/api/v1/status", nil)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 without auth, got %d", resp.StatusCode)
	}
}

// TestHandleSearch_ResponseShape verifies the search endpoint returns
// the correct JSON envelope matching the SDK's expected shape:
// { "data": [...], "total": N, "page": 1, "pageSize": 20 }
// Audit item C1: Fix API Contract Mismatch
func TestHandleSearch_ResponseShape(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest("GET", "/api/v1/search?q=test&limit=10", nil)
	w := httptest.NewRecorder()

	server.handleSearch(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Assert - status 200
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	// Decode response
	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Verify SDK-expected fields exist
	if _, ok := response["data"]; !ok {
		t.Error("Response must contain 'data' field (SDK expects 'data', not 'results')")
	}
	if _, ok := response["total"]; !ok {
		t.Error("Response must contain 'total' field")
	}
	if _, ok := response["page"]; !ok {
		t.Error("Response must contain 'page' field")
	}
	if _, ok := response["pageSize"]; !ok {
		t.Error("Response must contain 'pageSize' field")
	}

	// Verify 'data' is an array
	if data, ok := response["data"]; ok {
		if _, isArray := data.([]interface{}); !isArray {
			t.Error("'data' field must be an array")
		}
	}

	// Verify 'page' defaults to 1
	if page, ok := response["page"]; ok {
		if pageNum, isNum := page.(float64); isNum {
			if pageNum != 1 {
				t.Errorf("Expected page=1 (default), got %v", pageNum)
			}
		}
	}

	// Verify 'pageSize' defaults to 20
	if pageSize, ok := response["pageSize"]; ok {
		if ps, isNum := pageSize.(float64); isNum {
			if ps != 20 {
				t.Errorf("Expected pageSize=20 (default), got %v", ps)
			}
		}
	}

	// Verify old 'results' field is NOT present (breaking change from old shape)
	if _, ok := response["results"]; ok {
		t.Error("Response must NOT contain deprecated 'results' field — use 'data' instead")
	}
}

// TestHandleSearch_ResponseShapeWithPagination verifies custom page/pageSize params
func TestHandleSearch_ResponseShapeWithPagination(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest("GET", "/api/v1/search?q=test&page=2&pageSize=5", nil)
	w := httptest.NewRecorder()

	server.handleSearch(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Assert page and pageSize are echoed back
	if page, ok := response["page"].(float64); !ok || page != 2 {
		t.Errorf("Expected page=2, got %v", response["page"])
	}
	if pageSize, ok := response["pageSize"].(float64); !ok || pageSize != 5 {
		t.Errorf("Expected pageSize=5, got %v", response["pageSize"])
	}
}

// TestAPIErrorResponseFormat - RED test (should pass immediately)
// Acceptance Criterion: Consistent error envelope
func TestAPIErrorResponseFormat(t *testing.T) {
	server := NewServer()
	err := server.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	body := []byte("invalid json")
	req, _ := http.NewRequest("POST", "http://127.0.0.1:7841/api/v1/share", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Assert - should be 400 Bad Request
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", resp.StatusCode)
	}

	// Check error envelope format
	var errorResp struct {
		Error struct {
			Code    string      `json:"code"`
			Message string      `json:"message"`
			Details interface{} `json:"details"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&errorResp); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	if errorResp.Error.Code == "" {
		t.Error("Error should have 'code' field")
	}
	if errorResp.Error.Message == "" {
		t.Error("Error should have 'message' field")
	}
}

// === F-025: Task A.4b — Update info in status ===

// GET /api/v1/status carries tracker_url and environment so the portal can
// spot an agent that belongs to another environment (installer 2.4.0).
func TestStatus_TrackerURLAndEnvironment(t *testing.T) {
	s := &Server{
		config:    &Config{Version: "2.4.0", TrackerURL: "https://tracker.dev.stonkagents.com/"},
		startTime: time.Now(),
	}
	status := func() map[string]interface{} {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
		w := httptest.NewRecorder()
		s.handleStatus(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		return resp
	}
	t.Setenv("STONKAGENTS_ENV", "")
	resp := status()
	if resp["tracker_url"] != "https://tracker.dev.stonkagents.com" || resp["environment"] != "prd" {
		t.Errorf("unset env: tracker_url=%v environment=%v", resp["tracker_url"], resp["environment"])
	}
	t.Setenv("STONKAGENTS_ENV", "dev")
	if resp := status(); resp["environment"] != "dev" {
		t.Errorf("dev env: environment=%v", resp["environment"])
	}
	t.Setenv("STONKAGENTS_ENV", "staging")
	if resp := status(); resp["environment"] != "stg" {
		t.Errorf("staging env: environment=%v", resp["environment"])
	}
	s.config.TrackerURL = ""
	if resp := status(); resp["tracker_url"] != "" {
		t.Errorf("no tracker: tracker_url=%v", resp["tracker_url"])
	}
}

func TestStatus_IncludesUpdateInfo_WhenAvailable(t *testing.T) {
	s := &Server{
		config:              &Config{Version: "0.2.0"},
		startTime:           time.Now(),
		cachedLatestVersion: "0.3.0",
		cachedReleaseNotes:  "Bug fixes and improvements",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	upd, ok := resp["update"]
	if !ok {
		t.Fatal("expected 'update' block in status response")
	}

	updMap := upd.(map[string]interface{})
	if updMap["available"] != true {
		t.Errorf("expected available=true, got %v", updMap["available"])
	}
	if updMap["latest_version"] != "0.3.0" {
		t.Errorf("expected latest_version=0.3.0, got %v", updMap["latest_version"])
	}
	if updMap["release_notes"] != "Bug fixes and improvements" {
		t.Errorf("expected release_notes, got %v", updMap["release_notes"])
	}
}

func TestStatus_UpdateFields_ConcurrentAccess(t *testing.T) {
	// TD-051: Verify no data race when heartbeat goroutine writes update fields
	// while HTTP handler reads them. Run with: go test -race
	s := &Server{
		config:    &Config{Version: "0.2.0"},
		startTime: time.Now(),
	}

	done := make(chan struct{})
	// Writer goroutine — simulates heartbeat updating cached fields
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			s.SetUpdateInfo(fmt.Sprintf("1.%d.0", i), fmt.Sprintf("Release %d", i))
		}
	}()

	// Reader goroutine — simulates concurrent /status requests
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
		w := httptest.NewRecorder()
		s.handleStatus(w, req)
	}
	<-done
}

func TestStatus_OmitsUpdateInfo_WhenNoUpdate(t *testing.T) {
	s := &Server{
		config:    &Config{Version: "0.2.0"},
		startTime: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := resp["update"]; ok {
		t.Error("expected no 'update' block when no update available")
	}
}

// === F-029 Proxy Header Forwarding + Error Sanitization (US-029-26) ===

// TestProxyForwardsRateLimitHeaders verifies portalProxyForward copies
// rate-limit and cache headers from the upstream tracker response to the client.
func TestProxyForwardsRateLimitHeaders(t *testing.T) {
	// Mock upstream tracker that returns rate limit headers
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "100")
		w.Header().Set("X-RateLimit-Remaining", "97")
		w.Header().Set("X-RateLimit-Reset", "1708000000")
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Request-Id", "req-abc-123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{}}`))
	}))
	defer upstream.Close()

	server := &Server{
		config: &Config{TrackerURL: upstream.URL},
	}
	// Set a fake API key so the proxy doesn't bail on missing key
	server.trackerAPIKey = "test-key-123"

	req := httptest.NewRequest("GET", "/api/v1/portal/test", nil)
	resp := httptest.NewRecorder()
	server.portalProxyForward(resp, req, "/api/test")

	// Verify all allowed headers are forwarded
	expectedHeaders := []string{
		"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
		"Retry-After", "Cache-Control", "X-Request-Id",
	}
	for _, header := range expectedHeaders {
		if resp.Header().Get(header) == "" {
			t.Errorf("expected %s header to be forwarded, but it was missing", header)
		}
	}

	// Verify actual values
	if resp.Header().Get("X-RateLimit-Limit") != "100" {
		t.Errorf("X-RateLimit-Limit = %q, want 100", resp.Header().Get("X-RateLimit-Limit"))
	}
	if resp.Header().Get("X-Request-Id") != "req-abc-123" {
		t.Errorf("X-Request-Id = %q, want req-abc-123", resp.Header().Get("X-Request-Id"))
	}
}

// TestProxyErrorDoesNotLeakGoErrors verifies that when the upstream tracker
// is unreachable, the error response uses a safe generic message instead of
// leaking Go internal error strings (e.g., "dial tcp", "connection refused").
func TestProxyErrorDoesNotLeakGoErrors(t *testing.T) {
	// Point proxy at an unreachable address
	server := &Server{
		config: &Config{TrackerURL: "http://127.0.0.1:1"},
	}
	server.trackerAPIKey = "test-key-123"

	req := httptest.NewRequest("GET", "/api/v1/portal/test", nil)
	resp := httptest.NewRecorder()
	server.portalProxyForward(resp, req, "/api/test")

	body := resp.Body.String()

	// Must NOT contain Go internal error strings
	if strings.Contains(body, "dial tcp") || strings.Contains(body, "connection refused") {
		t.Errorf("proxy error response leaks Go internal error: %s", body)
	}

	// Must contain our safe error code
	if !strings.Contains(body, "GATEWAY_UNREACHABLE") {
		t.Errorf("expected GATEWAY_UNREACHABLE error code, got: %s", body)
	}
}

// TestPortalProxyV1_PathTraversal_Blocked verifies that path traversal attempts
// via /api/v1/portal/../../../ are sanitized before forwarding to the tracker.
// TD-071: Portal proxy did not sanitize paths, allowing ../ escape to upstream.
func TestPortalProxyV1_PathTraversal_Blocked(t *testing.T) {
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{}}`))
	}))
	defer upstream.Close()

	server := &Server{
		config: &Config{TrackerURL: upstream.URL},
	}
	server.trackerAPIKey = "test-key-123"

	tests := []struct {
		name       string
		inputPath  string
		wantStatus int
		wantClean  bool   // if true, check upstream path is clean
		wantPath   string // expected cleaned upstream path (if wantClean)
	}{
		{
			name:       "traversal attempt escapes /api/ prefix",
			inputPath:  "/api/v1/portal/../../admin",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "traversal in middle is cleaned",
			inputPath:  "/api/v1/portal/peers/../peers/test-id",
			wantClean:  true,
			wantStatus: http.StatusOK,
			wantPath:   "/api/peers/test-id",
		},
		{
			name:       "normal path passes through",
			inputPath:  "/api/v1/portal/peers/test-id/trust",
			wantClean:  true,
			wantStatus: http.StatusOK,
			wantPath:   "/api/peers/test-id/trust",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receivedPath = ""
			req := httptest.NewRequest(http.MethodPost, tt.inputPath, nil)
			resp := httptest.NewRecorder()
			server.handlePortalProxyV1(resp, req)

			if resp.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body: %s", resp.Code, tt.wantStatus, resp.Body.String())
			}
			if tt.wantClean && receivedPath != tt.wantPath {
				t.Errorf("upstream path = %q, want %q", receivedPath, tt.wantPath)
			}
		})
	}
}

// --- F-029 / US-029-04: RetryDownload HTTP Endpoint Tests ---

// TestHandleRetryDownload_Success verifies AC-8: POST /api/v1/downloads/retry
// returns 200 with {"cid":"...","status":"queued"} when download is failed.
func TestHandleRetryDownload_Success(t *testing.T) {
	server := NewServer()
	dm := setupTestDownloadManager(t)
	server.downloadManager = dm

	// Queue then fail
	_ = dm.QueueDownload("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi", "test.bin", 1024, 4)
	_ = dm.FailDownload("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi", "timeout")

	body := `{"cid":"bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/retry", strings.NewReader(body))
	w := httptest.NewRecorder()

	server.handleRetryDownload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "queued" {
		t.Errorf("expected status=queued, got %q", resp["status"])
	}
}

// TestHandleRetryDownload_InvalidCID verifies AC-8: bad CID returns 400 VALIDATION_ERROR.
func TestHandleRetryDownload_InvalidCID(t *testing.T) {
	server := NewServer()

	body := `{"cid":"../../../etc/passwd"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/retry", strings.NewReader(body))
	w := httptest.NewRecorder()

	server.handleRetryDownload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Errorf("expected VALIDATION_ERROR, got: %s", w.Body.String())
	}
}

// TestHandleRetryDownload_EmptyCID verifies AC-8: missing CID returns 400.
func TestHandleRetryDownload_EmptyCID(t *testing.T) {
	server := NewServer()

	body := `{"cid":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/retry", strings.NewReader(body))
	w := httptest.NewRecorder()

	server.handleRetryDownload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleRetryDownload_MethodNotAllowed verifies only POST is accepted.
func TestHandleRetryDownload_MethodNotAllowed(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/retry", nil)
	w := httptest.NewRecorder()

	server.handleRetryDownload(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// --- F-029 / US-029-22: Path-Param Endpoint Tests ---

// TestHandleCancelDownloadByPath_InvalidCID verifies AC-28: bad CID returns 400.
func TestHandleCancelDownloadByPath_InvalidCID(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/badcid/cancel", nil)
	req.SetPathValue("cid", "badcid")
	w := httptest.NewRecorder()

	server.handleCancelDownloadByPath(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Errorf("expected VALIDATION_ERROR, got: %s", w.Body.String())
	}
}

// TestHandlePauseDownloadByPath_InvalidCID verifies AC-28: bad CID returns 400.
func TestHandlePauseDownloadByPath_InvalidCID(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/short/pause", nil)
	req.SetPathValue("cid", "short")
	w := httptest.NewRecorder()

	server.handlePauseDownloadByPath(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSeedDownloadByPath_MethodNotAllowed(t *testing.T) {
	server := NewServer()
	validCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/"+validCID+"/seed", nil)
	req.SetPathValue("cid", validCID)
	w := httptest.NewRecorder()
	server.handleSeedDownloadByPath(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleSeedDownloadByPath_NotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chunks.db")
	store, err := storage.NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{
		config:     &Config{DataDir: dir},
		chunkStore: store,
	}
	validCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+validCID+"/seed", nil)
	req.SetPathValue("cid", validCID)
	w := httptest.NewRecorder()
	server.handleSeedDownloadByPath(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCancelDownloadByPath_Success verifies AC-28: valid cancel returns 200.
func TestHandleCancelDownloadByPath_Success(t *testing.T) {
	server := NewServer()
	dm := setupTestDownloadManager(t)
	server.downloadManager = dm

	validCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	_ = dm.QueueDownload(validCID, "test.bin", 1024, 4)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+validCID+"/cancel", nil)
	req.SetPathValue("cid", validCID)
	w := httptest.NewRecorder()

	server.handleCancelDownloadByPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "cancelled" {
		t.Errorf("expected status=cancelled, got %q", resp["status"])
	}
}

// TestHandleDownloadsStatusIncludesCountsAndRecent verifies US-029-06:
// GET /api/v1/downloads/status returns downloads + counts + recent.
func TestHandleDownloadsStatusIncludesCountsAndRecent(t *testing.T) {
	dir := t.TempDir()
	dm := download.NewManager(dir, 3)
	_ = dm.QueueDownload("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi", "test.claw-skill", 1000, 4)

	historyDBPath := filepath.Join(dir, "history.db")
	historyRepo, err := history.NewRepository(historyDBPath)
	if err != nil {
		t.Fatalf("failed to create history repo: %v", err)
	}
	defer historyRepo.Close()

	// Insert a completed record for "recent"
	fixedTime := time.Date(2026, 2, 14, 10, 0, 0, 0, time.UTC)
	_ = historyRepo.Insert(&history.TransferRecord{
		CID: "QmOldRecord1234567890abcdefghijklmnopqrstuv12345", Filename: "old.txt", FileType: "file",
		Direction: "download", TotalSize: 100, State: "completed",
		StartedAt: fixedTime.Add(-5 * time.Minute), CompletedAt: fixedTime,
	})

	server := &Server{downloadManager: dm, historyRepo: historyRepo}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/downloads/status", nil)
	server.handleDownloadsStatus(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, ok := body["counts"]; !ok {
		t.Error("missing 'counts' in response")
	}
	if _, ok := body["recent"]; !ok {
		t.Error("missing 'recent' in response")
	}
	if _, ok := body["downloads"]; !ok {
		t.Error("missing 'downloads' in response")
	}

	// Verify counts has all 5 fields
	counts, _ := body["counts"].(map[string]interface{})
	for _, key := range []string{"queued", "active", "paused", "completed", "failed"} {
		if _, ok := counts[key]; !ok {
			t.Errorf("counts missing key: %s", key)
		}
	}

	// Verify recent is non-empty
	recent, _ := body["recent"].([]interface{})
	if len(recent) != 1 {
		t.Errorf("expected 1 recent record, got %d", len(recent))
	}
}

// TestHandleTransferHistory_ADR001Pagination verifies US-029-07:
// GET /api/v1/transfers/history returns paginated history per ADR-001.
func TestHandleTransferHistory_ADR001Pagination(t *testing.T) {
	dir := t.TempDir()
	historyDBPath := filepath.Join(dir, "history.db")
	historyRepo, err := history.NewRepository(historyDBPath)
	if err != nil {
		t.Fatalf("failed to create history repo: %v", err)
	}
	defer historyRepo.Close()

	// Insert 3 records
	baseTime := time.Date(2026, 2, 14, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		_ = historyRepo.Insert(&history.TransferRecord{
			CID:      fmt.Sprintf("QmHist%05dabcdefghijklmnopqrstuvwxyz12345678", i),
			Filename: fmt.Sprintf("file%d.txt", i), FileType: "file",
			Direction: "download", TotalSize: int64(100 * (i + 1)), State: "completed",
			StartedAt:   baseTime.Add(time.Duration(i) * time.Minute),
			CompletedAt: baseTime.Add(time.Duration(i)*time.Minute + 30*time.Second),
		})
	}

	server := &Server{historyRepo: historyRepo}

	// Default pagination
	req := httptest.NewRequest("GET", "/api/v1/transfers/history", nil)
	resp := httptest.NewRecorder()
	server.handleTransferHistory(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	transfers, ok := body["transfers"].([]interface{})
	if !ok {
		t.Fatal("missing 'transfers' array")
	}
	if len(transfers) != 3 {
		t.Errorf("expected 3 transfers, got %d", len(transfers))
	}

	meta, ok := body["meta"].(map[string]interface{})
	if !ok {
		t.Fatal("missing 'meta' object")
	}
	if meta["total"].(float64) != 3 {
		t.Errorf("expected total=3, got %v", meta["total"])
	}
	if meta["limit"].(float64) != 20 {
		t.Errorf("expected default limit=20, got %v", meta["limit"])
	}
	if meta["offset"].(float64) != 0 {
		t.Errorf("expected offset=0, got %v", meta["offset"])
	}

	// Explicit pagination: limit=2, offset=1
	req2 := httptest.NewRequest("GET", "/api/v1/transfers/history?limit=2&offset=1", nil)
	resp2 := httptest.NewRecorder()
	server.handleTransferHistory(resp2, req2)

	var body2 map[string]interface{}
	_ = json.Unmarshal(resp2.Body.Bytes(), &body2)
	transfers2, _ := body2["transfers"].([]interface{})
	if len(transfers2) != 2 {
		t.Errorf("expected 2 transfers with limit=2&offset=1, got %d", len(transfers2))
	}
	meta2, _ := body2["meta"].(map[string]interface{})
	if meta2["total"].(float64) != 3 {
		t.Errorf("total should still be 3, got %v", meta2["total"])
	}
}

// TestHandleTransferHistory_MethodNotAllowed verifies only GET is accepted.
func TestHandleTransferHistory_MethodNotAllowed(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest("POST", "/api/v1/transfers/history", nil)
	resp := httptest.NewRecorder()
	server.handleTransferHistory(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.Code)
	}
}

// TestHandleTransferHistory_NoHistoryRepo verifies graceful empty response.
func TestHandleTransferHistory_NoHistoryRepo(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest("GET", "/api/v1/transfers/history", nil)
	resp := httptest.NewRecorder()
	server.handleTransferHistory(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	var body map[string]interface{}
	_ = json.Unmarshal(resp.Body.Bytes(), &body)
	transfers, _ := body["transfers"].([]interface{})
	if len(transfers) != 0 {
		t.Errorf("expected empty transfers, got %d", len(transfers))
	}
}

// TestHandlePauseDownload_ValidRequest verifies US-029-08: POST /downloads/pause with JSON body.
func TestHandlePauseDownload_ValidRequest(t *testing.T) {
	dm := setupTestDownloadManager(t)
	validCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	_ = dm.QueueDownload(validCID, "test.txt", 1000, 4)

	server := &Server{downloadManager: dm}

	body := strings.NewReader(fmt.Sprintf(`{"cid":"%s"}`, validCID))
	req := httptest.NewRequest("POST", "/api/v1/downloads/pause", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.handlePauseDownload(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestHandlePauseDownload_MissingCID verifies validation.
func TestHandlePauseDownload_MissingCID(t *testing.T) {
	server := &Server{downloadManager: setupTestDownloadManager(t)}

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/downloads/pause", body)
	resp := httptest.NewRecorder()
	server.handlePauseDownload(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.Code)
	}
}

// TestHandlePauseDownload_WrongMethod verifies method checking.
func TestHandlePauseDownload_WrongMethod(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest("GET", "/api/v1/downloads/pause", nil)
	resp := httptest.NewRecorder()
	server.handlePauseDownload(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.Code)
	}
}

// TestHandleResumeDownload_ValidRequest verifies US-029-08: POST /downloads/resume with JSON body.
func TestHandleResumeDownload_ValidRequest(t *testing.T) {
	dm := setupTestDownloadManager(t)
	validCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	_ = dm.QueueDownload(validCID, "test.txt", 1000, 4)

	server := &Server{downloadManager: dm}

	// Pause first (from queued state, PauseDownload works on any download)
	_ = dm.PauseDownload(validCID)

	body := strings.NewReader(fmt.Sprintf(`{"cid":"%s"}`, validCID))
	req := httptest.NewRequest("POST", "/api/v1/downloads/resume", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.handleResumeDownload(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestHandleResumeDownload_NotReady verifies NOT_READY when manager is nil.
func TestHandleResumeDownload_NotReady(t *testing.T) {
	server := &Server{}

	body := strings.NewReader(`{"cid":"bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"}`)
	req := httptest.NewRequest("POST", "/api/v1/downloads/resume", body)
	resp := httptest.NewRecorder()
	server.handleResumeDownload(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.Code)
	}
}

// TestHandleLibrary_ADR001Pagination verifies US-029-09:
// GET /api/v1/library returns paginated files with storage summary per ADR-001.
func TestHandleLibrary_ADR001Pagination(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chunks.db")
	store, err := storage.NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	// Add a test file
	_ = store.StoreFile("QmTestLibraryFileabcdefghijklmnopqrstuvwxyz1234", "test.claw-skill", 1000, 4, nil)

	server := &Server{chunkStore: store}

	req := httptest.NewRequest("GET", "/api/v1/library?limit=20&offset=0", nil)
	resp := httptest.NewRecorder()
	server.handleLibrary(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify files array
	files, ok := body["files"].([]interface{})
	if !ok {
		t.Fatal("missing 'files' in response")
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}

	// Verify storage summary
	storageSummary, ok := body["storage"].(map[string]interface{})
	if !ok {
		t.Fatal("missing 'storage' in response")
	}
	if storageSummary["file_count"].(float64) != 1 {
		t.Errorf("expected file_count=1, got %v", storageSummary["file_count"])
	}
	if storageSummary["used_bytes"].(float64) != 1000 {
		t.Errorf("expected used_bytes=1000, got %v", storageSummary["used_bytes"])
	}

	// Verify ADR-001 meta
	meta, ok := body["meta"].(map[string]interface{})
	if !ok {
		t.Fatal("missing 'meta' in response")
	}
	if meta["limit"].(float64) != 20 {
		t.Errorf("expected default limit=20, got %v", meta["limit"])
	}
}

// TestHandleLibrary_Empty verifies empty response when no files shared.
func TestHandleLibrary_Empty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chunks.db")
	store, _ := storage.NewSQLiteChunkStore(dbPath)
	defer store.Close()

	server := &Server{chunkStore: store}

	req := httptest.NewRequest("GET", "/api/v1/library", nil)
	resp := httptest.NewRecorder()
	server.handleLibrary(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	var body map[string]interface{}
	_ = json.Unmarshal(resp.Body.Bytes(), &body)
	files, _ := body["files"].([]interface{})
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

// TestHandleLibrary_NoChunkStore verifies graceful empty response.
func TestHandleLibrary_NoChunkStore(t *testing.T) {
	server := &Server{}

	req := httptest.NewRequest("GET", "/api/v1/library", nil)
	resp := httptest.NewRecorder()
	server.handleLibrary(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
}

// TestHandleLibrary_MethodNotAllowed verifies only GET.
func TestHandleLibrary_MethodNotAllowed(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest("POST", "/api/v1/library", nil)
	resp := httptest.NewRecorder()
	server.handleLibrary(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.Code)
	}
}

// TestNoHandlerLeaksGoErrors (US-029-27) verifies no F-029 handler leaks raw Go error strings.
// Exercises all error paths on handlers that call manager methods with non-existent CIDs.
func TestNoHandlerLeaksGoErrors(t *testing.T) {
	goErrorPatterns := []string{
		"failed to", "download not found", "invalid CID:",
		"can only retry", "max concurrent", "not found for CID",
		"pause download failed", "resume download failed",
	}

	dm := setupTestDownloadManager(t)
	server := &Server{downloadManager: dm}

	// Use a valid-format but non-existent CID
	nonExistentCID := "bafynonexistentcidfortestAAAAAAAAAAAAAAAAAAAA"

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"pause", "POST", "/api/v1/downloads/pause", fmt.Sprintf(`{"cid":"%s"}`, nonExistentCID), server.handlePauseDownload},
		{"resume", "POST", "/api/v1/downloads/resume", fmt.Sprintf(`{"cid":"%s"}`, nonExistentCID), server.handleResumeDownload},
		{"retry", "POST", "/api/v1/downloads/retry", fmt.Sprintf(`{"cid":"%s"}`, nonExistentCID), server.handleRetryDownload},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			tc.handler(resp, req)

			responseBody := resp.Body.String()
			for _, pattern := range goErrorPatterns {
				if strings.Contains(responseBody, pattern) {
					t.Errorf("handler %s leaks Go error pattern %q: %s", tc.name, pattern, responseBody)
				}
			}
		})
	}

	// Path-param cancel with non-existent CID
	t.Run("cancel-by-path", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/downloads/"+nonExistentCID+"/cancel", nil)
		req.SetPathValue("cid", nonExistentCID)
		resp := httptest.NewRecorder()
		server.handleCancelDownloadByPath(resp, req)

		responseBody := resp.Body.String()
		for _, pattern := range goErrorPatterns {
			if strings.Contains(responseBody, pattern) {
				t.Errorf("handler cancel-by-path leaks Go error pattern %q: %s", pattern, responseBody)
			}
		}
	})
}
func TestStatus_IncludesTransferStats(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	ts, err := stats.NewRepository(dbPath)
	if err != nil {
		t.Fatalf("stats.NewRepository: %v", err)
	}
	defer ts.Close()
	ts.RecordUpload(1024 * 1024)  // 1 MB up
	ts.RecordDownload(512 * 1024) // 512 KB down

	s := &Server{
		config:        &Config{Version: "0.2.0"},
		startTime:     time.Now(),
		transferStats: ts,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if resp.Transfer.TotalUploadedBytes != 1024*1024 {
		t.Errorf("expected total_uploaded_bytes=1048576, got %d", resp.Transfer.TotalUploadedBytes)
	}
	if resp.Transfer.TotalDownloadedBytes != 512*1024 {
		t.Errorf("expected total_downloaded_bytes=524288, got %d", resp.Transfer.TotalDownloadedBytes)
	}
}

func TestStatus_IncludesHealthIndicators(t *testing.T) {
	s := &Server{
		config:    &Config{Version: "0.2.0"},
		startTime: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// When no p2pHost, all indicators should be false/unknown
	if resp.Health.NATStatus != "unknown" {
		t.Errorf("expected nat_status=unknown, got %s", resp.Health.NATStatus)
	}
	if resp.Health.DHTReady {
		t.Error("expected dht_ready=false when p2pHost is nil")
	}
	if resp.Health.MDNSReady {
		t.Error("expected mdns_ready=false when p2pHost is nil")
	}
	if resp.Health.RelayConnected {
		t.Error("expected relay_connected=false when p2pHost is nil")
	}
}

func TestConnections_ReturnsEmptyWhenNoP2PHost(t *testing.T) {
	s := &Server{
		config:    &Config{Version: "0.2.0"},
		startTime: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	w := httptest.NewRecorder()
	s.handleConnections(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp ConnectionsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(resp.Data) != 0 {
		t.Errorf("expected empty connections, got %d", len(resp.Data))
	}
}

// GET /api/v1/status answers at once even when the tracker is slow: the online
// peer count comes from a cache that a background call refreshes. The portal's
// health poll gives up after one second, so a status that waited on the tracker
// read as an offline agent on a slow link.
func TestStatus_DoesNotWaitOnTracker(t *testing.T) {
	release := make(chan struct{})
	trk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":7}`))
	}))
	defer trk.Close()
	defer close(release)

	s := &Server{config: &Config{Version: "2.6.0"}, startTime: time.Now()}
	s.trackerClient = tracker.NewClient(trk.URL, "peer")

	status := func() StatusResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
		w := httptest.NewRecorder()
		s.handleStatus(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp StatusResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		return resp
	}

	started := time.Now()
	first := status()
	if took := time.Since(started); took > 500*time.Millisecond {
		t.Fatalf("status waited on the tracker: %v", took)
	}
	if first.Network.Peers != 0 {
		t.Fatalf("expected 0 peers before the tracker answered, got %d", first.Network.Peers)
	}

	// A second call while the refresh is in flight starts no second one and still answers at once.
	status()
	s.peerCountMu.Lock()
	refreshing := s.peerCountRefreshing
	s.peerCountMu.Unlock()
	if !refreshing {
		t.Fatal("expected one background refresh to be in flight")
	}

	release <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for status().Network.Peers != 7 {
		if time.Now().After(deadline) {
			t.Fatal("the tracker's answer never reached /status")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
