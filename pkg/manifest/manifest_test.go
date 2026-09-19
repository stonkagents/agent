// Package: pkg/manifest
// Feature: F-003 (Universal File Sharing)
// Story: US-003-03 (Protocol Buffer Manifest Format)
// Purpose: Test suite for Protocol Buffer manifest encoding/decoding

package manifest

import (
	"bytes"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestEncodeAtRawManifest_Basic verifies basic AtRawManifest encoding
// Acceptance Criteria: Manifest serialization (Go struct → protobuf bytes) working
func TestEncodeAtRawManifest_Basic(t *testing.T) {
	manifest := &AtRawManifest{
		Filename:      "test.txt",
		MimeType:      "text/plain",
		Size:          1024,
		Cid:           "bafkreiabc123",
		Signature:     []byte("test_signature"),
		CreatedAt:     timestamppb.New(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)),
		FormatVersion: "1.0",
	}

	encoded, err := EncodeAtRawManifest(manifest)
	if err != nil {
		t.Fatalf("EncodeAtRawManifest() failed: %v", err)
	}

	if len(encoded) == 0 {
		t.Error("EncodeAtRawManifest() returned empty bytes")
	}
}

// TestDecodeAtRawManifest_Roundtrip verifies encode/decode roundtrip
// Acceptance Criteria: Manifest deserialization (protobuf bytes → Go struct) working
func TestDecodeAtRawManifest_Roundtrip(t *testing.T) {
	original := &AtRawManifest{
		Filename:      "document.pdf",
		MimeType:      "application/pdf",
		Size:          2048,
		Cid:           "bafkreixyz789",
		Signature:     []byte("signature_data"),
		CreatedAt:     timestamppb.New(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)),
		FormatVersion: "1.0",
	}

	// Encode
	encoded, err := EncodeAtRawManifest(original)
	if err != nil {
		t.Fatalf("EncodeAtRawManifest() failed: %v", err)
	}

	// Decode
	decoded, err := DecodeAtRawManifest(encoded)
	if err != nil {
		t.Fatalf("DecodeAtRawManifest() failed: %v", err)
	}

	// Verify all fields match
	if decoded.Filename != original.Filename {
		t.Errorf("Filename = %s, want %s", decoded.Filename, original.Filename)
	}
	if decoded.MimeType != original.MimeType {
		t.Errorf("MimeType = %s, want %s", decoded.MimeType, original.MimeType)
	}
	if decoded.Size != original.Size {
		t.Errorf("Size = %d, want %d", decoded.Size, original.Size)
	}
	if decoded.Cid != original.Cid {
		t.Errorf("Cid = %s, want %s", decoded.Cid, original.Cid)
	}
	if !bytes.Equal(decoded.Signature, original.Signature) {
		t.Errorf("Signature = %v, want %v", decoded.Signature, original.Signature)
	}
	if decoded.FormatVersion != original.FormatVersion {
		t.Errorf("FormatVersion = %s, want %s", decoded.FormatVersion, original.FormatVersion)
	}
}

// TestDecodeAtRawManifest_InvalidInput verifies error handling
func TestDecodeAtRawManifest_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []byte{},
			wantErr: false, // Empty protobuf is valid, just returns default values
		},
		{
			name:    "corrupted data",
			data:    []byte{0xFF, 0xFF, 0xFF, 0xFF},
			wantErr: true,
		},
		{
			name:    "truncated data",
			data:    []byte{0x08, 0x01}, // Incomplete protobuf
			wantErr: false,              // Protobuf handles partial data gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeAtRawManifest(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("DecodeAtRawManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestEncodeAtVecManifest_Basic verifies basic AtVecManifest encoding
func TestEncodeAtVecManifest_Basic(t *testing.T) {
	manifest := &AtVecManifest{
		Dimensions:    512,
		Model:         "openai/clip-vit-b-32",
		Framework:     "pytorch",
		FormatVersion: "1.0",
		Cid:           "bafkreiclip123",
		Signature:     []byte("vec_signature"),
		CreatedAt:     timestamppb.New(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)),
		Description:   "CLIP embedding for test image",
	}

	encoded, err := EncodeAtVecManifest(manifest)
	if err != nil {
		t.Fatalf("EncodeAtVecManifest() failed: %v", err)
	}

	if len(encoded) == 0 {
		t.Error("EncodeAtVecManifest() returned empty bytes")
	}
}

// TestDecodeAtVecManifest_Roundtrip verifies encode/decode roundtrip for embeddings
func TestDecodeAtVecManifest_Roundtrip(t *testing.T) {
	original := &AtVecManifest{
		Dimensions:    768,
		Model:         "sentence-transformers/all-MiniLM-L6-v2",
		Framework:     "tensorflow",
		FormatVersion: "1.0",
		Cid:           "bafkreisentence456",
		Signature:     []byte("sentence_signature"),
		CreatedAt:     timestamppb.New(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)),
		Description:   "Sentence embedding for text chunk",
	}

	// Encode
	encoded, err := EncodeAtVecManifest(original)
	if err != nil {
		t.Fatalf("EncodeAtVecManifest() failed: %v", err)
	}

	// Decode
	decoded, err := DecodeAtVecManifest(encoded)
	if err != nil {
		t.Fatalf("DecodeAtVecManifest() failed: %v", err)
	}

	// Verify all fields match
	if decoded.Dimensions != original.Dimensions {
		t.Errorf("Dimensions = %d, want %d", decoded.Dimensions, original.Dimensions)
	}
	if decoded.Model != original.Model {
		t.Errorf("Model = %s, want %s", decoded.Model, original.Model)
	}
	if decoded.Framework != original.Framework {
		t.Errorf("Framework = %s, want %s", decoded.Framework, original.Framework)
	}
	if decoded.FormatVersion != original.FormatVersion {
		t.Errorf("FormatVersion = %s, want %s", decoded.FormatVersion, original.FormatVersion)
	}
	if decoded.Cid != original.Cid {
		t.Errorf("Cid = %s, want %s", decoded.Cid, original.Cid)
	}
	if !bytes.Equal(decoded.Signature, original.Signature) {
		t.Errorf("Signature = %v, want %v", decoded.Signature, original.Signature)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description = %s, want %s", decoded.Description, original.Description)
	}
}

// TestManifestVersioning_BackwardCompatibility verifies v1.0 manifests readable
// Acceptance Criteria: Backward compatibility - v1.0 manifests can be read by v1.1 code
func TestManifestVersioning_BackwardCompatibility(t *testing.T) {
	// Create v1.0 manifest
	v10Manifest := &AtRawManifest{
		Filename:      "legacy.txt",
		MimeType:      "text/plain",
		Size:          512,
		Cid:           "bafkreiv10legacy",
		Signature:     []byte("v10_signature"),
		CreatedAt:     timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		FormatVersion: "1.0",
	}

	// Encode as v1.0
	encoded, err := EncodeAtRawManifest(v10Manifest)
	if err != nil {
		t.Fatalf("EncodeAtRawManifest() for v1.0 failed: %v", err)
	}

	// Decode (current code should handle v1.0)
	decoded, err := DecodeAtRawManifest(encoded)
	if err != nil {
		t.Fatalf("DecodeAtRawManifest() for v1.0 manifest failed: %v", err)
	}

	if decoded.FormatVersion != "1.0" {
		t.Errorf("FormatVersion = %s, want 1.0", decoded.FormatVersion)
	}

	// Verify data integrity
	if decoded.Filename != v10Manifest.Filename {
		t.Errorf("Backward compatibility failed: Filename = %s, want %s", decoded.Filename, v10Manifest.Filename)
	}
}

// TestManifestVersioning_FutureVersion verifies graceful handling of future versions
func TestManifestVersioning_FutureVersion(t *testing.T) {
	// Create manifest with future version
	futureManifest := &AtRawManifest{
		Filename:      "future.txt",
		MimeType:      "text/plain",
		Size:          1024,
		Cid:           "bafkreifuture999",
		Signature:     []byte("future_signature"),
		CreatedAt:     timestamppb.New(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)),
		FormatVersion: "2.5", // Future version
	}

	// Should encode without error
	encoded, err := EncodeAtRawManifest(futureManifest)
	if err != nil {
		t.Fatalf("EncodeAtRawManifest() for future version failed: %v", err)
	}

	// Should decode without error (forward compatibility not guaranteed, but shouldn't crash)
	decoded, err := DecodeAtRawManifest(encoded)
	if err != nil {
		t.Fatalf("DecodeAtRawManifest() for future version failed: %v", err)
	}

	if decoded.FormatVersion != "2.5" {
		t.Errorf("FormatVersion = %s, want 2.5", decoded.FormatVersion)
	}
}

// TestAtRawManifest_EmptyFields verifies handling of optional fields
func TestAtRawManifest_EmptyFields(t *testing.T) {
	manifest := &AtRawManifest{
		Filename:      "minimal.txt",
		MimeType:      "text/plain",
		Size:          0, // Empty file
		Cid:           "bafkreiempty",
		Signature:     []byte{}, // Empty signature (invalid in practice, but should encode)
		CreatedAt:     nil,      // Missing timestamp
		FormatVersion: "1.0",
	}

	// Should encode without error
	encoded, err := EncodeAtRawManifest(manifest)
	if err != nil {
		t.Fatalf("EncodeAtRawManifest() with empty fields failed: %v", err)
	}

	// Should decode without error
	decoded, err := DecodeAtRawManifest(encoded)
	if err != nil {
		t.Fatalf("DecodeAtRawManifest() with empty fields failed: %v", err)
	}

	// Verify nil timestamp handled gracefully
	if decoded.CreatedAt != nil {
		t.Error("Expected nil CreatedAt for manifest with missing timestamp")
	}
}

// TestAtVecManifest_CommonModels verifies support for common embedding models
// Acceptance Criteria: Support for CLIP, sentence-transformers, OpenAI embeddings
func TestAtVecManifest_CommonModels(t *testing.T) {
	tests := []struct {
		name       string
		dimensions int32
		model      string
		framework  string
	}{
		{
			name:       "CLIP ViT-B/32",
			dimensions: 512,
			model:      "openai/clip-vit-b-32",
			framework:  "pytorch",
		},
		{
			name:       "CLIP ViT-L/14",
			dimensions: 768,
			model:      "openai/clip-vit-l-14",
			framework:  "pytorch",
		},
		{
			name:       "sentence-transformers MiniLM",
			dimensions: 384,
			model:      "sentence-transformers/all-MiniLM-L6-v2",
			framework:  "pytorch",
		},
		{
			name:       "OpenAI text-embedding-3-small",
			dimensions: 1536,
			model:      "openai/text-embedding-3-small",
			framework:  "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &AtVecManifest{
				Dimensions:    tt.dimensions,
				Model:         tt.model,
				Framework:     tt.framework,
				FormatVersion: "1.0",
				Cid:           "bafkreitest",
				Signature:     []byte("test_signature"),
				CreatedAt:     timestamppb.Now(),
				Description:   "Test embedding",
			}

			// Encode/decode roundtrip
			encoded, err := EncodeAtVecManifest(manifest)
			if err != nil {
				t.Fatalf("EncodeAtVecManifest() failed for %s: %v", tt.name, err)
			}

			decoded, err := DecodeAtVecManifest(encoded)
			if err != nil {
				t.Fatalf("DecodeAtVecManifest() failed for %s: %v", tt.name, err)
			}

			if decoded.Dimensions != tt.dimensions {
				t.Errorf("Dimensions = %d, want %d", decoded.Dimensions, tt.dimensions)
			}
			if decoded.Model != tt.model {
				t.Errorf("Model = %s, want %s", decoded.Model, tt.model)
			}
		})
	}
}

// TestDecodeAtVecManifest_InvalidInput verifies error handling
func TestDecodeAtVecManifest_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []byte{},
			wantErr: false, // Empty protobuf is valid
		},
		{
			name:    "corrupted data",
			data:    []byte{0xFF, 0xFF, 0xFF, 0xFF},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeAtVecManifest(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("DecodeAtVecManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestEncodeAtRawManifest_NilInput verifies error handling for nil manifest
func TestEncodeAtRawManifest_NilInput(t *testing.T) {
	_, err := EncodeAtRawManifest(nil)
	if err == nil {
		t.Error("EncodeAtRawManifest(nil) should return error")
	}
}

// TestEncodeAtVecManifest_NilInput verifies error handling for nil manifest
func TestEncodeAtVecManifest_NilInput(t *testing.T) {
	_, err := EncodeAtVecManifest(nil)
	if err == nil {
		t.Error("EncodeAtVecManifest(nil) should return error")
	}
}
