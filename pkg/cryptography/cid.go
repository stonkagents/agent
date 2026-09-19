// Package: pkg/cryptography
// Feature: F-001 (Content Addressing & Signatures)
// Story: US-001-01 (CIDv1 Content Addressing)
// Purpose: Implement IPFS-compatible CIDv1 content addressing

package crypto

import (
	"fmt"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// GenerateCID creates a CIDv1 hash from the given data using SHA-256.
// Returns a base32-encoded CID string compatible with IPFS.
//
// The CID structure follows: <multibase><multicodec><multihash>
// - Multibase: base32 (default for CIDv1)
// - Multicodec: raw (0x55) for raw binary data
// - Multihash: sha2-256 (0x12) with hash digest
//
// Example output: "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
func GenerateCID(data []byte) (string, error) {
	// Create SHA-256 multihash from data
	mh, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		return "", fmt.Errorf("failed to create multihash: %w", err)
	}

	// Create CIDv1 with raw codec (0x55)
	c := cid.NewCidV1(cid.Raw, mh)

	// Return base32-encoded string (default for CIDv1)
	return c.String(), nil
}

// VerifyCID verifies that the given CID matches the provided data.
// Returns true if the CID is valid and matches the data content.
//
// This function:
// 1. Decodes the CID string
// 2. Generates a new CID from the data
// 3. Compares the two CIDs
//
// Returns false if the CID doesn't match (content has been modified).
func VerifyCID(cidString string, data []byte) (bool, error) {
	// Parse the provided CID string
	providedCID, err := cid.Decode(cidString)
	if err != nil {
		return false, fmt.Errorf("invalid CID format: %w", err)
	}

	// Generate CID from the actual data
	expectedCIDString, err := GenerateCID(data)
	if err != nil {
		return false, fmt.Errorf("failed to generate CID for verification: %w", err)
	}

	expectedCID, err := cid.Decode(expectedCIDString)
	if err != nil {
		return false, fmt.Errorf("failed to decode generated CID: %w", err)
	}

	// Compare the CIDs
	return providedCID.Equals(expectedCID), nil
}

// CIDToBytes converts a CID string to its binary representation.
// This is useful for storing CIDs in binary formats or databases.
func CIDToBytes(cidString string) ([]byte, error) {
	c, err := cid.Decode(cidString)
	if err != nil {
		return nil, fmt.Errorf("invalid CID format: %w", err)
	}

	return c.Bytes(), nil
}

// CIDFromBytes converts binary CID bytes back to a string representation.
// This is the inverse of CIDToBytes.
func CIDFromBytes(cidBytes []byte) (string, error) {
	c, err := cid.Cast(cidBytes)
	if err != nil {
		return "", fmt.Errorf("invalid CID bytes: %w", err)
	}

	return c.String(), nil
}
