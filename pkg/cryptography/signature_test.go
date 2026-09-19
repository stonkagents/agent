// Package: pkg/cryptography
// Feature: F-001 (Content Addressing & Signatures)
// Story: US-001-02 (Ed25519 Signature Verification)
// Purpose: Test suite for Ed25519 digital signature operations

package crypto

import (
	"encoding/base64"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/stonkagents/agent/pkg/manifest"
)

// TestGenerateKeypair verifies Ed25519 keypair generation
// Acceptance Criteria: Ed25519 keypair generated during `at init` command
func TestGenerateKeypair(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	// Ed25519 public keys are 32 bytes
	if len(publicKey) != 32 {
		t.Errorf("Public key length = %d, want 32 bytes", len(publicKey))
	}

	// Ed25519 private keys are 64 bytes (includes public key)
	if len(privateKey) != 64 {
		t.Errorf("Private key length = %d, want 64 bytes", len(privateKey))
	}
}

// TestGenerateKeypair_Uniqueness verifies each keypair generation is unique
func TestGenerateKeypair_Uniqueness(t *testing.T) {
	pub1, priv1, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("First GenerateKeypair() failed: %v", err)
	}

	pub2, priv2, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("Second GenerateKeypair() failed: %v", err)
	}

	// Public keys should be different
	if string(pub1) == string(pub2) {
		t.Error("Generated same public key twice - keypairs must be unique")
	}

	// Private keys should be different
	if string(priv1) == string(priv2) {
		t.Error("Generated same private key twice - keypairs must be unique")
	}
}

// TestSignData verifies signing data with Ed25519 private key
// Acceptance Criteria: Asset uploads signed with private key (signature included in manifest)
func TestSignData(t *testing.T) {
	// Generate test keypair
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("test data to sign")

	signature, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	// Ed25519 signatures are 64 bytes
	if len(signature) != 64 {
		t.Errorf("Signature length = %d, want 64 bytes", len(signature))
	}
}

// TestSignData_Deterministic verifies same data+key produces same signature
func TestSignData_Deterministic(t *testing.T) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("deterministic test")

	sig1, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("First SignData() failed: %v", err)
	}

	sig2, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("Second SignData() failed: %v", err)
	}

	if string(sig1) != string(sig2) {
		t.Error("Same data and key produced different signatures - should be deterministic")
	}
}

// TestVerifySignature_ValidSignature verifies valid signature verification
// Acceptance Criteria: Asset downloads verify signature with public key
func TestVerifySignature_ValidSignature(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("data to verify")

	signature, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	valid, err := VerifySignature(data, signature, publicKey)
	if err != nil {
		t.Fatalf("VerifySignature() failed: %v", err)
	}

	if !valid {
		t.Error("VerifySignature() returned false for valid signature")
	}
}

// TestVerifySignature_InvalidSignature verifies tampered signature detection
// Acceptance Criteria: Upload with invalid signature → rejected with error "invalid Ed25519 signature"
func TestVerifySignature_InvalidSignature(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	originalData := []byte("original data")
	tamperedData := []byte("tampered data")

	// Sign original data
	signature, err := SignData(originalData, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	// Verify with tampered data
	valid, err := VerifySignature(tamperedData, signature, publicKey)
	if err != nil {
		t.Fatalf("VerifySignature() failed: %v", err)
	}

	if valid {
		t.Error("VerifySignature() returned true for tampered data - should detect tampering")
	}
}

// TestVerifySignature_WrongPublicKey verifies signature fails with wrong key
func TestVerifySignature_WrongPublicKey(t *testing.T) {
	// Generate two different keypairs
	pub1, priv1, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("First GenerateKeypair() failed: %v", err)
	}

	pub2, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("Second GenerateKeypair() failed: %v", err)
	}

	data := []byte("test data")

	// Sign with first private key
	signature, err := SignData(data, priv1)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	// Verify with first public key (should succeed)
	valid1, err := VerifySignature(data, signature, pub1)
	if err != nil {
		t.Fatalf("VerifySignature() with correct key failed: %v", err)
	}
	if !valid1 {
		t.Error("VerifySignature() failed with correct public key")
	}

	// Verify with second public key (should fail)
	valid2, err := VerifySignature(data, signature, pub2)
	if err != nil {
		t.Fatalf("VerifySignature() with wrong key failed: %v", err)
	}
	if valid2 {
		t.Error("VerifySignature() succeeded with wrong public key - should fail")
	}
}

// TestSignatureFormat_Base64Encoding verifies base64 encoding
// Acceptance Criteria: Signature format - base64-encoded, 64-byte Ed25519 signature
func TestSignatureFormat_Base64Encoding(t *testing.T) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("test data")

	signature, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	// Convert to base64 string
	base64Sig := base64.StdEncoding.EncodeToString(signature)

	// Base64 of 64 bytes should be 88 characters (64 * 4/3 = 85.33, rounded up with padding)
	expectedLen := 88
	if len(base64Sig) != expectedLen {
		t.Errorf("Base64 signature length = %d, want %d characters", len(base64Sig), expectedLen)
	}

	// Verify it's valid base64
	decoded, err := base64.StdEncoding.DecodeString(base64Sig)
	if err != nil {
		t.Errorf("Base64 decode failed: %v", err)
	}

	if len(decoded) != 64 {
		t.Errorf("Decoded signature length = %d, want 64 bytes", len(decoded))
	}
}

// TestSignManifest verifies signing CID + metadata
// Acceptance Criteria: Signature covers CID + manifest metadata (filename, size, mime_type, timestamp)
func TestSignManifest(t *testing.T) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	// Example manifest data
	manifest := ManifestData{
		CID:      "bafkreiabcd1234567890",
		Filename: "test.txt",
		Size:     1024,
		MimeType: "text/plain",
	}

	signature, err := SignManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("SignManifest() failed: %v", err)
	}

	if len(signature) != 64 {
		t.Errorf("Manifest signature length = %d, want 64 bytes", len(signature))
	}
}

// TestVerifyManifestSignature verifies manifest signature verification
func TestVerifyManifestSignature(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifest := ManifestData{
		CID:      "bafkreiabcd1234567890",
		Filename: "document.pdf",
		Size:     2048,
		MimeType: "application/pdf",
	}

	signature, err := SignManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("SignManifest() failed: %v", err)
	}

	valid, err := VerifyManifestSignature(manifest, signature, publicKey)
	if err != nil {
		t.Fatalf("VerifyManifestSignature() failed: %v", err)
	}

	if !valid {
		t.Error("VerifyManifestSignature() returned false for valid signature")
	}
}

// TestVerifyManifestSignature_TamperedMetadata verifies tampering detection
func TestVerifyManifestSignature_TamperedMetadata(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	originalManifest := ManifestData{
		CID:      "bafkreiabcd1234567890",
		Filename: "original.txt",
		Size:     1024,
		MimeType: "text/plain",
	}

	signature, err := SignManifest(originalManifest, privateKey)
	if err != nil {
		t.Fatalf("SignManifest() failed: %v", err)
	}

	// Tamper with manifest
	tamperedManifest := originalManifest
	tamperedManifest.Filename = "tampered.txt" // Changed filename

	valid, err := VerifyManifestSignature(tamperedManifest, signature, publicKey)
	if err != nil {
		t.Fatalf("VerifyManifestSignature() failed: %v", err)
	}

	if valid {
		t.Error("VerifyManifestSignature() returned true for tampered manifest - should detect tampering")
	}
}

// TestSignData_InvalidPrivateKey verifies error handling for invalid keys
func TestSignData_InvalidPrivateKey(t *testing.T) {
	tests := []struct {
		name       string
		privateKey []byte
		wantErr    bool
	}{
		{
			name:       "empty private key",
			privateKey: []byte{},
			wantErr:    true,
		},
		{
			name:       "wrong length private key",
			privateKey: make([]byte, 32), // Should be 64
			wantErr:    true,
		},
		{
			name:       "corrupted private key",
			privateKey: make([]byte, 64), // All zeros
			wantErr:    false,            // Ed25519 doesn't validate key content
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("test")
			_, err := SignData(data, tt.privateKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("SignData() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// BenchmarkSignData measures signing performance
// Requirement: Fast enough for real-time use
func BenchmarkSignData(b *testing.B) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		b.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("benchmark data for signing performance test")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := SignData(data, privateKey)
		if err != nil {
			b.Fatalf("SignData() failed: %v", err)
		}
	}
}

// BenchmarkVerifySignature measures verification performance
// Acceptance Criteria: Signature verification >1000 ops/sec
func BenchmarkVerifySignature(b *testing.B) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		b.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("benchmark data for verification performance test")
	signature, err := SignData(data, privateKey)
	if err != nil {
		b.Fatalf("SignData() failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := VerifySignature(data, signature, publicKey)
		if err != nil {
			b.Fatalf("VerifySignature() failed: %v", err)
		}
	}
}

// TestSignAtRawManifest - SECURITY TEST
// Verifies deterministic protobuf signing for AtRawManifest
func TestSignAtRawManifest(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifest := &pb.AtRawManifest{
		Filename:      "test.txt",
		MimeType:      "text/plain",
		Size:          1024,
		Cid:           "bafkreiabcd1234567890",
		Signature:     nil, // Will be filled after signing
		CreatedAt:     timestamppb.New(time.Unix(1609459200, 0)),
		FormatVersion: "1.0",
	}

	signature, err := SignAtRawManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("SignAtRawManifest() failed: %v", err)
	}

	if len(signature) != 64 {
		t.Errorf("Signature length = %d, want 64 bytes", len(signature))
	}

	// Set signature in manifest for verification
	manifest.Signature = signature

	// Verify signature
	valid, err := VerifyAtRawManifestSignature(manifest, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawManifestSignature() failed: %v", err)
	}

	if !valid {
		t.Error("VerifyAtRawManifestSignature() returned false for valid signature")
	}
}

// TestSignAtRawManifest_Deterministic - SECURITY TEST
// Verifies same manifest produces same signature (deterministic marshaling)
func TestSignAtRawManifest_Deterministic(t *testing.T) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	timestamp := time.Unix(1609459200, 0)

	manifest1 := &pb.AtRawManifest{
		Filename:      "deterministic.txt",
		MimeType:      "text/plain",
		Size:          2048,
		Cid:           "bafkreixyz9876543210",
		CreatedAt:     timestamppb.New(timestamp),
		FormatVersion: "1.0",
	}

	manifest2 := &pb.AtRawManifest{
		Filename:      "deterministic.txt",
		MimeType:      "text/plain",
		Size:          2048,
		Cid:           "bafkreixyz9876543210",
		CreatedAt:     timestamppb.New(timestamp),
		FormatVersion: "1.0",
	}

	sig1, err := SignAtRawManifest(manifest1, privateKey)
	if err != nil {
		t.Fatalf("First SignAtRawManifest() failed: %v", err)
	}

	sig2, err := SignAtRawManifest(manifest2, privateKey)
	if err != nil {
		t.Fatalf("Second SignAtRawManifest() failed: %v", err)
	}

	if string(sig1) != string(sig2) {
		t.Error("Same manifest produced different signatures - should be deterministic")
	}
}

// TestVerifyAtRawManifestSignature_TamperedData - SECURITY TEST
// Verifies tampering detection with protobuf manifests
func TestVerifyAtRawManifestSignature_TamperedData(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifest := &pb.AtRawManifest{
		Filename:      "original.txt",
		MimeType:      "text/plain",
		Size:          1024,
		Cid:           "bafkreioriginal123",
		CreatedAt:     timestamppb.New(time.Unix(1609459200, 0)),
		FormatVersion: "1.0",
	}

	signature, err := SignAtRawManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("SignAtRawManifest() failed: %v", err)
	}

	manifest.Signature = signature

	// Tamper with manifest (change filename)
	manifest.Filename = "tampered.txt"

	// Verification should fail
	valid, err := VerifyAtRawManifestSignature(manifest, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawManifestSignature() failed: %v", err)
	}

	if valid {
		t.Error("VerifyAtRawManifestSignature() returned true for tampered manifest - should detect tampering")
	}
}

// TestSignAtVecManifest - SECURITY TEST
// Verifies deterministic protobuf signing for AtVecManifest
func TestSignAtVecManifest(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifest := &pb.AtVecManifest{
		Dimensions:    384,
		Model:         "sentence-transformers/all-MiniLM-L6-v2",
		Framework:     "pytorch",
		FormatVersion: "1.0",
		Cid:           "bafkreivec123456",
		Signature:     nil,
		CreatedAt:     timestamppb.New(time.Unix(1609459200, 0)),
		Description:   "Test vector embeddings",
	}

	signature, err := SignAtVecManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("SignAtVecManifest() failed: %v", err)
	}

	if len(signature) != 64 {
		t.Errorf("Signature length = %d, want 64 bytes", len(signature))
	}

	// Set signature in manifest for verification
	manifest.Signature = signature

	// Verify signature
	valid, err := VerifyAtVecManifestSignature(manifest, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecManifestSignature() failed: %v", err)
	}

	if !valid {
		t.Error("VerifyAtVecManifestSignature() returned false for valid signature")
	}
}

// TestSignAtVecManifest_Deterministic - SECURITY TEST
// Verifies same AtVecManifest produces same signature
func TestSignAtVecManifest_Deterministic(t *testing.T) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	timestamp := time.Unix(1609459200, 0)

	manifest1 := &pb.AtVecManifest{
		Dimensions:    768,
		Model:         "bert-base-uncased",
		Framework:     "tensorflow",
		FormatVersion: "1.0",
		Cid:           "bafkreivecabc",
		CreatedAt:     timestamppb.New(timestamp),
		Description:   "BERT embeddings",
	}

	manifest2 := &pb.AtVecManifest{
		Dimensions:    768,
		Model:         "bert-base-uncased",
		Framework:     "tensorflow",
		FormatVersion: "1.0",
		Cid:           "bafkreivecabc",
		CreatedAt:     timestamppb.New(timestamp),
		Description:   "BERT embeddings",
	}

	sig1, err := SignAtVecManifest(manifest1, privateKey)
	if err != nil {
		t.Fatalf("First SignAtVecManifest() failed: %v", err)
	}

	sig2, err := SignAtVecManifest(manifest2, privateKey)
	if err != nil {
		t.Fatalf("Second SignAtVecManifest() failed: %v", err)
	}

	if string(sig1) != string(sig2) {
		t.Error("Same AtVecManifest produced different signatures - should be deterministic")
	}
}
