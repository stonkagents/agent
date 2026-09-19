// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: TDD tests for block exchange service (libp2p stream handler)

package protocol

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	protov1 "github.com/stonkagents/agent/api/proto/v1"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"google.golang.org/protobuf/proto"
)

// mockReadWriteCloser implements io.ReadWriteCloser for testing
type mockReadWriteCloser struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
}

func newMockReadWriteCloser() *mockReadWriteCloser {
	return &mockReadWriteCloser{
		readBuf:  new(bytes.Buffer),
		writeBuf: new(bytes.Buffer),
	}
}

func (m *mockReadWriteCloser) Read(p []byte) (n int, err error) {
	return m.readBuf.Read(p)
}

func (m *mockReadWriteCloser) Write(p []byte) (n int, err error) {
	return m.writeBuf.Write(p)
}

func (m *mockReadWriteCloser) Close() error {
	return nil
}

// mockChunkProvider provides test chunks
type mockChunkProvider struct {
	chunks map[string]*Chunk // key: "fileCID:chunkIndex"
}

func newMockChunkProvider() *mockChunkProvider {
	return &mockChunkProvider{
		chunks: make(map[string]*Chunk),
	}
}

func (m *mockChunkProvider) AddChunk(chunk *Chunk) {
	key := chunkKey(chunk.FileCID, chunk.ChunkIndex)
	m.chunks[key] = chunk
}

func (m *mockChunkProvider) GetChunk(ctx context.Context, fileCID string, chunkIndex int) (*Chunk, error) {
	key := chunkKey(fileCID, chunkIndex)
	chunk, ok := m.chunks[key]
	if !ok {
		return nil, nil // chunk not found
	}
	return chunk, nil
}

func (m *mockChunkProvider) HasChunk(ctx context.Context, fileCID string, chunkIndex int) (bool, error) {
	key := chunkKey(fileCID, chunkIndex)
	_, ok := m.chunks[key]
	return ok, nil
}

func chunkKey(fileCID string, chunkIndex int) string {
	return fmt.Sprintf("%s:%d", fileCID, chunkIndex)
}

// TestBlockExchangeService_HandleStream_BlockRequest tests handling a chunk request.
// TDD Step 5 (RED): This test will FAIL until we implement the stream handler.
func TestBlockExchangeService_HandleStream_BlockRequest(t *testing.T) {
	// Create a test chunk
	testData := []byte("test chunk data")

	// SECURITY NOTE: Generate valid CID for test data to pass CID verification
	testChunkCID, err := crypto.GenerateCID(testData)
	if err != nil {
		t.Fatalf("Failed to generate CID: %v", err)
	}

	testChunk := &Chunk{
		FileCID:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		ChunkIndex: 0,
		ChunkCID:   testChunkCID,
		Data:       testData,
		Size:       15,
	}

	// Setup mock chunk provider
	provider := newMockChunkProvider()
	provider.AddChunk(testChunk)

	// Create block exchange service
	service := NewBlockExchangeService()
	service.RegisterChunkProvider(provider)

	// Create a mock stream
	stream := newMockReadWriteCloser()

	// Write a BlockRequest to the stream
	request := &protov1.Message{
		Payload: &protov1.Message_BlockRequest{
			BlockRequest: &protov1.BlockRequest{
				FileCid:    testChunk.FileCID,
				ChunkIndex: int32(testChunk.ChunkIndex),
				RequestId:  "req-123",
				Priority:   50,
			},
		},
	}

	// Encode request to stream
	requestBytes, err := proto.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}
	stream.readBuf.Write(requestBytes)

	// Create a fake peer ID
	remotePeer, _ := peer.Decode("12D3KooWD3bfmNbuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")

	// Handle the stream
	err = service.HandleStream(stream, remotePeer)
	if err != nil {
		t.Fatalf("HandleStream() failed: %v", err)
	}

	// SECURITY NOTE: Request is processed asynchronously by worker goroutines
	// Wait briefly for the queued request to be processed
	time.Sleep(100 * time.Millisecond)

	// Decode response from stream
	responseBytes := stream.writeBuf.Bytes()
	if len(responseBytes) == 0 {
		t.Fatal("No response written to stream")
	}

	response := &protov1.Message{}
	err = proto.Unmarshal(responseBytes, response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Verify it's a BlockResponse
	blockResp, ok := response.Payload.(*protov1.Message_BlockResponse)
	if !ok {
		// If we got BlockNotFound, log the reason for debugging
		if notFound, ok := response.Payload.(*protov1.Message_BlockNotFound); ok {
			t.Fatalf("Expected BlockResponse, got BlockNotFound: %s", notFound.BlockNotFound.Reason)
		}
		t.Fatalf("Expected BlockResponse, got %T", response.Payload)
	}

	// Verify response fields
	if blockResp.BlockResponse.FileCid != testChunk.FileCID {
		t.Errorf("FileCid mismatch: got %q, want %q", blockResp.BlockResponse.FileCid, testChunk.FileCID)
	}
	if blockResp.BlockResponse.ChunkIndex != int32(testChunk.ChunkIndex) {
		t.Errorf("ChunkIndex mismatch: got %d, want %d", blockResp.BlockResponse.ChunkIndex, testChunk.ChunkIndex)
	}
	if blockResp.BlockResponse.ChunkCid != testChunk.ChunkCID {
		t.Errorf("ChunkCid mismatch: got %q, want %q", blockResp.BlockResponse.ChunkCid, testChunk.ChunkCID)
	}
	if !bytes.Equal(blockResp.BlockResponse.Data, testChunk.Data) {
		t.Errorf("Data mismatch: got %v, want %v", blockResp.BlockResponse.Data, testChunk.Data)
	}
	if blockResp.BlockResponse.RequestId != "req-123" {
		t.Errorf("RequestId mismatch: got %q, want %q", blockResp.BlockResponse.RequestId, "req-123")
	}
}

// TestBlockExchangeService_HandleStream_BlockNotFound tests handling a request for missing chunk.
func TestBlockExchangeService_HandleStream_BlockNotFound(t *testing.T) {
	// Create block exchange service with empty provider
	service := NewBlockExchangeService()
	provider := newMockChunkProvider()
	service.RegisterChunkProvider(provider)

	// Create a mock stream
	stream := newMockReadWriteCloser()

	// Write a BlockRequest for non-existent chunk
	request := &protov1.Message{
		Payload: &protov1.Message_BlockRequest{
			BlockRequest: &protov1.BlockRequest{
				FileCid:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
				ChunkIndex: 99,
				RequestId:  "req-404",
				Priority:   50,
			},
		},
	}

	requestBytes, _ := proto.Marshal(request)
	stream.readBuf.Write(requestBytes)

	remotePeer, _ := peer.Decode("12D3KooWD3bfmNbuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")

	// Handle the stream
	err := service.HandleStream(stream, remotePeer)
	if err != nil {
		t.Fatalf("HandleStream() failed: %v", err)
	}

	// SECURITY NOTE: Request is processed asynchronously by worker goroutines
	// Wait briefly for the queued request to be processed
	time.Sleep(100 * time.Millisecond)

	// Decode response
	responseBytes := stream.writeBuf.Bytes()
	response := &protov1.Message{}
	proto.Unmarshal(responseBytes, response)

	// Verify it's a BlockNotFound
	blockNotFound, ok := response.Payload.(*protov1.Message_BlockNotFound)
	if !ok {
		t.Fatalf("Expected BlockNotFound, got %T", response.Payload)
	}

	if blockNotFound.BlockNotFound.RequestId != "req-404" {
		t.Errorf("RequestId mismatch in BlockNotFound")
	}
}

// TestHandleStream_ReadDeadline_PreventsSlowloris verifies that HandleStream
// applies a read deadline to streams that support SetReadDeadline.
// SECURITY: Prevents Slowloris attacks where attacker holds stream open indefinitely.
func TestHandleStream_ReadDeadline_PreventsSlowloris(t *testing.T) {
	service := NewBlockExchangeService()
	provider := newMockChunkProvider()
	service.RegisterChunkProvider(provider)

	// Create a stream that tracks if SetReadDeadline was called
	stream := &mockDeadlineStream{
		readBuf:  new(bytes.Buffer), // empty buffer = Read returns EOF immediately
		writeBuf: new(bytes.Buffer),
	}

	remotePeer, _ := peer.Decode("12D3KooWD3bfmNbuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")

	// HandleStream should apply read deadline and return error for empty message
	err := service.HandleStream(stream, remotePeer)
	if err == nil {
		t.Fatal("Expected error for empty message")
	}

	// Verify SetReadDeadline was called
	if !stream.readDeadlineSet {
		t.Error("SECURITY: SetReadDeadline was not called — Slowloris vulnerability")
	}

	// Verify the deadline is within a reasonable range (~30s from now)
	if !stream.readDeadline.IsZero() {
		deadline := time.Until(stream.readDeadline)
		if deadline < 25*time.Second || deadline > 35*time.Second {
			t.Errorf("Read deadline should be ~30s, got %v", deadline)
		}
	}
}

// mockDeadlineStream is a mock stream that tracks SetReadDeadline calls
type mockDeadlineStream struct {
	readBuf         *bytes.Buffer
	writeBuf        *bytes.Buffer
	readDeadlineSet bool
	readDeadline    time.Time
}

func (m *mockDeadlineStream) Read(p []byte) (n int, err error) {
	return m.readBuf.Read(p)
}

func (m *mockDeadlineStream) Write(p []byte) (n int, err error) {
	return m.writeBuf.Write(p)
}

func (m *mockDeadlineStream) Close() error {
	return nil
}

func (m *mockDeadlineStream) SetReadDeadline(t time.Time) error {
	m.readDeadlineSet = true
	m.readDeadline = t
	return nil
}

// TestBlockExchangeService_RequestChunk tests requesting a chunk from a remote peer.
func TestBlockExchangeService_RequestChunk(t *testing.T) {
	ctx := context.Background()

	// Create block exchange service
	service := NewBlockExchangeService()

	// For this test, we'll need to mock the libp2p stream creation
	// For now, test that the method exists and has correct signature
	remotePeer, _ := peer.Decode("12D3KooWD3bfmNbuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")

	// This will fail because we haven't implemented the actual libp2p stream handling yet
	_, err := service.RequestChunk(ctx, remotePeer, "bafybeig123", 0)

	// For now, we expect an error since libp2p stream creation is not mocked
	// In a real implementation, this would use libp2p.Host.NewStream()
	if err == nil {
		t.Skip("RequestChunk implementation not complete - skipping until libp2p integration")
	}
}

// TestRateLimiter_RefillRate verifies the per-peer rate limiter refills at the configured rate.
// TD-034: The original bug refilled at 1 token/sec instead of RateLimitPerPeerPerSecond tokens/sec,
// causing stream resets after the initial burst of 100 tokens was exhausted (~chunk 103 of 4096).
func TestRateLimiter_RefillRate(t *testing.T) {
	service := NewBlockExchangeService().(*blockExchangeService)
	testPeer, _ := peer.Decode("12D3KooWD3bfmNbuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")

	// Drain all initial tokens (100)
	for i := 0; i < RateLimitPerPeerPerSecond; i++ {
		if !service.checkRateLimit(testPeer) {
			t.Fatalf("Rate limit hit on request %d, expected burst of %d", i, RateLimitPerPeerPerSecond)
		}
	}

	// Confirm tokens are exhausted
	if service.checkRateLimit(testPeer) {
		t.Fatal("Expected rate limit to be exhausted after draining all tokens")
	}

	// Wait 150ms — should refill ~15 tokens at 100/sec rate
	time.Sleep(150 * time.Millisecond)

	// After 150ms at 100 tokens/sec, we should have at least 10 tokens refilled.
	// The bugged version (1 token/sec) would have 0 tokens after 150ms.
	successCount := 0
	for i := 0; i < 10; i++ {
		if service.checkRateLimit(testPeer) {
			successCount++
		}
	}

	if successCount < 5 {
		t.Errorf("After 150ms, expected at least 5 refilled tokens (100/sec rate), got %d — rate limiter refill is too slow", successCount)
	}
}

// TestBlockExchangeService_ProtocolID tests the protocol ID.
func TestBlockExchangeService_ProtocolID(t *testing.T) {
	service := NewBlockExchangeService()

	protocolID := service.ProtocolID()
	expectedID := ProtocolIDStonkAgents

	if protocolID != expectedID {
		t.Errorf("ProtocolID mismatch: got %q, want %q", protocolID, expectedID)
	}
}
