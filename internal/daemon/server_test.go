// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-01 (HTTP Server with Health Endpoint)
// Purpose: TDD tests for HTTP server

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/storage"
	"github.com/stonkagents/agent/internal/logger"
)

// TestHealthEndpointReturnsJSON is our first RED test
// Acceptance Criterion: Health endpoint returns JSON with status, version, uptime_seconds
func TestHealthEndpointReturnsJSON(t *testing.T) {
	// Arrange
	server := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	// Act
	server.handleHealth(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Check required fields
	if _, ok := response["status"]; !ok {
		t.Error("Response missing 'status' field")
	}
	if _, ok := response["version"]; !ok {
		t.Error("Response missing 'version' field")
	}
	if _, ok := response["uptime_seconds"]; !ok {
		t.Error("Response missing 'uptime_seconds' field")
	}
}

// TestUptimeIncreases - test for uptime calculation
// Acceptance Criterion: uptime_seconds should increase over time
func TestUptimeIncreases(t *testing.T) {
	// Arrange
	server := NewServer()

	// Act - first request
	req1 := httptest.NewRequest(http.MethodGet, "/health", nil)
	w1 := httptest.NewRecorder()
	server.handleHealth(w1, req1)

	var response1 map[string]interface{}
	json.NewDecoder(w1.Body).Decode(&response1)
	uptime1 := int64(response1["uptime_seconds"].(float64))

	// Wait a bit
	time.Sleep(1 * time.Second)

	// Act - second request
	req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
	w2 := httptest.NewRecorder()
	server.handleHealth(w2, req2)

	var response2 map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&response2)
	uptime2 := int64(response2["uptime_seconds"].(float64))

	// Assert - uptime should have increased
	if uptime2 <= uptime1 {
		t.Errorf("Uptime should increase: %d -> %d", uptime1, uptime2)
	}
}

// TestServerStartsOnCorrectAddress - test for server startup
// Acceptance Criterion: Server binds to 127.0.0.1:7841
func TestServerStartsOnCorrectAddress(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17841, // Use different port for test to avoid conflicts
	}
	server := NewServerWithConfig(config)
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	url := fmt.Sprintf("http://%s/health", config.Address())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

// TestWaitForShutdownHandlesSignals - test for signal handling
// Acceptance Criterion: Server gracefully shuts down on SIGTERM/SIGINT
func TestWaitForShutdownHandlesSignals(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 17842, // Different port to avoid conflicts
	}
	server := NewServerWithConfig(config)
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	// Create a channel to signal when WaitForShutdown completes
	done := make(chan bool)

	// Start WaitForShutdown in goroutine
	go func() {
		server.WaitForShutdown()
		done <- true
	}()

	// Give server time to start waiting
	time.Sleep(100 * time.Millisecond)

	// Send SIGTERM to trigger shutdown
	// Note: We'll use the server's shutdown channel instead of sending actual OS signals
	// This makes the test more deterministic
	server.TriggerShutdown()

	// Assert - WaitForShutdown should complete within reasonable time
	select {
	case <-done:
		// Success - shutdown completed
	case <-time.After(6 * time.Second):
		t.Error("WaitForShutdown did not complete within timeout")
	}

	url := fmt.Sprintf("http://%s/health", config.Address())
	_, err := http.Get(url)
	if err == nil {
		t.Error("Server should be stopped but is still responding")
	}
}

// TestReadiness_Returns503WhenNotReady - readiness endpoint returns 503 when components not initialized
// Audit: C3 (Readiness Endpoint)
func TestReadiness_Returns503WhenNotReady(t *testing.T) {
	// Arrange - server with no components initialized
	server := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
	w := httptest.NewRecorder()

	// Act
	server.handleReadiness(w, req)

	// Assert - should return 503
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}

	// Assert - content type is JSON
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", contentType)
	}

	// Assert - response body has correct structure
	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	// Check status field
	if status, ok := response["status"]; !ok || status != "not_ready" {
		t.Errorf("Expected status 'not_ready', got %v", response["status"])
	}

	// Check components field exists and has all three components
	components, ok := response["components"].(map[string]interface{})
	if !ok {
		t.Fatal("Response missing 'components' object")
	}

	expectedComponents := []string{"p2p_host", "download_manager", "chunk_store"}
	for _, comp := range expectedComponents {
		val, exists := components[comp]
		if !exists {
			t.Errorf("Missing component '%s' in response", comp)
		}
		if val != "not_ready" {
			t.Errorf("Expected component '%s' to be 'not_ready', got '%v'", comp, val)
		}
	}
}

// TestReadiness_Returns200WhenReady - readiness endpoint returns 200 when all components initialized
// Audit: C3 (Readiness Endpoint)
func TestReadiness_Returns200WhenReady(t *testing.T) {
	// Arrange - create temp dir for chunk store
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test-chunks.db")

	// Create a real chunk store (SQLite, lightweight)
	chunkStore, err := storage.NewSQLiteChunkStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}

	// Create server with all components set
	server := NewServer()
	server.p2pHost = &P2PHost{}                              // Minimal non-nil P2PHost
	server.downloadManager = download.NewManager(tempDir, 1) // Real download manager
	server.chunkStore = chunkStore                           // Real chunk store
	defer server.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
	w := httptest.NewRecorder()

	// Act
	server.handleReadiness(w, req)

	// Assert - should return 200
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Assert - content type is JSON
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", contentType)
	}

	// Assert - response body
	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	if status, ok := response["status"]; !ok || status != "ready" {
		t.Errorf("Expected status 'ready', got %v", response["status"])
	}
}

// TestReadiness_Returns503WhenPartiallyReady - readiness returns 503 when only some components initialized
// Audit: C3 (Readiness Endpoint)
func TestReadiness_Returns503WhenPartiallyReady(t *testing.T) {
	// Arrange - server with only p2pHost set
	server := NewServer()
	server.p2pHost = &P2PHost{} // Only one component

	req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
	w := httptest.NewRecorder()

	// Act
	server.handleReadiness(w, req)

	// Assert - should still return 503
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}

	var response map[string]interface{}
	json.NewDecoder(w.Body).Decode(&response)

	components := response["components"].(map[string]interface{})

	// p2p_host should be ready, others not
	if components["p2p_host"] != "ready" {
		t.Errorf("Expected p2p_host to be 'ready', got '%v'", components["p2p_host"])
	}
	if components["download_manager"] != "not_ready" {
		t.Errorf("Expected download_manager to be 'not_ready', got '%v'", components["download_manager"])
	}
	if components["chunk_store"] != "not_ready" {
		t.Errorf("Expected chunk_store to be 'not_ready', got '%v'", components["chunk_store"])
	}
}

// TestHealth_StillReturns200Always - verify /health endpoint is unchanged
// Audit: C3 (Readiness Endpoint)
func TestHealth_StillReturns200Always(t *testing.T) {
	// Arrange - server with no components (same as unready state)
	server := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	// Act
	server.handleHealth(w, req)

	// Assert - health always returns 200 regardless of component state
	if w.Code != http.StatusOK {
		t.Errorf("Expected health to always return 200, got %d", w.Code)
	}
}

// TestServerLogsStartupAndShutdown - test for logger integration
// Acceptance Criterion: Daemon logs to ~/.stonkagents/logs/daemon.log
func TestServerLogsStartupAndShutdown(t *testing.T) {
	// Arrange - create temp log directory
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "daemon.log")

	log, err := logger.New(logger.Config{
		LogPath:    logPath,
		MaxSize:    10,
		MaxBackups: 5,
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer log.Close()

	config := &Config{
		Host: "127.0.0.1",
		Port: 17843,
	}

	// Act - create server with logger
	server := NewServerWithLogger(config, log)

	if server == nil {
		t.Fatal("NewServerWithLogger returned nil")
	}

	// Start server
	err = server.Start()
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Shutdown()

	// Verify logger is attached to server
	if server.GetLogger() == nil {
		t.Error("Server logger should not be nil")
	}

	// Verify log file was created
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("Log file should be created")
	}
}

// uuidV4Pattern matches UUID v4 format: 8-4-4-4-12 hex digits
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestCorrelationID_GeneratedWhenMissing verifies that a UUID v4 correlation ID
// is generated when no X-Request-ID header is present in the request.
// Audit: C2 (Correlation ID Middleware)
func TestCorrelationID_GeneratedWhenMissing(t *testing.T) {
	// Arrange: create a handler wrapped with correlationIDMiddleware
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify correlation ID is available in context
		cid := r.Context().Value(correlationIDKey)
		if cid == nil {
			t.Error("correlationID should be set in request context")
			return
		}
		cidStr, ok := cid.(string)
		if !ok {
			t.Error("correlationID in context should be a string")
			return
		}
		if !uuidV4Pattern.MatchString(cidStr) {
			t.Errorf("correlationID should be UUID v4 format, got %q", cidStr)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := correlationIDMiddleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	// Do NOT set X-Request-ID header
	w := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(w, req)

	// Assert: response header contains a valid UUID v4
	respID := w.Header().Get("X-Request-ID")
	if respID == "" {
		t.Fatal("Response should include X-Request-ID header")
	}
	if !uuidV4Pattern.MatchString(respID) {
		t.Errorf("X-Request-ID should be UUID v4 format, got %q", respID)
	}
}

// TestCorrelationID_PreservedWhenPresent verifies that an incoming X-Request-ID
// header value is preserved and not overwritten by the middleware.
// Audit: C2 (Correlation ID Middleware)
func TestCorrelationID_PreservedWhenPresent(t *testing.T) {
	existingID := "my-custom-request-id-12345"

	// Arrange
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the original ID is in context
		cid := r.Context().Value(correlationIDKey)
		if cid == nil {
			t.Error("correlationID should be set in request context")
			return
		}
		cidStr, ok := cid.(string)
		if !ok {
			t.Error("correlationID in context should be a string")
			return
		}
		if cidStr != existingID {
			t.Errorf("correlationID should preserve incoming value %q, got %q", existingID, cidStr)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := correlationIDMiddleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Request-ID", existingID)
	w := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(w, req)

	// Assert: response header matches the original ID
	respID := w.Header().Get("X-Request-ID")
	if respID != existingID {
		t.Errorf("Response X-Request-ID should be %q, got %q", existingID, respID)
	}
}

// TestCorrelationID_InResponseHeader verifies that the X-Request-ID header
// is always present in the response, whether generated or preserved.
// Audit: C2 (Correlation ID Middleware)
func TestCorrelationID_InResponseHeader(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := correlationIDMiddleware(inner)

	t.Run("generated ID in response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		respID := w.Header().Get("X-Request-ID")
		if respID == "" {
			t.Fatal("Response must include X-Request-ID header when none sent")
		}
	})

	t.Run("preserved ID in response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-ID", "preserved-id")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		respID := w.Header().Get("X-Request-ID")
		if respID != "preserved-id" {
			t.Errorf("Response X-Request-ID should be %q, got %q", "preserved-id", respID)
		}
	})
}

// TestServerWriteTimeout_ExceedsHandlerContextTimeout verifies WriteTimeout > 12s
// to prevent silent connection drops when handlers use context.WithTimeout(12s).
func TestServerWriteTimeout_ExceedsHandlerContextTimeout(t *testing.T) {
	handlerTimeout := 12 * time.Second
	if ServerWriteTimeout <= handlerTimeout {
		t.Errorf("ServerWriteTimeout (%v) must exceed handler context timeout (%v)", ServerWriteTimeout, handlerTimeout)
	}
}
