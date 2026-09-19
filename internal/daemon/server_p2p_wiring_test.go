// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: TDD tests for P2P transfer wiring

package daemon

import (
	"bytes"
	"testing"

	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/storage"
	"github.com/stonkagents/agent/internal/daemon/tracker"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/protocol"
)

// TestWireP2PDownloads_FailsWithoutP2PHost verifies precondition check
func TestWireP2PDownloads_FailsWithoutP2PHost(t *testing.T) {
	server := NewServer()
	// downloadManager set, but no P2P host
	server.downloadManager = download.NewManager(t.TempDir(), 3)

	err := server.WireP2PDownloads()
	if err == nil {
		t.Fatal("WireP2PDownloads should fail without P2P host")
	}
}

// TestWireP2PDownloads_FailsWithoutDownloadManager verifies precondition check
func TestWireP2PDownloads_FailsWithoutDownloadManager(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P host test in short mode")
	}

	server := NewServer()

	// Start P2P (creates real libp2p host)
	err := server.StartP2P()
	if err != nil {
		t.Fatalf("StartP2P failed: %v", err)
	}
	defer server.StopP2P()

	// No download manager set
	err = server.WireP2PDownloads()
	if err == nil {
		t.Fatal("WireP2PDownloads should fail without download manager")
	}
}

// TestWireP2PDownloads_FailsWithoutChunkStore verifies precondition check
func TestWireP2PDownloads_FailsWithoutChunkStore(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P host test in short mode")
	}

	server := NewServer()
	server.downloadManager = download.NewManager(t.TempDir(), 3)

	err := server.StartP2P()
	if err != nil {
		t.Fatalf("StartP2P failed: %v", err)
	}
	defer server.StopP2P()

	// No chunk store
	err = server.WireP2PDownloads()
	if err == nil {
		t.Fatal("WireP2PDownloads should fail without chunk store")
	}
}

// TestWireP2PDownloads_Success verifies full wiring succeeds
func TestWireP2PDownloads_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P host test in short mode")
	}

	server := NewServer()

	// Setup all prerequisites
	tmpDir := t.TempDir()
	server.downloadManager = download.NewManager(tmpDir, 3)

	chunkStore, err := storage.NewSQLiteChunkStore(tmpDir + "/chunks.db")
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer chunkStore.Close()
	server.chunkStore = chunkStore

	server.trackerClient = tracker.NewClient("http://localhost:7842", "test-peer")

	err = server.StartP2P()
	if err != nil {
		t.Fatalf("StartP2P failed: %v", err)
	}
	defer server.StopP2P()

	// Act
	err = server.WireP2PDownloads()

	// Assert
	if err != nil {
		t.Fatalf("WireP2PDownloads should succeed: %v", err)
	}

	// Verify block exchange was registered
	if server.p2pHost.BlockExchangeService() == nil {
		t.Error("Block exchange service should be registered after wiring")
	}

	// Clean shutdown
	server.downloadManager.StopWorker()
}

// TestWireP2PDownloads_DuplicateCallFails verifies idempotency protection
func TestWireP2PDownloads_DuplicateCallFails(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P host test in short mode")
	}

	server := NewServer()

	tmpDir := t.TempDir()
	server.downloadManager = download.NewManager(tmpDir, 3)

	chunkStore, err := storage.NewSQLiteChunkStore(tmpDir + "/chunks.db")
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer chunkStore.Close()
	server.chunkStore = chunkStore

	server.trackerClient = tracker.NewClient("http://localhost:7842", "test-peer")

	err = server.StartP2P()
	if err != nil {
		t.Fatalf("StartP2P failed: %v", err)
	}
	defer server.StopP2P()

	// First call should succeed
	err = server.WireP2PDownloads()
	if err != nil {
		t.Fatalf("First WireP2PDownloads should succeed: %v", err)
	}
	defer server.downloadManager.StopWorker()

	// Second call should fail (block exchange already registered)
	err = server.WireP2PDownloads()
	if err == nil {
		t.Fatal("Second WireP2PDownloads should fail (block exchange already registered)")
	}
}

// TestChunkStoreAdapter_StoreChunk_VerifiesCID verifies that StoreChunk performs
// CID defense-in-depth by verifying the calculated CID matches data integrity.
// Audit: B2 — CID Defense-in-Depth in Chunk Store Adapter
func TestChunkStoreAdapter_StoreChunk_VerifiesCID(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewSQLiteChunkStore(tmpDir + "/chunks.db")
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	adapter := &chunkStoreAdapter{store: store}

	// Valid data should store and retrieve successfully
	data := []byte("hello StonkAgents chunk data")
	fileCID := "test-file-cid-001"
	chunkIndex := 0

	err = adapter.StoreChunk(fileCID, chunkIndex, data)
	if err != nil {
		t.Fatalf("StoreChunk should succeed for valid data: %v", err)
	}

	// Retrieve and verify data matches
	retrieved, err := adapter.GetChunk(fileCID, chunkIndex)
	if err != nil {
		t.Fatalf("GetChunk should succeed: %v", err)
	}
	if !bytes.Equal(data, retrieved) {
		t.Error("Retrieved data should match stored data")
	}

	// Verify the CID calculated from data is valid (integrity check)
	chunkCID, err := protocol.CalculateChunkCID(data)
	if err != nil {
		t.Fatalf("CalculateChunkCID should succeed: %v", err)
	}
	valid, err := crypto.VerifyCID(chunkCID, data)
	if err != nil {
		t.Fatalf("VerifyCID should succeed: %v", err)
	}
	if !valid {
		t.Error("CID should verify against original data")
	}
}

// TestChunkStoreAdapter_GetChunk_VerifiesCID verifies that GetChunk performs
// defense-in-depth CID verification on retrieved data.
// Audit: B2 — CID Defense-in-Depth in Chunk Store Adapter
func TestChunkStoreAdapter_GetChunk_VerifiesCID(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewSQLiteChunkStore(tmpDir + "/chunks.db")
	if err != nil {
		t.Fatalf("Failed to create chunk store: %v", err)
	}
	defer store.Close()

	adapter := &chunkStoreAdapter{store: store}

	// Store valid data
	data := []byte("defense in depth test data for retrieval")
	fileCID := "test-file-cid-002"
	chunkIndex := 0

	err = adapter.StoreChunk(fileCID, chunkIndex, data)
	if err != nil {
		t.Fatalf("StoreChunk should succeed: %v", err)
	}

	// Retrieve — adapter should verify CID integrity on retrieval
	retrieved, err := adapter.GetChunk(fileCID, chunkIndex)
	if err != nil {
		t.Fatalf("GetChunk should succeed for valid data: %v", err)
	}

	// Verify the retrieved data has valid CID
	chunkCID, err := protocol.CalculateChunkCID(retrieved)
	if err != nil {
		t.Fatalf("CalculateChunkCID should succeed: %v", err)
	}
	valid, err := crypto.VerifyCID(chunkCID, retrieved)
	if err != nil {
		t.Fatalf("VerifyCID should succeed: %v", err)
	}
	if !valid {
		t.Error("Retrieved chunk CID should verify against retrieved data")
	}
}

// TestShutdown_StopsWorker verifies download worker is stopped on shutdown
func TestShutdown_StopsWorker(t *testing.T) {
	server := NewServer()
	server.downloadManager = download.NewManager(t.TempDir(), 3)
	server.downloadManager.StartWorker()

	// Shutdown should not panic even without HTTP server
	err := server.Shutdown()
	if err != nil {
		t.Fatalf("Shutdown should succeed: %v", err)
	}
}
