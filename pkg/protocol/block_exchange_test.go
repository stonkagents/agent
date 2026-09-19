// Package: pkg/protocol
// Feature: F-010 (P2P Transfer Protocol)
// Story: US-010-05 (Block Exchange Protocol)
// Purpose: TDD tests for protobuf encoding/decoding

package protocol

import (
	"testing"

	protov1 "github.com/stonkagents/agent/api/proto/v1"
	"google.golang.org/protobuf/proto"
)

// TestBlockRequestEncoding tests that BlockRequest messages can be encoded to protobuf.
// TDD Step 1: This test will FAIL until we generate the protobuf code.
func TestBlockRequestEncoding(t *testing.T) {
	req := &protov1.BlockRequest{
		FileCid:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		ChunkIndex: 42,
		RequestId:  "req-123",
		Priority:   50,
	}

	// Encode to protobuf bytes
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to encode BlockRequest: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Encoded data is empty")
	}

	// Decode back to verify round-trip
	decoded := &protov1.BlockRequest{}
	err = proto.Unmarshal(data, decoded)
	if err != nil {
		t.Fatalf("Failed to decode BlockRequest: %v", err)
	}

	// Verify fields match
	if decoded.FileCid != req.FileCid {
		t.Errorf("FileCid mismatch: got %q, want %q", decoded.FileCid, req.FileCid)
	}
	if decoded.ChunkIndex != req.ChunkIndex {
		t.Errorf("ChunkIndex mismatch: got %d, want %d", decoded.ChunkIndex, req.ChunkIndex)
	}
	if decoded.RequestId != req.RequestId {
		t.Errorf("RequestId mismatch: got %q, want %q", decoded.RequestId, req.RequestId)
	}
	if decoded.Priority != req.Priority {
		t.Errorf("Priority mismatch: got %d, want %d", decoded.Priority, req.Priority)
	}
}

// TestBlockResponseEncoding tests that BlockResponse messages can be encoded.
func TestBlockResponseEncoding(t *testing.T) {
	// Create a 256KB chunk of test data
	chunkData := make([]byte, 256*1024)
	for i := range chunkData {
		chunkData[i] = byte(i % 256)
	}

	resp := &protov1.BlockResponse{
		FileCid:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		ChunkIndex: 0,
		ChunkCid:   "bafybeiabc123",
		Data:       chunkData,
		RequestId:  "req-123",
	}

	// Encode
	data, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to encode BlockResponse: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Encoded data is empty")
	}

	// Decode
	decoded := &protov1.BlockResponse{}
	err = proto.Unmarshal(data, decoded)
	if err != nil {
		t.Fatalf("Failed to decode BlockResponse: %v", err)
	}

	// Verify
	if decoded.FileCid != resp.FileCid {
		t.Errorf("FileCid mismatch")
	}
	if decoded.ChunkIndex != resp.ChunkIndex {
		t.Errorf("ChunkIndex mismatch")
	}
	if decoded.ChunkCid != resp.ChunkCid {
		t.Errorf("ChunkCid mismatch")
	}
	if len(decoded.Data) != len(resp.Data) {
		t.Errorf("Data length mismatch: got %d, want %d", len(decoded.Data), len(resp.Data))
	}
}

// TestBlockNotFoundEncoding tests BlockNotFound message encoding.
func TestBlockNotFoundEncoding(t *testing.T) {
	notFound := &protov1.BlockNotFound{
		FileCid:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		ChunkIndex: 99,
		RequestId:  "req-456",
		Reason:     "chunk not available locally",
	}

	data, err := proto.Marshal(notFound)
	if err != nil {
		t.Fatalf("Failed to encode BlockNotFound: %v", err)
	}

	decoded := &protov1.BlockNotFound{}
	err = proto.Unmarshal(data, decoded)
	if err != nil {
		t.Fatalf("Failed to decode BlockNotFound: %v", err)
	}

	if decoded.FileCid != notFound.FileCid {
		t.Errorf("FileCid mismatch")
	}
	if decoded.ChunkIndex != notFound.ChunkIndex {
		t.Errorf("ChunkIndex mismatch")
	}
	if decoded.Reason != notFound.Reason {
		t.Errorf("Reason mismatch: got %q, want %q", decoded.Reason, notFound.Reason)
	}
}

// TestBlockPresenceEncoding tests BlockPresence message encoding.
func TestBlockPresenceEncoding(t *testing.T) {
	presence := &protov1.BlockPresence{
		FileCid:         "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		AvailableChunks: []int32{0, 10, 20, 5}, // chunks 0-9 and 20-24
		TotalChunks:     100,
		IsComplete:      false,
	}

	data, err := proto.Marshal(presence)
	if err != nil {
		t.Fatalf("Failed to encode BlockPresence: %v", err)
	}

	decoded := &protov1.BlockPresence{}
	err = proto.Unmarshal(data, decoded)
	if err != nil {
		t.Fatalf("Failed to decode BlockPresence: %v", err)
	}

	if decoded.FileCid != presence.FileCid {
		t.Errorf("FileCid mismatch")
	}
	if decoded.TotalChunks != presence.TotalChunks {
		t.Errorf("TotalChunks mismatch")
	}
	if len(decoded.AvailableChunks) != len(presence.AvailableChunks) {
		t.Errorf("AvailableChunks length mismatch")
	}
}

// TestMessageEnvelope tests the top-level Message envelope with oneof.
func TestMessageEnvelope(t *testing.T) {
	req := &protov1.BlockRequest{
		FileCid:    "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		ChunkIndex: 5,
		RequestId:  "req-789",
		Priority:   100,
	}

	// Wrap in Message envelope
	msg := &protov1.Message{
		Payload: &protov1.Message_BlockRequest{
			BlockRequest: req,
		},
	}

	// Encode
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to encode Message: %v", err)
	}

	// Decode
	decoded := &protov1.Message{}
	err = proto.Unmarshal(data, decoded)
	if err != nil {
		t.Fatalf("Failed to decode Message: %v", err)
	}

	// Verify oneof payload is BlockRequest
	blockReq, ok := decoded.Payload.(*protov1.Message_BlockRequest)
	if !ok {
		t.Fatalf("Payload is not BlockRequest, got type: %T", decoded.Payload)
	}

	if blockReq.BlockRequest.FileCid != req.FileCid {
		t.Errorf("FileCid mismatch in unwrapped BlockRequest")
	}
}

// TestFileMetadataEncoding tests FileMetadata message encoding.
func TestFileMetadataEncoding(t *testing.T) {
	metadata := &protov1.FileMetadata{
		FileCid:      "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:     "my-dataset.csv",
		TotalSize:    1048576, // 1MB
		TotalChunks:  4,       // 4 x 256KB chunks
		ChunkSize:    262144,  // 256KB
		ChunkCids:    []string{"bafybeia1", "bafybeia2", "bafybeia3", "bafybeia4"},
		ManifestType: "raw",
	}

	data, err := proto.Marshal(metadata)
	if err != nil {
		t.Fatalf("Failed to encode FileMetadata: %v", err)
	}

	decoded := &protov1.FileMetadata{}
	err = proto.Unmarshal(data, decoded)
	if err != nil {
		t.Fatalf("Failed to decode FileMetadata: %v", err)
	}

	if decoded.FileCid != metadata.FileCid {
		t.Errorf("FileCid mismatch")
	}
	if decoded.Filename != metadata.Filename {
		t.Errorf("Filename mismatch")
	}
	if decoded.TotalSize != metadata.TotalSize {
		t.Errorf("TotalSize mismatch")
	}
	if decoded.TotalChunks != metadata.TotalChunks {
		t.Errorf("TotalChunks mismatch")
	}
	if len(decoded.ChunkCids) != len(metadata.ChunkCids) {
		t.Errorf("ChunkCids length mismatch")
	}
}
