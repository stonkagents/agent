// Package: internal/daemon
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-05 (Backend — Node Stats Endpoint)
// Purpose: Tests for GET /api/v1/node/stats — upload/download speeds, uptime, peer count

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/daemon/stats"
)

// NodeStatsTestResponse mirrors the expected JSON response shape.
type NodeStatsTestResponse struct {
	UploadSpeedBPS     int64 `json:"upload_speed_bps"`
	DownloadSpeedBPS   int64 `json:"download_speed_bps"`
	TotalUploadBytes   int64 `json:"total_upload_bytes"`
	TotalDownloadBytes int64 `json:"total_download_bytes"`
	ActivePeers        int   `json:"active_peers"`
	SharedAssets       int   `json:"shared_assets"`
	UptimeSeconds      int64 `json:"uptime_seconds"`
}

// TestHandleNodeStats_ReturnsStatsJSON verifies the /api/v1/node/stats endpoint
// returns the expected JSON shape with transfer stats from TransferStats.
func TestHandleNodeStats_ReturnsStatsJSON(t *testing.T) {
	server := NewServer()
	server.startTime = time.Now().Add(-60 * time.Second) // 60s ago
	repo, err := stats.NewRepository(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("stats repo: %v", err)
	}
	defer repo.Close()
	server.transferStats = repo

	// Record some transfer activity
	server.transferStats.RecordUpload(1024)
	server.transferStats.RecordDownload(2048)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/node/stats", nil)
	w := httptest.NewRecorder()

	server.handleNodeStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/node/stats status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var resp NodeStatsTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify total bytes
	if resp.TotalUploadBytes != 1024 {
		t.Errorf("total_upload_bytes = %d, want 1024", resp.TotalUploadBytes)
	}
	if resp.TotalDownloadBytes != 2048 {
		t.Errorf("total_download_bytes = %d, want 2048", resp.TotalDownloadBytes)
	}

	// Uptime should be ~60s (allow some slack)
	if resp.UptimeSeconds < 59 || resp.UptimeSeconds > 62 {
		t.Errorf("uptime_seconds = %d, want ~60", resp.UptimeSeconds)
	}

	// With no p2pHost, active_peers should be 0
	if resp.ActivePeers != 0 {
		t.Errorf("active_peers = %d, want 0 (no p2pHost)", resp.ActivePeers)
	}
}

// TestHandleNodeStats_NilTransferStats verifies graceful handling when
// transferStats is nil (daemon started without transfer tracking).
func TestHandleNodeStats_NilTransferStats(t *testing.T) {
	server := NewServer()
	server.startTime = time.Now()
	server.transferStats = nil // explicitly nil

	req := httptest.NewRequest(http.MethodGet, "/api/v1/node/stats", nil)
	w := httptest.NewRecorder()

	server.handleNodeStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/node/stats status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var resp NodeStatsTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// All transfer stats should be zero
	if resp.TotalUploadBytes != 0 || resp.TotalDownloadBytes != 0 {
		t.Errorf("expected zero bytes, got upload=%d download=%d", resp.TotalUploadBytes, resp.TotalDownloadBytes)
	}
	if resp.UploadSpeedBPS != 0 || resp.DownloadSpeedBPS != 0 {
		t.Errorf("expected zero speeds, got up=%d down=%d", resp.UploadSpeedBPS, resp.DownloadSpeedBPS)
	}
}

// TestHandleNodeStats_MethodNotAllowed verifies POST is rejected.
func TestHandleNodeStats_MethodNotAllowed(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/stats", nil)
	w := httptest.NewRecorder()

	server.handleNodeStats(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/node/stats status = %d, want 405", w.Code)
	}
}
