// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P7 (Observability)
// Purpose: TDD tests for download metrics and structured logging

package download

import (
	"testing"
)

// TestMetrics_LeaseTracking - RED test
// Acceptance Criterion: Metrics tracks lease acquire, release, expire events
func TestMetrics_LeaseTracking(t *testing.T) {
	m := NewMetrics()

	// Simulate lease lifecycle
	m.RecordLeaseAcquired(0, 1)
	m.RecordLeaseAcquired(1, 2)
	m.RecordLeaseReleased(0, "completed")
	m.RecordLeaseReleased(1, "failed")
	m.RecordLeaseExpired(2)

	snapshot := m.Snapshot()

	if snapshot["lease_acquired"] != 2 {
		t.Errorf("Expected 2 leases acquired, got %v", snapshot["lease_acquired"])
	}

	if snapshot["lease_released"] != 2 {
		t.Errorf("Expected 2 leases released, got %v", snapshot["lease_released"])
	}

	if snapshot["lease_expired"] != 1 {
		t.Errorf("Expected 1 lease expired, got %v", snapshot["lease_expired"])
	}
}

// TestMetrics_RetryTracking - RED test
// Acceptance Criterion: Metrics tracks per-chunk retry counts
func TestMetrics_RetryTracking(t *testing.T) {
	m := NewMetrics()

	// Simulate retries for chunk 5
	m.RecordChunkRetry(5, 1, "network_timeout")
	m.RecordChunkRetry(5, 2, "network_timeout")
	m.RecordChunkRetry(5, 3, "peer_disconnect")

	snapshot := m.Snapshot()
	retries := snapshot["chunk_retries"].(map[int]int)

	if retries[5] != 3 {
		t.Errorf("Expected 3 retries for chunk 5, got %d", retries[5])
	}
}

// TestMetrics_WorkerGauges - RED test
// Acceptance Criterion: Worker in-flight/idle gauges update correctly
func TestMetrics_WorkerGauges(t *testing.T) {
	m := NewMetrics()

	m.RecordWorkerInFlight(1)
	m.RecordWorkerInFlight(1)
	m.RecordWorkerIdle(1)

	snapshot := m.Snapshot()

	if snapshot["workers_in_flight"] != 2 {
		t.Errorf("Expected 2 workers in flight, got %v", snapshot["workers_in_flight"])
	}
	if snapshot["workers_idle"] != 1 {
		t.Errorf("Expected 1 worker idle, got %v", snapshot["workers_idle"])
	}

	// Worker finishes: decrement in-flight
	m.RecordWorkerInFlight(-1)
	snapshot = m.Snapshot()
	if snapshot["workers_in_flight"] != 1 {
		t.Errorf("Expected 1 worker in flight after decrement, got %v", snapshot["workers_in_flight"])
	}
}

// TestMetrics_PeerConcurrency - RED test
// Acceptance Criterion: Per-peer active request counts track correctly
func TestMetrics_PeerConcurrency(t *testing.T) {
	m := NewMetrics()

	m.RecordPeerConcurrency("peer1", 1)
	m.RecordPeerConcurrency("peer1", 1)
	m.RecordPeerConcurrency("peer2", 1)

	snapshot := m.Snapshot()
	concurrency := snapshot["peer_concurrency"].(map[string]int)

	if concurrency["peer1"] != 2 {
		t.Errorf("Expected peer1 concurrency 2, got %d", concurrency["peer1"])
	}
	if concurrency["peer2"] != 1 {
		t.Errorf("Expected peer2 concurrency 1, got %d", concurrency["peer2"])
	}

	// Peer finishes a request
	m.RecordPeerConcurrency("peer1", -1)
	snapshot = m.Snapshot()
	concurrency = snapshot["peer_concurrency"].(map[string]int)
	if concurrency["peer1"] != 1 {
		t.Errorf("Expected peer1 concurrency 1 after decrement, got %d", concurrency["peer1"])
	}
}

// TestMetrics_Snapshot_IsCopy - RED test
// Acceptance Criterion: Snapshot returns copies, not references to internal state
func TestMetrics_Snapshot_IsCopy(t *testing.T) {
	m := NewMetrics()

	m.RecordPeerConcurrency("peer1", 1)
	m.RecordChunkRetry(0, 1, "timeout")

	snapshot := m.Snapshot()

	// Mutate snapshot maps — should NOT affect internal state
	peerConcurrency := snapshot["peer_concurrency"].(map[string]int)
	peerConcurrency["peer1"] = 999

	chunkRetries := snapshot["chunk_retries"].(map[int]int)
	chunkRetries[0] = 999

	// Get fresh snapshot — should be unchanged
	fresh := m.Snapshot()
	freshPeers := fresh["peer_concurrency"].(map[string]int)
	freshRetries := fresh["chunk_retries"].(map[int]int)

	if freshPeers["peer1"] != 1 {
		t.Errorf("Snapshot mutation leaked to internal state: peer1=%d, want 1", freshPeers["peer1"])
	}
	if freshRetries[0] != 1 {
		t.Errorf("Snapshot mutation leaked to internal state: chunk0=%d, want 1", freshRetries[0])
	}
}
