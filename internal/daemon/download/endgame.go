// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P8 (Endgame Mode)
// Purpose: Endgame mode for final chunk acceleration — when few chunks remain,
//          allow duplicate requests from multiple peers; first completion wins (TD-057)

package download

// EndgameThreshold returns the chunk count at or below which endgame mode
// should activate. Rule: max(8, totalChunks/20).
func EndgameThreshold(totalChunks int) int {
	dynamic := totalChunks / 20
	if dynamic > 8 {
		return dynamic
	}
	return 8
}

// EnterEndgame activates endgame mode on the coordinator.
// In endgame, GetNextChunk ignores lease exclusivity — multiple workers
// may download the same chunk in parallel from different peers.
func (c *Coordinator) EnterEndgame() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endgameMode = true
}

// IsEndgame reports whether endgame mode is active.
func (c *Coordinator) IsEndgame() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.endgameMode
}

// ShouldEnterEndgame checks whether the remaining chunk count has
// fallen to or below the endgame threshold.
func (c *Coordinator) ShouldEnterEndgame() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	remaining := c.totalChunks - len(c.completedChunks)
	return remaining <= EndgameThreshold(c.totalChunks)
}

// RemainingChunks returns the number of incomplete chunks.
func (c *Coordinator) RemainingChunks() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalChunks - len(c.completedChunks)
}
