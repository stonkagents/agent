// Package repository: shared API key generation for PeerAPIKeyRepository implementations.
package repository

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateAPIKey returns a 64-character hex string (32 random bytes). Safe for storage and header use.
func GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
