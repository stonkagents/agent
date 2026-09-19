// Package: pkg/cryptography
// Feature: F-001 (Content Addressing & Signatures)
// Story: US-001-02 (Ed25519 Signature Verification)
// Purpose: Implement Ed25519 digital signatures for asset authenticity

package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	pb "github.com/stonkagents/agent/pkg/manifest"
)

// ManifestData represents the metadata that gets signed for an asset.
// This includes the CID and other manifest fields to ensure integrity.
type ManifestData struct {
	CID      string `json:"cid"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
}

// GenerateKeypair generates a new Ed25519 keypair for signing and verification.
// Returns the public key (32 bytes) and private key (64 bytes).
//
// The private key should be stored securely in the OS keychain.
// The public key can be stored in the config file.
func GenerateKeypair() (publicKey, privateKey []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate Ed25519 keypair: %w", err)
	}

	return pub, priv, nil
}

// SignData signs the given data with an Ed25519 private key.
// Returns a 64-byte signature.
//
// The signature is deterministic - the same data and key will always
// produce the same signature.
func SignData(data []byte, privateKey []byte) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: got %d bytes, want %d", len(privateKey), ed25519.PrivateKeySize)
	}

	signature := ed25519.Sign(ed25519.PrivateKey(privateKey), data)
	return signature, nil
}

// VerifySignature verifies that a signature is valid for the given data and public key.
// Returns true if the signature is valid, false otherwise.
//
// This function does not return an error for invalid signatures - it simply
// returns false. Errors are only returned for malformed inputs.
func VerifySignature(data []byte, signature []byte, publicKey []byte) (bool, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("invalid public key size: got %d bytes, want %d", len(publicKey), ed25519.PublicKeySize)
	}

	if len(signature) != ed25519.SignatureSize {
		return false, fmt.Errorf("invalid signature size: got %d bytes, want %d", len(signature), ed25519.SignatureSize)
	}

	valid := ed25519.Verify(ed25519.PublicKey(publicKey), data, signature)
	return valid, nil
}

// SignManifest signs manifest metadata (CID + filename + size + mime_type).
// This ensures that not only the file content is authenticated, but also
// the metadata about the file.
//
// DEPRECATED: Use SignAtRawManifest for deterministic protobuf signing.
// This function uses JSON which is non-deterministic and may cause signature verification failures.
func SignManifest(manifest ManifestData, privateKey []byte) ([]byte, error) {
	// Serialize manifest to JSON for canonical representation
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Sign the serialized manifest
	signature, err := SignData(manifestBytes, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign manifest: %w", err)
	}

	return signature, nil
}

// VerifyManifestSignature verifies a signature for manifest metadata.
// Returns true if the signature is valid for the given manifest and public key.
//
// DEPRECATED: Use VerifyAtRawManifestSignature for deterministic protobuf signing.
// This function uses JSON which is non-deterministic and may cause signature verification failures.
func VerifyManifestSignature(manifest ManifestData, signature []byte, publicKey []byte) (bool, error) {
	// Serialize manifest to JSON (same as signing)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return false, fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Verify the signature
	valid, err := VerifySignature(manifestBytes, signature, publicKey)
	if err != nil {
		return false, fmt.Errorf("failed to verify manifest signature: %w", err)
	}

	return valid, nil
}

// SignAtRawManifest signs an AtRawManifest using deterministic protobuf marshaling.
// SECURITY: Uses proto.MarshalOptions{Deterministic: true} to ensure same data → same signature.
//
// The signature field in the manifest is excluded from signing (set to nil before marshaling).
// The resulting signature is returned and should be stored in the manifest.Signature field.
func SignAtRawManifest(manifest *pb.AtRawManifest, privateKey []byte) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest cannot be nil")
	}

	// SECURITY: Create a copy of the manifest with signature field cleared
	// (you don't sign the signature itself)
	manifestCopy := proto.Clone(manifest).(*pb.AtRawManifest)
	manifestCopy.Signature = nil

	// SECURITY: Use deterministic marshaling to ensure same data → same bytes
	marshaler := proto.MarshalOptions{Deterministic: true}
	manifestBytes, err := marshaler.Marshal(manifestCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal manifest deterministically: %w", err)
	}

	// Sign the marshaled bytes
	signature, err := SignData(manifestBytes, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign manifest: %w", err)
	}

	return signature, nil
}

// VerifyAtRawManifestSignature verifies a signature for an AtRawManifest.
// SECURITY: Uses deterministic protobuf marshaling (same as SignAtRawManifest).
//
// The signature field in the manifest is excluded from verification (set to nil before marshaling).
// Returns true if the signature is valid, false otherwise.
func VerifyAtRawManifestSignature(manifest *pb.AtRawManifest, publicKey []byte) (bool, error) {
	if manifest == nil {
		return false, fmt.Errorf("manifest cannot be nil")
	}

	if len(manifest.Signature) == 0 {
		return false, fmt.Errorf("manifest signature is empty")
	}

	// SECURITY: Create a copy of the manifest with signature field cleared
	manifestCopy := proto.Clone(manifest).(*pb.AtRawManifest)
	manifestCopy.Signature = nil

	// SECURITY: Use deterministic marshaling (same as signing)
	marshaler := proto.MarshalOptions{Deterministic: true}
	manifestBytes, err := marshaler.Marshal(manifestCopy)
	if err != nil {
		return false, fmt.Errorf("failed to marshal manifest deterministically: %w", err)
	}

	// Verify the signature
	valid, err := VerifySignature(manifestBytes, manifest.Signature, publicKey)
	if err != nil {
		return false, fmt.Errorf("failed to verify manifest signature: %w", err)
	}

	return valid, nil
}

// SignAtVecManifest signs an AtVecManifest using deterministic protobuf marshaling.
// SECURITY: Uses proto.MarshalOptions{Deterministic: true} to ensure same data → same signature.
func SignAtVecManifest(manifest *pb.AtVecManifest, privateKey []byte) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest cannot be nil")
	}

	// SECURITY: Create a copy of the manifest with signature field cleared
	manifestCopy := proto.Clone(manifest).(*pb.AtVecManifest)
	manifestCopy.Signature = nil

	// SECURITY: Use deterministic marshaling
	marshaler := proto.MarshalOptions{Deterministic: true}
	manifestBytes, err := marshaler.Marshal(manifestCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal manifest deterministically: %w", err)
	}

	// Sign the marshaled bytes
	signature, err := SignData(manifestBytes, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign manifest: %w", err)
	}

	return signature, nil
}

// VerifyAtVecManifestSignature verifies a signature for an AtVecManifest.
// SECURITY: Uses deterministic protobuf marshaling (same as SignAtVecManifest).
func VerifyAtVecManifestSignature(manifest *pb.AtVecManifest, publicKey []byte) (bool, error) {
	if manifest == nil {
		return false, fmt.Errorf("manifest cannot be nil")
	}

	if len(manifest.Signature) == 0 {
		return false, fmt.Errorf("manifest signature is empty")
	}

	// SECURITY: Create a copy of the manifest with signature field cleared
	manifestCopy := proto.Clone(manifest).(*pb.AtVecManifest)
	manifestCopy.Signature = nil

	// SECURITY: Use deterministic marshaling
	marshaler := proto.MarshalOptions{Deterministic: true}
	manifestBytes, err := marshaler.Marshal(manifestCopy)
	if err != nil {
		return false, fmt.Errorf("failed to marshal manifest deterministically: %w", err)
	}

	// Verify the signature
	valid, err := VerifySignature(manifestBytes, manifest.Signature, publicKey)
	if err != nil {
		return false, fmt.Errorf("failed to verify manifest signature: %w", err)
	}

	return valid, nil
}
