package fuzz

import (
	"testing"

	"github.com/stonkagents/agent/pkg/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FuzzDecodeAtRawManifest tests that DecodeAtRawManifest handles arbitrary
// byte input without panicking. Invalid protobuf data should return errors.
func FuzzDecodeAtRawManifest(f *testing.F) {
	// Seed with empty and garbage data
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x08, 0x01}) // minimal protobuf field
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff})

	// Seed with a valid encoded manifest
	valid := &manifest.AtRawManifest{
		Filename:      "test.dat",
		MimeType:      "application/octet-stream",
		Size:          1024,
		Cid:           "bafkreigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		FormatVersion: "1.0",
		CreatedAt:     timestamppb.Now(),
		Signature:     make([]byte, 64),
	}
	if encoded, err := proto.Marshal(valid); err == nil {
		f.Add(encoded)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Should never panic
		decoded, err := manifest.DecodeAtRawManifest(data)
		if err != nil {
			return // Errors are expected for invalid protobuf
		}

		// If decode succeeded, re-encode should not panic
		reencoded, err := manifest.EncodeAtRawManifest(decoded)
		if err != nil {
			t.Fatalf("EncodeAtRawManifest failed after successful decode: %v", err)
		}

		// Roundtrip: decode the re-encoded data
		decoded2, err := manifest.DecodeAtRawManifest(reencoded)
		if err != nil {
			t.Fatalf("DecodeAtRawManifest failed on re-encoded data: %v", err)
		}

		// The two decoded manifests should be equivalent
		if !proto.Equal(decoded, decoded2) {
			t.Error("roundtrip decode mismatch")
		}
	})
}

// FuzzDecodeAtVecManifest tests that DecodeAtVecManifest handles arbitrary
// byte input without panicking.
func FuzzDecodeAtVecManifest(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x08, 0x80, 0x04}) // varint 512

	// Seed with a valid encoded manifest
	valid := &manifest.AtVecManifest{
		Model:         "clip-vit-b32",
		Framework:     "pytorch",
		Dimensions:    512,
		Cid:           "bafkreigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		FormatVersion: "1.0",
		CreatedAt:     timestamppb.Now(),
		Signature:     make([]byte, 64),
	}
	if encoded, err := proto.Marshal(valid); err == nil {
		f.Add(encoded)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := manifest.DecodeAtVecManifest(data)
		if err != nil {
			return
		}

		reencoded, err := manifest.EncodeAtVecManifest(decoded)
		if err != nil {
			t.Fatalf("EncodeAtVecManifest failed after successful decode: %v", err)
		}

		decoded2, err := manifest.DecodeAtVecManifest(reencoded)
		if err != nil {
			t.Fatalf("DecodeAtVecManifest failed on re-encoded data: %v", err)
		}

		if !proto.Equal(decoded, decoded2) {
			t.Error("roundtrip decode mismatch")
		}
	})
}

// FuzzValidateAtRawManifest tests that ValidateAtRawManifest handles
// any protobuf-decodable manifest without panicking.
func FuzzValidateAtRawManifest(f *testing.F) {
	f.Add("", "", int64(0), "", "", "")
	f.Add("test.dat", "text/plain", int64(100), "bafk...", "1.0", "sig")
	f.Add("file.bin", "application/octet-stream", int64(-1), "", "", "")

	f.Fuzz(func(t *testing.T, filename, mimeType string, size int64, cidStr, version, sigStr string) {
		m := &manifest.AtRawManifest{
			Filename:      filename,
			MimeType:      mimeType,
			Size:          size,
			Cid:           cidStr,
			FormatVersion: version,
			CreatedAt:     timestamppb.Now(),
			Signature:     []byte(sigStr),
		}
		// Should never panic, errors are expected for invalid fields
		_ = manifest.ValidateAtRawManifest(m)
	})
}

// FuzzValidateAtVecManifest tests that ValidateAtVecManifest handles
// any manifest without panicking.
func FuzzValidateAtVecManifest(f *testing.F) {
	f.Add("", "", int32(0), "", "", "")
	f.Add("clip", "pytorch", int32(512), "bafk...", "1.0", "sig")

	f.Fuzz(func(t *testing.T, model, framework string, dims int32, cidStr, version, sigStr string) {
		m := &manifest.AtVecManifest{
			Model:         model,
			Framework:     framework,
			Dimensions:    dims,
			Cid:           cidStr,
			FormatVersion: version,
			CreatedAt:     timestamppb.Now(),
			Signature:     []byte(sigStr),
		}
		_ = manifest.ValidateAtVecManifest(m)
	})
}
