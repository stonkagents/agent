// Package: pkg/protocol
// Feature: F-003 (Universal File Sharing)
// Story: US-003-04 (Chunked Transfer for All Asset Types)
// Purpose: TDD tests for manifest preservation through P2P transfer

package protocol

import (
	"bytes"
	"testing"

	protov1 "github.com/stonkagents/agent/api/proto/v1"
	"github.com/stonkagents/agent/pkg/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestAtRawTransferPreservesManifest - RED test
// Acceptance Criterion: Share .at-raw file → manifest stored → receiver reconstructs manifest
func TestAtRawTransferPreservesManifest(t *testing.T) {
	// Arrange - Create original .at-raw manifest
	originalManifest := &manifest.AtRawManifest{
		Filename:      "test-data.csv",
		MimeType:      "text/csv",
		Size:          1024,
		Cid:           "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Signature:     []byte("signature-bytes"),
		CreatedAt:     timestamppb.Now(),
		FormatVersion: "1.0",
	}

	// Serialize manifest to bytes
	manifestData, err := proto.Marshal(originalManifest)
	if err != nil {
		t.Fatalf("Failed to marshal manifest: %v", err)
	}

	// Create FileMetadata with manifest_data
	fileMetadata := &protov1.FileMetadata{
		FileCid:      "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		Filename:     "test-data.csv",
		TotalSize:    1024,
		TotalChunks:  1,
		ChunkSize:    262144,
		ChunkCids:    []string{"bafybeiachunk1"},
		ManifestType: "raw",
		ManifestData: manifestData,
	}

	// Act - Serialize and deserialize (simulating P2P transfer)
	data, err := proto.Marshal(fileMetadata)
	if err != nil {
		t.Fatalf("Failed to marshal FileMetadata: %v", err)
	}

	receivedMetadata := &protov1.FileMetadata{}
	err = proto.Unmarshal(data, receivedMetadata)
	if err != nil {
		t.Fatalf("Failed to unmarshal FileMetadata: %v", err)
	}

	// Verify ManifestData was preserved
	if len(receivedMetadata.ManifestData) == 0 {
		t.Fatal("ManifestData was not preserved through transfer")
	}

	// Reconstruct original manifest from received data
	reconstructedManifest := &manifest.AtRawManifest{}
	err = proto.Unmarshal(receivedMetadata.ManifestData, reconstructedManifest)
	if err != nil {
		t.Fatalf("Failed to unmarshal manifest from ManifestData: %v", err)
	}

	// Assert - Verify all fields match
	if reconstructedManifest.Filename != originalManifest.Filename {
		t.Errorf("Filename mismatch: expected %s, got %s", originalManifest.Filename, reconstructedManifest.Filename)
	}

	if reconstructedManifest.MimeType != originalManifest.MimeType {
		t.Errorf("MimeType mismatch: expected %s, got %s", originalManifest.MimeType, reconstructedManifest.MimeType)
	}

	if reconstructedManifest.Size != originalManifest.Size {
		t.Errorf("Size mismatch: expected %d, got %d", originalManifest.Size, reconstructedManifest.Size)
	}

	if reconstructedManifest.Cid != originalManifest.Cid {
		t.Errorf("CID mismatch: expected %s, got %s", originalManifest.Cid, reconstructedManifest.Cid)
	}

	if !bytes.Equal(reconstructedManifest.Signature, originalManifest.Signature) {
		t.Error("Signature bytes do not match")
	}

	if reconstructedManifest.FormatVersion != originalManifest.FormatVersion {
		t.Errorf("FormatVersion mismatch: expected %s, got %s", originalManifest.FormatVersion, reconstructedManifest.FormatVersion)
	}

	t.Log("✓ .at-raw manifest preserved through P2P transfer")
}

// TestAtVecTransferPreservesDimensions - RED test
// Acceptance Criterion: Share .at-vec file → dimensions, model, framework preserved through transfer
func TestAtVecTransferPreservesDimensions(t *testing.T) {
	// Arrange - Create original .at-vec manifest
	originalManifest := &manifest.AtVecManifest{
		Dimensions:    1000,
		Model:         "openai/text-embedding-3-small",
		Framework:     "openai",
		FormatVersion: "1.0",
		Cid:           "bafybeigdyrzt5sfpvector123456789abcdef",
		Signature:     []byte("vec-signature"),
		CreatedAt:     timestamppb.Now(),
		Description:   "Text embeddings for semantic search",
	}

	// Serialize manifest to bytes
	manifestData, err := proto.Marshal(originalManifest)
	if err != nil {
		t.Fatalf("Failed to marshal manifest: %v", err)
	}

	// Create FileMetadata with manifest_data
	fileMetadata := &protov1.FileMetadata{
		FileCid:      "bafybeigdyrzt5sfpvector123456789abcdef",
		Filename:     "embeddings.at-vec",
		TotalSize:    4000, // 1000 dims * 4 bytes (float32)
		TotalChunks:  1,
		ChunkSize:    262144,
		ChunkCids:    []string{"bafybeiavectorchunk"},
		ManifestType: "vector",
		ManifestData: manifestData,
	}

	// Act - Serialize and deserialize (simulating P2P transfer)
	data, err := proto.Marshal(fileMetadata)
	if err != nil {
		t.Fatalf("Failed to marshal FileMetadata: %v", err)
	}

	receivedMetadata := &protov1.FileMetadata{}
	err = proto.Unmarshal(data, receivedMetadata)
	if err != nil {
		t.Fatalf("Failed to unmarshal FileMetadata: %v", err)
	}

	// Verify ManifestData was preserved
	if len(receivedMetadata.ManifestData) == 0 {
		t.Fatal("ManifestData was not preserved through transfer")
	}

	// Reconstruct original manifest from received data
	reconstructedManifest := &manifest.AtVecManifest{}
	err = proto.Unmarshal(receivedMetadata.ManifestData, reconstructedManifest)
	if err != nil {
		t.Fatalf("Failed to unmarshal manifest from ManifestData: %v", err)
	}

	// Assert - Verify critical fields match (especially dimensions, model, framework)
	if reconstructedManifest.Dimensions != originalManifest.Dimensions {
		t.Errorf("Dimensions mismatch: expected %d, got %d", originalManifest.Dimensions, reconstructedManifest.Dimensions)
	}

	if reconstructedManifest.Model != originalManifest.Model {
		t.Errorf("Model mismatch: expected %s, got %s", originalManifest.Model, reconstructedManifest.Model)
	}

	if reconstructedManifest.Framework != originalManifest.Framework {
		t.Errorf("Framework mismatch: expected %s, got %s", originalManifest.Framework, reconstructedManifest.Framework)
	}

	if reconstructedManifest.Cid != originalManifest.Cid {
		t.Errorf("CID mismatch: expected %s, got %s", originalManifest.Cid, reconstructedManifest.Cid)
	}

	if !bytes.Equal(reconstructedManifest.Signature, originalManifest.Signature) {
		t.Error("Signature bytes do not match")
	}

	if reconstructedManifest.Description != originalManifest.Description {
		t.Errorf("Description mismatch: expected %s, got %s", originalManifest.Description, reconstructedManifest.Description)
	}

	t.Logf("✓ .at-vec manifest preserved: %d dimensions, model=%s, framework=%s",
		reconstructedManifest.Dimensions,
		reconstructedManifest.Model,
		reconstructedManifest.Framework)
}

// TestManifestDataRoundTrip - RED test
// Acceptance Criterion: Manifest included in FileMetadata protobuf (manifest_data field)
func TestManifestDataRoundTrip(t *testing.T) {
	// Test with minimal .at-raw manifest
	minimalManifest := &manifest.AtRawManifest{
		Filename: "minimal.txt",
		Cid:      "bafybeigminimal",
	}

	manifestData, err := proto.Marshal(minimalManifest)
	if err != nil {
		t.Fatalf("Failed to marshal manifest: %v", err)
	}

	// Create FileMetadata
	metadata := &protov1.FileMetadata{
		FileCid:      "bafybeigminimal",
		Filename:     "minimal.txt",
		ManifestType: "raw",
		ManifestData: manifestData,
	}

	// Round-trip
	data, err := proto.Marshal(metadata)
	if err != nil {
		t.Fatalf("Failed to marshal FileMetadata: %v", err)
	}

	received := &protov1.FileMetadata{}
	err = proto.Unmarshal(data, received)
	if err != nil {
		t.Fatalf("Failed to unmarshal FileMetadata: %v", err)
	}

	// Verify ManifestData survives round-trip
	if !bytes.Equal(metadata.ManifestData, received.ManifestData) {
		t.Error("ManifestData changed during round-trip")
	}

	// Verify we can reconstruct the manifest
	reconstructed := &manifest.AtRawManifest{}
	err = proto.Unmarshal(received.ManifestData, reconstructed)
	if err != nil {
		t.Fatalf("Failed to reconstruct manifest: %v", err)
	}

	if reconstructed.Filename != minimalManifest.Filename {
		t.Errorf("Filename mismatch after round-trip: expected %s, got %s",
			minimalManifest.Filename, reconstructed.Filename)
	}

	t.Log("✓ Manifest data round-trip successful")
}
