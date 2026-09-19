// Package: pkg/assets
// Feature: F-003 (Universal File Sharing)
// Story: US-003-02 (.at-vec Asset Type Support)
// Purpose: Test suite for .at-vec vector embedding asset creation and verification

package assets

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/manifest"
)

// TestDetectEmbeddingDimensions verifies automatic dimension detection from file size
// Acceptance Criteria: Metadata extraction - automatically detect dimensions from embedding file
func TestDetectEmbeddingDimensions(t *testing.T) {
	tests := []struct {
		name         string
		numFloats    int
		expectedDims int32
		wantErr      bool
	}{
		{
			name:         "CLIP ViT-B/32 (512 dimensions)",
			numFloats:    512,
			expectedDims: 512,
			wantErr:      false,
		},
		{
			name:         "CLIP ViT-L/14 (768 dimensions)",
			numFloats:    768,
			expectedDims: 768,
			wantErr:      false,
		},
		{
			name:         "sentence-transformers MiniLM (384 dimensions)",
			numFloats:    384,
			expectedDims: 384,
			wantErr:      false,
		},
		{
			name:         "OpenAI embeddings (1536 dimensions)",
			numFloats:    1536,
			expectedDims: 1536,
			wantErr:      false,
		},
		{
			name:         "invalid size (not multiple of 4)",
			numFloats:    -1, // Will create 10 bytes (not divisible by 4)
			expectedDims: 0,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var data []byte
			if tt.numFloats == -1 {
				// Create data not divisible by 4
				data = make([]byte, 10)
			} else {
				// Create float32 array (4 bytes per float)
				data = make([]byte, tt.numFloats*4)
			}

			dims, err := DetectEmbeddingDimensions(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("DetectEmbeddingDimensions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && dims != tt.expectedDims {
				t.Errorf("DetectEmbeddingDimensions() = %d, want %d", dims, tt.expectedDims)
			}
		})
	}
}

// TestCreateAtVecAsset_Basic verifies basic .at-vec asset creation
// Acceptance Criteria: Support for common embedding models (CLIP, sentence-transformers, OpenAI)
func TestCreateAtVecAsset_Basic(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	// Create a 512-dimension CLIP embedding (512 floats = 2048 bytes)
	embeddingData := createTestEmbedding(512)

	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32",
		"pytorch",
		"CLIP embedding for test image",
		embeddingData,
		privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Verify manifest fields
	if manifest.Dimensions != 512 {
		t.Errorf("Dimensions = %d, want 512", manifest.Dimensions)
	}

	if manifest.Model != "openai/clip-vit-b-32" {
		t.Errorf("Model = %s, want openai/clip-vit-b-32", manifest.Model)
	}

	if manifest.Framework != "pytorch" {
		t.Errorf("Framework = %s, want pytorch", manifest.Framework)
	}

	if manifest.Description != "CLIP embedding for test image" {
		t.Errorf("Description = %s, want 'CLIP embedding for test image'", manifest.Description)
	}

	if manifest.Cid == "" {
		t.Error("CID is empty")
	}

	if len(manifest.Signature) == 0 {
		t.Error("Signature is empty")
	}

	if manifest.CreatedAt == nil {
		t.Error("CreatedAt is nil")
	}

	if manifest.FormatVersion != "1.0" {
		t.Errorf("FormatVersion = %s, want 1.0", manifest.FormatVersion)
	}

	// Verify signature is valid
	valid, err := VerifyAtVecAsset(manifest, embeddingData, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() failed: %v", err)
	}
	if !valid {
		t.Error("VerifyAtVecAsset() returned false for valid embedding")
	}
}

// TestVerifyAtVecAsset_ValidEmbedding verifies asset verification with valid data
func TestVerifyAtVecAsset_ValidEmbedding(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	embeddingData := createTestEmbedding(768)

	// Create asset
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-l-14",
		"pytorch",
		"Test embedding",
		embeddingData,
		privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Verify asset
	valid, err := VerifyAtVecAsset(manifest, embeddingData, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() failed: %v", err)
	}

	if !valid {
		t.Error("VerifyAtVecAsset() returned false for valid embedding")
	}
}

// TestVerifyAtVecAsset_TamperedData verifies detection of tampered embedding data
func TestVerifyAtVecAsset_TamperedData(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	originalData := createTestEmbedding(512)
	tamperedData := make([]byte, len(originalData))
	copy(tamperedData, originalData)
	// Modify one byte to simulate tampering
	tamperedData[0] = tamperedData[0] ^ 0xFF

	// Create asset with original data
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32",
		"pytorch",
		"Original embedding",
		originalData,
		privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Verify with tampered data
	valid, err := VerifyAtVecAsset(manifest, tamperedData, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() failed: %v", err)
	}

	if valid {
		t.Error("VerifyAtVecAsset() returned true for tampered data - should detect tampering")
	}
}

// TestVerifyAtVecAsset_WrongDimensions verifies detection of dimension mismatch
func TestVerifyAtVecAsset_WrongDimensions(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	originalData := createTestEmbedding(512)
	wrongDimensionData := createTestEmbedding(768) // Different dimensions

	// Create asset with 512-dim embedding
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32",
		"pytorch",
		"512-dim embedding",
		originalData,
		privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Verify with 768-dim embedding
	valid, err := VerifyAtVecAsset(manifest, wrongDimensionData, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() failed: %v", err)
	}

	if valid {
		t.Error("VerifyAtVecAsset() returned true for wrong dimensions - should detect mismatch")
	}
}

// TestIntegration_CommonEmbeddingModels verifies end-to-end workflow for common embedding models
// Acceptance Criteria: Integration test - Upload CLIP embedding → verify schema → download → validate
func TestIntegration_CommonEmbeddingModels(t *testing.T) {
	testModels := []struct {
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
			name:       "sentence-transformers MPNet",
			dimensions: 768,
			model:      "sentence-transformers/all-mpnet-base-v2",
			framework:  "pytorch",
		},
		{
			name:       "OpenAI text-embedding-3-small",
			dimensions: 1536,
			model:      "openai/text-embedding-3-small",
			framework:  "openai",
		},
	}

	// Generate keypair for signing
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	for _, tm := range testModels {
		t.Run(tm.name, func(t *testing.T) {
			// Create test embedding data
			embeddingData := createTestEmbedding(int(tm.dimensions))

			// Create .at-vec asset
			manifest, err := CreateAtVecAsset(
				tm.model,
				tm.framework,
				"Test embedding for "+tm.name,
				embeddingData,
				privateKey,
			)
			if err != nil {
				t.Fatalf("CreateAtVecAsset() failed for %s: %v", tm.name, err)
			}

			// Verify manifest fields
			if manifest.Dimensions != tm.dimensions {
				t.Errorf("Dimensions = %d, want %d", manifest.Dimensions, tm.dimensions)
			}

			if manifest.Model != tm.model {
				t.Errorf("Model = %s, want %s", manifest.Model, tm.model)
			}

			if manifest.Framework != tm.framework {
				t.Errorf("Framework = %s, want %s", manifest.Framework, tm.framework)
			}

			// Verify CID matches content
			expectedCID, err := crypto.GenerateCID(embeddingData)
			if err != nil {
				t.Fatalf("GenerateCID() failed: %v", err)
			}
			if manifest.Cid != expectedCID {
				t.Errorf("CID = %s, want %s", manifest.Cid, expectedCID)
			}

			// Verify asset integrity
			valid, err := VerifyAtVecAsset(manifest, embeddingData, publicKey)
			if err != nil {
				t.Fatalf("VerifyAtVecAsset() failed for %s: %v", tm.name, err)
			}
			if !valid {
				t.Errorf("VerifyAtVecAsset() returned false for %s", tm.name)
			}

			// Simulate "download" - verify downloaded data
			downloadedData := embeddingData // In real scenario, would be downloaded from peer

			// Verify downloaded data against manifest
			valid, err = VerifyAtVecAsset(manifest, downloadedData, publicKey)
			if err != nil {
				t.Fatalf("VerifyAtVecAsset() on downloaded data failed: %v", err)
			}
			if !valid {
				t.Error("Downloaded embedding failed verification")
			}

			// Verify dimensions can be detected from downloaded data
			detectedDims, err := DetectEmbeddingDimensions(downloadedData)
			if err != nil {
				t.Fatalf("DetectEmbeddingDimensions() failed: %v", err)
			}
			if detectedDims != tm.dimensions {
				t.Errorf("Detected dimensions = %d, want %d", detectedDims, tm.dimensions)
			}
		})
	}
}

// TestCreateAtVecAsset_ErrorCases verifies error handling for invalid inputs
func TestCreateAtVecAsset_ErrorCases(t *testing.T) {
	_, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	validData := createTestEmbedding(512)

	tests := []struct {
		name        string
		model       string
		framework   string
		description string
		data        []byte
		privateKey  []byte
		wantErr     bool
	}{
		{
			name:        "empty model",
			model:       "",
			framework:   "pytorch",
			description: "test",
			data:        validData,
			privateKey:  privateKey,
			wantErr:     true,
		},
		{
			name:        "empty framework",
			model:       "test/model",
			framework:   "",
			description: "test",
			data:        validData,
			privateKey:  privateKey,
			wantErr:     true,
		},
		{
			name:        "nil data",
			model:       "test/model",
			framework:   "pytorch",
			description: "test",
			data:        nil,
			privateKey:  privateKey,
			wantErr:     true,
		},
		{
			name:        "nil private key",
			model:       "test/model",
			framework:   "pytorch",
			description: "test",
			data:        validData,
			privateKey:  nil,
			wantErr:     true,
		},
		{
			name:        "invalid dimensions (not divisible by 4)",
			model:       "test/model",
			framework:   "pytorch",
			description: "test",
			data:        make([]byte, 10), // Not divisible by 4
			privateKey:  privateKey,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CreateAtVecAsset(tt.model, tt.framework, tt.description, tt.data, tt.privateKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateAtVecAsset() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestVerifyAtVecAsset_ErrorCases verifies error handling for invalid inputs
func TestVerifyAtVecAsset_ErrorCases(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	validData := createTestEmbedding(512)
	validManifest, err := CreateAtVecAsset("test/model", "pytorch", "test", validData, privateKey)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	tests := []struct {
		name         string
		testManifest *manifest.AtVecManifest
		data         []byte
		publicKey    []byte
		wantErr      bool
		wantValid    bool
	}{
		{
			name:         "nil manifest",
			testManifest: nil,
			data:         validData,
			publicKey:    publicKey,
			wantErr:      true,
			wantValid:    false,
		},
		{
			name:         "nil data",
			testManifest: validManifest,
			data:         nil,
			publicKey:    publicKey,
			wantErr:      true,
			wantValid:    false,
		},
		{
			name:         "nil public key",
			testManifest: validManifest,
			data:         validData,
			publicKey:    nil,
			wantErr:      true,
			wantValid:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, err := VerifyAtVecAsset(tt.testManifest, tt.data, tt.publicKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyAtVecAsset() error = %v, wantErr %v", err, tt.wantErr)
			}
			if valid != tt.wantValid {
				t.Errorf("VerifyAtVecAsset() valid = %v, want %v", valid, tt.wantValid)
			}
		})
	}
}

// createTestEmbedding creates a test embedding with the specified number of dimensions
// Each float is a random value between -1.0 and 1.0 (normalized embedding)
func createTestEmbedding(dimensions int) []byte {
	buf := new(bytes.Buffer)
	for i := 0; i < dimensions; i++ {
		// Create deterministic test values based on index
		value := float32(math.Sin(float64(i))) // Values between -1 and 1
		binary.Write(buf, binary.LittleEndian, value)
	}
	return buf.Bytes()
}
