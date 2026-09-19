// Feature: F-025 (Auto-Update System)
// Story: US-025-02 (Release Signing Infrastructure)
// Purpose: Tests for Ed25519 manifest signing, verification, validation, and public key loading
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// validSHA256 is a real SHA-256 hash (of the empty string) used in test fixtures.
const validSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// validSHA256Alt is a different valid SHA-256 hash for multi-platform test fixtures.
const validSHA256Alt = "a7ffc6f8bf1ed76651c14756a061d662f580ff4de43b49fa82d80a4b80f8434a"

// validTestManifest returns a fully valid ManifestContent for use in tests
// that require signing or verification (which now validate all fields).
func validTestManifest() ManifestContent {
	return ManifestContent{
		SchemaVersion: 1,
		Version:       "1.0.0",
		MinSupported:  "0.9.0",
		Released:      "2026-02-14",
		ReleaseNotes:  "Test release",
		Platforms: map[string]PlatformRelease{
			"darwin-arm64": {
				URL:    "https://releases.stonkagents.com/v1.0.0/darwin-arm64.tar.gz",
				SHA256: validSHA256,
				Size:   1024,
			},
		},
	}
}

// --- SignManifest tests ---

func TestSignManifest_ProducesValidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	manifest := validTestManifest()

	envelope, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	if envelope.Signed == "" {
		t.Fatal("envelope.Signed is empty")
	}
	if len(envelope.Content) == 0 {
		t.Fatal("envelope.Content is empty")
	}

	// Decode the base64 signature
	sig, err := base64.StdEncoding.DecodeString(envelope.Signed)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	// Verify signature over raw content bytes
	if !ed25519.Verify(pub, envelope.Content, sig) {
		t.Error("Ed25519 signature verification failed")
	}
}

func TestSignManifest_ContentIsDeterministic(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	manifest := validTestManifest()

	env1, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("first sign: %v", err)
	}

	// Must produce non-empty output (prevents false-pass with stubs)
	if env1.Signed == "" {
		t.Fatal("first envelope Signed is empty")
	}
	if len(env1.Content) == 0 {
		t.Fatal("first envelope Content is empty")
	}

	env2, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("second sign: %v", err)
	}

	// Content bytes must be identical (deterministic JSON serialization)
	if string(env1.Content) != string(env2.Content) {
		t.Errorf("content not deterministic:\n  first:  %s\n  second: %s", env1.Content, env2.Content)
	}

	// Signatures must be identical (Ed25519 is deterministic for same key + message)
	if env1.Signed != env2.Signed {
		t.Error("signatures differ for identical content and key")
	}
}

func TestSignManifest_ContentPreservesFields(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	manifest := ManifestContent{
		SchemaVersion: 1,
		Version:       "2.0.0",
		MinSupported:  "1.5.0",
		Released:      "2026-03-01",
		ReleaseNotes:  "Major update",
		Platforms: map[string]PlatformRelease{
			"darwin-arm64": {
				URL:    "https://releases.stonkagents.com/v2.0.0/darwin-arm64.tar.gz",
				SHA256: validSHA256,
				Size:   100,
			},
			"windows-amd64": {
				URL:    "https://releases.stonkagents.com/v2.0.0/windows-amd64.zip",
				SHA256: validSHA256Alt,
				Size:   200,
			},
		},
	}

	envelope, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	// Parse content back and verify fields
	var parsed ManifestContent
	if err := json.Unmarshal(envelope.Content, &parsed); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}

	if parsed.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", parsed.Version, "2.0.0")
	}
	if parsed.MinSupported != "1.5.0" {
		t.Errorf("MinSupported = %q, want %q", parsed.MinSupported, "1.5.0")
	}
	if parsed.Released != "2026-03-01" {
		t.Errorf("Released = %q, want %q", parsed.Released, "2026-03-01")
	}
	if len(parsed.Platforms) != 2 {
		t.Errorf("Platforms count = %d, want 2", len(parsed.Platforms))
	}
}

func TestSignManifest_InvalidKeyLength_ReturnsError(t *testing.T) {
	shortKey := ed25519.PrivateKey(make([]byte, 16)) // not 64 bytes
	manifest := validTestManifest()

	_, err := SignManifest(manifest, shortKey)
	if err == nil {
		t.Error("SignManifest should reject invalid key length, got nil error")
	}
}

func TestSignManifest_RejectsInvalidManifest(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	invalid := ManifestContent{Version: "not-semver"}

	_, err := SignManifest(invalid, priv)
	if err == nil {
		t.Error("SignManifest should reject invalid manifest, got nil error")
	}
}

// --- VerifyManifest tests ---

func TestVerifyManifest_ValidSignature_ReturnsContent(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	manifest := validTestManifest()

	envelope, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	result, err := VerifyManifest(envelope, pub)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if result == nil {
		t.Fatal("VerifyManifest returned nil result")
	}
	if result.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", result.Version, "1.0.0")
	}
}

func TestVerifyManifest_TamperedContent_RejectsSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	manifest := validTestManifest()

	envelope, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	// Tamper with content: change version in raw JSON
	tampered := make(json.RawMessage, len(envelope.Content))
	copy(tampered, envelope.Content)
	// Replace "1.0.0" with "9.9.9" in the raw bytes
	for i := range tampered {
		if i+4 < len(tampered) && string(tampered[i:i+5]) == "1.0.0" {
			copy(tampered[i:i+5], "9.9.9")
			break
		}
	}
	envelope.Content = tampered

	_, err = VerifyManifest(envelope, pub)
	if err == nil {
		t.Error("VerifyManifest should reject tampered content, got nil error")
	}
}

func TestVerifyManifest_WrongKey_RejectsSignature(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	manifest := validTestManifest()

	envelope, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	_, err = VerifyManifest(envelope, wrongPub)
	if err == nil {
		t.Error("VerifyManifest should reject wrong public key, got nil error")
	}
}

func TestVerifyManifest_EmptySignature_RejectsEnvelope(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	envelope := Envelope{
		Signed:  "",
		Content: json.RawMessage(`{"latest_version":"1.0.0"}`),
	}

	_, err := VerifyManifest(envelope, pub)
	if err == nil {
		t.Error("VerifyManifest should reject empty signature, got nil error")
	}
}

// --- ValidateManifestContent tests ---

func TestValidateManifestContent_ValidManifest(t *testing.T) {
	if err := ValidateManifestContent(validTestManifest()); err != nil {
		t.Errorf("valid manifest should pass validation: %v", err)
	}
}

func TestValidateManifestContent_RejectsInvalidVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{"empty", ""},
		{"not semver", "lol"},
		{"missing patch", "1.0"},
		{"leading v", "v1.0.0"},
		{"spaces", "1.0.0 "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validTestManifest()
			m.Version = tc.version
			if err := ValidateManifestContent(m); err == nil {
				t.Errorf("version %q should fail validation", tc.version)
			}
		})
	}
}

func TestValidateManifestContent_AcceptsPreRelease(t *testing.T) {
	m := validTestManifest()
	m.Version = "1.0.0-beta.1"
	if err := ValidateManifestContent(m); err != nil {
		t.Errorf("pre-release version should be valid: %v", err)
	}
}

func TestValidateManifestContent_RejectsZeroSchemaVersion(t *testing.T) {
	m := validTestManifest()
	m.SchemaVersion = 0
	if err := ValidateManifestContent(m); err == nil {
		t.Error("schema_version 0 should fail validation")
	}
}

func TestValidateManifestContent_RejectsEmptyPlatforms(t *testing.T) {
	m := validTestManifest()
	m.Platforms = map[string]PlatformRelease{}
	if err := ValidateManifestContent(m); err == nil {
		t.Error("empty platforms should fail validation")
	}
}

func TestValidateManifestContent_RejectsHttpURL(t *testing.T) {
	m := validTestManifest()
	m.Platforms["darwin-arm64"] = PlatformRelease{
		URL:    "http://insecure.example.com/file.tar.gz",
		SHA256: validSHA256,
		Size:   1024,
	}
	err := ValidateManifestContent(m)
	if err == nil {
		t.Error("http URL should fail validation — must be https")
	}
	if err != nil && !strings.Contains(err.Error(), "https") {
		t.Errorf("error should mention https, got: %v", err)
	}
}

func TestValidateManifestContent_RejectsInvalidSHA256(t *testing.T) {
	tests := []struct {
		name   string
		sha256 string
	}{
		{"empty", ""},
		{"too short", "abc123"},
		{"not hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"wrong length", "e3b0c44298fc1c149afbf4c8996fb924"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validTestManifest()
			m.Platforms["darwin-arm64"] = PlatformRelease{
				URL:    "https://example.com/file.tar.gz",
				SHA256: tc.sha256,
				Size:   1024,
			}
			if err := ValidateManifestContent(m); err == nil {
				t.Errorf("sha256 %q should fail validation", tc.sha256)
			}
		})
	}
}

func TestValidateManifestContent_RejectsZeroSize(t *testing.T) {
	m := validTestManifest()
	m.Platforms["darwin-arm64"] = PlatformRelease{
		URL:    "https://example.com/file.tar.gz",
		SHA256: validSHA256,
		Size:   0,
	}
	if err := ValidateManifestContent(m); err == nil {
		t.Error("zero size should fail validation")
	}
}

func TestValidateManifestContent_RejectsNegativeSize(t *testing.T) {
	m := validTestManifest()
	m.Platforms["darwin-arm64"] = PlatformRelease{
		URL:    "https://example.com/file.tar.gz",
		SHA256: validSHA256,
		Size:   -1,
	}
	if err := ValidateManifestContent(m); err == nil {
		t.Error("negative size should fail validation")
	}
}

func TestValidateManifestContent_AcceptsInstallerField(t *testing.T) {
	m := validTestManifest()
	m.Platforms["darwin-arm64"] = PlatformRelease{
		URL:    "https://example.com/file.tar.gz",
		SHA256: validSHA256,
		Size:   1024,
		Installer: &ArchiveInfo{
			URL:    "https://releases.stonkagents.com/v1.0.0/StonkAgents-1.0.0.dmg",
			SHA256: validSHA256Alt,
			Size:   2048,
		},
	}
	if err := ValidateManifestContent(m); err != nil {
		t.Errorf("manifest with installer should pass validation: %v", err)
	}
}

func TestValidateManifestContent_RejectsInvalidInstallerURL(t *testing.T) {
	m := validTestManifest()
	m.Platforms["darwin-arm64"] = PlatformRelease{
		URL:    "https://example.com/file.tar.gz",
		SHA256: validSHA256,
		Size:   1024,
		Installer: &ArchiveInfo{
			URL:    "http://insecure.example.com/installer.dmg",
			SHA256: validSHA256Alt,
			Size:   2048,
		},
	}
	err := ValidateManifestContent(m)
	if err == nil {
		t.Error("installer with http URL should fail validation")
	}
	if err != nil && !strings.Contains(err.Error(), "installer") {
		t.Errorf("error should mention installer, got: %v", err)
	}
}

func TestValidateManifestContent_RejectsInvalidInstallerSHA256(t *testing.T) {
	m := validTestManifest()
	m.Platforms["darwin-arm64"] = PlatformRelease{
		URL:    "https://example.com/file.tar.gz",
		SHA256: validSHA256,
		Size:   1024,
		Installer: &ArchiveInfo{
			URL:    "https://releases.stonkagents.com/v1.0.0/StonkAgents-1.0.0.dmg",
			SHA256: "invalid",
			Size:   2048,
		},
	}
	err := ValidateManifestContent(m)
	if err == nil {
		t.Error("installer with invalid SHA256 should fail validation")
	}
	if err != nil && !strings.Contains(err.Error(), "installer") {
		t.Errorf("error should mention installer, got: %v", err)
	}
}

// --- LoadPublicKey tests ---

func TestLoadPublicKey_ValidBase64_Returns32Bytes(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	encoded := base64.StdEncoding.EncodeToString(pub)

	loaded, err := LoadPublicKey(encoded)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}
	if len(loaded) != ed25519.PublicKeySize {
		t.Errorf("key size = %d, want %d", len(loaded), ed25519.PublicKeySize)
	}
	if !pub.Equal(loaded) {
		t.Error("loaded key does not match original")
	}
}

func TestLoadPublicKey_InvalidBase64_ReturnsError(t *testing.T) {
	_, err := LoadPublicKey("not-valid-base64!!!")
	if err == nil {
		t.Error("LoadPublicKey should reject invalid base64, got nil error")
	}
}

func TestLoadPublicKey_WrongSize_ReturnsError(t *testing.T) {
	// 16 bytes — not a valid 32-byte Ed25519 key
	shortKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := LoadPublicKey(shortKey)
	if err == nil {
		t.Error("LoadPublicKey should reject wrong-size key, got nil error")
	}
}

func TestLoadPublicKey_WhitespaceHandling(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	encoded := "  " + base64.StdEncoding.EncodeToString(pub) + "\n"

	loaded, err := LoadPublicKey(encoded)
	if err != nil {
		t.Fatalf("LoadPublicKey should handle whitespace: %v", err)
	}
	if !pub.Equal(loaded) {
		t.Error("loaded key does not match original after trimming")
	}
}

// --- Round-trip test ---

func TestSignManifest_RoundTrip_SignAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	manifest := ManifestContent{
		SchemaVersion: 1,
		Version:       "3.2.1",
		MinSupported:  "3.0.0",
		Released:      "2026-06-15",
		ReleaseNotes:  "Round-trip test",
		Platforms: map[string]PlatformRelease{
			"darwin-arm64": {
				URL:    "https://releases.stonkagents.com/v3.2.1/stonkagents-darwin-arm64.tar.gz",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:   15000000,
			},
		},
	}

	// Sign
	envelope, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	// Serialize to JSON (simulates writing to file)
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// Deserialize (simulates reading from file)
	var loaded Envelope
	if err := json.Unmarshal(envelopeJSON, &loaded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	// Verify
	result, err := VerifyManifest(loaded, pub)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if result == nil {
		t.Fatal("VerifyManifest returned nil result")
	}
	if result.Version != "3.2.1" {
		t.Errorf("Version = %q, want %q", result.Version, "3.2.1")
	}
	if result.MinSupported != "3.0.0" {
		t.Errorf("MinSupported = %q, want %q", result.MinSupported, "3.0.0")
	}
	if p, ok := result.Platforms["darwin-arm64"]; !ok {
		t.Error("missing darwin-arm64 platform")
	} else if p.Size != 15000000 {
		t.Errorf("Size = %d, want 15000000", p.Size)
	}
}
