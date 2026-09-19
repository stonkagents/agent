// Package: pkg/assets
// Feature: F-001 (Security-First Foundation)
// Story: US-001-04 (Anti-Tampering Tests)
// Purpose: End-to-end anti-tampering tests for .at-raw and .at-vec assets

package assets

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// =============================================================================
// .at-raw Anti-Tampering: Data Attacks
// =============================================================================

// TestAntiTamper_AtRaw_SingleBitFlip verifies that a single bit flip in file
// content is detected by the full asset verification chain (CID + signature).
func TestAntiTamper_AtRaw_SingleBitFlip(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := []byte("This is important document content that must remain intact")
	manifest, err := CreateAtRawAsset("document.txt", original, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	// Flip one bit at the midpoint
	tampered := make([]byte, len(original))
	copy(tampered, original)
	tampered[len(tampered)/2] ^= 0x01

	valid, err := VerifyAtRawAsset(manifest, tampered, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawAsset() error: %v", err)
	}
	if valid {
		t.Error("Single bit flip in .at-raw data was NOT detected")
	}
}

// TestAntiTamper_AtRaw_AppendedData verifies that appending extra bytes to
// file content is detected.
func TestAntiTamper_AtRaw_AppendedData(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := []byte("original content")
	manifest, err := CreateAtRawAsset("file.txt", original, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	appended := append(append([]byte(nil), original...), []byte(" extra payload")...)

	valid, err := VerifyAtRawAsset(manifest, appended, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawAsset() error: %v", err)
	}
	if valid {
		t.Error("Appended data in .at-raw was NOT detected")
	}
}

// TestAntiTamper_AtRaw_TruncatedData verifies that truncating file content
// is detected.
func TestAntiTamper_AtRaw_TruncatedData(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := []byte("original content that should not be truncated")
	manifest, err := CreateAtRawAsset("file.txt", original, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	truncated := original[:len(original)/2]

	valid, err := VerifyAtRawAsset(manifest, truncated, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawAsset() error: %v", err)
	}
	if valid {
		t.Error("Truncated .at-raw data was NOT detected")
	}
}

// TestAntiTamper_AtRaw_CompleteReplacement verifies that replacing file content
// entirely (same length) is detected.
func TestAntiTamper_AtRaw_CompleteReplacement(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := []byte("legitimate document")
	manifest, err := CreateAtRawAsset("doc.txt", original, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	replacement := make([]byte, len(original))
	copy(replacement, []byte("malicious content"))

	valid, err := VerifyAtRawAsset(manifest, replacement, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawAsset() error: %v", err)
	}
	if valid {
		t.Error("Complete content replacement in .at-raw was NOT detected")
	}
}

// =============================================================================
// .at-raw Anti-Tampering: Signature Attacks
// =============================================================================

// TestAntiTamper_AtRaw_SignatureSwapBetweenAssets verifies that a valid
// signature from asset A cannot be used to authenticate asset B.
func TestAntiTamper_AtRaw_SignatureSwapBetweenAssets(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	dataA := []byte("asset A content")
	dataB := []byte("asset B content - different")

	manifestA, err := CreateAtRawAsset("assetA.txt", dataA, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset(A) failed: %v", err)
	}

	manifestB, err := CreateAtRawAsset("assetB.txt", dataB, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset(B) failed: %v", err)
	}

	// Swap: use manifest A's signature on manifest B's data
	manifestB.Signature = manifestA.Signature

	valid, err := VerifyAtRawAsset(manifestB, dataB, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtRawAsset() error: %v", err)
	}
	if valid {
		t.Error("Signature swap between .at-raw assets was NOT detected")
	}
}

// TestAntiTamper_AtRaw_CrossKeypairImpersonation verifies that an asset
// signed by Alice cannot be verified with Bob's public key.
func TestAntiTamper_AtRaw_CrossKeypairImpersonation(t *testing.T) {
	_, privAlice, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Alice) failed: %v", err)
	}
	pubBob, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Bob) failed: %v", err)
	}

	data := []byte("Alice's secret data")
	manifest, err := CreateAtRawAsset("secret.txt", data, privAlice)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	valid, err := VerifyAtRawAsset(manifest, data, pubBob)
	if err != nil {
		t.Fatalf("VerifyAtRawAsset() error: %v", err)
	}
	if valid {
		t.Error("Alice's .at-raw asset verified with Bob's key — impersonation succeeded")
	}
}

// TestAntiTamper_AtRaw_ManifestFieldTampering verifies that changing manifest
// metadata after signing (CID stays correct but metadata is altered) is detected.
func TestAntiTamper_AtRaw_ManifestFieldTampering(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("legitimate content")
	manifest, err := CreateAtRawAsset("legit.txt", data, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	tests := []struct {
		name   string
		tamper func()
		reset  func()
	}{
		{
			name:   "tamper filename",
			tamper: func() { manifest.Filename = "evil.exe" },
			reset:  func() { manifest.Filename = "legit.txt" },
		},
		{
			name:   "tamper MIME type",
			tamper: func() { manifest.MimeType = "application/x-executable" },
			reset:  func() { manifest.MimeType = "text/plain; charset=utf-8" },
		},
		{
			name:   "inflate size",
			tamper: func() { manifest.Size = 999999 },
			reset:  func() { manifest.Size = int64(len(data)) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tamper()
			defer tt.reset()

			valid, err := VerifyAtRawAsset(manifest, data, publicKey)
			if err != nil {
				t.Fatalf("VerifyAtRawAsset() error: %v", err)
			}
			if valid {
				t.Errorf("Manifest field tampering (%s) was NOT detected in .at-raw", tt.name)
			}
		})
	}
}

// =============================================================================
// .at-vec Anti-Tampering: Embedding Attacks
// =============================================================================

// TestAntiTamper_AtVec_SingleBitFlip verifies that a single bit flip in
// embedding data is detected.
func TestAntiTamper_AtVec_SingleBitFlip(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := createAntiTamperEmbedding(512)
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "test embedding",
		original, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	tampered := make([]byte, len(original))
	copy(tampered, original)
	tampered[len(tampered)/2] ^= 0x01

	valid, err := VerifyAtVecAsset(manifest, tampered, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if valid {
		t.Error("Single bit flip in .at-vec data was NOT detected")
	}
}

// TestAntiTamper_AtVec_DimensionSwap verifies that replacing a 512-dim
// embedding with a 768-dim embedding is detected.
func TestAntiTamper_AtVec_DimensionSwap(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original512 := createAntiTamperEmbedding(512)
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "512-dim CLIP",
		original512, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Replace with a different-dimension embedding
	swapped768 := createAntiTamperEmbedding(768)

	valid, err := VerifyAtVecAsset(manifest, swapped768, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if valid {
		t.Error("Dimension swap (512→768) in .at-vec was NOT detected")
	}
}

// TestAntiTamper_AtVec_PoisonedEmbedding verifies that replacing an embedding
// with random noise of the same dimensions is detected by CID verification.
func TestAntiTamper_AtVec_PoisonedEmbedding(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := createAntiTamperEmbedding(512)
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "legit embedding",
		original, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Create a poisoned embedding (same dimensions, different values)
	poisoned := createPoisonedEmbedding(512)

	valid, err := VerifyAtVecAsset(manifest, poisoned, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if valid {
		t.Error("Poisoned embedding (random noise, same dims) was NOT detected")
	}
}

// TestAntiTamper_AtVec_SignatureSwapBetweenEmbeddings verifies that a valid
// signature from embedding A cannot authenticate embedding B.
func TestAntiTamper_AtVec_SignatureSwapBetweenEmbeddings(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	dataA := createAntiTamperEmbedding(512)
	dataB := createPoisonedEmbedding(512)

	manifestA, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "embedding A",
		dataA, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset(A) failed: %v", err)
	}

	manifestB, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "embedding B",
		dataB, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset(B) failed: %v", err)
	}

	// Swap: use A's signature on B
	manifestB.Signature = manifestA.Signature

	valid, err := VerifyAtVecAsset(manifestB, dataB, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if valid {
		t.Error("Signature swap between .at-vec assets was NOT detected")
	}
}

// TestAntiTamper_AtVec_CrossKeypairImpersonation verifies that an embedding
// signed by Alice cannot be verified with Bob's public key.
func TestAntiTamper_AtVec_CrossKeypairImpersonation(t *testing.T) {
	_, privAlice, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Alice) failed: %v", err)
	}
	pubBob, _, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Bob) failed: %v", err)
	}

	data := createAntiTamperEmbedding(512)
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "Alice's embedding",
		data, privAlice,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	valid, err := VerifyAtVecAsset(manifest, data, pubBob)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if valid {
		t.Error("Alice's .at-vec asset verified with Bob's key — impersonation succeeded")
	}
}

// TestAntiTamper_AtVec_ManifestModelTampering verifies that changing the model
// field in a signed .at-vec manifest (without re-signing) is detected.
// Note: The model field itself is not directly in the signed manifest data,
// but the filename derived from it is. This tests that the verification
// chain catches the mismatch.
func TestAntiTamper_AtVec_ManifestModelTampering(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := createAntiTamperEmbedding(512)
	manifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "CLIP embedding",
		data, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() failed: %v", err)
	}

	// Tamper the model field — the signature covers a filename derived from
	// the model name, so verification should reconstruct the expected filename
	// and find a mismatch.
	manifest.Model = "malicious/trojan-model"

	valid, err := VerifyAtVecAsset(manifest, data, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if valid {
		t.Error("Model field tampering in .at-vec manifest was NOT detected")
	}
}

// =============================================================================
// Cross-Asset-Type Anti-Tampering
// =============================================================================

// TestAntiTamper_CrossType_RawDataAsVecAsset verifies that raw file data
// cannot masquerade as a valid .at-vec embedding if it is not properly
// formatted (e.g., length not divisible by 4).
func TestAntiTamper_CrossType_RawDataAsVecAsset(t *testing.T) {
	publicKey, privateKey, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	// Create a .at-raw asset with text data (length likely not divisible by 4)
	rawData := []byte("This is plain text, not a float32 embedding array!x")
	_, err = CreateAtRawAsset("readme.md", rawData, privateKey)
	if err != nil {
		t.Fatalf("CreateAtRawAsset() failed: %v", err)
	}

	// Try to create a .at-vec asset with the same raw text data
	_, err = CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "fake embedding",
		rawData, privateKey,
	)
	// Should fail because len(rawData) % 4 != 0 (51 bytes)
	if err == nil {
		t.Error("Raw text data (non-float32-aligned) should fail .at-vec creation")
	}

	// Even if length happens to be divisible by 4, CID will differ
	paddedRaw := make([]byte, 512*4) // 512 "dimensions"
	copy(paddedRaw, rawData)

	vecManifest, err := CreateAtVecAsset(
		"openai/clip-vit-b-32", "pytorch", "padded fake",
		paddedRaw, privateKey,
	)
	if err != nil {
		t.Fatalf("CreateAtVecAsset() with padded data failed: %v", err)
	}

	// Verify succeeds with padded data (it's technically valid bytes)
	valid, err := VerifyAtVecAsset(vecManifest, paddedRaw, publicKey)
	if err != nil {
		t.Fatalf("VerifyAtVecAsset() error: %v", err)
	}
	if !valid {
		t.Error("Padded raw data should verify as valid .at-vec (bytes are bytes)")
	}

	// Verify with original non-padded data: should either error (non-float32-aligned)
	// or return false (CID mismatch). Either outcome blocks the attacker.
	valid, err = VerifyAtVecAsset(vecManifest, rawData, publicKey)
	if err == nil && valid {
		t.Error("Original non-padded data should NOT verify as .at-vec")
	}
}

// =============================================================================
// Helpers
// =============================================================================

// createAntiTamperEmbedding creates a deterministic embedding for testing.
func createAntiTamperEmbedding(dimensions int) []byte {
	buf := new(bytes.Buffer)
	for i := 0; i < dimensions; i++ {
		value := float32(math.Sin(float64(i)))
		binary.Write(buf, binary.LittleEndian, value)
	}
	return buf.Bytes()
}

// createPoisonedEmbedding creates a different embedding (random noise pattern)
// with the same number of dimensions.
func createPoisonedEmbedding(dimensions int) []byte {
	buf := new(bytes.Buffer)
	for i := 0; i < dimensions; i++ {
		// Use cosine instead of sine to guarantee different values
		value := float32(math.Cos(float64(i) * 7.3))
		binary.Write(buf, binary.LittleEndian, value)
	}
	return buf.Bytes()
}
