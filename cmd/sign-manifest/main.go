// Command sign-manifest generates Ed25519 keypairs and signs release manifests
// for the StonkAgents auto-update system.
//
// Feature: F-025 (Auto-Update System)
// Story: US-025-02 (Release Signing Infrastructure)
// Purpose: CLI tool for release pipeline — generates signing keys and signs manifest files
//
// Usage:
//
//	sign-manifest --generate-key --private-key <path> --public-key <path>
//	sign-manifest --sign --private-key <path> --input <manifest.json> --output <signed.json>
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/stonkagents/agent/internal/update"
)

func main() {
	generateKey := flag.Bool("generate-key", false, "Generate a new Ed25519 keypair")
	sign := flag.Bool("sign", false, "Sign a manifest file")
	privateKeyPath := flag.String("private-key", "", "Path to private key file")
	publicKeyPath := flag.String("public-key", "", "Path to public key file (generate-key only)")
	inputPath := flag.String("input", "", "Path to input manifest JSON (sign only)")
	outputPath := flag.String("output", "", "Path to output signed envelope JSON (sign only)")
	flag.Parse()

	if *generateKey {
		if err := runGenerateKey(*privateKeyPath, *publicKeyPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *sign {
		if err := runSign(*privateKeyPath, *inputPath, *outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	flag.Usage()
	os.Exit(1)
}

func runGenerateKey(privPath, pubPath string) error {
	if privPath == "" || pubPath == "" {
		return fmt.Errorf("--private-key and --public-key are required")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate keypair: %w", err)
	}

	// Private key: raw 32-byte seed, base64-encoded, 0600 perms.
	// WriteFile only sets mode on creation — Chmod enforces 0600 even if file pre-exists.
	privB64 := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(privPath, []byte(privB64+"\n"), 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.Chmod(privPath, 0600); err != nil {
		return fmt.Errorf("chmod private key: %w", err)
	}

	// Public key: raw 32-byte key, base64-encoded, 0644 perms
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(pubPath, []byte(pubB64+"\n"), 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Keypair generated:\n  Private: %s (0600)\n  Public:  %s (0644)\n", privPath, pubPath)
	return nil
}

func runSign(privPath, inputPath, outputPath string) error {
	if privPath == "" || inputPath == "" || outputPath == "" {
		return fmt.Errorf("--private-key, --input, and --output are required")
	}

	// Load private key
	privB64, err := os.ReadFile(privPath)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	priv, err := loadPrivateKey(string(privB64))
	if err != nil {
		return err
	}

	// Read and parse input manifest
	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read input manifest: %w", err)
	}

	var content update.ManifestContent
	dec := json.NewDecoder(bytes.NewReader(inputData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&content); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	// Sign
	envelope, err := update.SignManifest(content, priv)
	if err != nil {
		return fmt.Errorf("sign manifest: %w", err)
	}

	// Write output — MUST use json.Marshal (not MarshalIndent) to preserve
	// byte-identical content bytes that the signature was computed over.
	// MarshalIndent reformats json.RawMessage, breaking signature verification.
	outputData, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	outputData = append(outputData, '\n') // POSIX text file
	if err := os.WriteFile(outputPath, outputData, 0644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Signed manifest written to %s\n", outputPath)
	return nil
}

func loadPrivateKey(b64 string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("private key seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
