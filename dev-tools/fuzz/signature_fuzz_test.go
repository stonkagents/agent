package fuzz

import (
	"crypto/ed25519"
	"testing"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// FuzzVerifySignature tests that VerifySignature handles arbitrary inputs
// without panicking. It should return errors for malformed keys/signatures,
// and false for invalid signatures with correctly-sized inputs.
func FuzzVerifySignature(f *testing.F) {
	// Seed with various sizes
	f.Add([]byte("data"), make([]byte, ed25519.SignatureSize), make([]byte, ed25519.PublicKeySize))
	f.Add([]byte{}, make([]byte, 10), make([]byte, 10))
	f.Add([]byte("x"), []byte{}, []byte{})

	f.Fuzz(func(t *testing.T, data, signature, publicKey []byte) {
		// Should never panic regardless of input
		_, _ = crypto.VerifySignature(data, signature, publicKey)
	})
}

// FuzzSignDataRoundtrip generates a real keypair and verifies that signing
// arbitrary data always produces a valid, verifiable signature.
func FuzzSignDataRoundtrip(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte(""))
	f.Add([]byte{0x00, 0x01, 0x02})
	f.Add(make([]byte, 4096))

	f.Fuzz(func(t *testing.T, data []byte) {
		pub, priv, err := crypto.GenerateKeypair()
		if err != nil {
			t.Fatalf("GenerateKeypair failed: %v", err)
		}

		sig, err := crypto.SignData(data, priv)
		if err != nil {
			t.Fatalf("SignData failed: %v", err)
		}

		valid, err := crypto.VerifySignature(data, sig, pub)
		if err != nil {
			t.Fatalf("VerifySignature failed: %v", err)
		}
		if !valid {
			t.Error("valid signature rejected")
		}
	})
}

// FuzzSignDataTamper verifies that modifying signed data invalidates the signature.
func FuzzSignDataTamper(f *testing.F) {
	f.Add([]byte("original"), byte(0x42))

	f.Fuzz(func(t *testing.T, data []byte, tamperByte byte) {
		if len(data) == 0 {
			return
		}

		pub, priv, err := crypto.GenerateKeypair()
		if err != nil {
			t.Fatalf("GenerateKeypair failed: %v", err)
		}

		sig, err := crypto.SignData(data, priv)
		if err != nil {
			t.Fatalf("SignData failed: %v", err)
		}

		// Tamper with the data
		tampered := make([]byte, len(data))
		copy(tampered, data)
		tampered[0] = tampered[0] ^ tamperByte

		if string(tampered) == string(data) {
			return // XOR with 0 produces same data
		}

		valid, err := crypto.VerifySignature(tampered, sig, pub)
		if err != nil {
			return // Error is acceptable
		}
		if valid {
			t.Error("tampered data still verified as valid")
		}
	})
}
