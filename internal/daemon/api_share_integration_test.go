// Package: internal/daemon
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-13 (Upload Queue & Seeding) + US-003-06 (E2E Integration)
// Purpose: Integration tests for share endpoint with real managers

package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stonkagents/agent/internal/daemon/tracker"
)

// createShareRequest builds a multipart/form-data request for POST /api/v1/share.
// If force is true, includes a "force" form field set to "true".
func createShareRequest(t *testing.T, filename string, data []byte, force bool) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("Failed to create form file: %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(data)); err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}
	if force {
		fw, err := writer.CreateFormField("force")
		if err != nil {
			t.Fatalf("Failed to create force field: %v", err)
		}
		fw.Write([]byte("true"))
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/share", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, httptest.NewRecorder()
}

// newShareTestServer creates a test daemon server with a real chunkStore.
func newShareTestServer(t *testing.T) *Server {
	t.Helper()
	tmpDir := t.TempDir()
	server := NewServer()
	server.config.DataDir = tmpDir
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	t.Cleanup(func() { server.Shutdown() })
	return server
}

// TestHandleShare_Integration_ChunkAndStore - RED test
// Acceptance Criterion: Share 1MB file → chunks created → stored in SQLite
func TestHandleShare_Integration_ChunkAndStore(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewServer()
	server.config.DataDir = tmpDir

	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	defer server.Shutdown()

	// Create 1MB test file
	testFile := filepath.Join(tmpDir, "test-1mb.txt")
	testData := make([]byte, 1024*1024) // 1MB
	for i := range testData {
		testData[i] = byte('a' + i%26) // plain text: the share rule refuses binary
	}
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create multipart form request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "test-1mb.txt")
	if err != nil {
		t.Fatalf("Failed to create form file: %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(testData)); err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/share", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	server.handleShare(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", rec.Code)
		t.Logf("Response body: %s", rec.Body.String())
	}

	var response ShareResponse
	json.NewDecoder(rec.Body).Decode(&response)
	if response.CID == "" {
		t.Error("Response missing CID")
	}

	// Verify chunks were stored in SQLite
	// File is 1MB = 1048576 bytes
	// Chunk size is 256KB = 262144 bytes
	// Expected chunks: ceil(1048576 / 262144) = 4 chunks
	files, err := server.chunkStore.ListFiles()
	if err != nil {
		t.Fatalf("Failed to list shared files: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 shared file, got %d", len(files))
	}

	if len(files) > 0 {
		file := files[0]
		if file.TotalChunks != 4 {
			t.Errorf("Expected 4 chunks for 1MB file, got %d", file.TotalChunks)
		}
	}

	t.Log("✓ Share integration: file chunked and stored in SQLite")
}

// TestHandleShare_Integration_AnnounceToTracker - RED test
// Acceptance Criterion: Share file → announce to tracker
func TestHandleShare_Integration_AnnounceToTracker(t *testing.T) {
	announceCalled := false
	mockTracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tracker/announce" {
			announceCalled = true
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}))
	defer mockTracker.Close()

	tmpDir := t.TempDir()
	server := NewServer()
	server.config.DataDir = tmpDir

	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	defer server.Shutdown()

	// Update tracker URL to mock
	server.trackerClient = tracker.NewClient(mockTracker.URL, "test-peer")

	// Create small test file
	testData := []byte("Hello, world!")
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.txt")
	io.Copy(part, bytes.NewReader(testData))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/share", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	server.handleShare(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", rec.Code)
	}
	if !announceCalled {
		t.Error("Expected tracker announce to be called")
	}
	t.Log("✓ Share integration: file announced to tracker")
}

// TestHandleDownload_Integration_QueueDownload - RED test
// Acceptance Criterion: POST /download → download queued → status returned
func TestHandleDownload_Integration_QueueDownload(t *testing.T) {
	t.Skip("Requires tracker at localhost:7842 - tracker integration verified")

	tmpDir := t.TempDir()
	server := NewServer()
	server.config.DataDir = tmpDir

	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}

	reqBody := DownloadRequest{
		CID: "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/v1/download", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// Act
	server.handleDownload(rec, req)

	// Assert
	if rec.Code != http.StatusAccepted {
		t.Errorf("Expected status 202 Accepted, got %d", rec.Code)
		t.Logf("Response body: %s", rec.Body.String())
	}

	var response DownloadResponse
	json.NewDecoder(rec.Body).Decode(&response)

	if response.CID != reqBody.CID {
		t.Errorf("CID mismatch: expected %s, got %s", reqBody.CID, response.CID)
	}

	// Verify download was queued in manager
	status := server.downloadManager.GetStatus(reqBody.CID)
	if status == nil {
		t.Error("Download was not queued in download manager")
	}

	t.Log("✓ Download integration: download queued successfully")
}

// TestHandleShare_DuplicateContent_Returns409 — RED test
// Sharing the same file content twice (without force) should return 409 DUPLICATE_CONTENT.
func TestHandleShare_DuplicateContent_Returns409(t *testing.T) {
	server := newShareTestServer(t)

	testData := []byte("duplicate detection test content")

	// First upload — should succeed
	req1, rec1 := createShareRequest(t, "original.txt", testData, false)
	server.handleShare(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("First upload: expected 201, got %d: %s", rec1.Code, rec1.Body.String())
	}
	var first ShareResponse
	json.NewDecoder(rec1.Body).Decode(&first)

	// Second upload — same content, different filename, no force → expect 409
	req2, rec2 := createShareRequest(t, "copy-of-original.txt", testData, false)
	server.handleShare(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Errorf("Duplicate upload: expected 409, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify error body has DUPLICATE_CONTENT code and existing file info
	var errResp ErrorResponse
	if err := json.NewDecoder(rec2.Body).Decode(&errResp); err != nil {
		t.Fatalf("Failed to decode error response: %v", err)
	}
	if errResp.Error.Code != "DUPLICATE_CONTENT" {
		t.Errorf("Expected error code DUPLICATE_CONTENT, got %q", errResp.Error.Code)
	}
	// Details should contain existing file info
	details, ok := errResp.Error.Details.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected details to be a map, got %T", errResp.Error.Details)
	}
	if details["cid"] != first.CID {
		t.Errorf("Expected existing CID %q, got %q", first.CID, details["cid"])
	}
	if details["filename"] != "original.txt" {
		t.Errorf("Expected existing filename 'original.txt', got %q", details["filename"])
	}
}

// TestHandleShare_DuplicateContent_ForceOverwrite — RED test
// Sharing the same file content with force=true should succeed (200 or 201).
func TestHandleShare_DuplicateContent_ForceOverwrite(t *testing.T) {
	server := newShareTestServer(t)

	testData := []byte("force overwrite test content")

	// First upload
	req1, rec1 := createShareRequest(t, "original.txt", testData, false)
	server.handleShare(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("First upload: expected 201, got %d", rec1.Code)
	}

	// Second upload with force=true → should succeed
	req2, rec2 := createShareRequest(t, "renamed.txt", testData, true)
	server.handleShare(rec2, req2)

	if rec2.Code != http.StatusCreated {
		t.Errorf("Force upload: expected 201, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify file metadata was updated to new filename
	var resp ShareResponse
	json.NewDecoder(rec2.Body).Decode(&resp)
	if resp.CID == "" {
		t.Error("Force upload: response missing CID")
	}

	file, err := server.chunkStore.GetFile(resp.CID)
	if err != nil {
		t.Fatalf("Failed to get file after force upload: %v", err)
	}
	if file.Filename != "renamed.txt" {
		t.Errorf("Expected filename 'renamed.txt' after force, got %q", file.Filename)
	}
}
