// Package update provides shared auto-update types and functions used by both
// the sign-manifest CLI (signer) and the controller (verifier).
//
// Feature: F-025 (Auto-Update System)
// Story: US-025-02 (Release Signing Infrastructure)
// Purpose: Manifest envelope types, Ed25519 signing, verification, and public key loading
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Envelope is the top-level manifest JSON. Field names MUST be "signed" + "content".
// The signer writes this; the verifier reads this.
type Envelope struct {
	Signed  string          `json:"signed"`  // base64-encoded Ed25519 signature
	Content json.RawMessage `json:"content"` // raw JSON bytes (signed over these exact bytes)
}

// ManifestContent holds the parsed manifest payload inside the envelope.
type ManifestContent struct {
	SchemaVersion int                        `json:"schema_version"`
	Version       string                     `json:"latest_version"`
	MinSupported  string                     `json:"min_supported"`
	Released      string                     `json:"released"`
	ReleaseNotes  string                     `json:"release_notes"`
	Platforms     map[string]PlatformRelease `json:"platforms"`
}

// ArchiveInfo describes a single archive (update or installer) with URL, SHA256, and size.
type ArchiveInfo struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// PlatformRelease describes a single platform's archive in the manifest.
// The URL/SHA256/Size fields are for the update archive (tar.gz) - kept for backward compatibility.
// The Installer field is optional and contains the installer (Setup EXE/DMG) information.
type PlatformRelease struct {
	// Update archive (tar.gz) - existing fields, kept for backward compat
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`

	// Installer (Setup EXE/DMG) - new optional field
	Installer *ArchiveInfo `json:"installer,omitempty"`
}

// semverRegex matches semantic versions: MAJOR.MINOR.PATCH with optional pre-release/build.
var semverRegex = regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9.]+)?(\+[a-zA-Z0-9.]+)?$`)

// ValidateManifestContent checks that a manifest has valid, well-formed fields.
// Called by both SignManifest and VerifyManifest to ensure only correct data is signed or trusted.
func ValidateManifestContent(c ManifestContent) error {
	if c.SchemaVersion < 1 {
		return fmt.Errorf("schema_version must be >= 1, got %d", c.SchemaVersion)
	}
	if c.Version == "" {
		return fmt.Errorf("latest_version is required")
	}
	if !semverRegex.MatchString(c.Version) {
		return fmt.Errorf("latest_version %q is not valid semver", c.Version)
	}
	if c.MinSupported == "" {
		return fmt.Errorf("min_supported is required")
	}
	if !semverRegex.MatchString(c.MinSupported) {
		return fmt.Errorf("min_supported %q is not valid semver", c.MinSupported)
	}
	if c.Released == "" {
		return fmt.Errorf("released is required")
	}
	if len(c.Platforms) == 0 {
		return fmt.Errorf("at least one platform is required")
	}
	for platform, rel := range c.Platforms {
		if err := validatePlatformRelease(platform, rel); err != nil {
			return err
		}
	}
	return nil
}

func validatePlatformRelease(platform string, r PlatformRelease) error {
	// Validate update archive (required fields)
	if r.URL == "" {
		return fmt.Errorf("platform %q: url is required", platform)
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return fmt.Errorf("platform %q: invalid url: %w", platform, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("platform %q: url must use https, got %q", platform, u.Scheme)
	}
	if r.SHA256 == "" {
		return fmt.Errorf("platform %q: sha256 is required", platform)
	}
	decoded, err := hex.DecodeString(r.SHA256)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("platform %q: sha256 must be a 64-character hex string", platform)
	}
	if r.Size <= 0 {
		return fmt.Errorf("platform %q: size must be positive, got %d", platform, r.Size)
	}

	// Validate installer if present (optional field)
	if r.Installer != nil {
		if err := validateArchiveInfo(platform, *r.Installer, "installer"); err != nil {
			return err
		}
	}

	return nil
}

func validateArchiveInfo(platform string, info ArchiveInfo, fieldName string) error {
	if info.URL == "" {
		return fmt.Errorf("platform %q: %s url is required", platform, fieldName)
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return fmt.Errorf("platform %q: %s invalid url: %w", platform, fieldName, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("platform %q: %s url must use https, got %q", platform, fieldName, u.Scheme)
	}
	if info.SHA256 == "" {
		return fmt.Errorf("platform %q: %s sha256 is required", platform, fieldName)
	}
	decoded, err := hex.DecodeString(info.SHA256)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("platform %q: %s sha256 must be a 64-character hex string", platform, fieldName)
	}
	if info.Size <= 0 {
		return fmt.Errorf("platform %q: %s size must be positive, got %d", platform, fieldName, info.Size)
	}
	return nil
}

// SignManifest signs the given manifest content with the provided Ed25519 private key.
// The signature is computed over the raw JSON bytes of content (json.RawMessage),
// ensuring byte-identical verification without re-serialization.
// Validates the manifest content before signing — refuses to sign invalid data.
func SignManifest(content ManifestContent, privateKey ed25519.PrivateKey) (Envelope, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Envelope{}, fmt.Errorf("invalid private key: expected %d bytes, got %d", ed25519.PrivateKeySize, len(privateKey))
	}

	if err := ValidateManifestContent(content); err != nil {
		return Envelope{}, fmt.Errorf("invalid manifest: %w", err)
	}

	contentBytes, err := json.Marshal(content)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal manifest content: %w", err)
	}

	sig := ed25519.Sign(privateKey, contentBytes)

	return Envelope{
		Signed:  base64.StdEncoding.EncodeToString(sig),
		Content: json.RawMessage(contentBytes),
	}, nil
}

// VerifyManifest verifies the envelope's Ed25519 signature using the given public key.
// Returns the parsed ManifestContent if valid, or an error if the signature is invalid
// or the content cannot be parsed.
func VerifyManifest(envelope Envelope, publicKey ed25519.PublicKey) (*ManifestContent, error) {
	if envelope.Signed == "" {
		return nil, fmt.Errorf("envelope has empty signature")
	}

	sig, err := base64.StdEncoding.DecodeString(envelope.Signed)
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	if !ed25519.Verify(publicKey, envelope.Content, sig) {
		return nil, fmt.Errorf("Ed25519 signature verification failed")
	}

	var content ManifestContent
	if err := json.Unmarshal(envelope.Content, &content); err != nil {
		return nil, fmt.Errorf("unmarshal manifest content: %w", err)
	}

	if err := ValidateManifestContent(content); err != nil {
		return nil, fmt.Errorf("invalid manifest content: %w", err)
	}

	return &content, nil
}

// LoadPublicKey decodes a base64-encoded raw 32-byte Ed25519 public key.
// Whitespace is trimmed before decoding.
func LoadPublicKey(base64Key string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Key))
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
