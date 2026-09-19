// Package: pkg/manifest
// Feature: F-003 (Universal File Sharing)
// Story: US-003-03 (Protocol Buffer Manifest Format)
// Purpose: Protocol Buffer encoding/decoding helpers for asset manifests

package manifest

import (
	"fmt"

	"google.golang.org/protobuf/proto"
)

// EncodeAtRawManifest serializes an AtRawManifest to Protocol Buffer bytes.
// Returns the encoded bytes or an error if marshaling fails.
func EncodeAtRawManifest(manifest *AtRawManifest) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest cannot be nil")
	}

	data, err := proto.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal AtRawManifest: %w", err)
	}

	return data, nil
}

// DecodeAtRawManifest deserializes Protocol Buffer bytes into an AtRawManifest.
// Returns the decoded manifest or an error if unmarshaling fails.
func DecodeAtRawManifest(data []byte) (*AtRawManifest, error) {
	manifest := &AtRawManifest{}

	if err := proto.Unmarshal(data, manifest); err != nil {
		return nil, fmt.Errorf("failed to unmarshal AtRawManifest: %w", err)
	}

	return manifest, nil
}

// EncodeAtVecManifest serializes an AtVecManifest to Protocol Buffer bytes.
// Returns the encoded bytes or an error if marshaling fails.
func EncodeAtVecManifest(manifest *AtVecManifest) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest cannot be nil")
	}

	data, err := proto.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal AtVecManifest: %w", err)
	}

	return data, nil
}

// DecodeAtVecManifest deserializes Protocol Buffer bytes into an AtVecManifest.
// Returns the decoded manifest or an error if unmarshaling fails.
func DecodeAtVecManifest(data []byte) (*AtVecManifest, error) {
	manifest := &AtVecManifest{}

	if err := proto.Unmarshal(data, manifest); err != nil {
		return nil, fmt.Errorf("failed to unmarshal AtVecManifest: %w", err)
	}

	return manifest, nil
}
