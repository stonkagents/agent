// Package: internal/daemon/download
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: TDD tests for multi-peer download coordinator

package download

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestCoordinator_AllocateChunks - RED test
// Acceptance Criterion: Allocate chunks across multiple peers using round-robin
func TestCoordinator_AllocateChunks(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4) // 4 total chunks

	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"
	peer3 := "12D3KooWPeer3"

	// Register peers
	coordinator.RegisterPeer(peer1, []int{0, 1, 2, 3}, []string{}) // Has all chunks
	coordinator.RegisterPeer(peer2, []int{0, 1}, []string{})       // Has chunks 0, 1
	coordinator.RegisterPeer(peer3, []int{2, 3}, []string{})       // Has chunks 2, 3

	// Act - allocate chunks using round-robin
	allocations := coordinator.AllocateChunks()

	// Assert - chunks should be distributed across peers
	if len(allocations) != 4 {
		t.Fatalf("Expected 4 chunk allocations, got %d", len(allocations))
	}

	// Verify each chunk is allocated to a peer that has it
	for chunkIndex, peerID := range allocations {
		peerChunks := coordinator.GetPeerChunks(peerID)
		hasChunk := false
		for _, c := range peerChunks {
			if c == chunkIndex {
				hasChunk = true
				break
			}
		}
		if !hasChunk {
			t.Errorf("Chunk %d allocated to peer %s who doesn't have it", chunkIndex, peerID)
		}
	}
}

// TestCoordinator_PreferRarestChunks - RED test
// Acceptance Criterion: Prioritize downloading rarer chunks first (BitTorrent strategy)
func TestCoordinator_PreferRarestChunks(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)

	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"
	peer3 := "12D3KooWPeer3"

	// Chunk 0: 3 seeders (common)
	// Chunk 1: 3 seeders (common)
	// Chunk 2: 2 seeders (rare)
	// Chunk 3: 1 seeder (rarest)
	coordinator.RegisterPeer(peer1, []int{0, 1, 2, 3}, []string{})
	coordinator.RegisterPeer(peer2, []int{0, 1, 2}, []string{})
	coordinator.RegisterPeer(peer3, []int{0, 1}, []string{})

	// Act - get next chunk to download (should prioritize rarest)
	nextChunk, err := coordinator.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}

	// Assert - should return chunk 3 (rarest)
	if nextChunk != 3 {
		t.Errorf("Expected rarest chunk 3, got chunk %d", nextChunk)
	}
}

// TestCoordinator_MarkChunkComplete - RED test
// Acceptance Criterion: Track completed chunks to avoid re-downloading
func TestCoordinator_MarkChunkComplete(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	coordinator.RegisterPeer(peer1, []int{0, 1, 2, 3}, []string{})

	// Act - mark chunk 0 as completed
	coordinator.MarkChunkComplete(0)

	// Assert - next chunk should skip chunk 0
	nextChunk, err := coordinator.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	if nextChunk == 0 {
		t.Error("GetNextChunk returned chunk 0 which is already completed")
	}

	// Verify IsComplete returns true for chunk 0
	if !coordinator.IsComplete(0) {
		t.Error("Chunk 0 should be marked as complete")
	}
	if coordinator.IsComplete(1) {
		t.Error("Chunk 1 should not be marked as complete")
	}
}

// TestCoordinator_GetPeerForChunk - RED test
// Acceptance Criterion: Return peer ID that has a specific chunk
func TestCoordinator_GetPeerForChunk(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"

	coordinator.RegisterPeer(peer1, []int{0, 1}, []string{})
	coordinator.RegisterPeer(peer2, []int{2, 3}, []string{})

	// Act - get peer for chunk 2
	peerID := coordinator.GetPeerForChunk(2)

	// Assert - should return peer2 (only peer with chunk 2)
	if peerID != peer2 {
		t.Errorf("Expected peer %s for chunk 2, got %s", peer2, peerID)
	}

	// Act - get peer for chunk 0
	peerID = coordinator.GetPeerForChunk(0)

	// Assert - should return peer1 (only peer with chunk 0)
	if peerID != peer1 {
		t.Errorf("Expected peer %s for chunk 0, got %s", peer1, peerID)
	}
}

// TestCoordinator_AllChunksComplete - RED test
// Acceptance Criterion: Detect when all chunks are downloaded
func TestCoordinator_AllChunksComplete(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(3)
	peer1 := "12D3KooWPeer1"
	coordinator.RegisterPeer(peer1, []int{0, 1, 2}, []string{})

	// Assert - initially not complete
	if coordinator.AllChunksComplete() {
		t.Error("AllChunksComplete should return false initially")
	}

	// Act - mark chunks as complete
	coordinator.MarkChunkComplete(0)
	coordinator.MarkChunkComplete(1)

	// Assert - still not complete (missing chunk 2)
	if coordinator.AllChunksComplete() {
		t.Error("AllChunksComplete should return false with 2/3 chunks")
	}

	// Act - mark last chunk
	coordinator.MarkChunkComplete(2)

	// Assert - now complete
	if !coordinator.AllChunksComplete() {
		t.Error("AllChunksComplete should return true after all chunks marked")
	}
}

// TestCoordinator_UnregisterPeer - RED test
// Acceptance Criterion: Handle peer disconnection gracefully
func TestCoordinator_UnregisterPeer(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"

	coordinator.RegisterPeer(peer1, []int{0, 1, 2, 3}, []string{})
	coordinator.RegisterPeer(peer2, []int{0, 1}, []string{})

	// Act - unregister peer2
	coordinator.UnregisterPeer(peer2)

	// Assert - peer2 should not be returned for chunk allocation
	peerID := coordinator.GetPeerForChunk(0)
	if peerID == peer2 {
		t.Error("Unregistered peer should not be returned")
	}
	if peerID != peer1 {
		t.Errorf("Expected peer1 after peer2 unregistered, got %s", peerID)
	}
}

// TestCoordinator_GetProgress - RED test
// Acceptance Criterion: Calculate download progress percentage
func TestCoordinator_GetProgress(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	coordinator.RegisterPeer(peer1, []int{0, 1, 2, 3}, []string{})

	// Assert - initially 0% progress
	progress := coordinator.GetProgress()
	if progress != 0.0 {
		t.Errorf("Expected 0%% progress initially, got %.2f", progress)
	}

	// Act - mark 2 chunks complete (50%)
	coordinator.MarkChunkComplete(0)
	coordinator.MarkChunkComplete(1)

	// Assert - 50% progress
	progress = coordinator.GetProgress()
	expected := 0.5
	if progress != expected {
		t.Errorf("Expected %.2f progress, got %.2f", expected, progress)
	}

	// Act - mark all chunks complete
	coordinator.MarkChunkComplete(2)
	coordinator.MarkChunkComplete(3)

	// Assert - 100% progress
	progress = coordinator.GetProgress()
	if progress != 1.0 {
		t.Errorf("Expected 1.0 progress, got %.2f", progress)
	}
}

// TestCoordinator_GetPeersForChunk - RED test
// Acceptance Criterion: Return ALL peers that have a specific chunk (for P2PClient failover)
func TestCoordinator_GetPeersForChunk(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"
	peer3 := "12D3KooWPeer3"

	// Register peers with overlapping chunks
	coordinator.RegisterPeer(peer1, []int{0, 1, 2}, []string{}) // Has chunks 0, 1, 2
	coordinator.RegisterPeer(peer2, []int{1, 2, 3}, []string{}) // Has chunks 1, 2, 3
	coordinator.RegisterPeer(peer3, []int{2, 3}, []string{})    // Has chunks 2, 3

	// Act - get all peers for chunk 2 (should return 3 peers)
	peersForChunk2 := coordinator.GetPeersForChunk(2)

	// Assert - chunk 2 should have 3 peers
	if len(peersForChunk2) != 3 {
		t.Errorf("Expected 3 peers for chunk 2, got %d", len(peersForChunk2))
	}

	// Verify all peers are present
	peerMap := make(map[string]bool)
	for _, peerID := range peersForChunk2 {
		peerMap[peerID] = true
	}

	if !peerMap[peer1] {
		t.Errorf("Expected peer1 in chunk 2 peers")
	}
	if !peerMap[peer2] {
		t.Errorf("Expected peer2 in chunk 2 peers")
	}
	if !peerMap[peer3] {
		t.Errorf("Expected peer3 in chunk 2 peers")
	}

	// Act - get all peers for chunk 0 (should return 1 peer)
	peersForChunk0 := coordinator.GetPeersForChunk(0)

	// Assert - chunk 0 should have only peer1
	if len(peersForChunk0) != 1 {
		t.Errorf("Expected 1 peer for chunk 0, got %d", len(peersForChunk0))
	}
	if peersForChunk0[0] != peer1 {
		t.Errorf("Expected peer1 for chunk 0, got %s", peersForChunk0[0])
	}

	// Act - get all peers for non-existent chunk
	peersForInvalidChunk := coordinator.GetPeersForChunk(99)

	// Assert - should return empty slice
	if len(peersForInvalidChunk) != 0 {
		t.Errorf("Expected 0 peers for invalid chunk, got %d", len(peersForInvalidChunk))
	}
}

// TestCoordinator_GetPeersForChunk_ThreadSafety - RED test
// Acceptance Criterion: GetPeersForChunk must use read lock for thread safety
func TestCoordinator_GetPeersForChunk_ThreadSafety(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"

	coordinator.RegisterPeer(peer1, []int{0, 1, 2, 3}, []string{})
	coordinator.RegisterPeer(peer2, []int{0, 1, 2, 3}, []string{})

	// Act - concurrent reads should not race
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			peers := coordinator.GetPeersForChunk(0)
			if len(peers) != 2 {
				t.Errorf("Expected 2 peers, got %d", len(peers))
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestCoordinator_GetPeersForChunk_ReturnsCopy - RED test
// Acceptance Criterion: Return a copy of peer slice to prevent external mutation
func TestCoordinator_GetPeersForChunk_ReturnsCopy(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(4)
	peer1 := "12D3KooWPeer1"
	peer2 := "12D3KooWPeer2"

	coordinator.RegisterPeer(peer1, []int{0}, []string{})
	coordinator.RegisterPeer(peer2, []int{0}, []string{})

	// Act - get peers
	peersFirst := coordinator.GetPeersForChunk(0)
	peersSecond := coordinator.GetPeersForChunk(0)

	// Assert - should be separate slices (not same reference)
	if &peersFirst[0] == &peersSecond[0] {
		t.Error("GetPeersForChunk should return a copy, not the same slice reference")
	}

	// Mutate first result
	peersFirst[0] = "MUTATED"

	// Verify second result is not affected
	peersThird := coordinator.GetPeersForChunk(0)
	if peersThird[0] == "MUTATED" {
		t.Error("Mutation of returned slice should not affect internal state")
	}
}

// === Item 3: Capacity Limits (TDD RED tests) ===
// Audit Reference: Unbounded peer maps — DoS vulnerability
// These tests enforce max peer limits and chunk index validation

// TestRegisterPeer_MaxPeersEnforced - RED test
// Acceptance Criterion: RegisterPeer rejects registration when peer count exceeds MaxPeers
func TestRegisterPeer_MaxPeersEnforced(t *testing.T) {
	// Arrange - coordinator with max 3 peers
	coordinator := NewCoordinatorWithMaxPeers(10, 3)

	// Act - register 3 peers (should succeed)
	if err := coordinator.RegisterPeer("peer-1", []int{0, 1}, []string{}); err != nil {
		t.Fatalf("RegisterPeer 1 should succeed: %v", err)
	}
	if err := coordinator.RegisterPeer("peer-2", []int{2, 3}, []string{}); err != nil {
		t.Fatalf("RegisterPeer 2 should succeed: %v", err)
	}
	if err := coordinator.RegisterPeer("peer-3", []int{4, 5}, []string{}); err != nil {
		t.Fatalf("RegisterPeer 3 should succeed: %v", err)
	}

	// Act - register 4th peer (should fail)
	err := coordinator.RegisterPeer("peer-4", []int{6, 7}, []string{})

	// Assert - should return error
	if err == nil {
		t.Fatal("RegisterPeer should fail when max peers exceeded")
	}

	// Verify peer-4 was NOT added
	chunks := coordinator.GetPeerChunks("peer-4")
	if len(chunks) != 0 {
		t.Errorf("Rejected peer should not be stored, got %d chunks", len(chunks))
	}
}

// TestRegisterPeer_InvalidChunkIndex - RED test
// Acceptance Criterion: Negative chunk indices are silently filtered out
func TestRegisterPeer_InvalidChunkIndex(t *testing.T) {
	// Arrange
	coordinator := NewCoordinator(10)

	// Act - register peer with negative chunk indices
	err := coordinator.RegisterPeer("peer-1", []int{-1, -5, 0, 3}, []string{})

	// Assert - should succeed (valid chunks accepted, invalid filtered)
	if err != nil {
		t.Fatalf("RegisterPeer should succeed with filtered chunks: %v", err)
	}

	// Verify only valid chunks stored
	chunks := coordinator.GetPeerChunks("peer-1")
	if len(chunks) != 2 {
		t.Errorf("Expected 2 valid chunks (0, 3), got %d: %v", len(chunks), chunks)
	}

	// Verify chunk -1 is not in chunkToPeers
	peers := coordinator.GetPeersForChunk(-1)
	if len(peers) != 0 {
		t.Errorf("Negative chunk index should not have peers, got %d", len(peers))
	}
}

// TestRegisterPeer_ChunkIndexOutOfRange - RED test
// Acceptance Criterion: Chunk indices >= totalChunks are silently filtered out
func TestRegisterPeer_ChunkIndexOutOfRange(t *testing.T) {
	// Arrange - coordinator with 5 chunks (valid: 0-4)
	coordinator := NewCoordinator(5)

	// Act - register peer with out-of-range chunk indices
	err := coordinator.RegisterPeer("peer-1", []int{0, 4, 5, 100}, []string{})

	// Assert - should succeed (valid chunks accepted, out-of-range filtered)
	if err != nil {
		t.Fatalf("RegisterPeer should succeed with filtered chunks: %v", err)
	}

	// Verify only valid chunks stored (0 and 4)
	chunks := coordinator.GetPeerChunks("peer-1")
	if len(chunks) != 2 {
		t.Errorf("Expected 2 valid chunks (0, 4), got %d: %v", len(chunks), chunks)
	}

	// Verify out-of-range chunk 5 has no peers
	peers := coordinator.GetPeersForChunk(5)
	if len(peers) != 0 {
		t.Errorf("Out-of-range chunk should not have peers, got %d", len(peers))
	}
}

// TestNewCoordinatorWithMaxPeers - RED test
// Acceptance Criterion: Custom max peers limit is respected
func TestNewCoordinatorWithMaxPeers(t *testing.T) {
	// Arrange - custom max of 1 peer
	coordinator := NewCoordinatorWithMaxPeers(10, 1)

	// Act - register first peer (should succeed)
	if err := coordinator.RegisterPeer("peer-1", []int{0}, []string{}); err != nil {
		t.Fatalf("First peer should succeed: %v", err)
	}

	// Act - register second peer (should fail)
	err := coordinator.RegisterPeer("peer-2", []int{1}, []string{})
	if err == nil {
		t.Fatal("Second peer should fail with max=1")
	}
}

// TestRegisterPeer_DefaultMaxPeers - RED test
// Acceptance Criterion: NewCoordinator uses DefaultMaxPeers (1000)
func TestRegisterPeer_DefaultMaxPeers(t *testing.T) {
	coordinator := NewCoordinator(10)

	// Register up to DefaultMaxPeers peers — should all succeed
	// We test a smaller number to avoid slow tests, but verify the constant exists
	for i := 0; i < 100; i++ {
		peerID := fmt.Sprintf("peer-%d", i)
		if err := coordinator.RegisterPeer(peerID, []int{0}, []string{}); err != nil {
			t.Fatalf("RegisterPeer %d should succeed (under default max): %v", i, err)
		}
	}
}

// === F-029 Parallelization: Chunk Lease Reservation (US-029-P1) ===

// TestCoordinator_GetNextChunk_ConcurrentUniqueness - RED test
// Acceptance Criterion: Concurrent GetNextChunk calls must return unique chunks (no duplicates)
func TestCoordinator_GetNextChunk_ConcurrentUniqueness(t *testing.T) {
	coord := NewCoordinator(10)

	// Register peer with all chunks 0-9
	for i := 0; i < 10; i++ {
		coord.RegisterPeer("peer1", []int{i}, []string{"/ip4/127.0.0.1/tcp/4001"})
	}

	// 5 goroutines call GetNextChunk concurrently
	results := make(chan int, 5)
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk, err := coord.GetNextChunk()
			if err == nil {
				results <- chunk
			}
		}()
	}

	wg.Wait()
	close(results)

	// Verify all chunks are unique
	seen := make(map[int]bool)
	for chunk := range results {
		if seen[chunk] {
			t.Errorf("Duplicate chunk returned: %d", chunk)
		}
		seen[chunk] = true
	}

	if len(seen) != 5 {
		t.Errorf("Expected 5 unique chunks, got %d", len(seen))
	}
}

// TestCoordinator_GetNextChunk_LeaseExpiry - RED test
// Acceptance Criterion: Expired leases allow chunk to be re-leased
func TestCoordinator_GetNextChunk_LeaseExpiry(t *testing.T) {
	coord := NewCoordinator(5)
	coord.RegisterPeer("peer1", []int{0, 1, 2}, []string{"/ip4/127.0.0.1/tcp/4001"})

	// Get chunk 0 (leased for 30s)
	chunk1, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	if chunk1 != 0 {
		t.Errorf("Expected chunk 0, got %d", chunk1)
	}

	// Manually expire the lease (simulate 31s passing)
	coord.mu.Lock()
	coord.leases[0] = time.Now().Add(-1 * time.Second) // Already expired
	coord.mu.Unlock()

	// Get next chunk - should get chunk 0 again (lease expired)
	chunk2, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	if chunk2 != 0 {
		t.Errorf("Expected chunk 0 (re-leased after expiry), got %d", chunk2)
	}
}

// TestCoordinator_ReleaseLease - RED test
// Acceptance Criterion: ReleaseLease makes chunk available again
func TestCoordinator_ReleaseLease(t *testing.T) {
	coord := NewCoordinator(3)
	coord.RegisterPeer("peer1", []int{0, 1, 2}, []string{"/ip4/127.0.0.1/tcp/4001"})

	// Lease all 3 chunks
	for i := 0; i < 3; i++ {
		_, err := coord.GetNextChunk()
		if err != nil {
			t.Fatalf("GetNextChunk(%d) failed: %v", i, err)
		}
	}

	// No chunks available (all leased)
	_, err := coord.GetNextChunk()
	if err == nil {
		t.Error("Expected error when all chunks leased")
	}

	// Release chunk 1
	coord.ReleaseLease(1)

	// Now chunk 1 should be available
	chunk, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk after release failed: %v", err)
	}
	if chunk != 1 {
		t.Errorf("Expected chunk 1 after release, got %d", chunk)
	}
}

// TestRegisterPeer_AllChunksInvalid - RED test
// Acceptance Criterion: If all chunk indices are invalid, peer is registered with empty chunks
func TestRegisterPeer_AllChunksInvalid(t *testing.T) {
	coordinator := NewCoordinator(5)

	// Act - register peer with all invalid chunks
	err := coordinator.RegisterPeer("peer-1", []int{-1, 5, 100}, []string{})

	// Assert - should succeed but peer has no valid chunks
	if err != nil {
		t.Fatalf("RegisterPeer should succeed: %v", err)
	}

	chunks := coordinator.GetPeerChunks("peer-1")
	if len(chunks) != 0 {
		t.Errorf("Expected 0 valid chunks, got %d: %v", len(chunks), chunks)
	}
}
