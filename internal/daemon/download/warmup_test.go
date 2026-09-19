// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P3 (Connection Pre-Establishment)
// Purpose: TDD tests for connection warmup — pre-connect to peers before worker pool (TD-059)

package download

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// mockPeerConnectorWithDelay simulates connection with configurable delay.
type mockPeerConnectorWithDelay struct {
	delay        time.Duration
	connectCalls atomic.Int32
}

func (m *mockPeerConnectorWithDelay) ConnectToPeer(pid peer.ID, addrs []string) error {
	m.connectCalls.Add(1)
	time.Sleep(m.delay)
	return nil
}

// TestWarmupConnections_ConnectsAllPeers verifies that warmup attempts
// to connect to all registered peers.
func TestWarmupConnections_ConnectsAllPeers(t *testing.T) {
	connector := &mockPeerConnectorWithDelay{delay: 10 * time.Millisecond}
	coord := NewCoordinator(5)

	// Use valid peer IDs (generated via peer.IDFromPrivateKey)
	peerIDs := []string{
		"12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		"12D3KooWRi7j8ap6SuPycdDiyyXr6fLUVPyVB1jivsVXSLE6QS9f",
	}
	for _, pid := range peerIDs {
		coord.RegisterPeer(pid, []int{0, 1, 2, 3, 4}, []string{"/ip4/127.0.0.1/tcp/4001"})
	}

	warmupConnections(context.Background(), coord, connector, 500*time.Millisecond)

	if got := connector.connectCalls.Load(); got != 2 {
		t.Errorf("expected 2 connect calls, got %d", got)
	}
}

// TestWarmupConnections_RespectsDeadline verifies that warmup returns
// within the timeout even if connections are slow.
func TestWarmupConnections_RespectsDeadline(t *testing.T) {
	connector := &mockPeerConnectorWithDelay{delay: 2 * time.Second}
	coord := NewCoordinator(3)
	coord.RegisterPeer(
		"12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		[]int{0, 1, 2},
		[]string{"/ip4/127.0.0.1/tcp/4001"},
	)

	start := time.Now()
	warmupConnections(context.Background(), coord, connector, 200*time.Millisecond)
	elapsed := time.Since(start)

	// Should return within ~200ms (plus small margin), not 2s
	if elapsed > 500*time.Millisecond {
		t.Errorf("warmup took %v, expected ≤500ms (deadline=200ms)", elapsed)
	}
}

// TestWarmupConnections_ParallelNotSequential verifies connections run
// concurrently, not sequentially.
func TestWarmupConnections_ParallelNotSequential(t *testing.T) {
	connector := &mockPeerConnectorWithDelay{delay: 100 * time.Millisecond}
	coord := NewCoordinator(5)

	// 5 peers at 100ms each → sequential = 500ms, parallel = ~100ms
	peerIDs := []string{
		"12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		"12D3KooWRi7j8ap6SuPycdDiyyXr6fLUVPyVB1jivsVXSLE6QS9f",
		"12D3KooWMBYeBF7mYCjRT2X1qthFmbrghjjhsKowVqG5mX5onf94",
		"12D3KooWRWr2Fhw31ZxpdeVUDG7KactFjztH7pLKx9athas4MEpZ",
		"12D3KooWL3XvabUwiy5rp1B7qhDV2GKCLunFwFSYhgiH1DdoidRh",
	}
	for _, pid := range peerIDs {
		coord.RegisterPeer(pid, []int{0, 1, 2, 3, 4}, []string{"/ip4/127.0.0.1/tcp/4001"})
	}

	start := time.Now()
	warmupConnections(context.Background(), coord, connector, 500*time.Millisecond)
	elapsed := time.Since(start)

	// Parallel: ~100ms. Sequential would be ~500ms.
	if elapsed > 300*time.Millisecond {
		t.Errorf("warmup took %v, expected ≤300ms (parallel 100ms connections)", elapsed)
	}

	if got := connector.connectCalls.Load(); got != 5 {
		t.Errorf("expected 5 connect calls, got %d", got)
	}
}

// TestWarmupConnections_NoPeers is a no-op when no peers registered.
func TestWarmupConnections_NoPeers(t *testing.T) {
	connector := &mockPeerConnectorWithDelay{delay: 10 * time.Millisecond}
	coord := NewCoordinator(5)

	// Should not panic or block
	warmupConnections(context.Background(), coord, connector, 500*time.Millisecond)

	if got := connector.connectCalls.Load(); got != 0 {
		t.Errorf("expected 0 connect calls with no peers, got %d", got)
	}
}

// TestWarmupConnections_RespectsContext verifies that warmup stops
// if the parent context is cancelled.
func TestWarmupConnections_RespectsContext(t *testing.T) {
	connector := &mockPeerConnectorWithDelay{delay: 2 * time.Second}
	coord := NewCoordinator(3)
	coord.RegisterPeer(
		"12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		[]int{0, 1, 2},
		[]string{"/ip4/127.0.0.1/tcp/4001"},
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already cancelled

	start := time.Now()
	warmupConnections(ctx, coord, connector, 5*time.Second)
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Errorf("warmup with cancelled context took %v, expected near-instant", elapsed)
	}
}
