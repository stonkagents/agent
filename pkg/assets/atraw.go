// Package: pkg/assets
// Feature: F-003 (Universal File Sharing)
// Story: US-003-01 (.at-raw Asset Type Support)
// Purpose: Create and verify .at-raw assets with MIME type detection

package assets

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/manifest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DetectMIMEType detects the MIME type of file data using http.DetectContentType
// and falls back to file extension mapping for better accuracy with text formats.
//
// Returns the detected MIME type or an error if detection fails.
func DetectMIMEType(filename string, data []byte) (string, error) {
	if filename == "" {
		return "", fmt.Errorf("filename cannot be empty")
	}

	// Use http.DetectContentType for binary format detection (images, PDFs, etc.)
	detectedType := http.DetectContentType(data)

	// For better accuracy with text-based formats, use file extension mapping
	// when http.DetectContentType returns text/plain or application/octet-stream
	if detectedType == "text/plain; charset=utf-8" || detectedType == "application/octet-stream" {
		ext := filepath.Ext(filename)
		if mimeType, ok := extensionToMIME[ext]; ok {
			return mimeType, nil
		}
	}

	return detectedType, nil
}

// extensionToMIME maps file extensions to MIME types for better accuracy
var extensionToMIME = map[string]string{
	".md":   "text/plain; charset=utf-8", // Markdown
	".json": "application/json",
	".log":  "text/plain; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".svg":  "image/svg+xml",
}

// CreateAtRawAsset creates a .at-raw asset from file data.
// Generates CID, detects MIME type, creates manifest, and signs it with the private key.
//
// Returns the manifest or an error if creation fails.
func CreateAtRawAsset(filename string, data []byte, privateKey []byte) (*manifest.AtRawManifest, error) {
	if filename == "" {
		return nil, fmt.Errorf("filename cannot be empty")
	}
	if data == nil {
		return nil, fmt.Errorf("data cannot be nil")
	}
	if privateKey == nil {
		return nil, fmt.Errorf("privateKey cannot be nil")
	}

	// Generate CID from file content
	cid, err := crypto.GenerateCID(data)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CID: %w", err)
	}

	// Detect MIME type
	mimeType, err := DetectMIMEType(filename, data)
	if err != nil {
		return nil, fmt.Errorf("failed to detect MIME type: %w", err)
	}

	// Create AtRawManifest (signature will be added after signing)
	atRawManifest := &manifest.AtRawManifest{
		Filename:      filename,
		MimeType:      mimeType,
		Size:          int64(len(data)),
		Cid:           cid,
		CreatedAt:     timestamppb.New(time.Now()),
		FormatVersion: "1.0",
	}

	// Sign manifest using deterministic protobuf marshaling
	signature, err := crypto.SignAtRawManifest(atRawManifest, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign manifest: %w", err)
	}

	atRawManifest.Signature = signature

	return atRawManifest, nil
}

// VerifyAtRawAsset verifies the integrity of a .at-raw asset.
// Checks that:
// 1. CID matches the actual file content
// 2. Signature is valid for the manifest metadata
//
// Returns true if the asset is valid, false otherwise.
func VerifyAtRawAsset(atRawManifest *manifest.AtRawManifest, data []byte, publicKey []byte) (bool, error) {
	if atRawManifest == nil {
		return false, fmt.Errorf("manifest cannot be nil")
	}
	if data == nil {
		return false, fmt.Errorf("data cannot be nil")
	}
	if publicKey == nil {
		return false, fmt.Errorf("publicKey cannot be nil")
	}

	// Verify CID matches content
	actualCID, err := crypto.GenerateCID(data)
	if err != nil {
		return false, fmt.Errorf("failed to generate CID for verification: %w", err)
	}

	if actualCID != atRawManifest.Cid {
		return false, nil // CID mismatch indicates tampering
	}

	// Verify file size matches
	if int64(len(data)) != atRawManifest.Size {
		return false, nil // Size mismatch indicates tampering
	}

	// Verify signature using deterministic protobuf marshaling
	valid, err := crypto.VerifyAtRawManifestSignature(atRawManifest, publicKey)
	if err != nil {
		return false, fmt.Errorf("failed to verify signature: %w", err)
	}

	return valid, nil
}
