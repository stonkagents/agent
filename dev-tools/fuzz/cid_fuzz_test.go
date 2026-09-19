package fuzz

import (
	"testing"

	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// FuzzCIDRoundtrip tests that GenerateCID -> CIDToBytes -> CIDFromBytes produces
// a consistent CID string for any input data.
func FuzzCIDRoundtrip(f *testing.F) {
	// Seed corpus
	f.Add([]byte("hello world"))
	f.Add([]byte(""))
	f.Add([]byte{0x00})
	f.Add([]byte{0xff, 0xfe, 0xfd})
	f.Add(make([]byte, 1024))

	f.Fuzz(func(t *testing.T, data []byte) {
		cidStr, err := crypto.GenerateCID(data)
		if err != nil {
			// GenerateCID should handle any byte slice without error
			t.Fatalf("GenerateCID failed on valid input: %v", err)
		}

		// Roundtrip: string -> bytes -> string
		cidBytes, err := crypto.CIDToBytes(cidStr)
		if err != nil {
			t.Fatalf("CIDToBytes failed on valid CID %q: %v", cidStr, err)
		}

		cidStr2, err := crypto.CIDFromBytes(cidBytes)
		if err != nil {
			t.Fatalf("CIDFromBytes failed on valid bytes: %v", err)
		}

		if cidStr != cidStr2 {
			t.Errorf("CID roundtrip mismatch: %q != %q", cidStr, cidStr2)
		}
	})
}

// FuzzVerifyCID tests that VerifyCID correctly validates CID-data pairs
// and rejects mismatches.
func FuzzVerifyCID(f *testing.F) {
	f.Add([]byte("test data"), []byte("different data"))
	f.Add([]byte("same"), []byte("same"))
	f.Add([]byte{}, []byte{0x01})

	f.Fuzz(func(t *testing.T, original, modified []byte) {
		cidStr, err := crypto.GenerateCID(original)
		if err != nil {
			t.Fatalf("GenerateCID failed: %v", err)
		}

		// Verify with original data should pass
		valid, err := crypto.VerifyCID(cidStr, original)
		if err != nil {
			t.Fatalf("VerifyCID failed on matching data: %v", err)
		}
		if !valid {
			t.Error("VerifyCID returned false for matching data")
		}

		// Verify with different data should fail (unless data is identical)
		if string(original) != string(modified) {
			valid2, err := crypto.VerifyCID(cidStr, modified)
			if err != nil {
				// Error is acceptable for invalid inputs
				return
			}
			if valid2 {
				t.Error("VerifyCID returned true for non-matching data")
			}
		}
	})
}

// FuzzCIDFromBytes tests that CIDFromBytes handles arbitrary byte input
// without panicking. Invalid inputs should return errors, not crash.
func FuzzCIDFromBytes(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x01, 0x55, 0x12, 0x20}) // partial CIDv1 prefix
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Should not panic on any input
		_, _ = crypto.CIDFromBytes(data)
	})
}

// FuzzCIDToBytes tests that CIDToBytes handles arbitrary string input
// without panicking. Invalid CID strings should return errors.
func FuzzCIDToBytes(f *testing.F) {
	f.Add("")
	f.Add("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	f.Add("not-a-cid")
	f.Add("QmTest123") // CIDv0 format

	f.Fuzz(func(t *testing.T, cidStr string) {
		// Should not panic on any input
		_, _ = crypto.CIDToBytes(cidStr)
	})
}
