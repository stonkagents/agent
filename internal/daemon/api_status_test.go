// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-08 (REST Polling for Progress Tracking)
// Purpose: TDD tests for REST polling endpoints (BitTorrent architecture)

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleDownloadsStatus - RED test
// Acceptance Criterion: GET /api/v1/downloads/status returns active downloads
func TestHandleDownloadsStatus(t *testing.T) {
	server := NewServer()

	req := httptest.NewRequest("GET", "/api/v1/downloads/status", nil)
	rec := httptest.NewRecorder()

	// Act
	server.handleDownloadsStatus(rec, req)

	// Assert
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&response)

	// Should have "downloads" array
	if response["downloads"] == nil {
		t.Error("Response missing 'downloads' field")
	}
}

// TestHandleDownloadsStatus_NoAuthRequired - Endpoint works without authentication (BitTorrent-style)
func TestHandleDownloadsStatus_NoAuthRequired(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest("GET", "/api/v1/downloads/status", nil)
	rec := httptest.NewRecorder()
	server.handleDownloadsStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

// TestHandleDownloadStatus_ByCID - RED test
// Acceptance Criterion: GET /api/v1/downloads/{cid}/status returns specific download
func TestHandleDownloadStatus_ByCID(t *testing.T) {
	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	defer server.Shutdown()

	req := httptest.NewRequest("GET", "/api/v1/downloads/status?cid=bafybeigdyrzt5sfp123", nil)
	rec := httptest.NewRecorder()

	// Act
	server.handleDownloadStatusByCID(rec, req)

	// Assert
	// Should return 404 if download not found, or 200 with download data
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 200 or 404, got %d", rec.Code)
	}
}

// TestHandleUploadsStatus - RED test
// Acceptance Criterion: GET /api/v1/uploads/status returns active uploads
func TestHandleUploadsStatus(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest("GET", "/api/v1/uploads/status", nil)
	rec := httptest.NewRecorder()

	// Act
	server.handleUploadsStatus(rec, req)

	// Assert
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var response map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&response)

	// Should have "uploads" array
	if response["uploads"] == nil {
		t.Error("Response missing 'uploads' field")
	}
}

// TestHandleDownloadsStatus_ResponseFormat - RED test
// Acceptance Criterion: Response includes progress, speed, ETA, peers, state
func TestHandleDownloadsStatus_ResponseFormat(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest("GET", "/api/v1/downloads/status", nil)
	rec := httptest.NewRecorder()

	// Act
	server.handleDownloadsStatus(rec, req)

	// Assert
	var response map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&response)

	downloads, ok := response["downloads"].([]interface{})
	if !ok {
		t.Fatal("downloads should be an array")
	}

	// If there are downloads, verify format
	if len(downloads) > 0 {
		download := downloads[0].(map[string]interface{})

		// Verify required fields exist
		requiredFields := []string{"cid", "filename", "state", "progress"}
		for _, field := range requiredFields {
			if download[field] == nil {
				t.Errorf("Download missing required field: %s", field)
			}
		}
	}
}

// TestHandleUploadsStatus_ResponseFormat - RED test
// Acceptance Criterion: Response includes bytes sent, peers served, upload speed
func TestHandleUploadsStatus_ResponseFormat(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest("GET", "/api/v1/uploads/status", nil)
	rec := httptest.NewRecorder()

	// Act
	server.handleUploadsStatus(rec, req)

	// Assert
	var response map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&response)

	uploads, ok := response["uploads"].([]interface{})
	if !ok {
		t.Fatal("uploads should be an array")
	}

	// If there are uploads, verify format
	if len(uploads) > 0 {
		upload := uploads[0].(map[string]interface{})

		// Verify expected fields exist
		expectedFields := []string{"cid", "filename"}
		for _, field := range expectedFields {
			if upload[field] == nil {
				t.Errorf("Upload missing expected field: %s", field)
			}
		}
	}
}
