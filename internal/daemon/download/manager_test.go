// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: TDD tests for download manager (state machine + queue)

package download

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stonkagents/agent/pkg/protocol"
)

// generateTestPeerID creates a valid peer.ID for testing
func generateTestPeerID(t *testing.T) string {
	t.Helper()

	// Generate a random Ed25519 key pair
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	// Create peer ID from public key
	peerID, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("Failed to create peer ID: %v", err)
	}

	return peerID.String()
}

// TestManager_QueueDownload - RED test
// Acceptance Criterion: Downloads have states: queued → active → paused → completed → failed
func TestManager_QueueDownload(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3) // Max 3 concurrent downloads

	// Act - queue a download
	err := manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)

	// Assert
	if err != nil {
		t.Fatalf("QueueDownload() failed: %v", err)
	}

	// Verify download is in queued state
	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status == nil {
		t.Fatal("Download not found after queueing")
	}
	if status.State != StateQueued {
		t.Errorf("Expected state %q, got %q", StateQueued, status.State)
	}
}

// TestManager_StartDownload - RED test
// Acceptance Criterion: Transition from queued → active
func TestManager_StartDownload(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)

	// Act - start the download
	err := manager.StartDownload("bafybeigdyrzt5sfp1")

	// Assert
	if err != nil {
		t.Fatalf("StartDownload() failed: %v", err)
	}

	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status.State != StateActive {
		t.Errorf("Expected state %q, got %q", StateActive, status.State)
	}
}

// TestManager_MaxConcurrentDownloads - RED test
// Acceptance Criterion: Max concurrent downloads: 3 (configurable)
func TestManager_MaxConcurrentDownloads(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 2) // Max 2 concurrent

	// Queue and start 2 downloads
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)
	manager.QueueDownload("bafybeigdyrzt5sfp2", "test2.bin", 1048576, 4)
	manager.StartDownload("bafybeigdyrzt5sfp1")
	manager.StartDownload("bafybeigdyrzt5sfp2")

	// Act - try to start a 3rd download
	manager.QueueDownload("bafybeigdyrzt5sfp3", "test3.bin", 1048576, 4)
	err := manager.StartDownload("bafybeigdyrzt5sfp3")

	// Assert - should fail (at max capacity)
	if err == nil {
		t.Error("Expected error when exceeding max concurrent downloads")
	}

	// Verify 3rd download remains in queued state
	status := manager.GetStatus("bafybeigdyrzt5sfp3")
	if status.State != StateQueued {
		t.Errorf("3rd download should remain queued, got state %q", status.State)
	}
}

// TestManager_PauseDownload - RED test
// Acceptance Criterion: Pause active download
func TestManager_PauseDownload(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)
	manager.StartDownload("bafybeigdyrzt5sfp1")

	// Act - pause the download
	err := manager.PauseDownload("bafybeigdyrzt5sfp1")

	// Assert
	if err != nil {
		t.Fatalf("PauseDownload() failed: %v", err)
	}

	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status.State != StatePaused {
		t.Errorf("Expected state %q, got %q", StatePaused, status.State)
	}
}

// TestManager_ResumeDownload - RED test
// Acceptance Criterion: Resume paused download
func TestManager_ResumeDownload(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)
	manager.StartDownload("bafybeigdyrzt5sfp1")
	manager.PauseDownload("bafybeigdyrzt5sfp1")

	// Act - resume the download
	err := manager.ResumeDownload("bafybeigdyrzt5sfp1")

	// Assert
	if err != nil {
		t.Fatalf("ResumeDownload() failed: %v", err)
	}

	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status.State != StateActive {
		t.Errorf("Expected state %q after resume, got %q", StateActive, status.State)
	}
}

// TestManager_CompleteDownload - RED test
// Acceptance Criterion: Mark download as completed
func TestManager_CompleteDownload(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)
	manager.StartDownload("bafybeigdyrzt5sfp1")

	// Act - mark as completed
	err := manager.CompleteDownload("bafybeigdyrzt5sfp1")

	// Assert
	if err != nil {
		t.Fatalf("CompleteDownload() failed: %v", err)
	}

	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status.State != StateCompleted {
		t.Errorf("Expected state %q, got %q", StateCompleted, status.State)
	}

	// Verify completion time is set
	if status.CompletedAt == nil {
		t.Error("CompletedAt timestamp should be set")
	}
}

// TestManager_FailDownload - RED test
// Acceptance Criterion: Mark download as failed with error message
func TestManager_FailDownload(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)
	manager.StartDownload("bafybeigdyrzt5sfp1")

	// Act - mark as failed
	err := manager.FailDownload("bafybeigdyrzt5sfp1", "Network timeout")

	// Assert
	if err != nil {
		t.Fatalf("FailDownload() failed: %v", err)
	}

	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status.State != StateFailed {
		t.Errorf("Expected state %q, got %q", StateFailed, status.State)
	}
	if status.ErrorMessage == nil || *status.ErrorMessage != "Network timeout" {
		t.Errorf("Expected error message %q, got %v", "Network timeout", status.ErrorMessage)
	}
}

// TestManager_GetActiveDownloads - RED test
// Acceptance Criterion: List all active downloads (for REST polling endpoint)
func TestManager_GetActiveDownloads(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create downloads with different states
	manager.QueueDownload("bafybeigdyrzt5sfp1", "queued.bin", 1048576, 4)
	manager.QueueDownload("bafybeigdyrzt5sfp2", "active.bin", 1048576, 4)
	manager.QueueDownload("bafybeigdyrzt5sfp3", "completed.bin", 1048576, 4)

	manager.StartDownload("bafybeigdyrzt5sfp2")
	manager.CompleteDownload("bafybeigdyrzt5sfp3")

	// Act - get active downloads
	activeDownloads := manager.GetActiveDownloads()

	// Assert - should return queued and active, but NOT completed
	if len(activeDownloads) != 2 {
		t.Fatalf("Expected 2 active downloads, got %d", len(activeDownloads))
	}

	states := make(map[DownloadState]bool)
	for _, dl := range activeDownloads {
		states[dl.State] = true
	}

	if !states[StateQueued] {
		t.Error("Queued download not in active list")
	}
	if !states[StateActive] {
		t.Error("Active download not in active list")
	}
	if states[StateCompleted] {
		t.Error("Completed download should not be in active list")
	}
}

// TestGetVisibleDownloadsIncludesPaused - F-029 Task 2 RED test
// Acceptance Criterion: GetVisibleDownloads returns queued + active + paused (not completed/failed)
func TestGetVisibleDownloadsIncludesPaused(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	_ = m.QueueDownload("cid-1", "a.txt", 100, 1)
	_ = m.QueueDownload("cid-2", "b.txt", 200, 2)
	_ = m.StartDownload("cid-1")
	_ = m.PauseDownload("cid-1")
	_ = m.QueueDownload("cid-3", "c.txt", 300, 3)
	_ = m.CompleteDownload("cid-3") // should NOT appear

	visible := m.GetVisibleDownloads()
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible, got %d", len(visible))
	}

	states := map[DownloadState]int{}
	for _, d := range visible {
		states[d.State]++
	}
	if states[StatePaused] != 1 {
		t.Errorf("expected 1 paused, got %d", states[StatePaused])
	}
	if states[StateQueued] != 1 {
		t.Errorf("expected 1 queued, got %d", states[StateQueued])
	}
}

// TestGetCountsAllFiveStates - F-029 Task 2 RED test
// Acceptance Criterion: GetCounts returns DownloadCounts struct with all 5 states
func TestGetCountsAllFiveStates(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	_ = m.QueueDownload("cid-q", "q.txt", 100, 1)
	_ = m.QueueDownload("cid-a", "a.txt", 100, 1)
	_ = m.StartDownload("cid-a")
	_ = m.QueueDownload("cid-p", "p.txt", 100, 1)
	_ = m.PauseDownload("cid-p")
	_ = m.QueueDownload("cid-c", "c.txt", 100, 1)
	_ = m.CompleteDownload("cid-c")
	_ = m.QueueDownload("cid-f", "f.txt", 100, 1)
	_ = m.FailDownload("cid-f", "timeout")

	counts := m.GetCounts()
	if counts.Queued != 1 {
		t.Errorf("queued: want 1, got %d", counts.Queued)
	}
	if counts.Active != 1 {
		t.Errorf("active: want 1, got %d", counts.Active)
	}
	if counts.Paused != 1 {
		t.Errorf("paused: want 1, got %d", counts.Paused)
	}
	if counts.Completed != 1 {
		t.Errorf("completed: want 1, got %d", counts.Completed)
	}
	if counts.Failed != 1 {
		t.Errorf("failed: want 1, got %d", counts.Failed)
	}
}

// TestLoadIncompleteNormalizesActiveToQueued - F-029 Task 3 RED test
// Acceptance Criterion: Active downloads at crash time normalized to queued
func TestLoadIncompleteNormalizesActiveToQueued(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	// Queue and start a download (persists as "active")
	_ = m.QueueDownload("cid-stuck", "stuck.txt", 1000, 4)
	_ = m.StartDownload("cid-stuck")

	// Simulate daemon crash: create new manager from same dir
	m2 := NewManager(dir, 3)
	err := m2.LoadIncomplete()
	if err != nil {
		t.Fatalf("LoadIncomplete failed: %v", err)
	}

	status := m2.GetStatus("cid-stuck")
	if status == nil {
		t.Fatal("expected download to be loaded")
	}
	if status.State != StateQueued {
		t.Errorf("expected StateQueued after normalize, got %s", status.State)
	}
}

// TestLoadIncompleteDeletesOrphanDirectories - F-029 Task 3 RED test
// Acceptance Criterion: Orphan chunk directories cleaned up on startup
func TestLoadIncompleteDeletesOrphanDirectories(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	// Create a real download (has metadata)
	_ = m.QueueDownload("bafyvalidcidfortestingAAAAAAAAAAAAAAAAAAAAAAAA", "real.txt", 1000, 4)

	// Create an orphan directory (no metadata.json inside)
	orphanDir := filepath.Join(dir, "bafyorphanfortestAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	os.MkdirAll(orphanDir, 0755)
	// Write a fake chunk file inside so it's not empty
	os.WriteFile(filepath.Join(orphanDir, "chunk_0.dat"), []byte("orphan chunk"), 0644)

	// Simulate restart
	m2 := NewManager(dir, 3)
	err := m2.LoadIncomplete()
	if err != nil {
		t.Fatalf("LoadIncomplete failed: %v", err)
	}

	// Orphan directory should be deleted
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Error("expected orphan directory to be deleted by LoadIncomplete")
	}

	// Valid download should still be loaded
	status := m2.GetStatus("bafyvalidcidfortestingAAAAAAAAAAAAAAAAAAAAAAAA")
	if status == nil {
		t.Fatal("expected valid download to still be loaded")
	}
}

// TestManager_UpdateProgress - RED test
// Acceptance Criterion: Track download progress for REST polling
func TestManager_UpdateProgress(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4)
	manager.StartDownload("bafybeigdyrzt5sfp1")

	// Act - update progress
	err := manager.UpdateProgress("bafybeigdyrzt5sfp1", 2, 524288, 10485760) // 2 chunks, 512 KB, 10 MB/s

	// Assert
	if err != nil {
		t.Fatalf("UpdateProgress() failed: %v", err)
	}

	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status.CompletedChunks != 2 {
		t.Errorf("Expected 2 completed chunks, got %d", status.CompletedChunks)
	}
	if status.DownloadedBytes != 524288 {
		t.Errorf("Expected 524288 downloaded bytes, got %d", status.DownloadedBytes)
	}
	if status.SpeedBPS != 10485760 {
		t.Errorf("Expected 10485760 speed, got %d", status.SpeedBPS)
	}

	// Verify progress percentage
	expectedProgress := 0.5 // 2 out of 4 chunks
	if status.Progress != expectedProgress {
		t.Errorf("Expected progress %.2f, got %.2f", expectedProgress, status.Progress)
	}
}

// TestManager_LoadIncompleteOnStartup - RED test
// Acceptance Criterion: Resume incomplete downloads on daemon startup
func TestManager_LoadIncompleteOnStartup(t *testing.T) {
	// Arrange - create manager, add downloads, shut down
	tmpDir := t.TempDir()
	manager1 := NewManager(tmpDir, 3)
	manager1.QueueDownload("bafybeigdyrzt5sfp1", "resume1.bin", 1048576, 4)
	manager1.QueueDownload("bafybeigdyrzt5sfp2", "resume2.bin", 1048576, 4)
	manager1.StartDownload("bafybeigdyrzt5sfp1")
	// Persist chunks to disk (workers do this; UpdateProgress is now in-memory only)
	manager1.repo.UpdateChunkCompletion("bafybeigdyrzt5sfp1", 0, true)
	manager1.repo.UpdateChunkCompletion("bafybeigdyrzt5sfp1", 1, true)
	manager1.UpdateProgress("bafybeigdyrzt5sfp1", 2, 524288, 10485760) // 50% complete

	// Simulate daemon restart - create new manager with same directory
	manager2 := NewManager(tmpDir, 3)

	// Act - load incomplete downloads
	err := manager2.LoadIncomplete()

	// Assert
	if err != nil {
		t.Fatalf("LoadIncomplete() failed: %v", err)
	}

	// Verify download 1 was resumed
	status1 := manager2.GetStatus("bafybeigdyrzt5sfp1")
	if status1 == nil {
		t.Fatal("Download 1 not loaded after restart")
	}
	if status1.CompletedChunks != 2 {
		t.Errorf("Progress not preserved: expected 2 chunks, got %d", status1.CompletedChunks)
	}

	// Verify download 2 was resumed
	status2 := manager2.GetStatus("bafybeigdyrzt5sfp2")
	if status2 == nil {
		t.Fatal("Download 2 not loaded after restart")
	}
}

// TestManager_CalculateETA - RED test
// Acceptance Criterion: Calculate estimated time remaining
func TestManager_CalculateETA(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	manager.QueueDownload("bafybeigdyrzt5sfp1", "test1.bin", 1048576, 4) // 1 MB total
	manager.StartDownload("bafybeigdyrzt5sfp1")

	// 512 KB downloaded at 10 MB/s = 512 KB remaining = ~0.05 seconds
	manager.UpdateProgress("bafybeigdyrzt5sfp1", 2, 524288, 10485760)

	// Act
	status := manager.GetStatus("bafybeigdyrzt5sfp1")

	// Assert
	if status.ETASEC == 0 {
		t.Error("ETA should be calculated for active download with speed > 0")
	}

	// ETA should be reasonable (remaining bytes / speed)
	// 524288 bytes remaining / 10485760 bytes per second ≈ 0.05 seconds
	if status.ETASEC > 1 {
		t.Errorf("ETA seems incorrect: got %d seconds for 512 KB at 10 MB/s", status.ETASEC)
	}
}

// TestManager_GetAllPeersForChunk - RED test
// Acceptance Criterion: Convert peer strings from Coordinator to []peer.ID for P2PClient
func TestManager_GetAllPeersForChunk(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Generate valid test peer IDs
	peer1 := generateTestPeerID(t)
	peer2 := generateTestPeerID(t)
	peer3 := generateTestPeerID(t)

	// Create coordinator with peers
	coordinator := NewCoordinator(4)
	coordinator.RegisterPeer(peer1, []int{0, 1}, []string{})
	coordinator.RegisterPeer(peer2, []int{0, 2}, []string{})
	coordinator.RegisterPeer(peer3, []int{0, 3}, []string{})

	// Inject P2P dependencies
	manager.SetP2PDependencies(&P2PDependencies{
		Coordinator: coordinator,
	})

	// Act - get all peers for chunk 0 (should have 3 peers)
	peerIDs := manager.getAllPeersForChunk("test-cid", 0)

	// Assert - should return 3 valid peer.ID objects
	if len(peerIDs) != 3 {
		t.Errorf("Expected 3 peer IDs for chunk 0, got %d", len(peerIDs))
	}

	// Verify peer IDs are valid (can be converted back to string)
	for _, peerID := range peerIDs {
		peerStr := peerID.String()
		if peerStr == "" {
			t.Error("Peer ID should not be empty")
		}
	}
}

// TestManager_GetAllPeersForChunk_InvalidPeerID - RED test
// Acceptance Criterion: Skip invalid peer IDs without erroring
func TestManager_GetAllPeersForChunk_InvalidPeerID(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Generate valid test peer ID
	peer1 := generateTestPeerID(t)

	// Create coordinator with mix of valid and invalid peer IDs
	coordinator := NewCoordinator(4)

	// Manually inject valid and invalid peer IDs
	coordinator.RegisterPeer(peer1, []int{0}, []string{})
	// Inject invalid peer ID directly into chunkToPeers map
	coordinator.chunkToPeers[0] = append(coordinator.chunkToPeers[0], "INVALID_PEER_ID")

	manager.SetP2PDependencies(&P2PDependencies{
		Coordinator: coordinator,
	})

	// Act - should skip invalid peer ID
	peerIDs := manager.getAllPeersForChunk("test-cid", 0)

	// Assert - should return only 1 valid peer (skipped the invalid one)
	if len(peerIDs) != 1 {
		t.Errorf("Expected 1 valid peer ID (invalid skipped), got %d", len(peerIDs))
	}
}

// TestManager_GetAllPeersForChunk_EmptyChunk - RED test
// Acceptance Criterion: Handle empty peer list gracefully
func TestManager_GetAllPeersForChunk_EmptyChunk(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	coordinator := NewCoordinator(4)
	// Don't register any peers

	manager.SetP2PDependencies(&P2PDependencies{
		Coordinator: coordinator,
	})

	// Act - get peers for chunk with no peers
	peerIDs := manager.getAllPeersForChunk("test-cid", 0)

	// Assert - should return empty slice
	if len(peerIDs) != 0 {
		t.Errorf("Expected 0 peer IDs for empty chunk, got %d", len(peerIDs))
	}
	if peerIDs == nil {
		t.Error("Should return empty slice, not nil")
	}
}

// TestManager_GetAllPeersForChunk_NilP2PDeps - RED test
// Acceptance Criterion: Handle nil P2P dependencies gracefully
func TestManager_GetAllPeersForChunk_NilP2PDeps(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)
	// Don't set P2P dependencies (nil)

	// Act - should handle nil gracefully
	peerIDs := manager.getAllPeersForChunk("test-cid", 0)

	// Assert - should return empty slice
	if len(peerIDs) != 0 {
		t.Errorf("Expected 0 peer IDs with nil deps, got %d", len(peerIDs))
	}
}

// ============================================================================
// Phase 2: Tracker Search & Peer Registration Tests (TDD Security-First)
// ============================================================================

// mockTrackerClient is a mock implementation of TrackerClientInterface for testing
type mockTrackerClient struct {
	searchByCIDFunc func(cid string) ([]PeerInfo, error)
}

func (m *mockTrackerClient) SearchByCID(cid string) ([]PeerInfo, error) {
	if m.searchByCIDFunc != nil {
		return m.searchByCIDFunc(cid)
	}
	return []PeerInfo{}, nil
}

func (m *mockTrackerClient) UpdateAvailability(cid string, chunks []int) error {
	return nil
}

// TestManager_SearchAndRegisterPeers_Success - RED test
// Acceptance Criterion: Happy path with 2 peers
func TestManager_SearchAndRegisterPeers_Success(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that returns 2 peers
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			return []PeerInfo{
				{PeerID: "peer1", Chunks: []int{0, 1, 2, 3}},
				{PeerID: "peer2", Chunks: []int{0, 1}},
			}, nil
		},
	}

	// Create coordinator
	coordinator := NewCoordinator(4)

	// Setup P2P dependencies
	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act
	err := manager.searchAndRegisterPeers(context.Background(), "bafybeigdyrzt5sfp1", 4)

	// Assert
	if err != nil {
		t.Fatalf("searchAndRegisterPeers() failed: %v", err)
	}

	// Verify peers were registered with coordinator
	peer1Chunks := coordinator.GetPeerChunks("peer1")
	if len(peer1Chunks) != 4 {
		t.Errorf("Expected peer1 to have 4 chunks, got %d", len(peer1Chunks))
	}

	peer2Chunks := coordinator.GetPeerChunks("peer2")
	if len(peer2Chunks) != 2 {
		t.Errorf("Expected peer2 to have 2 chunks, got %d", len(peer2Chunks))
	}
}

// TestManager_SearchAndRegisterPeers_TrackerError - RED test
// Acceptance Criterion: Tracker returns error
func TestManager_SearchAndRegisterPeers_TrackerError(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that returns error
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			return nil, fmt.Errorf("tracker connection failed")
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

	// Assert - should return error
	if err == nil {
		t.Fatal("Expected error when tracker fails")
	}

	// Error message should mention tracker failure
	if err.Error() == "" {
		t.Error("Error message should be descriptive")
	}
}

// TestManager_SearchAndRegisterPeers_NoPeers - RED test
// Acceptance Criterion: Tracker returns empty list
func TestManager_SearchAndRegisterPeers_NoPeers(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that returns empty peer list
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			return []PeerInfo{}, nil
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

	// Assert - should return error
	if err == nil {
		t.Fatal("Expected error when no peers found")
	}

	// Error message should mention no peers
	if err.Error() == "" {
		t.Error("Error message should be descriptive")
	}
}

// TestManager_SearchAndRegisterPeers_InvalidCID - RED test
// Acceptance Criterion: Empty CID string
func TestManager_SearchAndRegisterPeers_InvalidCID(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			// Mock tracker should never be called with empty CID
			t.Error("Tracker should not be called with empty CID")
			return []PeerInfo{}, nil
		},
	}

	coordinator := NewCoordinator(4)
	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act - pass empty CID
	err := manager.searchAndRegisterPeers(context.Background(), "", 4)

	// Assert - should return error before calling tracker
	if err == nil {
		t.Fatal("Expected error when CID is empty")
	}
}

// --- Phase 4 Tests: File Assembly with 3-Layer CID Verification ---

// TestManager_AssembleFile_Success - Phase 4 RED test
// Acceptance Criterion: Assemble file from chunks with 3-layer CID verification
func TestManager_AssembleFile_Success(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock chunk store with 4 chunks
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	// Create test data (4 chunks of 256 KB each = 1 MB)
	chunkSize := 262144 // 256 KB
	testData := make([]byte, chunkSize*4)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	// Chunk the data using protocol.Chunker
	chunker := protocol.NewChunker()
	ctx := context.Background()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "test.bin")
	if err != nil {
		t.Fatalf("Failed to chunk test data: %v", err)
	}

	// Store chunks in mock store
	mockStore.chunks[metadata.FileCID] = make(map[int][]byte)
	for _, chunk := range chunks {
		mockStore.chunks[metadata.FileCID][chunk.ChunkIndex] = chunk.Data
	}

	// Set P2P dependencies
	manager.SetP2PDependencies(&P2PDependencies{
		ChunkStore: mockStore,
	})

	// Act - assemble file
	err = manager.assembleFile(metadata.FileCID, "test.bin", metadata.TotalChunks)

	// Assert
	if err != nil {
		t.Fatalf("assembleFile() failed: %v", err)
	}

	// Verify file was created in baseDir/assets/{cid}/test.bin
	outputPath := fmt.Sprintf("%s/assets/%s/test.bin", tmpDir, metadata.FileCID)
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Errorf("Assembled file not found at %s", outputPath)
	}

	// Verify file content matches original
	assembledData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read assembled file: %v", err)
	}

	if !bytes.Equal(assembledData, testData) {
		t.Errorf("Assembled file content does not match original (size: %d vs %d)", len(assembledData), len(testData))
	}

	// Verify chunks were deleted after successful assembly
	if !mockStore.deleted {
		t.Error("Chunks should be deleted after successful assembly")
	}
}

// TestManager_AssembleFile_MissingChunk - Phase 4 RED test
// Acceptance Criterion: Return descriptive error when chunk is missing
func TestManager_AssembleFile_MissingChunk(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock chunk store with only 3 chunks (chunk 2 missing)
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	testCID := "bafybeigdyrzt5sfp1"
	mockStore.chunks[testCID] = make(map[int][]byte)
	mockStore.chunks[testCID][0] = make([]byte, 256)
	mockStore.chunks[testCID][1] = make([]byte, 256)
	// mockStore.chunks[testCID][2] = MISSING
	mockStore.chunks[testCID][3] = make([]byte, 256)

	manager.SetP2PDependencies(&P2PDependencies{
		ChunkStore: mockStore,
	})

	// Act - attempt to assemble file with missing chunk
	err := manager.assembleFile(testCID, "test.bin", 4)

	// Assert
	if err == nil {
		t.Fatal("Expected error for missing chunk, got nil")
	}

	// Verify error message mentions chunk or missing
	if !stringContains(err.Error(), "chunk") && !stringContains(err.Error(), "missing") && !stringContains(err.Error(), "not found") {
		t.Errorf("Error message should mention missing chunk: %v", err)
	}

	// Verify chunks were NOT deleted (assembly failed)
	if mockStore.deleted {
		t.Error("Chunks should not be deleted after failed assembly")
	}
}

// TestManager_AssembleFile_CIDMismatch - Phase 4 RED test
// Acceptance Criterion: Return security error when final file CID doesn't match
func TestManager_AssembleFile_CIDMismatch(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create valid chunks
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	// Create test data and chunk it
	chunkSize := 262144 // 256 KB
	testData := make([]byte, chunkSize*4)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	chunker := protocol.NewChunker()
	ctx := context.Background()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "test.bin")
	if err != nil {
		t.Fatalf("Failed to chunk test data: %v", err)
	}

	// Store chunks under a DIFFERENT CID (simulating CID mismatch)
	fakeCID := "bafybeigdyrzt5sfp1FAKE"
	mockStore.chunks[fakeCID] = make(map[int][]byte)
	for _, chunk := range chunks {
		mockStore.chunks[fakeCID][chunk.ChunkIndex] = chunk.Data
	}

	manager.SetP2PDependencies(&P2PDependencies{
		ChunkStore: mockStore,
	})

	// Act - attempt to assemble file with wrong CID
	err = manager.assembleFile(fakeCID, "test.bin", metadata.TotalChunks)

	// Assert
	if err == nil {
		t.Fatal("Expected error for CID mismatch, got nil")
	}

	// Verify error message mentions CID or verification
	if !stringContains(err.Error(), "CID") && !stringContains(err.Error(), "verif") && !stringContains(err.Error(), "mismatch") {
		t.Errorf("Error message should mention CID verification: %v", err)
	}

	// Verify chunks were NOT deleted (assembly failed)
	if mockStore.deleted {
		t.Error("Chunks should not be deleted after failed assembly")
	}
}

// TestManager_AssembleFile_DiskWriteError - Phase 4 RED test
// Acceptance Criterion: Don't delete chunks if disk write fails
func TestManager_AssembleFile_DiskWriteError(t *testing.T) {
	// Arrange
	// Use /dev/null or read-only path to simulate write failure
	tmpDir := "/invalid/path/that/does/not/exist"
	manager := NewManager(tmpDir, 3)

	// Create valid chunks
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	testCID := "bafybeigdyrzt5sfp1"
	mockStore.chunks[testCID] = make(map[int][]byte)
	for i := 0; i < 4; i++ {
		mockStore.chunks[testCID][i] = make([]byte, 256)
	}

	manager.SetP2PDependencies(&P2PDependencies{
		ChunkStore: mockStore,
	})

	// Act - attempt to assemble file to invalid path
	err := manager.assembleFile(testCID, "test.bin", 4)

	// Assert
	if err == nil {
		t.Fatal("Expected error for disk write failure, got nil")
	}

	// Verify chunks were NOT deleted (write failed)
	if mockStore.deleted {
		t.Error("Chunks should not be deleted after failed disk write")
	}
}

// ============================================================================
// Phase 5: Download Orchestration Tests (TDD Security-First)
// ============================================================================

// TestManager_ExecuteDownload_Success - Phase 5 RED test
// Acceptance Criterion: Happy path end-to-end orchestration
func TestManager_ExecuteDownload_Success(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create test data (4 chunks)
	chunkSize := 262144 // 256 KB
	testData := make([]byte, chunkSize*4)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	// Chunk the data using protocol.Chunker
	chunker := protocol.NewChunker()
	ctx := context.Background()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "test.bin")
	if err != nil {
		t.Fatalf("Failed to chunk test data: %v", err)
	}

	// Create mock tracker that returns 2 peers
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			peer1 := generateTestPeerID(t)
			peer2 := generateTestPeerID(t)
			return []PeerInfo{
				{PeerID: peer1, Chunks: []int{0, 1, 2, 3}},
				{PeerID: peer2, Chunks: []int{0, 1}},
			}, nil
		},
	}

	// Create mock chunk store
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	// Store chunks in mock store
	mockStore.chunks[metadata.FileCID] = make(map[int][]byte)
	for _, chunk := range chunks {
		mockStore.chunks[metadata.FileCID][chunk.ChunkIndex] = chunk.Data
	}

	// Create coordinator
	coordinator := NewCoordinator(metadata.TotalChunks)

	// Create mock P2P client that returns chunks
	// Thread-safe: uses GetChunk (mutex-protected) instead of direct map access (US-029-P5)
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
			data, _ := mockStore.GetChunk(cid, chunkIndex)
			chunkCID, _ := protocol.CalculateChunkCID(data)
			return &protocol.Chunk{
				FileCID:    cid,
				ChunkIndex: chunkIndex,
				ChunkCID:   chunkCID,
				Data:       data,
				Size:       int64(len(data)),
			}, nil
		},
	}

	// Setup P2P dependencies
	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act - execute download
	err = manager.executeDownload(ctx, metadata.FileCID, "test.bin", metadata.TotalChunks)

	// Assert
	if err != nil {
		t.Fatalf("executeDownload() failed: %v", err)
	}

	// Verify file was created
	outputPath := fmt.Sprintf("%s/assets/%s/test.bin", tmpDir, metadata.FileCID)
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Errorf("Assembled file not found at %s", outputPath)
	}

	// Verify file content matches original
	assembledData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read assembled file: %v", err)
	}

	if !bytes.Equal(assembledData, testData) {
		t.Errorf("Assembled file content does not match original")
	}
}

// TestManager_ExecuteDownload_PeerDiscoveryFails - Phase 5 RED test
// Acceptance Criterion: searchAndRegisterPeers returns error
func TestManager_ExecuteDownload_PeerDiscoveryFails(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that returns error
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			return nil, fmt.Errorf("tracker connection failed")
		},
	}

	coordinator := NewCoordinator(4)
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		ChunkStore:    mockStore,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act
	ctx := context.Background()
	err := manager.executeDownload(ctx, "bafybeigdyrzt5sfp1", "test.bin", 4)

	// Assert - should return error with context
	if err == nil {
		t.Fatal("Expected error when peer discovery fails")
	}

	// Error message should mention peer discovery
	if !stringContains(err.Error(), "peer discovery") && !stringContains(err.Error(), "tracker") {
		t.Errorf("Error message should mention peer discovery: %v", err)
	}
}

// TestManager_ExecuteDownload_DownloadFails - Phase 5 RED test
// Acceptance Criterion: chunk download phase returns error
func TestManager_ExecuteDownload_DownloadFails(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that succeeds
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			peer1 := generateTestPeerID(t)
			return []PeerInfo{
				{PeerID: peer1, Chunks: []int{0, 1, 2, 3}},
			}, nil
		},
	}

	// Create mock chunk store that fails on GetChunk (simulates download failure)
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}
	// Don't populate chunks - GetChunk will fail

	coordinator := NewCoordinator(4)

	// Create mock P2P client (will fail when trying to store/retrieve chunks)
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
			// This will be called but store won't have chunks, causing assembly to fail
			return nil, fmt.Errorf("no chunks available")
		},
	}

	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act
	ctx := context.Background()
	err := manager.executeDownload(ctx, "bafybeigdyrzt5sfp1", "test.bin", 4)

	// Assert - should return error with context
	if err == nil {
		t.Fatal("Expected error when chunk download fails")
	}

	// Error message should mention chunk download or assembly
	if !stringContains(err.Error(), "chunk") && !stringContains(err.Error(), "download") && !stringContains(err.Error(), "assembly") {
		t.Errorf("Error message should mention chunk download: %v", err)
	}
}

// TestManager_ExecuteDownload_AssemblyFails - Phase 5 RED test
// Acceptance Criterion: assembleFile returns error
func TestManager_ExecuteDownload_AssemblyFails(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that succeeds
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			peer1 := generateTestPeerID(t)
			return []PeerInfo{
				{PeerID: peer1, Chunks: []int{0, 1, 2, 3}},
			}, nil
		},
	}

	// Create mock chunk store with invalid chunks (will fail CID verification)
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	testCID := "bafybeigdyrzt5sfp1"
	mockStore.chunks[testCID] = make(map[int][]byte)
	for i := 0; i < 4; i++ {
		mockStore.chunks[testCID][i] = make([]byte, 256) // Invalid chunks
	}

	coordinator := NewCoordinator(4)

	// Create mock P2P client that returns invalid chunks
	// Thread-safe: uses GetChunk (mutex-protected) instead of direct map access (US-029-P5)
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
			data, _ := mockStore.GetChunk(cid, chunkIndex)
			chunkCID, _ := protocol.CalculateChunkCID(data)
			return &protocol.Chunk{
				FileCID:    cid,
				ChunkIndex: chunkIndex,
				ChunkCID:   chunkCID,
				Data:       data,
				Size:       int64(len(data)),
			}, nil
		},
	}

	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Act
	ctx := context.Background()
	err := manager.executeDownload(ctx, testCID, "test.bin", 4)

	// Assert - should return error with context
	if err == nil {
		t.Fatal("Expected error when file assembly fails")
	}

	// Error message should mention assembly or CID verification
	if !stringContains(err.Error(), "assembly") && !stringContains(err.Error(), "CID") {
		t.Errorf("Error message should mention assembly failure: %v", err)
	}
}

// TestManager_ExecuteDownload_ContextCancelled - Phase 5 RED test
// Acceptance Criterion: Context cancelled between phases
func TestManager_ExecuteDownload_ContextCancelled(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that succeeds
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			peer1 := generateTestPeerID(t)
			return []PeerInfo{
				{PeerID: peer1, Chunks: []int{0, 1, 2, 3}},
			}, nil
		},
	}

	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}

	coordinator := NewCoordinator(4)

	// Create mock P2P client
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
			// This should never be called since context is already cancelled
			return nil, fmt.Errorf("unexpected call")
		},
	}

	p2pDeps := &P2PDependencies{
		TrackerClient: mockTracker,
		ChunkStore:    mockStore,
		P2PClient:     mockP2P,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Create context that is already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Act
	err := manager.executeDownload(ctx, "bafybeigdyrzt5sfp1", "test.bin", 4)

	// Assert - should return error related to context cancellation
	if err == nil {
		t.Fatal("Expected error when context is cancelled")
	}

	// Error message should mention context or cancellation
	if !stringContains(err.Error(), "context") && !stringContains(err.Error(), "cancel") {
		t.Errorf("Error message should mention context cancellation: %v", err)
	}
}

// ============================================================================
// Phase 6: processQueuedDownloads Tests (TDD Security-First)
// ============================================================================

// TestManager_ProcessQueuedDownloads_Success - RED test
// Acceptance Criterion: Queued download completes successfully
func TestManager_ProcessQueuedDownloads_Success(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create test data (4 chunks)
	chunkSize := 262144 // 256 KB
	testData := make([]byte, chunkSize*4)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	// Chunk the data
	chunker := protocol.NewChunker()
	ctx := context.Background()
	metadata, chunks, err := chunker.ChunkFile(ctx, bytes.NewReader(testData), "test.bin")
	if err != nil {
		t.Fatalf("Failed to chunk test data: %v", err)
	}

	// Create mock chunk store and store chunks
	mockStore := &MockChunkStore{
		chunks: make(map[string]map[int][]byte),
	}
	mockStore.chunks[metadata.FileCID] = make(map[int][]byte)
	for _, chunk := range chunks {
		mockStore.chunks[metadata.FileCID][chunk.ChunkIndex] = chunk.Data
	}

	// Create mock tracker that returns 1 peer
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			return []PeerInfo{
				{PeerID: generateTestPeerID(t), Chunks: []int{0, 1, 2, 3}},
			}, nil
		},
	}

	// Create mock P2P client that returns chunks
	// Thread-safe: uses GetChunk (mutex-protected) instead of direct map access (US-029-P5)
	mockP2P := &mockP2PClient{
		requestChunkFunc: func(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
			data, _ := mockStore.GetChunk(cid, chunkIndex)
			chunkCID, _ := protocol.CalculateChunkCID(data)
			return &protocol.Chunk{
				FileCID:    cid,
				ChunkIndex: chunkIndex,
				ChunkCID:   chunkCID,
				Data:       data,
				Size:       int64(len(data)),
			}, nil
		},
	}

	// Setup P2P dependencies
	coordinator := NewCoordinator(metadata.TotalChunks)
	p2pDeps := &P2PDependencies{
		ChunkStore:    mockStore,
		TrackerClient: mockTracker,
		P2PClient:     mockP2P,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Queue download
	err = manager.QueueDownload(metadata.FileCID, "test.bin", metadata.TotalSize, metadata.TotalChunks)
	if err != nil {
		t.Fatalf("QueueDownload() failed: %v", err)
	}

	// Act - process queued download (now spawns goroutine)
	manager.processQueuedDownloads()

	// Wait for the spawned worker goroutine to complete
	manager.mu.RLock()
	wh, hasWorker := manager.workers[metadata.FileCID]
	manager.mu.RUnlock()
	if hasWorker {
		select {
		case <-wh.done:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not complete within 10 seconds")
		}
	}

	// Assert - download should be completed
	status := manager.GetStatus(metadata.FileCID)
	if status == nil {
		t.Fatal("Download status not found")
	}
	if status.State != StateCompleted {
		errStr := ""
		if status.ErrorMessage != nil {
			errStr = *status.ErrorMessage
		}
		t.Errorf("Expected state %q, got %q (error: %s)", StateCompleted, status.State, errStr)
	}
}

// TestManager_ProcessQueuedDownloads_NilP2P - RED test
// Acceptance Criterion: P2P dependencies not initialized
func TestManager_ProcessQueuedDownloads_NilP2P(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Queue download WITHOUT setting P2P dependencies
	err := manager.QueueDownload("bafybeigdyrzt5sfp1", "test.bin", 1048576, 4)
	if err != nil {
		t.Fatalf("QueueDownload() failed: %v", err)
	}

	// Act - process queued download with nil P2P
	manager.processQueuedDownloads()

	// Assert - download should remain queued (no crash)
	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status == nil {
		t.Fatal("Download status not found")
	}
	if status.State != StateQueued {
		t.Errorf("Expected state %q (P2P not initialized), got %q", StateQueued, status.State)
	}
}

// TestManager_ProcessQueuedDownloads_MaxConcurrent - RED test
// Acceptance Criterion: Max concurrent limit reached
func TestManager_ProcessQueuedDownloads_MaxConcurrent(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 2) // Max 2 concurrent

	// Create mock P2P dependencies (minimal)
	mockStore := &MockChunkStore{chunks: make(map[string]map[int][]byte)}
	mockTracker := &mockTrackerClient{}
	mockP2P := &mockP2PClient{}
	coordinator := NewCoordinator(4)

	p2pDeps := &P2PDependencies{
		ChunkStore:    mockStore,
		TrackerClient: mockTracker,
		P2PClient:     mockP2P,
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Queue 3 downloads
	manager.QueueDownload("cid1", "test1.bin", 1048576, 4)
	manager.QueueDownload("cid2", "test2.bin", 1048576, 4)
	manager.QueueDownload("cid3", "test3.bin", 1048576, 4)

	// Start 2 downloads to reach max concurrent
	manager.StartDownload("cid1")
	manager.StartDownload("cid2")

	// Act - try to process queued download (cid3)
	manager.processQueuedDownloads()

	// Assert - cid3 should remain queued (max concurrent reached)
	status3 := manager.GetStatus("cid3")
	if status3 == nil {
		t.Fatal("Download cid3 not found")
	}
	if status3.State != StateQueued {
		t.Errorf("Expected cid3 to remain queued (max concurrent), got %q", status3.State)
	}
}

// TestManager_ProcessQueuedDownloads_ExecuteFails - RED test
// Acceptance Criterion: executeDownload returns error
func TestManager_ProcessQueuedDownloads_ExecuteFails(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Create mock tracker that returns error
	mockTracker := &mockTrackerClient{
		searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
			return nil, fmt.Errorf("tracker connection failed")
		},
	}

	// Setup P2P dependencies
	coordinator := NewCoordinator(4)
	p2pDeps := &P2PDependencies{
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		TrackerClient: mockTracker,
		P2PClient:     &mockP2PClient{},
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// Queue download
	err := manager.QueueDownload("bafybeigdyrzt5sfp1", "test.bin", 1048576, 4)
	if err != nil {
		t.Fatalf("QueueDownload() failed: %v", err)
	}

	// Act - process queued download (should fail during tracker search)
	manager.processQueuedDownloads()

	// Wait for the spawned worker goroutine to complete
	manager.mu.RLock()
	wh, hasWorker := manager.workers["bafybeigdyrzt5sfp1"]
	manager.mu.RUnlock()
	if hasWorker {
		select {
		case <-wh.done:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not complete within 10 seconds")
		}
	}

	// Assert - download should be in failed state
	status := manager.GetStatus("bafybeigdyrzt5sfp1")
	if status == nil {
		t.Fatal("Download status not found")
	}
	if status.State != StateFailed {
		t.Errorf("Expected state %q, got %q", StateFailed, status.State)
	}
	if status.ErrorMessage == nil || *status.ErrorMessage == "" {
		t.Error("Error message should be set for failed download")
	}
}

// TestStopWorker_WaitsForWorkerExit - Audit B1 test
// Acceptance Criterion: StopWorker blocks until the background worker goroutine exits
func TestStopWorker_WaitsForWorkerExit(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Start the background worker
	manager.StartWorker()

	// Act - call StopWorker in a goroutine and verify it blocks until worker exits
	stopped := make(chan struct{})
	go func() {
		manager.StopWorker()
		close(stopped)
	}()

	// Assert - StopWorker should complete (worker exited) within a reasonable time
	select {
	case <-stopped:
		// Success: StopWorker returned, meaning it waited for the worker to exit
	case <-time.After(5 * time.Second):
		t.Fatal("StopWorker did not return within 5 seconds - worker goroutine may not have exited")
	}

	// Verify calling StopWorker again does not panic or deadlock
	// (idempotency check - workerDone is already closed)
}

// TestStopWorker_BlocksUntilWorkerDone - Audit B1 test
// Acceptance Criterion: StopWorker actually waits (not just signals)
func TestStopWorker_BlocksUntilWorkerDone(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	manager.StartWorker()

	// Act - StopWorker should block until the goroutine finishes
	done := make(chan struct{})
	go func() {
		manager.StopWorker()
		close(done)
	}()

	// The worker should exit quickly once stop channel is closed
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("StopWorker blocked too long - wg.Wait() may not be working")
	}
}

// ============================================================================
// Task 21: Worker Lifecycle Redesign Tests (F-029)
// ============================================================================

// blockingP2PClient is a mock P2P client that blocks on RequestChunk until context is cancelled.
type blockingP2PClient struct{}

func (b *blockingP2PClient) RequestChunk(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndex int) (*protocol.Chunk, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (b *blockingP2PClient) RequestChunks(ctx context.Context, peerIDs []peer.ID, cid string, chunkIndices []int) (<-chan *protocol.ChunkResult, error) {
	return nil, ctx.Err()
}

// TestWorkerSpawnsPerDownloadGoroutine - F-029 Task 21 RED test
// Acceptance Criterion: Coordinator spawns per-download worker goroutines
func TestWorkerSpawnsPerDownloadGoroutine(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	// Use mock deps that keep the worker alive (tracker returns peers, P2P blocks)
	m.SetP2PDependencies(&P2PDependencies{
		TrackerClient: &mockTrackerClient{
			searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
				return []PeerInfo{{PeerID: generateTestPeerID(t), Chunks: []int{0, 1, 2, 3}}}, nil
			},
		},
		Coordinator: NewCoordinator(4),
		ChunkStore:  &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:   &blockingP2PClient{},
	})

	_ = m.QueueDownload("cid-spawn", "spawn.txt", 1000, 4)

	// Start the coordinator
	m.StartWorker()
	defer m.StopWorker()

	// Wait for the coordinator to pick up the queued download
	time.Sleep(2 * time.Second)

	// Verify worker handle exists (goroutine was spawned)
	m.mu.RLock()
	_, hasWorker := m.workers["cid-spawn"]
	m.mu.RUnlock()

	if !hasWorker {
		t.Error("expected worker handle to exist for cid-spawn — coordinator should have spawned a goroutine")
	}
}

// TestPauseDownloadCancelsWorkerContext - F-029 Task 21 RED test
// Acceptance Criterion: PauseDownload cancels worker context with errUserPause
func TestPauseDownloadCancelsWorkerContext(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	// Use mock deps that keep the worker alive until paused
	m.SetP2PDependencies(&P2PDependencies{
		TrackerClient: &mockTrackerClient{
			searchByCIDFunc: func(cid string) ([]PeerInfo, error) {
				return []PeerInfo{{PeerID: generateTestPeerID(t), Chunks: []int{0, 1, 2, 3}}}, nil
			},
		},
		Coordinator: NewCoordinator(4),
		ChunkStore:  &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		P2PClient:   &blockingP2PClient{},
	})

	_ = m.QueueDownload("cid-pause", "pause.txt", 1000, 4)
	m.StartWorker()
	defer m.StopWorker()

	// Wait for coordinator to spawn worker
	time.Sleep(2 * time.Second)

	// Pause should cancel the worker's context with errUserPause
	err := m.PauseDownload("cid-pause")
	if err != nil {
		t.Fatalf("pause failed: %v", err)
	}

	status := m.GetStatus("cid-pause")
	if status == nil {
		t.Fatal("expected status to exist after pause")
	}
	if status.State != StatePaused {
		t.Errorf("expected StatePaused, got %s", status.State)
	}

	// Worker handle should be cleaned up after pause
	time.Sleep(500 * time.Millisecond) // Give worker time to exit
	m.mu.RLock()
	_, hasWorker := m.workers["cid-pause"]
	m.mu.RUnlock()

	if hasWorker {
		t.Error("expected worker handle to be removed after pause — worker should have exited")
	}
}

// TestSentinelErrorsAreDefined - F-029 Task 21 RED test
// Acceptance Criterion: Sentinels must be distinct typed errors
func TestSentinelErrorsAreDefined(t *testing.T) {
	// Sentinels must be distinct (not wrapped versions of each other)
	if errors.Is(errUserPause, errUserCancel) {
		t.Error("errUserPause and errUserCancel must be distinct")
	}
	if errors.Is(errUserCancel, errInactivityTimeout) {
		t.Error("errUserCancel and errInactivityTimeout must be distinct")
	}
}

// TestManager_ProcessQueuedDownloads_NoQueuedDownloads - RED test
// Acceptance Criterion: No queued downloads to process
func TestManager_ProcessQueuedDownloads_NoQueuedDownloads(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, 3)

	// Setup P2P dependencies
	coordinator := NewCoordinator(4)
	p2pDeps := &P2PDependencies{
		ChunkStore:    &MockChunkStore{chunks: make(map[string]map[int][]byte)},
		TrackerClient: &mockTrackerClient{},
		P2PClient:     &mockP2PClient{},
		Coordinator:   coordinator,
	}
	manager.SetP2PDependencies(p2pDeps)

	// No downloads queued

	// Act - process queued downloads (should be no-op)
	manager.processQueuedDownloads()

	// Assert - no crash, no side effects
	activeDownloads := manager.GetActiveDownloads()
	if len(activeDownloads) != 0 {
		t.Errorf("Expected 0 active downloads, got %d", len(activeDownloads))
	}
}

// --- F-029 / US-029-04: RetryDownload Tests ---

// TestRetryDownloadResetsFailed verifies AC-7: RetryDownload resets
// StateFailed → StateQueued with cleared error, zero progress, zero bytes.
func TestRetryDownloadResetsFailed(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	_ = m.QueueDownload("bafybeigdyrzt5sfp1retryabc", "fail.txt", 500, 2)
	_ = m.FailDownload("bafybeigdyrzt5sfp1retryabc", "network timeout")

	// Verify it's failed
	status := m.GetStatus("bafybeigdyrzt5sfp1retryabc")
	if status == nil {
		t.Fatal("expected download to exist")
	}
	if status.State != StateFailed {
		t.Fatalf("expected StateFailed, got %s", status.State)
	}

	// Retry
	err := m.RetryDownload("bafybeigdyrzt5sfp1retryabc")
	if err != nil {
		t.Fatalf("RetryDownload failed: %v", err)
	}

	status = m.GetStatus("bafybeigdyrzt5sfp1retryabc")
	if status.State != StateQueued {
		t.Errorf("expected StateQueued after retry, got %s", status.State)
	}
	if status.ErrorMessage != nil {
		t.Errorf("expected nil ErrorMessage, got %q", *status.ErrorMessage)
	}
	if status.Progress != 0.0 {
		t.Errorf("expected Progress 0, got %f", status.Progress)
	}
	if status.DownloadedBytes != 0 {
		t.Errorf("expected DownloadedBytes 0, got %d", status.DownloadedBytes)
	}
	if status.CompletedChunks != 0 {
		t.Errorf("expected CompletedChunks 0, got %d", status.CompletedChunks)
	}
}

// TestRetryDownloadRejectsNonFailed verifies AC-7: RetryDownload errors on non-failed.
func TestRetryDownloadRejectsNonFailed(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	_ = m.QueueDownload("bafybeigdyrzt5sfp1rejectabc", "q.txt", 100, 1)

	err := m.RetryDownload("bafybeigdyrzt5sfp1rejectabc")
	if err == nil {
		t.Error("expected error retrying non-failed download")
	}
}

// TestRetryDownloadNotFound verifies AC-7: RetryDownload errors on missing CID.
func TestRetryDownloadNotFound(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	err := m.RetryDownload("bafybeigdyrzt5sfp1nonexist")
	if err == nil {
		t.Error("expected error for non-existent CID")
	}
}

// --- F-029 / US-029-04 + US-029-22: CID Validation Tests ---

// TestValidateCID_ValidFormats verifies valid CIDs pass.
func TestValidateCID_ValidFormats(t *testing.T) {
	validCIDs := []string{
		"bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		"QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG",
		"bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oc",
	}

	for _, cid := range validCIDs {
		if err := ValidateCID(cid); err != nil {
			t.Errorf("ValidateCID(%q) returned error: %v", cid, err)
		}
	}
}

// TestValidateCID_InvalidFormats verifies invalid CIDs are rejected.
func TestValidateCID_InvalidFormats(t *testing.T) {
	invalidCIDs := []struct {
		cid    string
		reason string
	}{
		{"", "empty string"},
		{"short", "too short"},
		{"bafybeig/../../../etc/passwd", "path traversal chars"},
		{"Qm spaces are not allowed here abcdef", "contains spaces"},
		{"bafybeig\x00null", "contains null byte"},
		{"zcidwithwrongprefix1234567890abcdefghijklmno", "wrong prefix"},
		{"abc", "too short and wrong prefix"},
	}

	for _, tc := range invalidCIDs {
		if err := ValidateCID(tc.cid); err == nil {
			t.Errorf("ValidateCID(%q) should fail: %s", tc.cid, tc.reason)
		}
	}
}

// --- F-029 / US-029-22: CancelDownload Tests ---

// TestCancelDownloadQueued verifies AC-24: cancel a queued download (no worker).
func TestCancelDownloadQueued(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	_ = m.QueueDownload("bafybeigdyrzt5sfp1cancelqueuedabcdef", "qcancel.txt", 1000, 4)
	err := m.CancelDownload("bafybeigdyrzt5sfp1cancelqueuedabcdef")
	if err != nil {
		t.Fatalf("cancel queued failed: %v", err)
	}

	status := m.GetStatus("bafybeigdyrzt5sfp1cancelqueuedabcdef")
	if status != nil {
		t.Error("expected nil status after cancelling queued download")
	}

	// Metadata should be deleted
	metadata, _ := m.repo.LoadMetadata("bafybeigdyrzt5sfp1cancelqueuedabcdef")
	if metadata != nil {
		t.Error("expected metadata to be deleted from disk after cancel")
	}
}

// TestCancelDownloadNotFound verifies AC-24: cancel non-existent CID returns error.
func TestCancelDownloadNotFound(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)

	err := m.CancelDownload("bafybeigdyrzt5sfp1nonexistcancelabc")
	if err == nil {
		t.Error("expected error for non-existent CID")
	}
}

// TestCancelDownloadWithWorker verifies AC-24: cancel active download with running worker.
func TestCancelDownloadWithWorker(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)
	m.SetP2PDependencies(&P2PDependencies{})

	_ = m.QueueDownload("bafybeigdyrzt5sfp1cancelworkerabcdef", "cancel.txt", 1000, 4)
	m.StartWorker()
	defer m.StopWorker()

	// Wait for worker to pick up the download
	time.Sleep(2 * time.Second)

	err := m.CancelDownload("bafybeigdyrzt5sfp1cancelworkerabcdef")
	if err != nil {
		t.Fatalf("cancel failed: %v", err)
	}

	// Download should be completely removed
	status := m.GetStatus("bafybeigdyrzt5sfp1cancelworkerabcdef")
	if status != nil {
		t.Error("expected nil status after cancel — download should be removed from map")
	}

	// Worker handle should be cleaned up
	m.mu.RLock()
	_, hasWorker := m.workers["bafybeigdyrzt5sfp1cancelworkerabcdef"]
	m.mu.RUnlock()
	if hasWorker {
		t.Error("expected worker handle to be removed after cancel")
	}
}

// TestInactivityTimeoutDefault verifies AC-26: default timeout is 90s.
func TestInactivityTimeoutDefault(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 3)
	if m.InactivityTimeoutSec != 90 {
		t.Errorf("expected default InactivityTimeoutSec=90, got %d", m.InactivityTimeoutSec)
	}
}

// === F-029 Parallelization: Out-of-order Progress (US-029-P2) ===

// setupTestManager creates a Manager with temp dir for testing
func setupTestManager(t *testing.T) *Manager {
	t.Helper()
	tmpDir := t.TempDir()
	return NewManager(tmpDir, 3)
}

// TestManager_UpdateProgress_OutOfOrder - RED test
// Acceptance Criterion: Out-of-order chunk completion must persist all chunks correctly
func TestManager_UpdateProgress_OutOfOrder(t *testing.T) {
	m := setupTestManager(t)

	cid := "test-ooo-cid"
	totalChunks := 5
	chunkSize := 256 * 1024 // 256KB

	// Create metadata with []bool ChunksMap (actual type from repository.go:34)
	metadata := &DownloadMetadata{
		CID:         cid,
		Filename:    "test.bin",
		TotalSize:   int64(totalChunks * chunkSize),
		TotalChunks: totalChunks,
		ChunkSize:   chunkSize,
		State:       StateQueued,
		ChunksMap:   make([]bool, totalChunks),
	}
	m.repo.SaveMetadata(metadata)

	// Register download in manager's in-memory map
	m.mu.Lock()
	m.downloads[cid] = &DownloadStatus{
		CID:         cid,
		TotalChunks: totalChunks,
		TotalSize:   int64(totalChunks * chunkSize),
	}
	m.mu.Unlock()

	// Complete chunks out of order: 0, 3, 1, 4, 2
	outOfOrderIndices := []int{0, 3, 1, 4, 2}

	for i, chunkIndex := range outOfOrderIndices {
		// Persist specific chunk (as worker would)
		m.repo.UpdateChunkCompletion(cid, chunkIndex, true)

		// Count from ChunksMap (as fixed worker would)
		loaded, _ := m.repo.LoadMetadata(cid)
		completedCount := 0
		for _, done := range loaded.ChunksMap {
			if done {
				completedCount++
			}
		}

		expectedCount := i + 1
		if completedCount != expectedCount {
			t.Errorf("After chunk %d: got %d completed, want %d",
				chunkIndex, completedCount, expectedCount)
		}

		// Call UpdateProgress with correct count
		downloadedBytes := int64(completedCount * chunkSize)
		err := m.UpdateProgress(cid, completedCount, downloadedBytes, int64(chunkSize))
		if err != nil {
			t.Fatalf("UpdateProgress failed: %v", err)
		}
	}
}

// === F-029 Parallelization: Connection Pooling (US-029-P3) ===

// TestManager_ensurePeerConnection_Caching - RED test
// Acceptance Criterion: Connection cache prevents redundant ConnectToPeer calls
func TestManager_ensurePeerConnection_Caching(t *testing.T) {
	m := setupTestManager(t)

	cid := "test-cid"
	peerIDStr := generateTestPeerID(t)
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}

	// Setup coordinator with peer info (needed for address lookup)
	coordinator := NewCoordinator(4)
	coordinator.RegisterPeer(peerIDStr, []int{0, 1, 2, 3}, []string{"/ip4/127.0.0.1/tcp/4001"})

	// Mock connector that counts connection attempts
	connectCount := 0
	connector := &mockConnector{
		connectFn: func(p peer.ID, addrs []string) error {
			connectCount++
			return nil
		},
	}

	m.SetP2PDependencies(&P2PDependencies{
		Coordinator:   coordinator,
		PeerConnector: connector,
	})

	// First call: should connect
	err = m.ensurePeerConnection(cid, peerID)
	if err != nil {
		t.Fatalf("First connect failed: %v", err)
	}
	if connectCount != 1 {
		t.Errorf("Expected 1 connection attempt, got %d", connectCount)
	}

	// Second call: should use cache (no new connection)
	err = m.ensurePeerConnection(cid, peerID)
	if err != nil {
		t.Fatalf("Second connect failed: %v", err)
	}
	if connectCount != 1 {
		t.Errorf("Expected 1 connection attempt (cached), got %d", connectCount)
	}

	// Clear cache
	m.clearConnectionCache(cid)

	// Third call: should reconnect after cache clear
	err = m.ensurePeerConnection(cid, peerID)
	if err != nil {
		t.Fatalf("Third connect failed: %v", err)
	}
	if connectCount != 2 {
		t.Errorf("Expected 2 connection attempts after cache clear, got %d", connectCount)
	}
}

// TestManager_clearConnectionCache_OnComplete - RED test
// Acceptance Criterion: Connection cache cleaned up on download complete/cancel
func TestManager_clearConnectionCache_OnComplete(t *testing.T) {
	m := setupTestManager(t)

	cid := "test-cleanup-cid"
	peerIDStr := generateTestPeerID(t)
	peerID, err := peer.Decode(peerIDStr)
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}

	coordinator := NewCoordinator(4)
	coordinator.RegisterPeer(peerIDStr, []int{0, 1, 2, 3}, []string{"/ip4/127.0.0.1/tcp/4001"})

	connector := &mockConnector{}

	m.SetP2PDependencies(&P2PDependencies{
		Coordinator:   coordinator,
		PeerConnector: connector,
	})

	// Populate cache
	m.ensurePeerConnection(cid, peerID)

	// Verify cache has entry
	m.connMu.RLock()
	if _, exists := m.connectedPeers[cid]; !exists {
		t.Fatal("Connection cache should have entry after ensurePeerConnection")
	}
	m.connMu.RUnlock()

	// Queue and complete download
	m.QueueDownload(cid, "test.bin", 1048576, 4)
	m.StartDownload(cid)
	m.CompleteDownload(cid)

	// Verify cache was cleared
	m.connMu.RLock()
	if _, exists := m.connectedPeers[cid]; exists {
		t.Error("Connection cache should be cleared after CompleteDownload")
	}
	m.connMu.RUnlock()
}

// TestGetStatus_ConnectedPeers - F-029 audit round 2
// Acceptance Criterion: GetStatus populates ConnectedPeers from connection cache
func TestGetStatus_ConnectedPeers(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(tmpDir, 3)
	cid := "bafybeigdyrzt5sfp1peercount"

	// Queue and start a download
	m.QueueDownload(cid, "test.bin", 1048576, 4)
	m.StartDownload(cid)

	// Before any peers connect, ConnectedPeers should be 0
	status := m.GetStatus(cid)
	if status == nil {
		t.Fatal("GetStatus returned nil for active download")
	}
	if status.ConnectedPeers != 0 {
		t.Errorf("Expected 0 connected peers before connection, got %d", status.ConnectedPeers)
	}

	// Simulate peer connections via connectedPeers cache
	peerID1, err := peer.Decode(generateTestPeerID(t))
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}
	peerID2, err := peer.Decode(generateTestPeerID(t))
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}

	m.connMu.Lock()
	m.connectedPeers[cid] = map[peer.ID]bool{peerID1: true, peerID2: true}
	m.connMu.Unlock()

	// After connecting 2 peers, ConnectedPeers should be 2
	status = m.GetStatus(cid)
	if status.ConnectedPeers != 2 {
		t.Errorf("Expected 2 connected peers, got %d", status.ConnectedPeers)
	}

	// GetVisibleDownloads should also include peer counts
	visible := m.GetVisibleDownloads()
	found := false
	for _, v := range visible {
		if v.CID == cid {
			found = true
			if v.ConnectedPeers != 2 {
				t.Errorf("GetVisibleDownloads: expected 2 connected peers, got %d", v.ConnectedPeers)
			}
		}
	}
	if !found {
		t.Error("GetVisibleDownloads did not include the active download")
	}
}
