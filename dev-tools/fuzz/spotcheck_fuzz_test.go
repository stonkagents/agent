package fuzz

import (
	"math"
	"testing"

	"github.com/stonkagents/agent/pkg/security"
)

// FuzzCosineSimilarity tests that CosineSimilarity handles arbitrary float32
// vectors without panicking. It should return errors for mismatched lengths,
// empty vectors, and zero vectors — never crash.
func FuzzCosineSimilarity(f *testing.F) {
	f.Add([]byte{0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x00, 0x40}, // [1.0, 2.0]
		[]byte{0x00, 0x00, 0x40, 0x40, 0x00, 0x00, 0x80, 0x40}) // [3.0, 4.0]
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0x00, 0x00, 0x80, 0x3f}, []byte{0x00, 0x00, 0x80, 0x3f}) // [1.0], [1.0]

	f.Fuzz(func(t *testing.T, raw1, raw2 []byte) {
		vec1, err1 := security.BytesToFloat32Slice(raw1)
		vec2, err2 := security.BytesToFloat32Slice(raw2)

		if err1 != nil || err2 != nil {
			return // Invalid byte lengths are fine
		}

		// Should never panic
		result, err := security.CosineSimilarity(vec1, vec2)
		if err != nil {
			return // Errors for mismatched lengths, zero vectors, etc.
		}

		// Result should be in [-1, 1] range (with floating point tolerance)
		if result < -1.01 || result > 1.01 {
			t.Errorf("cosine similarity %f out of [-1, 1] range", result)
		}
	})
}

// FuzzBytesToFloat32Slice tests that BytesToFloat32Slice handles arbitrary
// byte input without panicking.
func FuzzBytesToFloat32Slice(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x00, 0x00, 0x80, 0x3f}) // 1.0 as float32 LE
	f.Add([]byte{0x00, 0x00, 0xc0, 0x7f}) // NaN
	f.Add([]byte{0x00, 0x00, 0x80, 0x7f}) // +Inf

	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := security.BytesToFloat32Slice(data)
		if err != nil {
			// Non-4-byte-aligned data should error
			if len(data)%4 != 0 {
				return
			}
			t.Fatalf("unexpected error for %d-byte input: %v", len(data), err)
		}

		expectedLen := len(data) / 4
		if len(result) != expectedLen {
			t.Errorf("expected %d floats, got %d", expectedLen, len(result))
		}

		// Check that no panics occur when accessing values
		for _, v := range result {
			// NaN and Inf are valid float32 values from arbitrary bytes
			_ = math.IsNaN(float64(v))
			_ = math.IsInf(float64(v), 0)
		}
	})
}

// FuzzCheckEmbeddingAnomaly tests that CheckEmbeddingAnomaly handles
// various embedding vectors without panicking.
func FuzzCheckEmbeddingAnomaly(f *testing.F) {
	f.Add("clip-vit-b32", 512)
	f.Add("sentence-transformers", 768)
	f.Add("", 0)

	f.Fuzz(func(t *testing.T, model string, dims int) {
		if model == "" || dims <= 0 || dims > 4096 {
			return // Skip obviously invalid inputs to focus fuzzing
		}

		// Create a random-ish embedding
		embedding := make([]float32, dims)
		for i := range embedding {
			embedding[i] = float32(i) * 0.01
		}

		// Should never panic
		_, _ = security.CheckEmbeddingAnomaly(model, embedding)
	})
}
