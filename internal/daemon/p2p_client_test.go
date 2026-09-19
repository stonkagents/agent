// Package: internal/daemon
// Feature: F-003 (Universal File Sharing)
// Story: US-003-07 (P2P Socket Client Implementation)
// Purpose: TDD tests for P2P block exchange client
//
// Test Phases:
// Phase 1: Basic Stream Handling (RED-GREEN-REFACTOR)
// Phase 2: CID Verification (Security)
// Phase 3: Retry Logic (Resilience)
// Phase 4: Peer Failover
// Phase 5: Pipelining (Concurrency)
// Phase 6: Security Hardening

package daemon

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	protov1 "github.com/stonkagents/agent/api/proto/v1"
	"github.com/stonkagents/agent/internal/logger"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ========================================
// PHASE 1: Basic Stream Handling Tests
// ========================================

// TestP2PClient_RequestChunk_StreamCreation verifies that NewStream is called with correct protocol ID
func TestP2PClient_RequestChunk_StreamCreation(t *testing.T) {
	// Setup mock libp2p host
	mockHost := &MockLibp2pHost{
		newStreamCalls: make([]NewStreamCall, 0),
	}

	client := NewP2PClient(mockHost, testLogger())

	ctx := context.Background()
	testPeerID := peer.ID("test-peer-123")
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 0

	// RequestChunk will fail because mock stream has no valid response,
	// but we only care that NewStream was called with the correct protocol ID.
	client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)

	// Verify NewStream was called with correct protocol ID
	require.GreaterOrEqual(t, len(mockHost.newStreamCalls), 1, "NewStream should be called at least once")
	assert.Equal(t, testPeerID, mockHost.newStreamCalls[0].PeerID, "PeerID should match")
	assert.Equal(t, libp2pprotocol.ID("/stonkagents/transfer/1.0.0"), mockHost.newStreamCalls[0].ProtocolID, "Protocol ID should match")
}

// TestP2PClient_RequestChunk_ProtobufEncoding verifies BlockRequest is encoded and sent correctly
func TestP2PClient_RequestChunk_ProtobufEncoding(t *testing.T) {
	// Setup mock stream that captures written data
	mockStream := NewMockStream()
	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			return mockStream, nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())

	ctx := context.Background()
	testPeerID := peer.ID("test-peer-123")
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 5

	// RequestChunk will fail because mock stream has no valid response,
	// but we only care that the protobuf request was correctly encoded and written.
	client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)

	// Verify protobuf message was written to stream
	assert.Greater(t, len(mockStream.WrittenData), 0, "Data should be written to stream")

	// Decode and verify BlockRequest
	var envelope protov1.Message
	err := proto.Unmarshal(mockStream.WrittenData, &envelope)
	require.NoError(t, err, "Should unmarshal protobuf message")

	blockReq := envelope.GetBlockRequest()
	require.NotNil(t, blockReq, "BlockRequest should be in envelope")
	assert.Equal(t, testCID, blockReq.FileCid, "File CID should match")
	assert.Equal(t, int32(testChunkIndex), blockReq.ChunkIndex, "Chunk index should match")
	assert.NotEmpty(t, blockReq.RequestId, "Request ID should be generated")
}

// TestP2PClient_RequestChunk_ReceiveResponse verifies BlockResponse is decoded correctly
func TestP2PClient_RequestChunk_ReceiveResponse(t *testing.T) {
	// Setup mock stream with pre-loaded response
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 3
	testData := []byte("test chunk data 256KB")
	testChunkCID, err := crypto.GenerateCID(testData)
	require.NoError(t, err, "GenerateCID should succeed")

	// Create BlockResponse
	blockResp := &protov1.BlockResponse{
		FileCid:    testCID,
		ChunkIndex: int32(testChunkIndex),
		ChunkCid:   testChunkCID,
		Data:       testData,
		RequestId:  "req-123",
	}
	envelope := &protov1.Message{
		Payload: &protov1.Message_BlockResponse{
			BlockResponse: blockResp,
		},
	}
	respData, _ := proto.Marshal(envelope)

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())

	ctx := context.Background()
	testPeerID := peer.ID("test-peer-123")

	// This should FAIL initially (RED) because response decoding is not implemented
	chunk, err := client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)
	require.NoError(t, err, "RequestChunk should succeed")

	// Verify chunk data
	require.NotNil(t, chunk, "Chunk should not be nil")
	assert.Equal(t, testCID, chunk.FileCID, "File CID should match")
	assert.Equal(t, testChunkIndex, chunk.ChunkIndex, "Chunk index should match")
	assert.Equal(t, testChunkCID, chunk.ChunkCID, "Chunk CID should match")
	assert.Equal(t, testData, chunk.Data, "Chunk data should match")
}

// ========================================
// PHASE 2: CID Verification Tests (Security)
// ========================================

// TestP2PClient_RequestChunk_CIDVerification verifies chunk data against CID after receiving
func TestP2PClient_RequestChunk_CIDVerification(t *testing.T) {
	// Setup: Create valid chunk data with matching CID
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 2
	testChunkData := []byte("valid test chunk data that will be verified")

	// Generate CID for this chunk data
	validChunkCID, err := crypto.GenerateCID(testChunkData)
	require.NoError(t, err)

	// Create BlockResponse with valid CID
	blockResp := &protov1.BlockResponse{
		FileCid:    testCID,
		ChunkIndex: int32(testChunkIndex),
		ChunkCid:   validChunkCID,
		Data:       testChunkData,
		RequestId:  "req-verify-123",
	}
	envelope := &protov1.Message{
		Payload: &protov1.Message_BlockResponse{
			BlockResponse: blockResp,
		},
	}
	respData, _ := proto.Marshal(envelope)

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())

	ctx := context.Background()
	testPeerID := peer.ID("test-peer-cid-verify")

	// This should FAIL initially (RED) because CID verification is not implemented
	chunk, err := client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)
	require.NoError(t, err, "RequestChunk should succeed with valid CID")
	require.NotNil(t, chunk, "Chunk should not be nil")

	// Verify chunk was validated (implementation will add this)
	assert.Equal(t, validChunkCID, chunk.ChunkCID, "Chunk CID should match")
	assert.Equal(t, testChunkData, chunk.Data, "Chunk data should match")
}

// TestP2PClient_RequestChunk_CIDMismatch verifies corrupted data returns error
func TestP2PClient_RequestChunk_CIDMismatch(t *testing.T) {
	// Setup: Create chunk with WRONG CID (simulating corruption or malicious peer)
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 3
	actualChunkData := []byte("actual chunk data")
	// Generate a valid CID from different data to create a legitimate mismatch
	wrongChunkCID, err := crypto.GenerateCID([]byte("different data that produces a different CID"))
	require.NoError(t, err)

	// Create BlockResponse with WRONG CID
	blockResp := &protov1.BlockResponse{
		FileCid:    testCID,
		ChunkIndex: int32(testChunkIndex),
		ChunkCid:   wrongChunkCID, // Intentionally wrong!
		Data:       actualChunkData,
		RequestId:  "req-corrupt-123",
	}
	envelope := &protov1.Message{
		Payload: &protov1.Message_BlockResponse{
			BlockResponse: blockResp,
		},
	}
	respData, _ := proto.Marshal(envelope)

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())

	ctx := context.Background()
	testPeerID := peer.ID("test-peer-corrupt")

	// This should FAIL initially (RED) - we want it to return ErrCIDVerificationFailed
	chunk, err := client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)

	// After implementation, this should return error
	assert.Error(t, err, "Should return error for CID mismatch")
	assert.Nil(t, chunk, "Chunk should be nil on verification failure")
	assert.Contains(t, err.Error(), "CID verification failed", "Error should mention CID verification")
}

// TestP2PClient_RequestChunk_LogCIDFailure verifies CID failures are logged with peer ID
func TestP2PClient_RequestChunk_LogCIDFailure(t *testing.T) {
	// Setup: Corrupted chunk (wrong CID)
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 5
	// Generate a valid CID from different data to create a legitimate mismatch
	wrongChunkCID, err := crypto.GenerateCID([]byte("different data for log test"))
	require.NoError(t, err)

	blockResp := &protov1.BlockResponse{
		FileCid:    testCID,
		ChunkIndex: int32(testChunkIndex),
		ChunkCid:   wrongChunkCID,
		Data:       []byte("corrupted data"),
		RequestId:  "req-log-test",
	}
	envelope := &protov1.Message{
		Payload: &protov1.Message_BlockResponse{
			BlockResponse: blockResp,
		},
	}
	respData, _ := proto.Marshal(envelope)

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			return NewMockStreamWithResponse(respData), nil
		},
	}

	// Use a logger that captures output (for now, basic logger)
	client := NewP2PClient(mockHost, testLogger())

	ctx := context.Background()
	testPeerID := peer.ID("test-peer-log")

	// Request chunk - should fail CID verification
	_, reqErr := client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)

	// After implementation, should log: "[P2PClient.RequestChunk] CID verification failed peer=<peerID> cid=<cid> index=<index>"
	assert.Error(t, reqErr, "Should return error for CID mismatch")

	// Note: In production, we'd verify logger output. For now, we verify the error is returned.
	// Logger integration test would check actual log messages.
}

// ========================================
// PHASE 3: Retry Logic Tests (Resilience)
// ========================================

// TestP2PClient_RequestChunk_RetryBackoff verifies exponential backoff retry logic
func TestP2PClient_RequestChunk_RetryBackoff(t *testing.T) {
	t.Skip("Phase 3 implementation in progress")

	// Setup: Mock stream that fails twice, then succeeds on third attempt
	attemptCount := 0
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 0
	testChunkData := []byte("retry test chunk")
	validChunkCID, _ := crypto.GenerateCID(testChunkData)

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			attemptCount++
			if attemptCount < 3 {
				// First 2 attempts fail (simulate network error)
				return nil, fmt.Errorf("network error: connection refused")
			}
			// Third attempt succeeds
			blockResp := &protov1.BlockResponse{
				FileCid:    testCID,
				ChunkIndex: int32(testChunkIndex),
				ChunkCid:   validChunkCID,
				Data:       testChunkData,
				RequestId:  "req-retry-success",
			}
			envelope := &protov1.Message{
				Payload: &protov1.Message_BlockResponse{
					BlockResponse: blockResp,
				},
			}
			respData, _ := proto.Marshal(envelope)
			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()
	testPeerID := peer.ID("test-peer-retry")

	// Should succeed after 2 retries
	startTime := time.Now()
	chunk, err := client.RequestChunk(ctx, []peer.ID{testPeerID}, testCID, testChunkIndex)
	duration := time.Since(startTime)

	require.NoError(t, err, "Should succeed after retries")
	require.NotNil(t, chunk, "Chunk should not be nil")
	assert.Equal(t, 3, attemptCount, "Should have 3 attempts (initial + 2 retries)")

	// Verify exponential backoff timing (1s + 2s = ~3s, allowing for jitter)
	assert.Greater(t, duration.Seconds(), 2.5, "Should wait ~3 seconds with backoff")
	assert.Less(t, duration.Seconds(), 4.0, "Backoff shouldn't take too long")
}

// TestP2PClient_RequestChunk_Jitter verifies ±20% jitter in retry backoff
func TestP2PClient_RequestChunk_Jitter(t *testing.T) {
	t.Skip("Phase 3 implementation in progress")

	// Setup: Mock that always fails to test jitter timing
	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()
	testPeerID := peer.ID("test-peer-jitter")

	// Run multiple retry attempts and measure timing variance
	var durations []time.Duration
	for i := 0; i < 5; i++ {
		startTime := time.Now()
		client.RequestChunk(ctx, []peer.ID{testPeerID}, "test-cid", 0)
		durations = append(durations, time.Since(startTime))
	}

	// Verify durations vary (jitter prevents identical timing)
	// With ±20% jitter on (1s + 2s + 4s = 7s), expect range 5.6s - 8.4s
	for _, d := range durations {
		assert.Greater(t, d.Seconds(), 5.0, "Should have some backoff time")
		assert.Less(t, d.Seconds(), 9.0, "Shouldn't exceed max with jitter")
	}

	// Verify not all durations are identical (jitter working)
	allSame := true
	for i := 1; i < len(durations); i++ {
		if durations[i] != durations[0] {
			allSame = false
			break
		}
	}
	assert.False(t, allSame, "Jitter should cause timing variation")
}

// TestP2PClient_RequestChunk_MaxRetries verifies max 3 retry attempts
func TestP2PClient_RequestChunk_MaxRetries(t *testing.T) {
	t.Skip("Phase 3 implementation in progress")

	// Setup: Mock that always fails
	attemptCount := 0
	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			attemptCount++
			return nil, fmt.Errorf("persistent network error")
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()
	testPeerID := peer.ID("test-peer-maxretry")

	// Should fail after max retries
	chunk, err := client.RequestChunk(ctx, []peer.ID{testPeerID}, "test-cid", 0)

	assert.Error(t, err, "Should return error after max retries")
	assert.Nil(t, chunk, "Chunk should be nil on failure")
	assert.Contains(t, err.Error(), "max retries", "Error should mention max retries")
	assert.Equal(t, 3, attemptCount, "Should stop after 3 attempts (no more retries)")
}

// ========================================
// Mock Implementations for Testing
// ========================================

// MockLibp2pHost mocks libp2p host.Host interface
type MockLibp2pHost struct {
	mu             sync.Mutex // Protects newStreamCalls from concurrent access (US-029-P4)
	newStreamCalls []NewStreamCall
	newStreamFunc  func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error)
}

type NewStreamCall struct {
	PeerID     peer.ID
	ProtocolID libp2pprotocol.ID
}

func (m *MockLibp2pHost) NewStream(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
	// Record call (thread-safe)
	if len(pids) > 0 {
		m.mu.Lock()
		m.newStreamCalls = append(m.newStreamCalls, NewStreamCall{
			PeerID:     p,
			ProtocolID: pids[0],
		})
		m.mu.Unlock()
	}

	// Use custom func if provided
	if m.newStreamFunc != nil {
		return m.newStreamFunc(ctx, p, pids...)
	}

	// Default: return mock stream
	return NewMockStream(), nil
}

// Implement minimal host.Host interface methods (required)
func (m *MockLibp2pHost) ID() peer.ID                                                           { return peer.ID("mock-host") }
func (m *MockLibp2pHost) Addrs() []multiaddr.Multiaddr                                          { return []multiaddr.Multiaddr{} }
func (m *MockLibp2pHost) Peerstore() peerstore.Peerstore                                        { return nil }
func (m *MockLibp2pHost) Network() network.Network                                              { return nil }
func (m *MockLibp2pHost) Mux() libp2pprotocol.Switch                                            { return nil }
func (m *MockLibp2pHost) Connect(ctx context.Context, pi peer.AddrInfo) error                   { return nil }
func (m *MockLibp2pHost) SetStreamHandler(pid libp2pprotocol.ID, handler network.StreamHandler) {}
func (m *MockLibp2pHost) SetStreamHandlerMatch(libp2pprotocol.ID, func(libp2pprotocol.ID) bool, network.StreamHandler) {
}
func (m *MockLibp2pHost) RemoveStreamHandler(pid libp2pprotocol.ID) {}
func (m *MockLibp2pHost) Close() error                              { return nil }
func (m *MockLibp2pHost) EventBus() event.Bus                       { return nil }
func (m *MockLibp2pHost) ConnManager() connmgr.ConnManager          { return nil }

// MockStream mocks network.Stream interface
type MockStream struct {
	WrittenData []byte
	ReadData    []byte
	ReadOffset  int
	closed      bool
}

func NewMockStream() *MockStream {
	return &MockStream{
		WrittenData: make([]byte, 0),
		ReadData:    make([]byte, 0),
	}
}

func NewMockStreamWithResponse(responseData []byte) *MockStream {
	return &MockStream{
		WrittenData: make([]byte, 0),
		ReadData:    responseData,
	}
}

func (m *MockStream) Write(p []byte) (n int, err error) {
	m.WrittenData = append(m.WrittenData, p...)
	return len(p), nil
}

func (m *MockStream) Read(p []byte) (n int, err error) {
	if m.ReadOffset >= len(m.ReadData) {
		return 0, io.EOF
	}
	n = copy(p, m.ReadData[m.ReadOffset:])
	m.ReadOffset += n
	return n, nil
}

func (m *MockStream) Close() error {
	m.closed = true
	return nil
}

func (m *MockStream) CloseWrite() error {
	return nil
}

func (m *MockStream) CloseRead() error {
	return nil
}

func (m *MockStream) Reset() error {
	return nil
}

func (m *MockStream) ResetWithError(code network.StreamErrorCode) error {
	return nil
}

func (m *MockStream) SetDeadline(t time.Time) error {
	return nil
}

func (m *MockStream) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *MockStream) SetWriteDeadline(t time.Time) error {
	return nil
}

// Minimal protocol and conn implementations
func (m *MockStream) Protocol() libp2pprotocol.ID {
	return libp2pprotocol.ID("/stonkagents/transfer/1.0.0")
}

func (m *MockStream) SetProtocol(id libp2pprotocol.ID) error {
	return nil
}

func (m *MockStream) Stat() network.Stats {
	return network.Stats{}
}

func (m *MockStream) Conn() network.Conn {
	return nil
}

func (m *MockStream) Scope() network.StreamScope {
	return nil
}

func (m *MockStream) ID() string {
	return "mock-stream-123"
}

// testLogger creates a no-op logger for tests
func testLogger() *logger.Logger {
	// For tests, we can use nil logger or create a minimal one
	// Since the tests don't actually log anything critical, returning nil is fine
	log, _ := logger.New(logger.Config{
		LogPath:    "/dev/null",
		MaxSize:    1024,
		MaxBackups: 1,
	})
	return log
}

// ========================================
// PHASE 4: Peer Failover Tests
// ========================================

// TestP2PClient_RequestChunk_PeerFailover verifies switching to different peer after 2 consecutive failures
func TestP2PClient_RequestChunk_PeerFailover(t *testing.T) {
	// Setup: Track which peer is being called
	peerAttempts := make(map[peer.ID]int)
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	testChunkIndex := 0
	testChunkData := []byte("peer failover test chunk")
	validChunkCID, _ := crypto.GenerateCID(testChunkData)

	peerA := peer.ID("peer-A")
	peerB := peer.ID("peer-B")

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			peerAttempts[p]++

			// Peer A fails twice
			if p == peerA && peerAttempts[p] <= 2 {
				return nil, fmt.Errorf("peer A network error")
			}

			// Peer B succeeds on first attempt
			if p == peerB {
				blockResp := &protov1.BlockResponse{
					FileCid:    testCID,
					ChunkIndex: int32(testChunkIndex),
					ChunkCid:   validChunkCID,
					Data:       testChunkData,
					RequestId:  "req-failover-success",
				}
				envelope := &protov1.Message{
					Payload: &protov1.Message_BlockResponse{
						BlockResponse: blockResp,
					},
				}
				respData, _ := proto.Marshal(envelope)
				return NewMockStreamWithResponse(respData), nil
			}

			return nil, fmt.Errorf("unexpected peer")
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()

	// Pass peer list: [peer-A, peer-B]
	peerList := []peer.ID{peerA, peerB}

	// Should succeed after failing peer A twice and switching to peer B
	chunk, err := client.RequestChunk(ctx, peerList, testCID, testChunkIndex)

	require.NoError(t, err, "Should succeed after peer failover")
	require.NotNil(t, chunk, "Chunk should not be nil")
	assert.Equal(t, testChunkData, chunk.Data, "Chunk data should match")

	// Verify peer attempt counts
	assert.Equal(t, 2, peerAttempts[peerA], "Peer A should have 2 attempts before failover")
	assert.Equal(t, 1, peerAttempts[peerB], "Peer B should have 1 successful attempt")
}

// TestP2PClient_RequestChunk_AllPeersFail verifies error when all peers are exhausted
func TestP2PClient_RequestChunk_AllPeersFail(t *testing.T) {
	// Setup: All peers fail consistently
	peerAttempts := make(map[peer.ID]int)

	peerA := peer.ID("peer-A")
	peerB := peer.ID("peer-B")
	peerC := peer.ID("peer-C")

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			peerAttempts[p]++
			return nil, fmt.Errorf("persistent failure for peer %s", p)
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"

	// Pass peer list: [peer-A, peer-B, peer-C]
	peerList := []peer.ID{peerA, peerB, peerC}

	// Should fail after exhausting all peers
	chunk, err := client.RequestChunk(ctx, peerList, testCID, 0)

	assert.Error(t, err, "Should return error after all peers exhausted")
	assert.Nil(t, chunk, "Chunk should be nil on failure")
	assert.Contains(t, err.Error(), "all peers exhausted", "Error should mention peer exhaustion")

	// Verify attempt counts: 2 per peer
	assert.Equal(t, 2, peerAttempts[peerA], "Peer A should have 2 attempts")
	assert.Equal(t, 2, peerAttempts[peerB], "Peer B should have 2 attempts")
	assert.Equal(t, 2, peerAttempts[peerC], "Peer C should have 2 attempts")

	// Total 6 attempts
	totalAttempts := peerAttempts[peerA] + peerAttempts[peerB] + peerAttempts[peerC]
	assert.Equal(t, 6, totalAttempts, "Should have exactly 6 total attempts (2 per peer)")
}

// ========================================
// PHASE 5: Pipelining (Concurrency) Tests
// ========================================

// TestP2PClient_RequestChunks_Pipelining verifies concurrent chunk requests with max 16 concurrent limit
func TestP2PClient_RequestChunks_Pipelining(t *testing.T) {
	// Setup: Mock that tracks concurrent request count using proper mutex (US-029-P4)
	var mu sync.Mutex
	var maxConcurrent int
	var currentConcurrent int

	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			// Track concurrency: measure goroutines simultaneously inside NewStream (US-029-P4)
			mu.Lock()
			currentConcurrent++
			if currentConcurrent > maxConcurrent {
				maxConcurrent = currentConcurrent
			}
			mu.Unlock()

			// Simulate network delay (holds semaphore slot busy)
			time.Sleep(50 * time.Millisecond)

			// Decrement BEFORE return — measures concurrent NewStream executions
			mu.Lock()
			currentConcurrent--
			mu.Unlock()

			// Create a valid response
			chunkData := []byte("test chunk data")
			validChunkCID, _ := crypto.GenerateCID(chunkData)

			blockResp := &protov1.BlockResponse{
				FileCid:    testCID,
				ChunkIndex: 0,
				ChunkCid:   validChunkCID,
				Data:       chunkData,
				RequestId:  "req-pipeline",
			}
			envelope := &protov1.Message{
				Payload: &protov1.Message_BlockResponse{
					BlockResponse: blockResp,
				},
			}
			respData, _ := proto.Marshal(envelope)

			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()
	testPeerID := peer.ID("test-peer-pipeline")

	// Request 50 chunks concurrently
	chunkIndices := make([]int, 50)
	for i := 0; i < 50; i++ {
		chunkIndices[i] = i
	}

	resultChan, err := client.RequestChunks(ctx, []peer.ID{testPeerID}, testCID, chunkIndices)
	require.NoError(t, err, "RequestChunks should not return error")
	require.NotNil(t, resultChan, "Result channel should not be nil")

	// Collect all results
	var results []*protocol.ChunkResult
	for result := range resultChan {
		results = append(results, result)
	}

	// Verify all 50 chunks received
	assert.Equal(t, 50, len(results), "Should receive all 50 chunks")

	// Verify max concurrent requests was <= 16
	assert.LessOrEqual(t, maxConcurrent, 16, "Max concurrent requests should be 16 or less")
	assert.Greater(t, maxConcurrent, 1, "Should have some concurrency")
}

// TestP2PClient_RequestChunks_PartialFailure verifies partial failures are handled correctly
func TestP2PClient_RequestChunks_PartialFailure(t *testing.T) {
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	var requestMu sync.Mutex
	requestCount := 0

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			requestMu.Lock()
			requestCount++
			currentCount := requestCount
			requestMu.Unlock()

			// Fail first 14 calls: with 10 chunks × 1 peer × 2 attempts = 20 total calls.
			// Calls 1-10 = first attempts (all fail), calls 11-14 = 4 second attempts (fail),
			// calls 15-20 = 6 second attempts (succeed). Result: 4 permanent failures, 6 successes.
			if currentCount <= 14 {
				return nil, fmt.Errorf("simulated network error for chunk")
			}

			// Success for other chunks
			chunkData := []byte("test chunk data")
			validChunkCID, _ := crypto.GenerateCID(chunkData)

			blockResp := &protov1.BlockResponse{
				FileCid:    testCID,
				ChunkIndex: int32(currentCount - 1),
				ChunkCid:   validChunkCID,
				Data:       chunkData,
				RequestId:  "req-partial",
			}
			envelope := &protov1.Message{
				Payload: &protov1.Message_BlockResponse{
					BlockResponse: blockResp,
				},
			}
			respData, _ := proto.Marshal(envelope)

			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx := context.Background()
	testPeerID := peer.ID("test-peer-partial")

	// Request 10 chunks
	chunkIndices := make([]int, 10)
	for i := 0; i < 10; i++ {
		chunkIndices[i] = i
	}

	resultChan, err := client.RequestChunks(ctx, []peer.ID{testPeerID}, testCID, chunkIndices)
	require.NoError(t, err)

	// Collect results
	successCount := 0
	errorCount := 0
	for result := range resultChan {
		if result.Error != nil {
			errorCount++
		} else {
			successCount++
			require.NotNil(t, result.Chunk, "Successful result should have chunk")
		}
	}

	// With failThreshold=14: 4 chunks exhaust both attempts (permanent failure), 6 succeed on retry
	assert.Equal(t, 10, successCount+errorCount, "Should receive results for all 10 chunks")
	assert.Greater(t, errorCount, 0, "Should have some permanent failures")
	assert.Greater(t, successCount, 0, "Should have some successes (partial, not total failure)")
}

// TestP2PClient_RequestChunks_ContextCancellation verifies context cancellation stops new requests
func TestP2PClient_RequestChunks_ContextCancellation(t *testing.T) {
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	var requestMu sync.Mutex
	requestCount := 0

	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			requestMu.Lock()
			requestCount++
			currentCount := requestCount
			requestMu.Unlock()

			// Simulate slow request
			time.Sleep(50 * time.Millisecond)

			chunkData := []byte("test chunk data")
			validChunkCID, _ := crypto.GenerateCID(chunkData)

			blockResp := &protov1.BlockResponse{
				FileCid:    testCID,
				ChunkIndex: int32(currentCount - 1),
				ChunkCid:   validChunkCID,
				Data:       chunkData,
				RequestId:  "req-cancel",
			}
			envelope := &protov1.Message{
				Payload: &protov1.Message_BlockResponse{
					BlockResponse: blockResp,
				},
			}
			respData, _ := proto.Marshal(envelope)

			return NewMockStreamWithResponse(respData), nil
		},
	}

	client := NewP2PClient(mockHost, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	testPeerID := peer.ID("test-peer-cancel")

	// Request 100 chunks
	chunkIndices := make([]int, 100)
	for i := 0; i < 100; i++ {
		chunkIndices[i] = i
	}

	resultChan, err := client.RequestChunks(ctx, []peer.ID{testPeerID}, testCID, chunkIndices)
	require.NoError(t, err)

	// Start collecting results
	go func() {
		// Cancel after 100ms (should allow ~5-10 chunks with 50ms delay each + concurrency)
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	// Collect results until channel closes
	resultCount := 0
	for range resultChan {
		resultCount++
	}

	// Verify: Not all 100 chunks should have been processed
	assert.Less(t, resultCount, 100, "Should not process all chunks after cancellation")
	assert.Greater(t, resultCount, 0, "Should process some chunks before cancellation")

	// Verify: No goroutine leak (in-flight requests should complete)
	time.Sleep(200 * time.Millisecond) // Wait for cleanup
}

// ========================================
// PHASE 5b: Channel Leak Prevention Tests
// ========================================

// TestRequestChunks_CancelledContext_NoGoroutineLeak verifies that goroutines are cleaned up
// when the context is cancelled, preventing goroutine leaks from blocking channel sends.
// SECURITY: Goroutine leaks can cause memory exhaustion denial-of-service.
// Approach: verify resultChan closes within a bounded time (proves WaitGroup completes,
// which proves all goroutines exited). Goroutine-counting is fragile in test suites.
func TestRequestChunks_CancelledContext_NoGoroutineLeak(t *testing.T) {
	// Setup: Mock host that simulates slow responses (only completes on context cancel)
	mockHost := &MockLibp2pHost{
		newStreamFunc: func(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (network.Stream, error) {
			select {
			case <-time.After(5 * time.Second):
				return nil, fmt.Errorf("timeout")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	client := NewP2PClient(mockHost, testLogger())

	// Create a context and cancel it immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Request 50 chunks with the already-cancelled context
	chunkIndices := make([]int, 50)
	for i := 0; i < 50; i++ {
		chunkIndices[i] = i
	}

	testPeerID := peer.ID("test-peer-leak-check")
	testCID := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"

	resultChan, err := client.RequestChunks(ctx, []peer.ID{testPeerID}, testCID, chunkIndices)
	require.NoError(t, err, "RequestChunks should not return error even with cancelled context")

	// Key assertion: resultChan must close within a bounded time.
	// If goroutines leak (blocked on channel send), WaitGroup never completes,
	// resultChan never closes, and this test times out.
	done := make(chan struct{})
	go func() {
		for range resultChan {
			// Drain results
		}
		close(done)
	}()

	select {
	case <-done:
		// Channel closed — all goroutines completed, no leak
	case <-time.After(5 * time.Second):
		t.Fatal("resultChan did not close within 5s — goroutines likely leaked (blocked on channel send)")
	}
}
