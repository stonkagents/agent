// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P8 (Endgame Mode)
// Purpose: TDD tests for endgame mode — final chunk acceleration (TD-057)

package download

import (
	"testing"
)

// TestEndgameThreshold calculates when endgame should trigger.
// Rule: remaining chunks ≤ max(8, totalChunks/20)
func TestEndgameThreshold(t *testing.T) {
	tests := []struct {
		name        string
		totalChunks int
		want        int
	}{
		{"small file (10 chunks)", 10, 8},           // max(8, 10/20=0) = 8
		{"medium file (100 chunks)", 100, 8},        // max(8, 100/20=5) = 8
		{"large file (200 chunks)", 200, 10},        // max(8, 200/20=10) = 10
		{"very large file (1000 chunks)", 1000, 50}, // max(8, 1000/20=50) = 50
		{"tiny file (3 chunks)", 3, 8},              // max(8, 3/20=0) = 8
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EndgameThreshold(tt.totalChunks)
			if got != tt.want {
				t.Errorf("EndgameThreshold(%d) = %d, want %d", tt.totalChunks, got, tt.want)
			}
		})
	}
}

// TestCoordinator_ShouldEnterEndgame checks the auto-detection logic.
func TestCoordinator_ShouldEnterEndgame(t *testing.T) {
	coord := NewCoordinator(20) // threshold = max(8, 20/20=1) = 8
	coord.RegisterPeer("peer1", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, nil)

	// 0 completed → 20 remaining → no endgame
	if coord.ShouldEnterEndgame() {
		t.Error("should not enter endgame with 20 remaining")
	}

	// Complete 12 chunks → 8 remaining → threshold reached
	for i := 0; i < 12; i++ {
		coord.MarkChunkComplete(i)
	}
	if !coord.ShouldEnterEndgame() {
		t.Error("should enter endgame with 8 remaining (threshold=8)")
	}

	// Complete 1 more → 7 remaining → still endgame
	coord.MarkChunkComplete(12)
	if !coord.ShouldEnterEndgame() {
		t.Error("should still be in endgame with 7 remaining")
	}
}

// TestCoordinator_EnterEndgame_State verifies the mode flag.
func TestCoordinator_EnterEndgame_State(t *testing.T) {
	coord := NewCoordinator(10)

	if coord.IsEndgame() {
		t.Error("should not be in endgame initially")
	}

	coord.EnterEndgame()

	if !coord.IsEndgame() {
		t.Error("should be in endgame after EnterEndgame()")
	}
}

// TestCoordinator_EndgameGetNextChunk_IgnoresLeases verifies that in endgame
// mode, already-leased (but not completed) chunks are returned for parallel download.
func TestCoordinator_EndgameGetNextChunk_IgnoresLeases(t *testing.T) {
	coord := NewCoordinator(3)
	coord.RegisterPeer("peer1", []int{0, 1, 2}, nil)

	// Lease chunk 0 and 1 in normal mode
	c0, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	c1, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	c2, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	_ = c0
	_ = c1
	_ = c2

	// Normal mode: all chunks leased → no available chunks
	_, err = coord.GetNextChunk()
	if err == nil {
		t.Fatal("expected error in normal mode when all chunks leased")
	}

	// Enter endgame
	coord.EnterEndgame()

	// Endgame mode: leased chunks should be available again
	chunk, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("endgame GetNextChunk should return leased chunk, got error: %v", err)
	}

	// Should return one of the leased chunks (not completed)
	if chunk < 0 || chunk > 2 {
		t.Errorf("unexpected chunk %d in endgame", chunk)
	}
}

// TestCoordinator_EndgameGetNextChunk_SkipsCompleted verifies that completed
// chunks are never returned even in endgame mode.
func TestCoordinator_EndgameGetNextChunk_SkipsCompleted(t *testing.T) {
	coord := NewCoordinator(3)
	coord.RegisterPeer("peer1", []int{0, 1, 2}, nil)

	// Complete chunks 0 and 1
	coord.MarkChunkComplete(0)
	coord.MarkChunkComplete(1)

	// Lease chunk 2
	c2, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("GetNextChunk failed: %v", err)
	}
	if c2 != 2 {
		t.Fatalf("expected chunk 2, got %d", c2)
	}

	// Normal mode: chunk 2 is leased → no available
	_, err = coord.GetNextChunk()
	if err == nil {
		t.Fatal("expected error when only chunk 2 is leased")
	}

	// Enter endgame
	coord.EnterEndgame()

	// Should return chunk 2 (leased but not completed)
	chunk, err := coord.GetNextChunk()
	if err != nil {
		t.Fatalf("endgame should return leased-but-incomplete chunk: %v", err)
	}
	if chunk != 2 {
		t.Errorf("expected chunk 2 (only incomplete), got %d", chunk)
	}
}

// TestCoordinator_EndgameDuplicateCompletion verifies that completing
// a chunk that's already complete is a safe no-op.
func TestCoordinator_EndgameDuplicateCompletion(t *testing.T) {
	coord := NewCoordinator(3)
	coord.RegisterPeer("peer1", []int{0, 1, 2}, nil)

	// Mark chunk 0 complete
	coord.MarkChunkComplete(0)
	if !coord.IsComplete(0) {
		t.Fatal("chunk 0 should be complete")
	}

	// Mark chunk 0 complete again (duplicate) — should not panic
	coord.MarkChunkComplete(0)
	if !coord.IsComplete(0) {
		t.Fatal("chunk 0 should still be complete after duplicate mark")
	}

	// Verify progress is correct (not double-counted)
	completed := coord.GetCompletedChunks()
	if len(completed) != 1 {
		t.Errorf("expected 1 completed chunk, got %d", len(completed))
	}
}

// TestCoordinator_EndgameAllChunksCompleteStillWorks verifies AllChunksComplete
// works correctly after endgame mode is activated.
func TestCoordinator_EndgameAllChunksCompleteStillWorks(t *testing.T) {
	coord := NewCoordinator(3)
	coord.RegisterPeer("peer1", []int{0, 1, 2}, nil)

	coord.EnterEndgame()

	coord.MarkChunkComplete(0)
	coord.MarkChunkComplete(1)
	if coord.AllChunksComplete() {
		t.Error("should not be complete with 1 remaining")
	}

	coord.MarkChunkComplete(2)
	if !coord.AllChunksComplete() {
		t.Error("should be complete after all chunks marked")
	}
}

// TestCoordinator_RemainingChunks counts incomplete chunks.
func TestCoordinator_RemainingChunks(t *testing.T) {
	coord := NewCoordinator(5)
	coord.RegisterPeer("peer1", []int{0, 1, 2, 3, 4}, nil)

	if got := coord.RemainingChunks(); got != 5 {
		t.Errorf("expected 5 remaining, got %d", got)
	}

	coord.MarkChunkComplete(0)
	coord.MarkChunkComplete(1)
	if got := coord.RemainingChunks(); got != 3 {
		t.Errorf("expected 3 remaining, got %d", got)
	}

	coord.MarkChunkComplete(2)
	coord.MarkChunkComplete(3)
	coord.MarkChunkComplete(4)
	if got := coord.RemainingChunks(); got != 0 {
		t.Errorf("expected 0 remaining, got %d", got)
	}
}

// TestCoordinator_ResetClearsEndgame verifies that Reset clears endgame state.
func TestCoordinator_ResetClearsEndgame(t *testing.T) {
	coord := NewCoordinator(5)
	coord.EnterEndgame()

	if !coord.IsEndgame() {
		t.Fatal("should be in endgame")
	}

	coord.Reset(10)

	if coord.IsEndgame() {
		t.Error("Reset should clear endgame mode")
	}
}
