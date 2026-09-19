// Package: internal/daemon/upload
// Feature: F-010 (P2P Transfer Protocol), F-029 (Transfer Page E2E)
// Story: US-010-07 (Upload Queue & Seeding), US-029-23 (Upload Status Enrichment)
// Purpose: TDD tests for upload manager with fair queuing and status enrichment

package upload

import (
	"context"
	"testing"
	"time"
)

// TestManager_QueueUpload - RED test
// Acceptance Criterion: Queue chunk upload requests
func TestManager_QueueUpload(t *testing.T) {
	// Arrange
	manager := NewManager(10) // Max 10 concurrent uploads

	// Act - queue an upload request
	err := manager.QueueUpload("12D3KooWPeer1", "bafybeigdyrzt5sfpfile", 0)

	// Assert
	if err != nil {
		t.Fatalf("QueueUpload() failed: %v", err)
	}

	// Verify upload is queued
	queueSize := manager.GetQueueSize()
	if queueSize != 1 {
		t.Errorf("Expected queue size 1, got %d", queueSize)
	}
}

// TestManager_ProcessUpload - RED test
// Acceptance Criterion: Process queued uploads and serve chunks
func TestManager_ProcessUpload(t *testing.T) {
	// Arrange
	manager := NewManager(10)
	manager.QueueUpload("12D3KooWPeer1", "bafybeigdyrzt5sfpfile", 0)

	// Act - process next upload
	upload := manager.GetNextUpload()

	// Assert
	if upload == nil {
		t.Fatal("GetNextUpload() should return queued upload")
	}

	if upload.PeerID != "12D3KooWPeer1" {
		t.Errorf("Expected peer 12D3KooWPeer1, got %s", upload.PeerID)
	}
	if upload.FileCID != "bafybeigdyrzt5sfpfile" {
		t.Errorf("Expected file CID bafybeigdyrzt5sfpfile, got %s", upload.FileCID)
	}
	if upload.ChunkIndex != 0 {
		t.Errorf("Expected chunk index 0, got %d", upload.ChunkIndex)
	}
}

// TestManager_FairQueuing - RED test
// Acceptance Criterion: Round-robin scheduling across peers (fair queuing)
func TestManager_FairQueuing(t *testing.T) {
	// Arrange
	manager := NewManager(10)

	// Queue multiple uploads from different peers
	manager.QueueUpload("12D3KooWPeer1", "file1", 0)
	manager.QueueUpload("12D3KooWPeer1", "file1", 1) // Same peer, chunk 1
	manager.QueueUpload("12D3KooWPeer2", "file2", 0) // Different peer
	manager.QueueUpload("12D3KooWPeer1", "file1", 2) // Same peer again

	// Act - process uploads
	upload1 := manager.GetNextUpload()
	upload2 := manager.GetNextUpload()
	upload3 := manager.GetNextUpload()

	// Assert - should alternate between peers (fair queuing)
	// Order should be: Peer1-chunk0, Peer2-chunk0, Peer1-chunk1
	// NOT: Peer1-chunk0, Peer1-chunk1, Peer1-chunk2 (unfair)

	if upload1.PeerID != "12D3KooWPeer1" {
		t.Errorf("First upload expected Peer1, got %s", upload1.PeerID)
	}

	if upload2.PeerID != "12D3KooWPeer2" {
		t.Errorf("Second upload expected Peer2 (fair queuing), got %s", upload2.PeerID)
	}

	if upload3.PeerID != "12D3KooWPeer1" {
		t.Errorf("Third upload expected Peer1, got %s", upload3.PeerID)
	}
}

// TestManager_MaxConcurrentUploads - RED test
// Acceptance Criterion: Limit concurrent uploads to max (default 10)
func TestManager_MaxConcurrentUploads(t *testing.T) {
	// Arrange
	manager := NewManager(2) // Max 2 concurrent

	// Act - start 2 uploads
	manager.StartUpload("upload1")
	manager.StartUpload("upload2")

	// Assert - should be at capacity
	active := manager.GetActiveCount()
	if active != 2 {
		t.Errorf("Expected 2 active uploads, got %d", active)
	}

	// Try to start 3rd upload
	canStart := manager.CanStartUpload()
	if canStart {
		t.Error("Should not allow 3rd upload when at max capacity")
	}
}

// TestManager_CompleteUpload - RED test
// Acceptance Criterion: Mark upload as complete and free slot
func TestManager_CompleteUpload(t *testing.T) {
	// Arrange
	manager := NewManager(2)
	manager.StartUpload("upload1")
	manager.StartUpload("upload2")

	// Act - complete one upload
	manager.CompleteUpload("upload1")

	// Assert - should have freed slot
	active := manager.GetActiveCount()
	if active != 1 {
		t.Errorf("Expected 1 active upload after completion, got %d", active)
	}

	// Should allow new upload
	if !manager.CanStartUpload() {
		t.Error("Should allow new upload after completion")
	}
}

// TestManager_TrackUploadStats - RED test
// Acceptance Criterion: Track upload statistics (bytes sent, peers served)
func TestManager_TrackUploadStats(t *testing.T) {
	// Arrange
	manager := NewManager(10)
	fileCID := "bafybeigdyrzt5sfpfile"

	// Act - record upload statistics
	manager.RecordUpload(fileCID, 262144) // 256 KB uploaded

	// Assert - stats should be updated
	stats := manager.GetStats(fileCID)
	if stats == nil {
		t.Fatal("Stats not found for file")
	}

	if stats.BytesSent != 262144 {
		t.Errorf("Expected 262144 bytes sent, got %d", stats.BytesSent)
	}
	if stats.RequestsServed != 1 {
		t.Errorf("Expected 1 request served, got %d", stats.RequestsServed)
	}
}

// TestManager_GetActiveUploads - RED test
// Acceptance Criterion: List all active uploads (for REST polling endpoint)
func TestManager_GetActiveUploads(t *testing.T) {
	// Arrange
	manager := NewManager(10)

	// Start 2 uploads
	manager.StartUpload("upload1")
	manager.StartUpload("upload2")

	// Act - get active uploads
	activeUploads := manager.GetActiveUploads()

	// Assert
	if len(activeUploads) != 2 {
		t.Errorf("Expected 2 active uploads, got %d", len(activeUploads))
	}
}

// TestManager_BandwidthThrottling - RED test
// Acceptance Criterion: Apply bandwidth limit (configurable max upload speed)
func TestManager_BandwidthThrottling(t *testing.T) {
	// Arrange - 1 MB/s upload limit
	manager := NewManager(10)
	manager.SetBandwidthLimit(1048576) // 1 MB/s

	// Act - request to send 512 KB
	allowed := manager.AllowUpload(524288)

	// Assert - should allow (within limit)
	if !allowed {
		t.Error("Upload within bandwidth limit should be allowed")
	}

	// Immediately request another 512 KB (total 1 MB)
	allowed = manager.AllowUpload(524288)
	if !allowed {
		t.Error("Second upload within limit should be allowed")
	}

	// Request another 512 KB (exceeds the 1 MB/s bucket; refilling it takes 0.5s)
	allowed = manager.AllowUpload(524288)
	if allowed {
		t.Error("Upload exceeding bandwidth limit should be denied")
	}
	if got := manager.BandwidthLimit(); got != 1048576 {
		t.Errorf("BandwidthLimit = %d", got)
	}
}

// TestManager_WaitUpload verifies the blocking path used by the chunk provider:
// unlimited without a cap, paced with one, and cancellable.
func TestManager_WaitUpload(t *testing.T) {
	manager := NewManager(10)
	if err := manager.WaitUpload(context.Background(), 10<<20); err != nil {
		t.Fatalf("unlimited: %v", err)
	}

	manager.SetBandwidthLimit(100_000) // 100 KB/s
	start := time.Now()
	if err := manager.WaitUpload(context.Background(), 100_000); err != nil { // drains the bucket
		t.Fatal(err)
	}
	if err := manager.WaitUpload(context.Background(), 30_000); err != nil { // ~300ms of refill
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("second chunk returned after %v; expected pacing at 100 KB/s", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := manager.WaitUpload(ctx, 1<<20); err == nil {
		t.Error("expected context deadline while waiting for 1 MiB at 100 KB/s")
	}

	manager.SetBandwidthLimit(0)
	if err := manager.WaitUpload(context.Background(), 1<<20); err != nil {
		t.Errorf("after reset: %v", err)
	}
}

// === F-029 Upload Status Enrichment (US-029-23) ===

// TestGetDetailedUploads_WithActiveAndStats verifies GetDetailedUploads returns
// enriched UploadStatusDetail structs combining active state with stats.
func TestGetDetailedUploads_WithActiveAndStats(t *testing.T) {
	manager := NewManager(10)

	// Record stats for two files
	manager.RecordUpload("QmFileA", 1024)
	manager.RecordUpload("QmFileA", 2048) // Second chunk same file
	manager.RecordUpload("QmFileB", 512)

	// Mark one as actively uploading
	manager.StartUpload("QmFileA")

	// Act
	details := manager.GetDetailedUploads()

	// Assert: should have 2 entries (one per fileCID with stats)
	if len(details) != 2 {
		t.Fatalf("expected 2 upload details, got %d", len(details))
	}

	// Find QmFileA entry
	var fileA, fileB *UploadStatusDetail
	for i := range details {
		switch details[i].FileCID {
		case "QmFileA":
			fileA = &details[i]
		case "QmFileB":
			fileB = &details[i]
		}
	}

	if fileA == nil {
		t.Fatal("QmFileA not found in details")
	}
	if fileA.BytesSent != 3072 {
		t.Errorf("QmFileA bytes_sent: expected 3072, got %d", fileA.BytesSent)
	}
	if fileA.RequestsServed != 2 {
		t.Errorf("QmFileA requests_served: expected 2, got %d", fileA.RequestsServed)
	}
	if !fileA.Active {
		t.Error("QmFileA should be active")
	}

	if fileB == nil {
		t.Fatal("QmFileB not found in details")
	}
	if fileB.BytesSent != 512 {
		t.Errorf("QmFileB bytes_sent: expected 512, got %d", fileB.BytesSent)
	}
	if fileB.Active {
		t.Error("QmFileB should not be active")
	}
}

// TestGetDetailedUploads_EmptyReturnsEmptySlice verifies empty manager returns
// empty (not nil) slice for safe JSON serialization.
func TestGetDetailedUploads_EmptyReturnsEmptySlice(t *testing.T) {
	manager := NewManager(10)

	details := manager.GetDetailedUploads()

	if details == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(details) != 0 {
		t.Errorf("expected 0 details, got %d", len(details))
	}
}
