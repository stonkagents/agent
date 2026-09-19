// Package: tracker/internal/api
// Feature: F-012 (Observability)
// Story: US-012-01 (Prometheus Metrics Export)
// Purpose: TDD tests for Prometheus metrics endpoint and middleware

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RED TEST 1: /metrics endpoint returns Prometheus text format
func TestMetricsEndpoint(t *testing.T) {
	// Create a minimal server for testing (will implement NewTestServer helper)
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("Expected Content-Type: text/plain, got %s", contentType)
	}

	body := w.Body.String()
	if body == "" {
		t.Error("Expected non-empty metrics response")
	}

	// Should contain Prometheus HELP and TYPE comments
	if !strings.Contains(body, "# HELP") || !strings.Contains(body, "# TYPE") {
		t.Error("Expected Prometheus format with HELP and TYPE comments")
	}
}

// RED TEST 2: Request counter increments
func TestRequestCounter(t *testing.T) {
	server, _ := newTestServer(t)

	// Make 5 requests to health endpoint
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		w := httptest.NewRecorder()
		server.Router().ServeHTTP(w, req)
	}

	// Check metrics
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	body := w.Body.String()

	// Should contain at_api_requests_total counter with 5 requests
	if !strings.Contains(body, "at_api_requests_total") {
		t.Error("Expected at_api_requests_total counter in metrics")
	}

	// Should have recorded requests to /health
	if !strings.Contains(body, `path="/health"`) {
		t.Error("Expected /health path label in metrics")
	}
}

// RED TEST 3: Latency histogram records observations
func TestLatencyHistogram(t *testing.T) {
	server, _ := newTestServer(t)

	// Make a request
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	// Check metrics
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	body := w.Body.String()

	// Should contain at_api_latency_seconds histogram
	if !strings.Contains(body, "at_api_latency_seconds") {
		t.Error("Expected at_api_latency_seconds histogram in metrics")
	}

	// Histogram should have buckets
	if !strings.Contains(body, "_bucket{") {
		t.Error("Expected histogram buckets in metrics")
	}
}

// RED TEST 4: Error counter increments on errors
func TestErrorCounter(t *testing.T) {
	server, _ := newTestServer(t)

	// Make a request to non-existent endpoint (404)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracker/nonexistent", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	// Check metrics
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	body := w.Body.String()

	// Should contain at_api_errors_total counter
	if !strings.Contains(body, "at_api_errors_total") {
		t.Error("Expected at_api_errors_total counter in metrics")
	}
}

// RED TEST 5: Resource gauges (peers, assets)
func TestResourceGauges(t *testing.T) {
	server, _ := newTestServer(t)

	// Check metrics
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	body := w.Body.String()

	// Should contain gauges for peers and assets
	if !strings.Contains(body, "at_peers_total") {
		t.Error("Expected at_peers_total gauge in metrics")
	}

	if !strings.Contains(body, "at_assets_total") {
		t.Error("Expected at_assets_total gauge in metrics")
	}
}
