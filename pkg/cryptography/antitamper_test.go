// Package: pkg/cryptography
// Feature: F-001 (Security-First Foundation)
// Story: US-001-04 (Anti-Tampering Tests)
// Purpose: Comprehensive anti-tampering tests for CID and Ed25519 subsystems

package crypto

import (
	"encoding/json"
	"testing"
)

// =============================================================================
// CID Anti-Tampering: Bit-Level Attacks
// =============================================================================

// TestAntiTamper_CID_SingleBitFlip verifies that flipping a single bit at
// the first, middle, and last byte positions causes CID verification to fail.
func TestAntiTamper_CID_SingleBitFlip(t *testing.T) {
	originalData := []byte("The quick brown fox jumps over the lazy dog")

	cidStr, err := GenerateCID(originalData)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	positions := []int{0, len(originalData) / 2, len(originalData) - 1}
	for _, pos := range positions {
		tampered := make([]byte, len(originalData))
		copy(tampered, originalData)
		tampered[pos] ^= 0x01 // flip lowest bit

		valid, err := VerifyCID(cidStr, tampered)
		if err != nil {
			t.Fatalf("VerifyCID() at position %d error: %v", pos, err)
		}
		if valid {
			t.Errorf("Single bit flip at byte %d was NOT detected by CID", pos)
		}
	}
}

// TestAntiTamper_CID_AppendedByte verifies appending a byte is detected.
func TestAntiTamper_CID_AppendedByte(t *testing.T) {
	original := []byte("original payload")

	cidStr, err := GenerateCID(original)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	appended := append(append([]byte(nil), original...), 0x00)

	valid, err := VerifyCID(cidStr, appended)
	if err != nil {
		t.Fatalf("VerifyCID() error: %v", err)
	}
	if valid {
		t.Error("Appended byte was NOT detected by CID verification")
	}
}

// TestAntiTamper_CID_TruncatedByte verifies removing the last byte is detected.
func TestAntiTamper_CID_TruncatedByte(t *testing.T) {
	original := []byte("payload that will be truncated")

	cidStr, err := GenerateCID(original)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	truncated := original[:len(original)-1]

	valid, err := VerifyCID(cidStr, truncated)
	if err != nil {
		t.Fatalf("VerifyCID() error: %v", err)
	}
	if valid {
		t.Error("Truncated byte was NOT detected by CID verification")
	}
}

// TestAntiTamper_CID_PrependedByte verifies prepending a byte is detected.
func TestAntiTamper_CID_PrependedByte(t *testing.T) {
	original := []byte("original payload")

	cidStr, err := GenerateCID(original)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	prepended := append([]byte{0x00}, original...)

	valid, err := VerifyCID(cidStr, prepended)
	if err != nil {
		t.Fatalf("VerifyCID() error: %v", err)
	}
	if valid {
		t.Error("Prepended byte was NOT detected by CID verification")
	}
}

// TestAntiTamper_CID_ZeroByteInsertion verifies inserting a zero byte
// at the midpoint is detected.
func TestAntiTamper_CID_ZeroByteInsertion(t *testing.T) {
	original := []byte("abcdefghijklmnop")

	cidStr, err := GenerateCID(original)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	mid := len(original) / 2
	inserted := make([]byte, 0, len(original)+1)
	inserted = append(inserted, original[:mid]...)
	inserted = append(inserted, 0x00)
	inserted = append(inserted, original[mid:]...)

	valid, err := VerifyCID(cidStr, inserted)
	if err != nil {
		t.Fatalf("VerifyCID() error: %v", err)
	}
	if valid {
		t.Error("Zero-byte insertion was NOT detected by CID verification")
	}
}

// TestAntiTamper_CID_SameLengthReplacement verifies replacing data entirely
// with same-length content is detected.
func TestAntiTamper_CID_SameLengthReplacement(t *testing.T) {
	original := []byte("secret agent data")

	cidStr, err := GenerateCID(original)
	if err != nil {
		t.Fatalf("GenerateCID() failed: %v", err)
	}

	replacement := make([]byte, len(original))
	copy(replacement, []byte("malicious replace"))

	valid, err := VerifyCID(cidStr, replacement)
	if err != nil {
		t.Fatalf("VerifyCID() error: %v", err)
	}
	if valid {
		t.Error("Same-length content replacement was NOT detected by CID")
	}
}

// =============================================================================
// Signature Anti-Tampering: Bit-Level & Swap Attacks
// =============================================================================

// TestAntiTamper_Sig_BitFlipInSignature verifies that a single bit flip at
// the first, middle, and last byte of the signature causes rejection.
func TestAntiTamper_Sig_BitFlipInSignature(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("signed content")
	signature, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	positions := []int{0, 31, 63}
	for _, pos := range positions {
		tampered := make([]byte, len(signature))
		copy(tampered, signature)
		tampered[pos] ^= 0x01

		valid, err := VerifySignature(data, tampered, publicKey)
		if err != nil {
			t.Fatalf("VerifySignature() at pos %d error: %v", pos, err)
		}
		if valid {
			t.Errorf("Bit flip at signature byte %d was NOT detected", pos)
		}
	}
}

// TestAntiTamper_Sig_SwapBetweenMessages verifies that signature A cannot
// authenticate message B and vice versa (same keypair).
func TestAntiTamper_Sig_SwapBetweenMessages(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	msgA := []byte("message A")
	msgB := []byte("message B")

	sigA, err := SignData(msgA, privateKey)
	if err != nil {
		t.Fatalf("SignData(A) failed: %v", err)
	}
	sigB, err := SignData(msgB, privateKey)
	if err != nil {
		t.Fatalf("SignData(B) failed: %v", err)
	}

	// sigA must NOT verify msgB
	valid, err := VerifySignature(msgB, sigA, publicKey)
	if err != nil {
		t.Fatalf("VerifySignature(B,sigA) error: %v", err)
	}
	if valid {
		t.Error("Signature swap: sigA verified msgB")
	}

	// sigB must NOT verify msgA
	valid, err = VerifySignature(msgA, sigB, publicKey)
	if err != nil {
		t.Fatalf("VerifySignature(A,sigB) error: %v", err)
	}
	if valid {
		t.Error("Signature swap: sigB verified msgA")
	}
}

// TestAntiTamper_Sig_CrossKeypairReplay verifies that Alice's signature
// cannot be verified with Bob's public key (impersonation attack).
func TestAntiTamper_Sig_CrossKeypairReplay(t *testing.T) {
	_, privAlice, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Alice) failed: %v", err)
	}
	pubBob, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Bob) failed: %v", err)
	}

	data := []byte("important data from Alice")
	sigAlice, err := SignData(data, privAlice)
	if err != nil {
		t.Fatalf("SignData(Alice) failed: %v", err)
	}

	valid, err := VerifySignature(data, sigAlice, pubBob)
	if err != nil {
		t.Fatalf("VerifySignature() error: %v", err)
	}
	if valid {
		t.Error("Alice's signature verified with Bob's public key — replay attack succeeded")
	}
}

// TestAntiTamper_Sig_TruncatedSignature verifies that a truncated signature
// is rejected with an error (not silently accepted).
func TestAntiTamper_Sig_TruncatedSignature(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("data to sign")
	signature, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	truncated := signature[:32]
	_, err = VerifySignature(data, truncated, publicKey)
	if err == nil {
		t.Error("Truncated signature (32 bytes) should return error")
	}
}

// TestAntiTamper_Sig_ExtendedSignature verifies that a signature with extra
// trailing bytes is rejected with an error.
func TestAntiTamper_Sig_ExtendedSignature(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("data to sign")
	signature, err := SignData(data, privateKey)
	if err != nil {
		t.Fatalf("SignData() failed: %v", err)
	}

	extended := append(append([]byte(nil), signature...), 0x00, 0x01)
	_, err = VerifySignature(data, extended, publicKey)
	if err == nil {
		t.Error("Extended signature (66 bytes) should return error")
	}
}

// TestAntiTamper_Sig_AllZeroSignature verifies that an all-zero 64-byte
// signature does not verify.
func TestAntiTamper_Sig_AllZeroSignature(t *testing.T) {
	publicKey, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	data := []byte("data to sign")
	zeroSig := make([]byte, 64)

	valid, err := VerifySignature(data, zeroSig, publicKey)
	if err != nil {
		t.Fatalf("VerifySignature() error: %v", err)
	}
	if valid {
		t.Error("All-zero signature should NOT verify")
	}
}

// =============================================================================
// Manifest Anti-Tampering: Field-Level Attacks
// =============================================================================

// TestAntiTamper_Manifest_TamperIndividualFields verifies that changing any
// single field in a signed manifest causes signature verification to fail.
func TestAntiTamper_Manifest_TamperIndividualFields(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	original := ManifestData{
		CID:      "bafkreioriginalcid123456789abcdef",
		Filename: "document.pdf",
		Size:     4096,
		MimeType: "application/pdf",
	}

	signature, err := SignManifest(original, privateKey)
	if err != nil {
		t.Fatalf("SignManifest() failed: %v", err)
	}

	tests := []struct {
		name   string
		tamper func(ManifestData) ManifestData
	}{
		{
			name: "tamper CID",
			tamper: func(m ManifestData) ManifestData {
				m.CID = "bafkreitamperedcid987654321xyz"
				return m
			},
		},
		{
			name: "tamper filename",
			tamper: func(m ManifestData) ManifestData {
				m.Filename = "malware.exe"
				return m
			},
		},
		{
			name: "tamper size (inflate)",
			tamper: func(m ManifestData) ManifestData {
				m.Size = 999999
				return m
			},
		},
		{
			name: "tamper size (deflate)",
			tamper: func(m ManifestData) ManifestData {
				m.Size = 1
				return m
			},
		},
		{
			name: "tamper MIME type",
			tamper: func(m ManifestData) ManifestData {
				m.MimeType = "application/x-executable"
				return m
			},
		},
		{
			name: "empty CID",
			tamper: func(m ManifestData) ManifestData {
				m.CID = ""
				return m
			},
		},
		{
			name: "empty filename",
			tamper: func(m ManifestData) ManifestData {
				m.Filename = ""
				return m
			},
		},
		{
			name: "zero size",
			tamper: func(m ManifestData) ManifestData {
				m.Size = 0
				return m
			},
		},
		{
			name: "whitespace in filename",
			tamper: func(m ManifestData) ManifestData {
				m.Filename = "document.pdf "
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := tt.tamper(original)
			valid, err := VerifyManifestSignature(tampered, signature, publicKey)
			if err != nil {
				t.Fatalf("VerifyManifestSignature() error: %v", err)
			}
			if valid {
				t.Errorf("Manifest tampering (%s) was NOT detected", tt.name)
			}
		})
	}
}

// TestAntiTamper_Manifest_SignatureSwapBetweenManifests verifies that a valid
// signature from manifest A cannot authenticate manifest B.
func TestAntiTamper_Manifest_SignatureSwapBetweenManifests(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifestA := ManifestData{
		CID:      "bafkreimanifesta111",
		Filename: "legit-file.txt",
		Size:     100,
		MimeType: "text/plain",
	}
	manifestB := ManifestData{
		CID:      "bafkreimanifestb222",
		Filename: "malicious.exe",
		Size:     999999,
		MimeType: "application/x-executable",
	}

	sigA, err := SignManifest(manifestA, privateKey)
	if err != nil {
		t.Fatalf("SignManifest(A) failed: %v", err)
	}

	valid, err := VerifyManifestSignature(manifestB, sigA, publicKey)
	if err != nil {
		t.Fatalf("VerifyManifestSignature() error: %v", err)
	}
	if valid {
		t.Error("Manifest signature swap: sigA verified manifestB")
	}
}

// TestAntiTamper_Manifest_CrossKeypairAttack verifies that a manifest signed
// by Alice cannot be verified with Bob's public key.
func TestAntiTamper_Manifest_CrossKeypairAttack(t *testing.T) {
	_, privAlice, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Alice) failed: %v", err)
	}
	pubBob, _, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair(Bob) failed: %v", err)
	}

	manifest := ManifestData{
		CID:      "bafkreitest",
		Filename: "test.txt",
		Size:     42,
		MimeType: "text/plain",
	}

	sigAlice, err := SignManifest(manifest, privAlice)
	if err != nil {
		t.Fatalf("SignManifest() failed: %v", err)
	}

	valid, err := VerifyManifestSignature(manifest, sigAlice, pubBob)
	if err != nil {
		t.Fatalf("VerifyManifestSignature() error: %v", err)
	}
	if valid {
		t.Error("Alice's manifest signature verified with Bob's key — cross-keypair attack succeeded")
	}
}

// TestAntiTamper_Manifest_JSONCanonical verifies that manifest JSON
// serialization is deterministic (consistent field order).
func TestAntiTamper_Manifest_JSONCanonical(t *testing.T) {
	manifest := ManifestData{
		CID:      "bafkreitestcid",
		Filename: "test.txt",
		Size:     42,
		MimeType: "text/plain",
	}

	bytes1, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("First json.Marshal() failed: %v", err)
	}
	bytes2, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Second json.Marshal() failed: %v", err)
	}

	if string(bytes1) != string(bytes2) {
		t.Errorf("JSON is not deterministic: %s vs %s", bytes1, bytes2)
	}
}

// TestAntiTamper_Manifest_JSONInjection verifies that manually crafted JSON
// with injected fields does not match a canonical manifest signature.
func TestAntiTamper_Manifest_JSONInjection(t *testing.T) {
	publicKey, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifest := ManifestData{
		CID:      "bafkreitest",
		Filename: "test.txt",
		Size:     42,
		MimeType: "text/plain",
	}

	signature, err := SignManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("SignManifest() failed: %v", err)
	}

	// Craft JSON with an injected "admin" field
	injected := `{"cid":"bafkreitest","filename":"test.txt","size":42,"mime_type":"text/plain","admin":true}`

	valid, err := VerifySignature([]byte(injected), signature, publicKey)
	if err != nil {
		t.Fatalf("VerifySignature() error: %v", err)
	}
	if valid {
		t.Error("JSON field injection bypassed manifest signature verification")
	}
}

// TestAntiTamper_Manifest_DeterministicSigning verifies that signing the same
// manifest twice with the same key produces identical signatures.
func TestAntiTamper_Manifest_DeterministicSigning(t *testing.T) {
	_, privateKey, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair() failed: %v", err)
	}

	manifest := ManifestData{
		CID:      "bafkreitest",
		Filename: "test.txt",
		Size:     100,
		MimeType: "text/plain",
	}

	sig1, err := SignManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("First SignManifest() failed: %v", err)
	}
	sig2, err := SignManifest(manifest, privateKey)
	if err != nil {
		t.Fatalf("Second SignManifest() failed: %v", err)
	}

	if string(sig1) != string(sig2) {
		t.Error("Manifest signing is NOT deterministic — potential canonicalization issue")
	}
}
