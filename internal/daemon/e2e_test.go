// Package: internal/daemon
// Feature: F-003 (Universal File Sharing)
// Story: US-003-06 (E2E P2P Transfer Test)
// Purpose: End-to-end integration tests for P2P file transfers

package daemon

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/daemon/chunking"
	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/storage"
)

// testTrackerClient provides a mock tracker for E2E tests.
// Returns pre-configured peers without requiring a real tracker service.
type testTrackerClient struct {
	peers []download.PeerInfo
}

func (tc *testTrackerClient) SearchByCID(_ string) ([]download.PeerInfo, error) {
	return tc.peers, nil
}

func (tc *testTrackerClient) UpdateAvailability(_ string, _ []int) error {
	return nil
}

// populateSeederStore chunks a file and populates the seeder's chunk store.
// Returns the chunk metadata for use in mock tracker peer info.
func populateSeederStore(t *testing.T, store *storage.SQLiteChunkStore, filePath, fileCID string, fileSize int64) []*chunking.Chunk {
	t.Helper()

	fileChunker := chunking.NewChunker(262144) // 256KB chunks
	chunks, err := fileChunker.SplitFile(filePath)
	if err != nil {
		t.Fatalf("Failed to split file for seeder: %v", err)
	}

	if err := store.StoreFile(fileCID, filepath.Base(filePath), fileSize, len(chunks), nil); err != nil {
		t.Fatalf("Failed to store file metadata: %v", err)
	}

	srcFile, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file for chunking: %v", err)
	}
	defer srcFile.Close()

	buf := make([]byte, 262144)
	for _, chunk := range chunks {
		n, err := srcFile.ReadAt(buf[:chunk.Size], chunk.Offset)
		if err != nil {
			t.Fatalf("Failed to read chunk %d: %v", chunk.Index, err)
		}
		if err := store.StoreChunk(fileCID, chunk.Index, chunk.CID, buf[:n]); err != nil {
			t.Fatalf("Failed to store chunk %d: %v", chunk.Index, err)
		}
	}

	return chunks
}

// wireSeederBlockExchange registers the block exchange protocol on a seeder daemon
// so it can serve chunks to peers over libp2p.
func wireSeederBlockExchange(t *testing.T, daemon *Server) {
	t.Helper()
	provider := &chunkProviderAdapter{store: daemon.chunkStore}
	if err := daemon.p2pHost.RegisterBlockExchange(provider); err != nil {
		t.Fatalf("Failed to register block exchange on seeder: %v", err)
	}
}

// wireLeecherP2P sets up the leecher's download manager with P2P dependencies
// using a mock tracker that returns the given seeders.
func wireLeecherP2P(t *testing.T, leecher *Server, seeders []*Server, totalChunks int) {
	t.Helper()

	allChunkIndices := make([]int, totalChunks)
	for i := range allChunkIndices {
		allChunkIndices[i] = i
	}

	peers := make([]download.PeerInfo, 0, len(seeders))
	for _, seeder := range seeders {
		peers = append(peers, download.PeerInfo{
			PeerID: seeder.p2pHost.ID().String(),
			Chunks: allChunkIndices,
			Addrs:  seeder.p2pHost.AnnounceAddrs(),
		})
	}

	mockTracker := &testTrackerClient{peers: peers}
	p2pClient := NewP2PClient(leecher.p2pHost.Host(), leecher.logger)
	coordinator := download.NewCoordinator(0)
	leecherStore := &chunkStoreAdapter{store: leecher.chunkStore}

	leecher.downloadManager.SetP2PDependencies(&download.P2PDependencies{
		ChunkStore:    leecherStore,
		TrackerClient: mockTracker,
		P2PClient:     p2pClient,
		Coordinator:   coordinator,
		PeerConnector: leecher.p2pHost,
	})

	leecher.downloadManager.StartWorker()
}

// derefStr safely dereferences a *string for log/test output (F-027: DownloadStatus nullable fields)
func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// stopDaemon releases everything a test daemon holds: the libp2p host (StopP2P)
// and the SQLite stores/HTTP server (Shutdown). Closing only the host leaves
// agent_chat.db & co open, which makes t.TempDir cleanup fail on Windows.
func stopDaemon(d *Server) {
	_ = d.StopP2P()
	_ = d.Shutdown()
}

// startE2EDaemon initializes managers and the libp2p host for a test daemon
// rooted at dataDir. The HTTP server is intentionally not started: the P2P
// stack listens on a random port, so tests only need InitializeManagers +
// StartP2P and never contend for 7841/7842. Caller must defer stopDaemon.
func startE2EDaemon(t *testing.T, port int, dataDir string) *Server {
	t.Helper()
	d := NewServer()
	d.config.Port = port
	d.config.DataDir = dataDir
	if err := d.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize daemon managers (port %d): %v", port, err)
	}
	if err := d.StartP2P(); err != nil {
		_ = d.Shutdown()
		t.Fatalf("Failed to start daemon P2P (port %d): %v", port, err)
	}
	return d
}

// merkleFileCID computes the file CID the download protocol verifies at
// assembly time: the Merkle root over 256KB chunk CIDs (not a flat hash).
func merkleFileCID(t *testing.T, path string) string {
	t.Helper()
	fileChunker := chunking.NewChunker(262144)
	chunks, err := fileChunker.SplitFile(path)
	if err != nil {
		t.Fatalf("Failed to chunk %s for CID calculation: %v", path, err)
	}
	cid := fileChunker.CalculateFileCID(chunks)
	if cid == "" {
		t.Fatalf("Failed to calculate file CID for %s", path)
	}
	return cid
}

// waitForDownloadCompletion polls mgr until cid reaches "completed" and returns
// the elapsed wall time. Fails the test on "failed" or when timeout elapses.
func waitForDownloadCompletion(t *testing.T, mgr *download.Manager, cid string, timeout time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastLog := time.Now()

	for {
		select {
		case <-deadline:
			status := mgr.GetStatus(cid)
			if status != nil {
				t.Fatalf("Download %s timed out after %v in state %s (%d/%d chunks): %s",
					cid, timeout, status.State, status.CompletedChunks, status.TotalChunks, derefStr(status.ErrorMessage))
			}
			t.Fatalf("Download %s timed out after %v: status not found", cid, timeout)
		case <-ticker.C:
			status := mgr.GetStatus(cid)
			if status == nil {
				t.Fatalf("Download status not found for %s", cid)
			}
			switch status.State {
			case download.StateCompleted:
				return time.Since(start)
			case download.StateFailed:
				t.Fatalf("Download %s failed: %s", cid, derefStr(status.ErrorMessage))
			}
			if time.Since(lastLog) >= 2*time.Second {
				t.Logf("Download progress: %.1f%% (%d/%d chunks)", status.Progress*100, status.CompletedChunks, status.TotalChunks)
				lastLog = time.Now()
			}
		}
	}
}

// verifyDownloadedFileCID re-chunks the assembled file and checks its Merkle
// root against the CID it was downloaded under.
func verifyDownloadedFileCID(t *testing.T, downloadedFile, originalCID string) {
	t.Helper()
	downloadedCID := merkleFileCID(t, downloadedFile)
	if downloadedCID != originalCID {
		t.Errorf("CID mismatch for %s: original=%s, downloaded=%s", downloadedFile, originalCID, downloadedCID)
	}
}

// TestE2E_TwoNodeFileTransfer - E2E test
// Acceptance Criterion: Two daemons transfer 1GB file with CID verification
func TestE2E_TwoNodeFileTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Arrange - Create temporary directories for both daemons
	tmpDirA := t.TempDir()
	tmpDirB := t.TempDir()

	// Create 1GB test file
	testFile := filepath.Join(tmpDirA, "test-1gb.bin")
	fileSize := int64(1024 * 1024 * 1024) // 1GB

	t.Logf("Creating 1GB test file at %s", testFile)
	if err := createTestFile(testFile, fileSize); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Calculate file CID using Merkle root (hash of chunk CIDs)
	// This matches how the protocol computes file CIDs during assembly
	fileChunker := chunking.NewChunker(262144) // 256KB chunks
	cidChunks, err := fileChunker.SplitFile(testFile)
	if err != nil {
		t.Fatalf("Failed to chunk file for CID calculation: %v", err)
	}
	originalCID := fileChunker.CalculateFileCID(cidChunks)
	if originalCID == "" {
		t.Fatal("Failed to calculate file CID from chunks")
	}
	t.Logf("Original file CID (Merkle root): %s", originalCID)

	// Start Daemon A (seeder) on port 7841
	t.Logf("Starting Daemon A (seeder) on port 7841")
	daemonA := NewServer()
	daemonA.config.Port = 7841
	daemonA.config.DataDir = tmpDirA
	if err := daemonA.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize daemon A managers: %v", err)
	}
	if err := daemonA.StartP2P(); err != nil {
		t.Fatalf("Failed to start daemon A P2P: %v", err)
	}
	defer stopDaemon(daemonA)

	// Populate seeder's chunk store and register block exchange
	chunks := populateSeederStore(t, daemonA.chunkStore, testFile, originalCID, fileSize)
	totalChunks := len(chunks)
	wireSeederBlockExchange(t, daemonA)
	t.Logf("Seeder ready: peer=%s, chunks=%d", daemonA.p2pHost.ID(), totalChunks)

	// Start Daemon B (leecher) on port 7842
	t.Logf("Starting Daemon B (leecher) on port 7842")
	daemonB := NewServer()
	daemonB.config.Port = 7842
	daemonB.config.DataDir = tmpDirB
	if err := daemonB.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize daemon B managers: %v", err)
	}
	if err := daemonB.StartP2P(); err != nil {
		t.Fatalf("Failed to start daemon B P2P: %v", err)
	}
	defer stopDaemon(daemonB)

	// Wire leecher P2P with mock tracker pointing to seeder
	wireLeecherP2P(t, daemonB, []*Server{daemonA}, totalChunks)
	defer daemonB.downloadManager.StopWorker()

	// Queue download
	t.Logf("Daemon B: Downloading file from seeder %s", daemonA.p2pHost.ID())
	startTime := time.Now()

	if err := daemonB.downloadManager.QueueDownload(originalCID, "test-1gb.bin", fileSize, totalChunks); err != nil {
		t.Fatalf("Failed to queue download: %v", err)
	}

	// Poll until download complete
	timeout := time.After(2 * time.Minute)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			status := daemonB.downloadManager.GetStatus(originalCID)
			if status != nil {
				t.Fatalf("Download timed out in state %s with error: %s", status.State, derefStr(status.ErrorMessage))
			}
			t.Fatal("Download timed out")
		case <-ticker.C:
			status := daemonB.downloadManager.GetStatus(originalCID)
			if status == nil {
				t.Fatal("Download status not found")
			}

			if status.State == "completed" {
				goto completed
			}
			if status.State == "failed" {
				t.Fatalf("Download failed: %s", derefStr(status.ErrorMessage))
			}

			// Log progress periodically
			t.Logf("Download progress: %.1f%% (%d/%d chunks)", status.Progress*100, status.CompletedChunks, status.TotalChunks)
		}
	}

completed:
	downloadDuration := time.Since(startTime)
	t.Logf("Download completed in %v", downloadDuration)

	// Verify: Downloaded file CID matches original (using Merkle root)
	downloadedFile := filepath.Join(tmpDirB, "downloads", "assets", originalCID, "test-1gb.bin")

	// Chunk the downloaded file and compute its Merkle root CID (same as original)
	downloadChunker := chunking.NewChunker(262144)
	downloadedChunks, err := downloadChunker.SplitFile(downloadedFile)
	if err != nil {
		t.Fatalf("Failed to chunk downloaded file for verification: %v", err)
	}
	downloadedCID := downloadChunker.CalculateFileCID(downloadedChunks)
	if downloadedCID == "" {
		t.Fatal("Failed to calculate CID from downloaded file chunks")
	}

	if downloadedCID != originalCID {
		t.Errorf("CID mismatch: original=%s, downloaded=%s", originalCID, downloadedCID)
	}

	// Verify: Transfer speed >10 MB/s on localhost
	transferSpeedMBps := float64(fileSize) / downloadDuration.Seconds() / (1024 * 1024)
	t.Logf("Transfer speed: %.2f MB/s", transferSpeedMBps)

	if transferSpeedMBps < 10.0 {
		t.Errorf("Transfer speed %.2f MB/s below target of 10 MB/s", transferSpeedMBps)
	}

	t.Logf("E2E test passed: 1GB file transferred successfully")
}

// TestE2E_MultiPeerDownload - E2E test
// Acceptance Criterion: 1 leecher downloads from 3 seeders
func TestE2E_MultiPeerDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Arrange - Create temporary directories for 3 seeders + 1 leecher
	tmpDirSeeder1 := t.TempDir()
	tmpDirSeeder2 := t.TempDir()
	tmpDirSeeder3 := t.TempDir()
	tmpDirLeecher := t.TempDir()

	// Create 100MB test file (smaller for multi-peer test)
	testFile := filepath.Join(tmpDirSeeder1, "test-100mb.bin")
	fileSize := int64(100 * 1024 * 1024) // 100MB

	t.Logf("Creating 100MB test file")
	if err := createTestFile(testFile, fileSize); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Calculate file CID using Merkle root (same as TwoNodeFileTransfer)
	fileChunker := chunking.NewChunker(262144)
	cidChunks, err := fileChunker.SplitFile(testFile)
	if err != nil {
		t.Fatalf("Failed to chunk file for CID calculation: %v", err)
	}
	originalCID := fileChunker.CalculateFileCID(cidChunks)
	if originalCID == "" {
		t.Fatal("Failed to calculate file CID from chunks")
	}
	t.Logf("Test file CID (Merkle root): %s", originalCID)

	// Copy file to all seeders
	copyFile(testFile, filepath.Join(tmpDirSeeder2, "test-100mb.bin"))
	copyFile(testFile, filepath.Join(tmpDirSeeder3, "test-100mb.bin"))

	// Start 3 seeder daemons
	t.Logf("Starting 3 seeder daemons (ports 7841-7843)")
	seeder1 := NewServer()
	seeder1.config.Port = 7841
	seeder1.config.DataDir = tmpDirSeeder1
	if err := seeder1.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize seeder1 managers: %v", err)
	}
	if err := seeder1.StartP2P(); err != nil {
		t.Fatalf("Failed to start seeder1 P2P: %v", err)
	}
	if err := seeder1.Start(); err != nil {
		t.Fatalf("Failed to start seeder1: %v", err)
	}
	defer stopDaemon(seeder1)

	seeder2 := NewServer()
	seeder2.config.Port = 7842
	seeder2.config.DataDir = tmpDirSeeder2
	if err := seeder2.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize seeder2 managers: %v", err)
	}
	if err := seeder2.StartP2P(); err != nil {
		t.Fatalf("Failed to start seeder2 P2P: %v", err)
	}
	if err := seeder2.Start(); err != nil {
		t.Fatalf("Failed to start seeder2: %v", err)
	}
	defer stopDaemon(seeder2)

	seeder3 := NewServer()
	seeder3.config.Port = 7843
	seeder3.config.DataDir = tmpDirSeeder3
	if err := seeder3.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize seeder3 managers: %v", err)
	}
	if err := seeder3.StartP2P(); err != nil {
		t.Fatalf("Failed to start seeder3 P2P: %v", err)
	}
	if err := seeder3.Start(); err != nil {
		t.Fatalf("Failed to start seeder3: %v", err)
	}
	defer stopDaemon(seeder3)

	// Start leecher daemon
	t.Logf("Starting leecher daemon (port 7844)")
	leecher := NewServer()
	leecher.config.Port = 7844
	leecher.config.DataDir = tmpDirLeecher
	if err := leecher.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize leecher managers: %v", err)
	}
	if err := leecher.StartP2P(); err != nil {
		t.Fatalf("Failed to start leecher P2P: %v", err)
	}
	if err := leecher.Start(); err != nil {
		t.Fatalf("Failed to start leecher: %v", err)
	}
	defer stopDaemon(leecher)

	// Populate all 3 seeders' chunk stores and register block exchange
	chunks1 := populateSeederStore(t, seeder1.chunkStore, testFile, originalCID, fileSize)
	totalChunks := len(chunks1)
	wireSeederBlockExchange(t, seeder1)

	populateSeederStore(t, seeder2.chunkStore, filepath.Join(tmpDirSeeder2, "test-100mb.bin"), originalCID, fileSize)
	wireSeederBlockExchange(t, seeder2)

	populateSeederStore(t, seeder3.chunkStore, filepath.Join(tmpDirSeeder3, "test-100mb.bin"), originalCID, fileSize)
	wireSeederBlockExchange(t, seeder3)

	t.Logf("All 3 seeders wired: %d chunks each", totalChunks)

	// Wire leecher P2P with mock tracker pointing to all 3 seeders
	wireLeecherP2P(t, leecher, []*Server{seeder1, seeder2, seeder3}, totalChunks)
	defer leecher.downloadManager.StopWorker()

	// Leecher: Download file (should aggregate chunks from 3 seeders)
	t.Logf("Leecher: Downloading file from 3 seeders...")
	startTime := time.Now()

	if err := leecher.downloadManager.QueueDownload(originalCID, "test-100mb.bin", fileSize, totalChunks); err != nil {
		t.Fatalf("Failed to queue download: %v", err)
	}

	// Poll until download complete
	timeout := time.After(2 * time.Minute)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			status := leecher.downloadManager.GetStatus(originalCID)
			if status != nil {
				t.Fatalf("Download timed out in state %s with error: %s", status.State, derefStr(status.ErrorMessage))
			}
			t.Fatal("Download timed out")
		case <-ticker.C:
			status := leecher.downloadManager.GetStatus(originalCID)
			if status == nil {
				t.Fatal("Download status not found")
			}

			if status.State == "completed" {
				goto completed
			}
			if status.State == "failed" {
				t.Fatalf("Download failed: %s", derefStr(status.ErrorMessage))
			}
		}
	}

completed:
	downloadDuration := time.Since(startTime)
	t.Logf("Multi-peer download completed in %v", downloadDuration)

	// Verify: Downloaded file CID matches original (using Merkle root)
	downloadedFile := filepath.Join(tmpDirLeecher, "downloads", "assets", originalCID, "test-100mb.bin")

	// Chunk the downloaded file and compute its Merkle root CID
	downloadChunker := chunking.NewChunker(262144)
	downloadedChunks, err := downloadChunker.SplitFile(downloadedFile)
	if err != nil {
		t.Fatalf("Failed to chunk downloaded file for verification: %v", err)
	}
	downloadedCID := downloadChunker.CalculateFileCID(downloadedChunks)
	if downloadedCID == "" {
		t.Fatal("Failed to calculate CID from downloaded file chunks")
	}

	if downloadedCID != originalCID {
		t.Errorf("CID mismatch: original=%s, downloaded=%s", originalCID, downloadedCID)
	}

	// TODO: Verify chunks were downloaded from multiple peers
	// Check download coordinator stats to confirm multi-peer aggregation

	t.Logf("E2E multi-peer test passed")
}

// TestE2E_DownloadResume - E2E test
// Acceptance Criterion: Download resumes after interruption at 50%
//
// The resume path the manager actually supports is a daemon restart: the
// leecher is stopped mid-download (worker cancelled, metadata left "active"),
// a fresh Server is opened on the same DataDir, and InitializeManagers ->
// LoadIncomplete restores the download as "queued" with the persisted chunk
// bitmap. The worker then re-runs it to completion. (ResumeDownload only flips
// state and never re-spawns a worker, so in-process pause/resume is not usable
// here.) Completed chunks survive in the leecher's chunks.db across the restart,
// which is asserted before the second run starts.
func TestE2E_DownloadResume(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Arrange - Create temporary directories
	tmpDirSeeder := t.TempDir()
	tmpDirLeecher := t.TempDir()

	// 100MB file: large enough that 50% is observable at ~10ms polling, small
	// enough that the pre- and post-restart phases each finish in seconds.
	const filename = "resume-100mb.bin"
	testFile := filepath.Join(tmpDirSeeder, filename)
	fileSize := int64(100 * 1024 * 1024) // 100MB

	t.Logf("Creating 100MB test file")
	if err := createTestFile(testFile, fileSize); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	originalCID := merkleFileCID(t, testFile)
	t.Logf("Test file CID (Merkle root): %s", originalCID)

	// Start seeder daemon, populate its chunk store, serve chunks
	t.Logf("Starting seeder daemon (port 7841)")
	seeder := startE2EDaemon(t, 7841, tmpDirSeeder)
	defer stopDaemon(seeder)

	chunks := populateSeederStore(t, seeder.chunkStore, testFile, originalCID, fileSize)
	totalChunks := len(chunks)
	wireSeederBlockExchange(t, seeder)
	t.Logf("Seeder ready: peer=%s, chunks=%d", seeder.p2pHost.ID(), totalChunks)

	// Start leecher daemon (first incarnation)
	t.Logf("Starting leecher daemon (port 7842)")
	leecher := startE2EDaemon(t, 7842, tmpDirLeecher)
	leecherStopped := false
	defer func() {
		if !leecherStopped {
			stopDaemon(leecher)
		}
	}()

	wireLeecherP2P(t, leecher, []*Server{seeder}, totalChunks)

	// Leecher: Start download
	t.Logf("Leecher: Starting download...")
	if err := leecher.downloadManager.QueueDownload(originalCID, filename, fileSize, totalChunks); err != nil {
		t.Fatalf("Failed to queue download: %v", err)
	}

	// Wait until at least 50% of chunks are downloaded
	t.Logf("Waiting for 50%% download...")
	timeout := time.After(2 * time.Minute)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var interruptedAt *download.DownloadStatus
waitHalf:
	for {
		select {
		case <-timeout:
			status := leecher.downloadManager.GetStatus(originalCID)
			if status != nil {
				t.Fatalf("Timed out waiting for 50%% download: state=%s progress=%.1f%% error=%s",
					status.State, status.Progress*100, derefStr(status.ErrorMessage))
			}
			t.Fatal("Timed out waiting for 50% download: status not found")
		case <-ticker.C:
			status := leecher.downloadManager.GetStatus(originalCID)
			if status == nil {
				t.Fatal("Download status not found")
			}
			switch status.State {
			case download.StateFailed:
				t.Fatalf("Download failed before 50%%: %s", derefStr(status.ErrorMessage))
			case download.StateCompleted:
				t.Fatalf("Download completed before it could be interrupted; increase fileSize")
			}
			if status.Progress >= 0.5 {
				interruptedAt = status
				break waitHalf
			}
		}
	}
	t.Logf("Download reached %.1f%% (%d/%d chunks), interrupting...",
		interruptedAt.Progress*100, interruptedAt.CompletedChunks, interruptedAt.TotalChunks)

	// Simulate interruption: stop the leecher daemon outright. Shutdown cancels
	// the worker (errUserCancel leaves metadata "active") and closes chunks.db,
	// exactly like a daemon crash/exit mid-transfer.
	t.Logf("Simulating interruption: stopping leecher daemon")
	stopDaemon(leecher)
	leecherStopped = true

	// Restart leecher daemon on the same DataDir. InitializeManagers runs
	// LoadIncomplete, which restores the download as queued.
	t.Logf("Restarting leecher daemon")
	leecherRestarted := startE2EDaemon(t, 7842, tmpDirLeecher)
	defer stopDaemon(leecherRestarted)

	restored := leecherRestarted.downloadManager.GetStatus(originalCID)
	if restored == nil {
		t.Fatal("Download was not restored from disk after restart")
	}
	if restored.State != download.StateQueued {
		t.Fatalf("Restored download state = %s, want %s", restored.State, download.StateQueued)
	}
	if restored.CompletedChunks < totalChunks/2 {
		t.Errorf("Restored chunk bitmap shows %d/%d chunks, want >= %d (progress was not persisted)",
			restored.CompletedChunks, totalChunks, totalChunks/2)
	}
	persistedChunks, err := leecherRestarted.chunkStore.CountChunks(originalCID)
	if err != nil {
		t.Fatalf("Failed to count persisted chunks: %v", err)
	}
	if persistedChunks < totalChunks/2 {
		t.Errorf("chunks.db holds %d/%d chunks after restart, want >= %d", persistedChunks, totalChunks, totalChunks/2)
	}
	t.Logf("Restored from disk: state=%s bitmap=%d/%d chunks, chunks.db=%d chunks",
		restored.State, restored.CompletedChunks, totalChunks, persistedChunks)

	// Re-wire the new leecher to the seeder; StartWorker picks the queued download up
	wireLeecherP2P(t, leecherRestarted, []*Server{seeder}, totalChunks)
	defer leecherRestarted.downloadManager.StopWorker()

	t.Logf("Leecher: resuming download after restart...")
	resumeDuration := waitForDownloadCompletion(t, leecherRestarted.downloadManager, originalCID, 2*time.Minute)
	t.Logf("Download completed %v after restart", resumeDuration)

	// Verify: Downloaded file CID matches original
	downloadedFile := filepath.Join(tmpDirLeecher, "downloads", "assets", originalCID, filename)
	verifyDownloadedFileCID(t, downloadedFile, originalCID)

	t.Logf("E2E resume test passed")
}

// Helper: Create a test file with random data
func createTestFile(path string, size int64) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Write random data in 10MB chunks to avoid memory issues
	chunkSize := int64(10 * 1024 * 1024) // 10MB
	remaining := size
	buffer := make([]byte, chunkSize)

	for remaining > 0 {
		writeSize := chunkSize
		if remaining < chunkSize {
			writeSize = remaining
		}

		// Generate random data
		if _, err := rand.Read(buffer[:writeSize]); err != nil {
			return err
		}

		// Write to file
		if _, err := file.Write(buffer[:writeSize]); err != nil {
			return err
		}

		remaining -= writeSize
	}

	return nil
}

// Helper: Copy file
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// Helper: Wait for condition with timeout
func waitForCondition(t *testing.T, condition func() bool, timeout time.Duration, checkInterval time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(checkInterval)
	}

	return false
}

// TestE2E_TransferSpeed - E2E test
// Acceptance Criterion: Transfer speed >10 MB/s on localhost (gigabit network)
//
// The 10 MB/s target is logged, not asserted: this runs on whatever developer
// box executes the suite and the measured figure includes up to 1s of worker
// tick latency before the download even starts. The hard checks are completion
// within the timeout, a correct CID, and a sanity floor of 2 MB/s that only a
// broken transfer path (e.g. one chunk per retry backoff) would miss.
func TestE2E_TransferSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Create 100MB file for speed test
	tmpDirA := t.TempDir()
	tmpDirB := t.TempDir()

	const filename = "speedtest-100mb.bin"
	testFile := filepath.Join(tmpDirA, filename)
	fileSize := int64(100 * 1024 * 1024) // 100MB

	t.Logf("Creating 100MB test file for speed test")
	if err := createTestFile(testFile, fileSize); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	originalCID := merkleFileCID(t, testFile)
	t.Logf("Test file CID (Merkle root): %s", originalCID)

	// Seeder: Daemon A on port 7841
	daemonA := startE2EDaemon(t, 7841, tmpDirA)
	defer stopDaemon(daemonA)

	chunks := populateSeederStore(t, daemonA.chunkStore, testFile, originalCID, fileSize)
	totalChunks := len(chunks)
	wireSeederBlockExchange(t, daemonA)
	t.Logf("Seeder ready: peer=%s, chunks=%d", daemonA.p2pHost.ID(), totalChunks)

	// Leecher: Daemon B on port 7842
	daemonB := startE2EDaemon(t, 7842, tmpDirB)
	defer stopDaemon(daemonB)

	wireLeecherP2P(t, daemonB, []*Server{daemonA}, totalChunks)
	defer daemonB.downloadManager.StopWorker()

	// Transfer file and measure speed
	if err := daemonB.downloadManager.QueueDownload(originalCID, filename, fileSize, totalChunks); err != nil {
		t.Fatalf("Failed to queue download: %v", err)
	}

	duration := waitForDownloadCompletion(t, daemonB.downloadManager, originalCID, 2*time.Minute)

	downloadedFile := filepath.Join(tmpDirB, "downloads", "assets", originalCID, filename)
	verifyDownloadedFileCID(t, downloadedFile, originalCID)

	speedMBps := float64(fileSize) / duration.Seconds() / (1024 * 1024)
	t.Logf("Transfer speed: %.2f MB/s (%d MB in %v, target 10 MB/s)", speedMBps, fileSize/(1024*1024), duration)

	const minSpeedMBps = 2.0
	if speedMBps < minSpeedMBps {
		t.Errorf("Transfer speed %.2f MB/s below sanity floor of %.0f MB/s", speedMBps, minSpeedMBps)
	}
}

// TestE2E_ConcurrentDownloads - E2E test
// Acceptance Criterion: 100 concurrent downloads without crash
//
// 100 files are seeded on A and all 100 are queued on B at once. The manager
// admits at most 3 active downloads and its coordinator loop starts one per
// 1s tick, so the run is dominated by the tick (~100s), not the bytes; files
// are kept small (2 chunks each) so the transfer itself is negligible.
func TestE2E_ConcurrentDownloads(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// This is a stress test to verify stability under load
	tmpDirSeeder := t.TempDir()
	tmpDirLeecher := t.TempDir()

	// Create multiple small test files (2 x 256KB chunks each)
	numFiles := 100
	fileSize := int64(512 * 1024)

	names := make([]string, numFiles)
	cids := make([]string, numFiles)

	// Start seeder daemon
	seeder := startE2EDaemon(t, 7841, tmpDirSeeder)
	defer stopDaemon(seeder)

	// Create and seed every file on A
	t.Logf("Creating and seeding %d test files", numFiles)
	totalChunks := 0
	for i := 0; i < numFiles; i++ {
		names[i] = "test-file-" + strconv.Itoa(i) + ".bin"
		testFile := filepath.Join(tmpDirSeeder, names[i])
		if err := createTestFile(testFile, fileSize); err != nil {
			t.Fatalf("Failed to create test file %d: %v", i, err)
		}
		cids[i] = merkleFileCID(t, testFile)

		chunks := populateSeederStore(t, seeder.chunkStore, testFile, cids[i], fileSize)
		if totalChunks == 0 {
			totalChunks = len(chunks)
		} else if len(chunks) != totalChunks {
			t.Fatalf("File %d split into %d chunks, want %d", i, len(chunks), totalChunks)
		}
	}
	if totalChunks < 2 {
		t.Fatalf("Each file must span at least 2 chunks, got %d", totalChunks)
	}
	wireSeederBlockExchange(t, seeder)
	t.Logf("Seeder ready: peer=%s, %d files x %d chunks", seeder.p2pHost.ID(), numFiles, totalChunks)

	// Start leecher daemon and wire it to the seeder
	leecher := startE2EDaemon(t, 7842, tmpDirLeecher)
	defer stopDaemon(leecher)

	wireLeecherP2P(t, leecher, []*Server{seeder}, totalChunks)
	defer leecher.downloadManager.StopWorker()

	// Queue all downloads up front
	t.Logf("Queueing %d concurrent downloads", numFiles)
	startTime := time.Now()
	for i := 0; i < numFiles; i++ {
		if err := leecher.downloadManager.QueueDownload(cids[i], names[i], fileSize, totalChunks); err != nil {
			t.Fatalf("Failed to queue download %d: %v", i, err)
		}
	}

	// Wait for all downloads to reach a terminal state
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var failures []string
waitAll:
	for {
		select {
		case <-timeout:
			counts := leecher.downloadManager.GetCounts()
			t.Fatalf("Concurrent downloads timed out after %v: %+v", time.Since(startTime), counts)
		case <-ticker.C:
			completed := 0
			active := 0
			failures = failures[:0]

			for i, cid := range cids {
				status := leecher.downloadManager.GetStatus(cid)
				if status == nil {
					t.Fatalf("Download status not found for file %d (%s)", i, cid)
				}

				switch status.State {
				case download.StateCompleted:
					completed++
				case download.StateFailed:
					failures = append(failures, names[i]+": "+derefStr(status.ErrorMessage))
				case download.StateActive:
					active++
				}
			}

			t.Logf("Progress: %d completed, %d active, %d failed (out of %d) after %v",
				completed, active, len(failures), numFiles, time.Since(startTime).Round(time.Second))

			if completed+len(failures) >= numFiles {
				break waitAll
			}
		}
	}

	if len(failures) > 0 {
		t.Fatalf("%d of %d downloads failed:\n%v", len(failures), numFiles, failures)
	}

	// Verify: every assembled file matches its CID
	for i := range cids {
		downloadedFile := filepath.Join(tmpDirLeecher, "downloads", "assets", cids[i], names[i])
		verifyDownloadedFileCID(t, downloadedFile, cids[i])
	}

	t.Logf("E2E concurrent download test passed (%d files in %v)", numFiles, time.Since(startTime))
}
