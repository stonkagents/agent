// Package: pkg/assets
// Feature: F-003 (Universal File Sharing)
// Story: US-003-02 (.at-vec Asset Type Support)
// Purpose: Create and verify .at-vec assets with embedding dimension detection

package assets

import (
	"fmt"
	"time"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/manifest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DetectEmbeddingDimensions detects the number of dimensions in an embedding file.
// Assumes float32 format (4 bytes per dimension).
//
// Returns the number of dimensions or an error if the file size is invalid.
func DetectEmbeddingDimensions(data []byte) (int32, error) {
	if data == nil {
		return 0, fmt.Errorf("data cannot be nil")
	}

	dataSize := len(data)
	if dataSize%4 != 0 {
		return 0, fmt.Errorf("invalid embedding file size: %d bytes is not divisible by 4 (float32 size)", dataSize)
	}

	dimensions := int32(dataSize / 4)
	return dimensions, nil
}

// CreateAtVecAsset creates a .at-vec asset from embedding data.
// Automatically detects dimensions, generates CID, creates manifest, and signs it.
//
// Returns the manifest or an error if creation fails.
func CreateAtVecAsset(model, framework, description string, data []byte, privateKey []byte) (*manifest.AtVecManifest, error) {
	if model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}
	if framework == "" {
		return nil, fmt.Errorf("framework cannot be empty")
	}
	if data == nil {
		return nil, fmt.Errorf("data cannot be nil")
	}
	if privateKey == nil {
		return nil, fmt.Errorf("privateKey cannot be nil")
	}

	// Detect dimensions from embedding data
	dimensions, err := DetectEmbeddingDimensions(data)
	if err != nil {
		return nil, fmt.Errorf("failed to detect dimensions: %w", err)
	}

	// Generate CID from embedding content
	cid, err := crypto.GenerateCID(data)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CID: %w", err)
	}

	// Create AtVecManifest (signature will be added after signing)
	atVecManifest := &manifest.AtVecManifest{
		Dimensions:    dimensions,
		Model:         model,
		Framework:     framework,
		FormatVersion: "1.0",
		Cid:           cid,
		CreatedAt:     timestamppb.New(time.Now()),
		Description:   description,
	}

	// Sign manifest using deterministic protobuf marshaling
	signature, err := crypto.SignAtVecManifest(atVecManifest, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign manifest: %w", err)
	}

	atVecManifest.Signature = signature

	return atVecManifest, nil
}

// VerifyAtVecAsset verifies the integrity of a .at-vec asset.
// Checks that:
// 1. Dimensions match the actual embedding data
// 2. CID matches the actual file content
// 3. Signature is valid for the manifest metadata
//
// Returns true if the asset is valid, false otherwise.
func VerifyAtVecAsset(atVecManifest *manifest.AtVecManifest, data []byte, publicKey []byte) (bool, error) {
	if atVecManifest == nil {
		return false, fmt.Errorf("manifest cannot be nil")
	}
	if data == nil {
		return false, fmt.Errorf("data cannot be nil")
	}
	if publicKey == nil {
		return false, fmt.Errorf("publicKey cannot be nil")
	}

	// Verify dimensions match actual data
	actualDimensions, err := DetectEmbeddingDimensions(data)
	if err != nil {
		return false, fmt.Errorf("failed to detect dimensions for verification: %w", err)
	}

	if actualDimensions != atVecManifest.Dimensions {
		return false, nil // Dimension mismatch indicates tampering
	}

	// Verify CID matches content
	actualCID, err := crypto.GenerateCID(data)
	if err != nil {
		return false, fmt.Errorf("failed to generate CID for verification: %w", err)
	}

	if actualCID != atVecManifest.Cid {
		return false, nil // CID mismatch indicates tampering
	}

	// Verify signature using deterministic protobuf marshaling
	valid, err := crypto.VerifyAtVecManifestSignature(atVecManifest, publicKey)
	if err != nil {
		return false, fmt.Errorf("failed to verify signature: %w", err)
	}

	return valid, nil
}
