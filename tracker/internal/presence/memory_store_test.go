// Package: tracker/internal/presence
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: Tests for in-memory presence store with TTL

package presence

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

const testTTL = 5 * time.Minute

func TestMemoryPresence_Heartbeat_NewPeer(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	err := store.Heartbeat(ctx, "peer-1", testTTL)
	if err != nil {
		t.Fatalf("Heartbeat() unexpected error: %v", err)
	}

	online, err := store.IsOnline(ctx, "peer-1")
	if err != nil {
		t.Fatalf("IsOnline() unexpected error: %v", err)
	}
	if !online {
		t.Error("IsOnline() = false after heartbeat, want true")
	}
}

func TestMemoryPresence_Heartbeat_RefreshExisting(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", testTTL)

	// Advance 3 minutes, send another heartbeat
	clk.Advance(3 * time.Minute)
	_ = store.Heartbeat(ctx, "peer-1", testTTL)

	// Advance another 3 minutes (6 total, but only 3 since last heartbeat)
	clk.Advance(3 * time.Minute)

	online, _ := store.IsOnline(ctx, "peer-1")
	if !online {
		t.Error("IsOnline() = false after refresh, want true (only 3min since last heartbeat)")
	}
}

func TestMemoryPresence_IsOnline_NeverSeen(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	online, err := store.IsOnline(ctx, "unknown-peer")
	if err != nil {
		t.Fatalf("IsOnline() unexpected error: %v", err)
	}
	if online {
		t.Error("IsOnline() = true for never-seen peer, want false")
	}
}

func TestMemoryPresence_IsOnline_Expired(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", testTTL)

	// Advance past TTL
	clk.Advance(6 * time.Minute)

	online, _ := store.IsOnline(ctx, "peer-1")
	if online {
		t.Error("IsOnline() = true after TTL expired, want false")
	}
}

func TestMemoryPresence_OnlinePeers_Mixed(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-a", testTTL)
	_ = store.Heartbeat(ctx, "peer-b", testTTL)
	_ = store.Heartbeat(ctx, "peer-c", testTTL)

	// Advance 3 minutes, refresh only peer-a and peer-b
	clk.Advance(3 * time.Minute)
	_ = store.Heartbeat(ctx, "peer-a", testTTL)
	_ = store.Heartbeat(ctx, "peer-b", testTTL)

	// Advance 3 more minutes — peer-c has now been idle for 6 min (expired)
	clk.Advance(3 * time.Minute)

	ids, err := store.OnlinePeerIDs(ctx)
	if err != nil {
		t.Fatalf("OnlinePeerIDs() unexpected error: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("OnlinePeerIDs() got %d peers, want 2", len(ids))
	}

	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	if !idSet["peer-a"] || !idSet["peer-b"] {
		t.Errorf("OnlinePeerIDs() missing expected peers, got %v", ids)
	}
	if idSet["peer-c"] {
		t.Error("OnlinePeerIDs() should not include expired peer-c")
	}
}

func TestMemoryPresence_Remove(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", testTTL)

	err := store.Remove(ctx, "peer-1")
	if err != nil {
		t.Fatalf("Remove() unexpected error: %v", err)
	}

	online, _ := store.IsOnline(ctx, "peer-1")
	if online {
		t.Error("IsOnline() = true after Remove(), want false")
	}
}

func TestMemoryPresence_OnlinePeerIDs_Empty(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	ctx := context.Background()

	ids, err := store.OnlinePeerIDs(ctx)
	if err != nil {
		t.Fatalf("OnlinePeerIDs() unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("OnlinePeerIDs() got %d peers, want 0", len(ids))
	}
}

// TestMemoryPresence_CapacityLimit - SECURITY TEST
// Verifies that memory exhaustion DoS is prevented by capacity limits
func TestMemoryPresence_CapacityLimit(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	defer store.Close()

	// Set low capacity for testing
	store.SetMaxPeers(100)
	ctx := context.Background()

	// Register 100 peers (should succeed)
	for i := 0; i < 100; i++ {
		peerID := fmt.Sprintf("peer-%d", i)
		err := store.Heartbeat(ctx, peerID, testTTL)
		if err != nil {
			t.Fatalf("Failed to register peer %d: %v", i, err)
		}
	}

	// Try to register 101st peer (should fail)
	err := store.Heartbeat(ctx, "peer-101", testTTL)
	if err != ErrCapacityExceeded {
		t.Errorf("Expected ErrCapacityExceeded for 101st peer, got: %v", err)
	}

	// Verify existing peer can still refresh
	err = store.Heartbeat(ctx, "peer-50", testTTL)
	if err != nil {
		t.Errorf("Existing peer refresh failed: %v", err)
	}
}

// TestMemoryPresence_CapacityLimitWithExpiredEviction - SECURITY TEST
// Verifies that expired entries are evicted to make room for new peers
func TestMemoryPresence_CapacityLimitWithExpiredEviction(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	defer store.Close()

	store.SetMaxPeers(10)
	ctx := context.Background()

	// Register 10 peers
	for i := 0; i < 10; i++ {
		peerID := fmt.Sprintf("peer-%d", i)
		err := store.Heartbeat(ctx, peerID, 5*time.Minute)
		if err != nil {
			t.Fatalf("Failed to register peer %d: %v", i, err)
		}
	}

	// Advance time beyond TTL to expire all peers
	clk.Advance(6 * time.Minute)

	// New peer should succeed because expired entries are evicted
	err := store.Heartbeat(ctx, "new-peer", testTTL)
	if err != nil {
		t.Errorf("Expected new peer to succeed after expiration, got error: %v", err)
	}

	// Verify expired peers were actually removed
	ids, _ := store.OnlinePeerIDs(ctx)
	if len(ids) != 1 {
		t.Errorf("Expected 1 online peer after eviction, got %d", len(ids))
	}
}

// TestMemoryPresence_BackgroundCleanup - SECURITY TEST
// Verifies that background cleanup prevents memory leaks from expired entries
func TestMemoryPresence_BackgroundCleanup(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	store := NewMemoryPresenceStore(clk)
	defer store.Close()

	ctx := context.Background()

	// Register peers with short TTL
	for i := 0; i < 100; i++ {
		peerID := fmt.Sprintf("peer-%d", i)
		err := store.Heartbeat(ctx, peerID, 1*time.Minute)
		if err != nil {
			t.Fatalf("Failed to register peer %d: %v", i, err)
		}
	}

	// Manually trigger cleanup (simulate background goroutine)
	clk.Advance(2 * time.Minute)
	store.evictExpired()

	// All peers should be evicted
	ids, _ := store.OnlinePeerIDs(ctx)
	if len(ids) != 0 {
		t.Errorf("Expected 0 online peers after cleanup, got %d", len(ids))
	}

	// Verify map is empty (no memory leak)
	store.mu.RLock()
	mapSize := len(store.peers)
	store.mu.RUnlock()
	if mapSize != 0 {
		t.Errorf("Expected empty peer map after cleanup, got %d entries", mapSize)
	}
}
