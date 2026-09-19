// Command sign-manifest tests for keypair generation, signing, and round-trip verification.
//
// Feature: F-025 (Auto-Update System)
// Story: US-025-02 (Release Signing Infrastructure)
// Purpose: Verify CLI functions produce valid Ed25519 keypairs and signed manifest envelopes
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stonkagents/agent/internal/update"
)

// validSHA256 is a real SHA-256 hash used in test fixtures.
const validSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// validSHA256Alt is a different valid SHA-256 hash for multi-platform tests.
const validSHA256Alt = "a7ffc6f8bf1ed76651c14756a061d662f580ff4de43b49fa82d80a4b80f8434a"

// --- loadPrivateKey tests ---

func TestLoadPrivateKey_ValidBase64Seed(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()
	b64 := base64.StdEncoding.EncodeToString(seed)

	key, err := loadPrivateKey(b64)
	if err != nil {
		t.Fatalf("loadPrivateKey: %v", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		t.Errorf("key length = %d, want %d", len(key), ed25519.PrivateKeySize)
	}
}

func TestLoadPrivateKey_WhitespaceHandling(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	seed := priv.Seed()
	b64 := "  \n" + base64.StdEncoding.EncodeToString(seed) + "\n  "

	key, err := loadPrivateKey(b64)
	if err != nil {
		t.Fatalf("loadPrivateKey with whitespace: %v", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		t.Errorf("key length = %d, want %d", len(key), ed25519.PrivateKeySize)
	}
}

func TestLoadPrivateKey_InvalidBase64_ReturnsError(t *testing.T) {
	_, err := loadPrivateKey("not-valid-base64!!!")
	if err == nil {
		t.Error("loadPrivateKey should reject invalid base64, got nil error")
	}
}

func TestLoadPrivateKey_WrongSeedSize_ReturnsError(t *testing.T) {
	shortSeed := make([]byte, 16) // 16 bytes, not 32
	b64 := base64.StdEncoding.EncodeToString(shortSeed)

	_, err := loadPrivateKey(b64)
	if err == nil {
		t.Error("loadPrivateKey should reject wrong seed size, got nil error")
	}
}

// --- runGenerateKey tests ---

func TestRunGenerateKey_CreatesValidKeypair(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "test-release-key")
	pubPath := filepath.Join(dir, "test-release-key.pub")

	if err := runGenerateKey(privPath, pubPath); err != nil {
		t.Fatalf("runGenerateKey: %v", err)
	}

	// Both files must exist
	privData, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	pubData, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}

	// Private key: base64-encoded 32-byte seed
	privKey, err := loadPrivateKey(string(privData))
	if err != nil {
		t.Fatalf("loadPrivateKey from generated file: %v", err)
	}
	if len(privKey) != ed25519.PrivateKeySize {
		t.Errorf("private key length = %d, want %d", len(privKey), ed25519.PrivateKeySize)
	}

	// Public key: base64-encoded 32-byte key, loadable by update.LoadPublicKey
	pubKey, err := update.LoadPublicKey(string(pubData))
	if err != nil {
		t.Fatalf("update.LoadPublicKey from generated file: %v", err)
	}
	if len(pubKey) != ed25519.PublicKeySize {
		t.Errorf("public key length = %d, want %d", len(pubKey), ed25519.PublicKeySize)
	}

	// Keypair must be a valid pair: sign with priv, verify with pub
	msg := []byte("test message")
	sig := ed25519.Sign(privKey, msg)
	if !ed25519.Verify(pubKey, msg, sig) {
		t.Error("generated keypair failed sign/verify round-trip")
	}
}

func TestRunGenerateKey_PrivateKeyHas0600Perms(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "test-release-key")
	pubPath := filepath.Join(dir, "test-release-key.pub")

	if err := runGenerateKey(privPath, pubPath); err != nil {
		t.Fatalf("runGenerateKey: %v", err)
	}

	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("private key permissions = %o, want 0600", perm)
	}
}

func TestRunGenerateKey_OverwriteEnforcesPerms(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "test-release-key")
	pubPath := filepath.Join(dir, "test-release-key.pub")

	// Pre-create with insecure permissions
	if err := os.WriteFile(privPath, []byte("old"), 0644); err != nil {
		t.Fatalf("pre-create: %v", err)
	}

	if err := runGenerateKey(privPath, pubPath); err != nil {
		t.Fatalf("runGenerateKey: %v", err)
	}

	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("overwritten private key permissions = %o, want 0600", perm)
	}
}

func TestRunGenerateKey_MissingPaths_ReturnsError(t *testing.T) {
	if err := runGenerateKey("", "pub.key"); err == nil {
		t.Error("expected error with empty privPath")
	}
	if err := runGenerateKey("priv.key", ""); err == nil {
		t.Error("expected error with empty pubPath")
	}
}

// --- runSign tests ---

func TestRunSign_ProducesValidEnvelope(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "test-release-key")
	pubPath := filepath.Join(dir, "test-release-key.pub")
	inputPath := filepath.Join(dir, "manifest.json")
	outputPath := filepath.Join(dir, "signed-manifest.json")

	// Generate keypair
	if err := runGenerateKey(privPath, pubPath); err != nil {
		t.Fatalf("runGenerateKey: %v", err)
	}

	// Write input manifest with valid fields
	manifest := update.ManifestContent{
		SchemaVersion: 1,
		Version:       "2.0.0",
		MinSupported:  "1.5.0",
		Released:      "2026-03-01",
		ReleaseNotes:  "Test release",
		Platforms: map[string]update.PlatformRelease{
			"darwin-arm64": {
				URL:    "https://releases.stonkagents.com/v2.0.0/darwin-arm64.tar.gz",
				SHA256: validSHA256,
				Size:   1024,
			},
		},
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(inputPath, manifestBytes, 0644); err != nil {
		t.Fatalf("write input manifest: %v", err)
	}

	// Sign
	if err := runSign(privPath, inputPath, outputPath); err != nil {
		t.Fatalf("runSign: %v", err)
	}

	// Read output
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	// Parse as envelope
	var envelope update.Envelope
	if err := json.Unmarshal(outputData, &envelope); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if envelope.Signed == "" {
		t.Error("envelope Signed is empty")
	}
	if len(envelope.Content) == 0 {
		t.Error("envelope Content is empty")
	}

	// Verify with public key using update.VerifyManifest
	pubData, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	pubKey, err := update.LoadPublicKey(string(pubData))
	if err != nil {
		t.Fatalf("load public key: %v", err)
	}
	result, err := update.VerifyManifest(envelope, pubKey)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if result.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "2.0.0")
	}
	if result.MinSupported != "1.5.0" {
		t.Errorf("MinSupported = %q, want %q", result.MinSupported, "1.5.0")
	}
	if result.ReleaseNotes != "Test release" {
		t.Errorf("ReleaseNotes = %q, want %q", result.ReleaseNotes, "Test release")
	}
}

func TestRunSign_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "test-release-key")
	pubPath := filepath.Join(dir, "test-release-key.pub")
	inputPath := filepath.Join(dir, "manifest.json")
	outputPath := filepath.Join(dir, "signed-manifest.json")

	if err := runGenerateKey(privPath, pubPath); err != nil {
		t.Fatalf("runGenerateKey: %v", err)
	}

	// Write manifest JSON with an unknown field
	badJSON := `{"schema_version":1,"latest_version":"1.0.0","min_supported":"0.9.0","released":"2026-01-01","release_notes":"test","platforms":{"darwin-arm64":{"url":"https://example.com/f.tar.gz","sha256":"` + validSHA256 + `","size":1024}},"unknown_field":"surprise"}`
	if err := os.WriteFile(inputPath, []byte(badJSON), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := runSign(privPath, inputPath, outputPath)
	if err == nil {
		t.Error("runSign should reject manifest with unknown fields")
	}
}

func TestRunSign_MissingPaths_ReturnsError(t *testing.T) {
	if err := runSign("", "in.json", "out.json"); err == nil {
		t.Error("expected error with empty privPath")
	}
	if err := runSign("priv.key", "", "out.json"); err == nil {
		t.Error("expected error with empty inputPath")
	}
	if err := runSign("priv.key", "in.json", ""); err == nil {
		t.Error("expected error with empty outputPath")
	}
}

// --- End-to-end round-trip ---

func TestEndToEnd_GenerateSignVerify(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "test-release-key")
	pubPath := filepath.Join(dir, "test-release-key.pub")
	inputPath := filepath.Join(dir, "manifest.json")
	outputPath := filepath.Join(dir, "signed-manifest.json")

	// Step 1: Generate keypair
	if err := runGenerateKey(privPath, pubPath); err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	// Step 2: Create input manifest with all fields
	manifest := update.ManifestContent{
		SchemaVersion: 1,
		Version:       "3.0.0",
		MinSupported:  "2.5.0",
		Released:      "2026-06-15",
		ReleaseNotes:  "Major update with auto-update support",
		Platforms: map[string]update.PlatformRelease{
			"darwin-arm64": {
				URL:    "https://releases.stonkagents.com/v3.0.0/darwin-arm64.tar.gz",
				SHA256: validSHA256,
				Size:   2048000,
			},
			"windows-amd64": {
				URL:    "https://releases.stonkagents.com/v3.0.0/windows-amd64.zip",
				SHA256: validSHA256Alt,
				Size:   3072000,
			},
		},
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(inputPath, manifestBytes, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Step 3: Sign the manifest
	if err := runSign(privPath, inputPath, outputPath); err != nil {
		t.Fatalf("sign manifest: %v", err)
	}

	// Step 4: Load signed envelope
	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read signed: %v", err)
	}
	var envelope update.Envelope
	if err := json.Unmarshal(outputData, &envelope); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}

	// Step 5: Load public key and verify
	pubData, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("read pubkey: %v", err)
	}
	pubKey, err := update.LoadPublicKey(string(pubData))
	if err != nil {
		t.Fatalf("load pubkey: %v", err)
	}
	result, err := update.VerifyManifest(envelope, pubKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Step 6: Verify all fields survived the round-trip
	if result.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", result.SchemaVersion)
	}
	if result.Version != "3.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "3.0.0")
	}
	if result.MinSupported != "2.5.0" {
		t.Errorf("MinSupported = %q, want %q", result.MinSupported, "2.5.0")
	}
	if result.Released != "2026-06-15" {
		t.Errorf("Released = %q, want %q", result.Released, "2026-06-15")
	}
	if result.ReleaseNotes != "Major update with auto-update support" {
		t.Errorf("ReleaseNotes = %q", result.ReleaseNotes)
	}
	if len(result.Platforms) != 2 {
		t.Errorf("Platforms count = %d, want 2", len(result.Platforms))
	}
	if p, ok := result.Platforms["darwin-arm64"]; !ok {
		t.Error("missing darwin-arm64 platform")
	} else if p.Size != 2048000 {
		t.Errorf("darwin-arm64 Size = %d, want 2048000", p.Size)
	}
	if p, ok := result.Platforms["windows-amd64"]; !ok {
		t.Error("missing windows-amd64 platform")
	} else if p.SHA256 != validSHA256Alt {
		t.Errorf("windows-amd64 SHA256 = %q, want %q", p.SHA256, validSHA256Alt)
	}
}
