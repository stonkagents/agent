// Package: pkg/cryptography
// Feature: F-001 (Content Addressing & Signatures)
// Story: US-001-01 (CIDv1 Content Addressing)
// Purpose: Test suite for IPFS-compatible CIDv1 content addressing

package crypto

import (
	"bytes"
	"strings"
	"testing"
)

// TestGenerateCID_Basic verifies basic CID generation from byte array
// Acceptance Criteria: CIDv1 hash generated using IPFS multihash format with SHA-256
func TestGenerateCID_Basic(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{
			name:    "simple text content",
			data:    []byte("hello world"),
			wantErr: false,
		},
		{
			name:    "empty content",
			data:    []byte(""),
			wantErr: false,
		},
		{
			name:    "large binary content",
			data:    bytes.Repeat([]byte{0xFF}, 1024*1024), // 1MB
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cid, err := GenerateCID(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateCID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && cid == "" {
				t.Error("GenerateCID() returned empty CID")
			}
		})
	}
}

// TestCIDFormat_Base32Encoding verifies CID format with base32 encoding
// Acceptance Criteria: CID format validated - base32-encoded, starts with "bafy" prefix
func TestCIDFormat_Base32Encoding(t *testing.T) {
	data := []byte("test content for CID generation")
	cid, err := GenerateCID(data)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	// Verify base32 encoding with "bafy" prefix (CIDv1 raw data with sha2-256)
	if !strings.HasPrefix(cid, "bafy") && !strings.HasPrefix(cid, "bafk") {
		t.Errorf("CID does not have expected base32 prefix (bafy/bafk), got: %s", cid)
	}

	// Verify CID length is reasonable (base32 CIDv1 should be ~59 characters)
	if len(cid) < 50 || len(cid) > 70 {
		t.Errorf("CID length unexpected: got %d characters, expected 50-70", len(cid))
	}
}

// TestCIDDeterminism verifies same input produces same CID
// Acceptance Criteria: CID calculation is deterministic (same content → same CID)
func TestCIDDeterminism(t *testing.T) {
	data := []byte("deterministic test content")

	cid1, err := GenerateCID(data)
	if err != nil {
		t.Fatalf("GenerateCID() first call failed: %v", err)
	}

	cid2, err := GenerateCID(data)
	if err != nil {
		t.Fatalf("GenerateCID() second call failed: %v", err)
	}

	if cid1 != cid2 {
		t.Errorf("CID not deterministic: got %s and %s for same input", cid1, cid2)
	}
}

// TestCIDUniqueness verifies different inputs produce different CIDs
// Acceptance Criteria: Different content produces different CIDs (collision resistance)
func TestCIDUniqueness(t *testing.T) {
	data1 := []byte("content one")
	data2 := []byte("content two")

	cid1, err := GenerateCID(data1)
	if err != nil {
		t.Fatalf("GenerateCID() for data1 failed: %v", err)
	}

	cid2, err := GenerateCID(data2)
	if err != nil {
		t.Fatalf("GenerateCID() for data2 failed: %v", err)
	}

	if cid1 == cid2 {
		t.Errorf("Different content produced same CID: %s", cid1)
	}
}

// TestVerifyCID_ValidContent verifies CID verification succeeds for unmodified content
// Acceptance Criteria: CID verification performed on download (hash matches content)
func TestVerifyCID_ValidContent(t *testing.T) {
	originalData := []byte("original content for verification")

	cid, err := GenerateCID(originalData)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	// Verify with same content
	valid, err := VerifyCID(cid, originalData)
	if err != nil {
		t.Fatalf("VerifyCID() failed: %v", err)
	}

	if !valid {
		t.Error("VerifyCID() returned false for valid content")
	}
}

// TestVerifyCID_ModifiedContent verifies CID verification fails for tampered content
// Acceptance Criteria: Upload file → modify file → verify CID mismatch detected
func TestVerifyCID_ModifiedContent(t *testing.T) {
	originalData := []byte("original content")
	modifiedData := []byte("modified content")

	cid, err := GenerateCID(originalData)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	// Verify with modified content
	valid, err := VerifyCID(cid, modifiedData)
	if err != nil {
		t.Fatalf("VerifyCID() failed: %v", err)
	}

	if valid {
		t.Error("VerifyCID() returned true for tampered content - should detect mismatch")
	}
}

// TestVerifyCID_InvalidCIDFormat verifies error handling for malformed CIDs
func TestVerifyCID_InvalidCIDFormat(t *testing.T) {
	tests := []struct {
		name      string
		cid       string
		data      []byte
		wantValid bool
		wantErr   bool
	}{
		{
			name:      "empty CID",
			cid:       "",
			data:      []byte("test"),
			wantValid: false,
			wantErr:   true,
		},
		{
			name:      "malformed CID",
			cid:       "not-a-valid-cid",
			data:      []byte("test"),
			wantValid: false,
			wantErr:   true,
		},
		{
			name:      "wrong base encoding",
			cid:       "Qm1234567890", // base58 (CIDv0), not base32 (CIDv1)
			data:      []byte("test"),
			wantValid: false,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, err := VerifyCID(tt.cid, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyCID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if valid != tt.wantValid {
				t.Errorf("VerifyCID() valid = %v, want %v", valid, tt.wantValid)
			}
		})
	}
}

// TestCIDToBytes_Roundtrip verifies binary representation conversion
// Acceptance Criteria: Return both string and binary representations
func TestCIDToBytes_Roundtrip(t *testing.T) {
	data := []byte("test content for binary representation")

	// Generate CID (string representation)
	cidString, err := GenerateCID(data)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	// Convert to bytes
	cidBytes, err := CIDToBytes(cidString)
	if err != nil {
		t.Fatalf("CIDToBytes() failed: %v", err)
	}

	if len(cidBytes) == 0 {
		t.Error("CIDToBytes() returned empty byte array")
	}

	// Convert back to string
	cidStringRoundtrip, err := CIDFromBytes(cidBytes)
	if err != nil {
		t.Fatalf("CIDFromBytes() failed: %v", err)
	}

	if cidString != cidStringRoundtrip {
		t.Errorf("Roundtrip failed: original %s, got %s", cidString, cidStringRoundtrip)
	}
}

// TestCIDInteroperability_IPFSCompatibility verifies IPFS CID compatibility
// Acceptance Criteria: Support IPFS-compatible CID format
func TestCIDInteroperability_IPFSCompatibility(t *testing.T) {
	// Known test vector from IPFS spec
	// "hello world" with raw codec → starts with "bafk" (raw) or "bafy" (dag-pb)
	data := []byte("hello world")
	validPrefixes := []string{"bafy", "bafk"} // Both are valid CIDv1 base32 prefixes

	cid, err := GenerateCID(data)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	// Check if CID has a valid CIDv1 base32 prefix
	hasValidPrefix := false
	for _, prefix := range validPrefixes {
		if strings.HasPrefix(cid, prefix) {
			hasValidPrefix = true
			break
		}
	}

	if !hasValidPrefix {
		t.Errorf("CID does not have valid IPFS CIDv1 prefix: got %s, expected one of %v",
			cid[:4], validPrefixes)
	}

	// Verify the CID is decodable (would fail if not proper CIDv1 format)
	_, err = CIDToBytes(cid)
	if err != nil {
		t.Errorf("Generated CID is not valid CIDv1 format: %v", err)
	}
}

// TestCIDToBytes_InvalidInput verifies error handling for invalid CID strings
func TestCIDToBytes_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		cid     string
		wantErr bool
	}{
		{
			name:    "empty string",
			cid:     "",
			wantErr: true,
		},
		{
			name:    "invalid base32",
			cid:     "not-a-valid-cid-string",
			wantErr: true,
		},
		{
			name:    "corrupted CID",
			cid:     "bafk2bzaced!!!invalid!!!",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CIDToBytes(tt.cid)
			if (err != nil) != tt.wantErr {
				t.Errorf("CIDToBytes() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCIDFromBytes_InvalidInput verifies error handling for invalid CID bytes
func TestCIDFromBytes_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		bytes   []byte
		wantErr bool
	}{
		{
			name:    "empty bytes",
			bytes:   []byte{},
			wantErr: true,
		},
		{
			name:    "invalid CID bytes",
			bytes:   []byte{0xFF, 0xFF, 0xFF},
			wantErr: true,
		},
		{
			name:    "truncated CID",
			bytes:   []byte{0x01, 0x55}, // Version 1, raw codec, but incomplete
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CIDFromBytes(tt.bytes)
			if (err != nil) != tt.wantErr {
				t.Errorf("CIDFromBytes() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// BenchmarkGenerateCID measures CID generation performance
// Requirement: CID generation should be fast enough for real-time use
func BenchmarkGenerateCID(b *testing.B) {
	data := bytes.Repeat([]byte("benchmark test content"), 1024) // ~22KB

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := GenerateCID(data)
		if err != nil {
			b.Fatalf("GenerateCID() failed: %v", err)
		}
	}
}

// BenchmarkVerifyCID measures CID verification performance
func BenchmarkVerifyCID(b *testing.B) {
	data := bytes.Repeat([]byte("verification benchmark"), 1024)
	cid, err := GenerateCID(data)
	if err != nil {
		b.Fatalf("Setup failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := VerifyCID(cid, data)
		if err != nil {
			b.Fatalf("VerifyCID() failed: %v", err)
		}
	}
}
