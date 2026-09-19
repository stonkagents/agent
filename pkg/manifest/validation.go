// Package: pkg/manifest
// Feature: F-003 (Universal File Sharing)
// Story: US-003-04 (Manifest Validation Logic)
// Purpose: Validate manifest fields for structural correctness before use

package manifest

import (
	"crypto/ed25519"
	"fmt"
	"regexp"
	"time"

	"github.com/ipfs/go-cid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ed25519SignatureSize is the expected size of an Ed25519 signature in bytes.
const ed25519SignatureSize = ed25519.SignatureSize // 64 bytes

// versionPattern matches format versions like "1.0", "2.1", "10.5" (major.minor only).
var versionPattern = regexp.MustCompile(`^\d+\.\d+$`)

// ValidateAtRawManifest validates the structural integrity of an AtRawManifest.
// Checks that all required fields are present and well-formed.
//
// Validation rules:
//   - filename: non-empty
//   - mime_type: non-empty
//   - size: non-negative
//   - cid: non-empty, valid CIDv1 format
//   - signature: non-empty, exactly 64 bytes (Ed25519)
//   - created_at: non-nil, not in the future
//   - format_version: non-empty, matches "major.minor" pattern
func ValidateAtRawManifest(m *AtRawManifest) error {
	if m == nil {
		return fmt.Errorf("manifest cannot be nil")
	}

	if m.Filename == "" {
		return fmt.Errorf("filename is required")
	}

	if m.MimeType == "" {
		return fmt.Errorf("mime_type is required")
	}

	if m.Size < 0 {
		return fmt.Errorf("size must be non-negative, got %d", m.Size)
	}

	if err := validateCID(m.Cid); err != nil {
		return err
	}

	if err := validateSignature(m.Signature); err != nil {
		return err
	}

	if err := validateTimestamp(m.CreatedAt); err != nil {
		return err
	}

	if err := validateFormatVersion(m.FormatVersion); err != nil {
		return err
	}

	return nil
}

// ValidateAtVecManifest validates the structural integrity of an AtVecManifest.
// Checks that all required fields are present and well-formed.
//
// Validation rules:
//   - dimensions: positive integer
//   - model: non-empty
//   - framework: non-empty
//   - cid: non-empty, valid CIDv1 format
//   - signature: non-empty, exactly 64 bytes (Ed25519)
//   - created_at: non-nil, not in the future
//   - format_version: non-empty, matches "major.minor" pattern
//   - description: optional (not validated)
func ValidateAtVecManifest(m *AtVecManifest) error {
	if m == nil {
		return fmt.Errorf("manifest cannot be nil")
	}

	if m.Dimensions <= 0 {
		return fmt.Errorf("dimensions must be positive, got %d", m.Dimensions)
	}

	if m.Model == "" {
		return fmt.Errorf("model is required")
	}

	if m.Framework == "" {
		return fmt.Errorf("framework is required")
	}

	if err := validateCID(m.Cid); err != nil {
		return err
	}

	if err := validateSignature(m.Signature); err != nil {
		return err
	}

	if err := validateTimestamp(m.CreatedAt); err != nil {
		return err
	}

	if err := validateFormatVersion(m.FormatVersion); err != nil {
		return err
	}

	return nil
}

// validateCID checks that a CID string is non-empty and parseable as a valid CID.
func validateCID(cidStr string) error {
	if cidStr == "" {
		return fmt.Errorf("cid is required")
	}

	_, err := cid.Decode(cidStr)
	if err != nil {
		return fmt.Errorf("cid has invalid format: %w", err)
	}

	return nil
}

// validateSignature checks that a signature is present and has the correct Ed25519 size.
func validateSignature(sig []byte) error {
	if len(sig) == 0 {
		return fmt.Errorf("signature is required")
	}

	if len(sig) != ed25519SignatureSize {
		return fmt.Errorf("signature must be %d bytes (Ed25519), got %d", ed25519SignatureSize, len(sig))
	}

	return nil
}

// validateTimestamp checks that a timestamp is present and not in the future.
func validateTimestamp(ts *timestamppb.Timestamp) error {
	if ts == nil {
		return fmt.Errorf("created_at is required")
	}

	if ts.AsTime().After(time.Now()) {
		return fmt.Errorf("created_at must not be in the future")
	}

	return nil
}

// validateFormatVersion checks that the version string matches "major.minor" pattern.
func validateFormatVersion(version string) error {
	if version == "" {
		return fmt.Errorf("format_version is required")
	}

	if !versionPattern.MatchString(version) {
		return fmt.Errorf("format_version must match 'major.minor' pattern (e.g. '1.0'), got %q", version)
	}

	return nil
}
