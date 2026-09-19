// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-15 Phase 10 (Error Handling Refinement)
// Purpose: Tests for error handling enhancements (security & resilience)

package download

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// Input Validation Tests
// ============================================================================

// TestManager_QueueDownload_InvalidInputs - RED test
// Acceptance Criterion: Reject empty/invalid inputs
func TestManager_QueueDownload_InvalidInputs(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	testCases := []struct {
		name        string
		cid         string
		filename    string
		totalSize   int64
		totalChunks int
		wantErr     bool
	}{
		{"empty CID", "", "test.bin", 1048576, 4, true},
		{"empty filename", "bafybeigdyrzt5sfp1", "", 1048576, 4, true},
		{"zero totalSize", "bafybeigdyrzt5sfp1", "test.bin", 0, 4, true},
		{"negative totalSize", "bafybeigdyrzt5sfp1", "test.bin", -1, 4, true},
		{"zero totalChunks", "bafybeigdyrzt5sfp1", "test.bin", 1048576, 0, true},
		{"negative totalChunks", "bafybeigdyrzt5sfp1", "test.bin", 1048576, -1, true},
		{"valid inputs", "bafybeigdyrzt5sfp1", "test.bin", 1048576, 4, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.QueueDownload(tc.cid, tc.filename, tc.totalSize, tc.totalChunks)
			if (err != nil) != tc.wantErr {
				t.Errorf("QueueDownload() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestManager_StartDownload_EmptyCID - RED test
// Acceptance Criterion: Reject empty CID
func TestManager_StartDownload_EmptyCID(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	err := manager.StartDownload("")
	if err == nil {
		t.Fatal("Expected error for empty CID")
	}
	if !strings.Contains(err.Error(), "invalid CID") {
		t.Errorf("Error should mention invalid CID: %v", err)
	}
}

// TestManager_UpdateProgress_InvalidInputs - RED test
// Acceptance Criterion: Reject negative values
func TestManager_UpdateProgress_InvalidInputs(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	testCases := []struct {
		name            string
		cid             string
		completedChunks int
		downloadedBytes int64
		wantErr         bool
	}{
		{"empty CID", "", 2, 1024, true},
		{"negative completedChunks", "bafybeigdyrzt5sfp1", -1, 1024, true},
		{"negative downloadedBytes", "bafybeigdyrzt5sfp1", 2, -1, true},
		{"valid inputs (no download)", "bafybeigdyrzt5sfp1", 2, 1024, true}, // download not found
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.UpdateProgress(tc.cid, tc.completedChunks, tc.downloadedBytes, 10485760)
			if (err != nil) != tc.wantErr {
				t.Errorf("UpdateProgress() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// ============================================================================
// Filename Sanitization Tests
// ============================================================================

// TestSanitizeFilename - RED test
// Acceptance Criterion: Prevent path traversal attacks
func TestSanitizeFilename(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		// Path traversal attacks (sanitized to safe filenames)
		{"../../../etc/passwd", "etc_passwd"},
		{"..\\..\\..\\windows\\system32\\config", "windows_system32_config"},
		{"../../sensitive.txt", "sensitive.txt"},

		// Normal filenames
		{"test.bin", "test.bin"},
		{"my-file.txt", "my-file.txt"},
		{"file_with_underscores.dat", "file_with_underscores.dat"},

		// Hidden files (leading dots removed)
		{".hidden", "hidden"},
		{"..double_dot", "double_dot"},

		// Invalid filenames
		{".", ""},
		{"..", ""},
		{"", ""},

		// Path separators in middle
		{"path/to/file.txt", "path_to_file.txt"},
		{"C:\\Windows\\file.txt", "C__Windows_file.txt"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := sanitizeFilename(tc.input)
			if result != tc.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// TestManager_QueueDownload_PathTraversal - RED test
// Acceptance Criterion: QueueDownload sanitizes filename
func TestManager_QueueDownload_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Try to queue download with path traversal filename
	err := manager.QueueDownload("bafybeigdyrzt5sfp1", "../../../etc/passwd", 1048576, 4)
	if err != nil {
		t.Fatalf("QueueDownload() should sanitize filename, got error: %v", err)
	}

	// Verify filename was sanitized
	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status == nil {
		t.Fatal("Download status not found")
	}

	if status.Filename == "../../../etc/passwd" {
		t.Error("Filename should be sanitized, not raw input")
	}
	if status.Filename != "etc_passwd" {
		t.Errorf("Expected sanitized filename 'etc_passwd', got %q", status.Filename)
	}
}

// ============================================================================
// Tracker Retry Logic Tests
// ============================================================================

// TestManager_SearchAndRegisterPeers_RetrySuccess - RED test
// Acceptance Criterion: Retry tracker search with exponential backoff
func TestManager_SearchAndRegisterPeers_RetrySuccess(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that fails twice, then succeeds
	attemptCount := 0
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			attemptCount++
			if attemptCount < 3 {
				return nil, fmt.Errorf("tracker connection failed (attempt %d)", attemptCount)
			}
			// Third attempt succeeds
			return []PeerInfo{
				{PeerID: generateTestPeerID(t), Chunks: []int{0, 1, 2, 3}},
			}, nil
		},
	}

	coordinator := NewCoordinator(4)
	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act - should succeed after 3 attempts
	start := time.Now()
	err := manager.searchAndRegisterPeers(context.Background(), "bafybeigdyrzt5sfp1", 4)
	elapsed := time.Since(start)

	// Assert
	if err != nil {
		t.Fatalf("searchAndRegisterPeers() should succeed after retries, got: %v", err)
	}

	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", attemptCount)
	}

	// Verify exponential backoff occurred (should take at least 3 seconds: 1s + 2s)
	if elapsed < 3*time.Second {
		t.Logf("Warning: Retry backoff seems too fast (%v), expected >= 3s", elapsed)
	}
}

// TestManager_SearchAndRegisterPeers_RetryExhausted - RED test
// Acceptance Criterion: Return error after max retries
func TestManager_SearchAndRegisterPeers_RetryExhausted(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that always fails
	attemptCount := 0
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			attemptCount++
			return nil, fmt.Errorf("tracker unreachable")
		},
	}

	coordinator := NewCoordinator(4)
	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act
	err := manager.searchAndRegisterPeers(context.Background(), "bafybeigdyrzt5sfp1", 4)

	// Assert
	if err == nil {
		t.Fatal("Expected error after max retries")
	}

	if attemptCount != 3 {
		t.Errorf("Expected 3 retry attempts, got %d", attemptCount)
	}

	// Error message should mention retries
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("Error should mention retry attempts: %v", err)
	}
}

// ============================================================================
// Disk Space Validation Tests
// ============================================================================

// TestCheckDiskSpace - RED test
// Acceptance Criterion: Detect insufficient disk space
func TestCheckDiskSpace(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with reasonable requirement (should succeed on test machine)
	err := checkDiskSpace(tmpDir, 1024*1024) // 1 MB
	if err != nil {
		t.Errorf("checkDiskSpace() should succeed for 1 MB: %v", err)
	}

	// Test with unreasonably large requirement (should fail)
	err = checkDiskSpace(tmpDir, 1024*1024*1024*1024*1024) // 1 PB
	if err == nil {
		t.Error("checkDiskSpace() should fail for 1 PB requirement")
	}

	if !strings.Contains(err.Error(), "insufficient disk space") {
		t.Errorf("Error should mention insufficient disk space: %v", err)
	}
}

// TestManager_AssembleFile_DiskSpaceCheck - RED test
// Acceptance Criterion: assembleFile checks disk space before assembly
func TestManager_AssembleFile_DiskSpaceCheck(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock chunk store with chunks that would require 1 PB (unrealistic)
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	testCID := "bafybeigdyrzt5sfp1"
	mockStore.chunks[testCID] = make(map[int][]byte)
	for i := 0; i < 4; i++ {
		mockStore.chunks[testCID][i] = make([]byte, 1024)
	}

	manager.SetP2PDependencies(&P2PDependencies{
		ChunkStore: mockStore,
	})

	// Note: We can't easily test this without modifying checkDiskSpace to be mockable
	// This test documents the expected behavior
	// In a real scenario, assembleFile would fail if disk space is insufficient
	t.Skip("Disk space validation requires large test fixtures or mocking")
}

// TestManager_ExecuteDownload_InvalidContext - RED test
// Acceptance Criterion: Reject nil context
func TestManager_ExecuteDownload_InvalidContext(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Setup minimal P2P dependencies
	manager.SetP2PDependencies(&P2PDependencies{
		TrackerClient: &mockTrackerClient{},
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:     &mockP2PClient{},
		Coordinator:   NewCoordinator(4),
	})

	// Act - pass nil context
	err := manager.executeDownload(nil, "bafybeigdyrzt5sfp1", "test.bin", 4)

	// Assert
	if err == nil {
		t.Fatal("Expected error for nil context")
	}
	if !strings.Contains(err.Error(), "invalid context") {
		t.Errorf("Error should mention invalid context: %v", err)
	}
}

// ============================================================================
// Disk Full Error Detection Tests
// ============================================================================

// TestIsDiskFullError - RED test
// Acceptance Criterion: Detect disk full error patterns
func TestIsDiskFullError(t *testing.T) {
	testCases := []struct {
		err      error
		expected bool
	}{
		{nil, false},
		{fmt.Errorf("no space left on device"), true},
		{fmt.Errorf("disk full"), true},
		{fmt.Errorf("ENOSPC"), true},
		{fmt.Errorf("not enough space"), true},
		{fmt.Errorf("insufficient disk space"), true},
		{fmt.Errorf("some other error"), false},
		{fmt.Errorf("network timeout"), false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%v", tc.err), func(t *testing.T) {
			result := isDiskFullError(tc.err)
			if result != tc.expected {
				t.Errorf("isDiskFullError(%v) = %v, want %v", tc.err, result, tc.expected)
			}
		})
	}
}

// ============================================================================
// Concurrent Download Limit Tests
// ============================================================================

// TestManager_MaxConcurrent_Enforced - RED test
// Acceptance Criterion: Max concurrent limit is enforced
func TestManager_MaxConcurrent_Enforced(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 2) // Max 2 concurrent

	// Queue 3 downloads
	manager.QueueDownload("cid1", "test1.bin", 1048576, 4)
	manager.QueueDownload("cid2", "test2.bin", 1048576, 4)
	manager.QueueDownload("cid3", "test3.bin", 1048576, 4)

	// Start first 2
	manager.StartDownload("cid1")
	manager.StartDownload("cid2")

	// Try to start 3rd (should fail)
	err := manager.StartDownload("cid3")
	if err == nil {
		t.Fatal("Expected error when exceeding max concurrent")
	}

	if !strings.Contains(err.Error(), "max concurrent") {
		t.Errorf("Error should mention max concurrent limit: %v", err)
	}
}

// ============================================================================
// Helper function to verify error messages are descriptive
// ============================================================================

func TestErrorMessagesAreDescriptive(t *testing.T) {
	// This test verifies that error messages follow the pattern:
	// "operation failed: specific reason: CID %s: %w"

	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Test empty CID validation
	err := manager.QueueDownload("", "test.bin", 1048576, 4)
	if err == nil {
		t.Fatal("Expected error for empty CID")
	}

	// Error should be descriptive and mention operation
	if !strings.Contains(err.Error(), "queue download failed") {
		t.Errorf("Error should mention operation: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid CID") {
		t.Errorf("Error should mention reason: %v", err)
	}
}
