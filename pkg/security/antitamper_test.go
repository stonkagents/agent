// Package: pkg/security
// Feature: F-001 (Security-First Foundation)
// Story: US-001-04 (Anti-Tampering Tests)
// Purpose: Anti-tampering tests for embedding poisoning detection via cosine similarity

package security

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// =============================================================================
// Embedding Poisoning Attacks
// =============================================================================

// TestAntiTamper_Embedding_AllZeros verifies that an all-zero embedding
// (degenerate case) is rejected by cosine similarity calculation.
func TestAntiTamper_Embedding_AllZeros(t *testing.T) {
	baseline := make([]float32, 512)
	for i := range baseline {
		baseline[i] = float32(math.Sin(float64(i) * 0.01))
	}

	zeros := make([]float32, 512)

	_, err := CosineSimilarity(baseline, zeros)
	if err == nil {
		t.Error("Cosine similarity with all-zero embedding should return error (zero vector)")
	}
}

// TestAntiTamper_Embedding_NegatedVector verifies that a negated embedding
// (exact opposite direction) is detected as anomalous.
func TestAntiTamper_Embedding_NegatedVector(t *testing.T) {
	model := "test/model"

	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Negate the baseline — cosine similarity should be -1.0
	negated := make([]float32, len(baseline))
	for i, val := range baseline {
		negated[i] = -val
	}

	result, err := CheckEmbeddingAnomaly(model, negated)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() error: %v", err)
	}
	if !result.IsAnomalous {
		t.Errorf("Negated embedding should be anomalous (similarity: %f)", result.Similarity)
	}
	if result.Similarity > -0.9 {
		t.Errorf("Negated vector similarity = %f, expected near -1.0", result.Similarity)
	}
}

// TestAntiTamper_Embedding_OrthogonalVector verifies that an orthogonal
// embedding (zero cosine similarity) is detected as anomalous.
func TestAntiTamper_Embedding_OrthogonalVector(t *testing.T) {
	model := "test/model"

	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Construct an orthogonal vector using Gram-Schmidt on first two dimensions
	// Simple approach: rotate the vector 90 degrees in a 2D subspace
	orthogonal := make([]float32, len(baseline))
	copy(orthogonal, baseline)
	// Swap first two dimensions and negate one for approximate orthogonality
	for i := 0; i < len(orthogonal)-1; i += 2 {
		orthogonal[i], orthogonal[i+1] = -orthogonal[i+1], orthogonal[i]
	}

	result, err := CheckEmbeddingAnomaly(model, orthogonal)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() error: %v", err)
	}
	if !result.IsAnomalous {
		t.Errorf("Orthogonal embedding should be anomalous (similarity: %f)", result.Similarity)
	}
}

// TestAntiTamper_Embedding_SubtlePerturbation verifies that adding small noise
// to the baseline (within normal range) does NOT trigger a false positive.
func TestAntiTamper_Embedding_SubtlePerturbation(t *testing.T) {
	model := "test/model"

	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Add very small noise (±0.01) — should remain similar
	perturbed := make([]float32, len(baseline))
	for i, val := range baseline {
		noise := float32(math.Sin(float64(i)*0.7)) * 0.01
		perturbed[i] = val + noise
	}
	// Re-normalize
	mag := float32(0.0)
	for _, v := range perturbed {
		mag += v * v
	}
	mag = float32(math.Sqrt(float64(mag)))
	for i := range perturbed {
		perturbed[i] /= mag
	}

	result, err := CheckEmbeddingAnomaly(model, perturbed)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() error: %v", err)
	}
	if result.IsAnomalous {
		t.Errorf("Subtle perturbation should NOT be flagged as anomalous (similarity: %f)", result.Similarity)
	}
	if result.Similarity < 0.9 {
		t.Errorf("Subtly perturbed embedding similarity = %f, expected > 0.9", result.Similarity)
	}
}

// TestAntiTamper_Embedding_ScaledVector verifies that a uniformly scaled
// version of the baseline (same direction, different magnitude) still has
// cosine similarity = 1.0 and is NOT flagged as anomalous.
func TestAntiTamper_Embedding_ScaledVector(t *testing.T) {
	model := "test/model"

	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Scale by 5x — cosine similarity should still be ~1.0
	scaled := make([]float32, len(baseline))
	for i, val := range baseline {
		scaled[i] = val * 5.0
	}

	similarity, err := CosineSimilarity(baseline, scaled)
	if err != nil {
		t.Fatalf("CosineSimilarity() error: %v", err)
	}
	if math.Abs(float64(similarity)-1.0) > 0.001 {
		t.Errorf("Scaled vector cosine similarity = %f, expected 1.0", similarity)
	}

	result, err := CheckEmbeddingAnomaly(model, scaled)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() error: %v", err)
	}
	if result.IsAnomalous {
		t.Errorf("Scaled vector should NOT be anomalous (similarity: %f)", result.Similarity)
	}
}

// =============================================================================
// Byte-Level Embedding Tampering
// =============================================================================

// TestAntiTamper_Embedding_SingleFloatTampered verifies that changing a single
// float32 value in the binary embedding data is detectable via CID
// (tested at the byte level since embeddings are stored as raw bytes).
func TestAntiTamper_Embedding_SingleFloatTampered(t *testing.T) {
	original := createSecurityTestEmbedding(512)
	tampered := make([]byte, len(original))
	copy(tampered, original)

	// Modify one float (bytes 100-103) to a different value
	var newVal float32 = 99.99
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, newVal)
	copy(tampered[100:104], buf.Bytes())

	// Convert both to float32 slices
	origFloats, err := BytesToFloat32Slice(original)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(original) failed: %v", err)
	}
	tamperedFloats, err := BytesToFloat32Slice(tampered)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(tampered) failed: %v", err)
	}

	// The cosine similarity should differ from 1.0
	similarity, err := CosineSimilarity(origFloats, tamperedFloats)
	if err != nil {
		t.Fatalf("CosineSimilarity() error: %v", err)
	}
	if math.Abs(float64(similarity)-1.0) < 0.0001 {
		t.Error("Single float tampering produced identical cosine similarity — undetected")
	}
}

// TestAntiTamper_Embedding_ByteSwapWithinFloat verifies that swapping two
// bytes within a single float32 produces a different float value, which is
// detectable by cosine similarity comparison.
func TestAntiTamper_Embedding_ByteSwapWithinFloat(t *testing.T) {
	original := createSecurityTestEmbedding(512)
	tampered := make([]byte, len(original))
	copy(tampered, original)

	// Swap bytes at positions 0 and 1 within the first float
	tampered[0], tampered[1] = tampered[1], tampered[0]

	origFloats, err := BytesToFloat32Slice(original)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(original) failed: %v", err)
	}
	tamperedFloats, err := BytesToFloat32Slice(tampered)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(tampered) failed: %v", err)
	}

	// Verify the float values actually changed
	if origFloats[0] == tamperedFloats[0] {
		t.Skip("Byte swap did not change float value — edge case")
	}

	// Cosine similarity should differ
	similarity, err := CosineSimilarity(origFloats, tamperedFloats)
	if err != nil {
		t.Fatalf("CosineSimilarity() error: %v", err)
	}
	if math.Abs(float64(similarity)-1.0) < 0.0001 {
		t.Error("Byte swap within float was undetectable by cosine similarity")
	}
}

// TestAntiTamper_Embedding_TruncatedBytes verifies that truncating the binary
// embedding (removing dimensions) causes dimension mismatch detection.
func TestAntiTamper_Embedding_TruncatedBytes(t *testing.T) {
	original := createSecurityTestEmbedding(512)

	// Truncate to 256 floats (half)
	truncated := original[:256*4]

	origFloats, err := BytesToFloat32Slice(original)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(original) failed: %v", err)
	}
	truncatedFloats, err := BytesToFloat32Slice(truncated)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(truncated) failed: %v", err)
	}

	// Cosine similarity should fail due to length mismatch
	_, err = CosineSimilarity(origFloats, truncatedFloats)
	if err == nil {
		t.Error("Cosine similarity should fail for vectors of different lengths (512 vs 256)")
	}
}

// TestAntiTamper_Embedding_AppendedDimensions verifies that appending extra
// float32 values to an embedding causes dimension mismatch detection.
func TestAntiTamper_Embedding_AppendedDimensions(t *testing.T) {
	original := createSecurityTestEmbedding(512)

	// Append 256 extra floats
	extra := createSecurityTestEmbedding(256)
	extended := append(append([]byte(nil), original...), extra...)

	origFloats, err := BytesToFloat32Slice(original)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(original) failed: %v", err)
	}
	extendedFloats, err := BytesToFloat32Slice(extended)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice(extended) failed: %v", err)
	}

	// Cosine similarity should fail due to length mismatch
	_, err = CosineSimilarity(origFloats, extendedFloats)
	if err == nil {
		t.Error("Cosine similarity should fail for vectors of different lengths (512 vs 768)")
	}
}

// =============================================================================
// NaN and Infinity Attacks
// =============================================================================

// TestAntiTamper_Embedding_NaNInjection verifies that injecting NaN values
// into an embedding is handled (NaN poisons all arithmetic).
func TestAntiTamper_Embedding_NaNInjection(t *testing.T) {
	baseline := make([]float32, 512)
	for i := range baseline {
		baseline[i] = float32(math.Sin(float64(i) * 0.01))
	}

	nanEmbedding := make([]float32, 512)
	copy(nanEmbedding, baseline)
	nanEmbedding[0] = float32(math.NaN())

	similarity, err := CosineSimilarity(baseline, nanEmbedding)

	// Either returns error or returns NaN — neither should produce valid=true
	if err == nil && !math.IsNaN(float64(similarity)) && similarity >= 0.5 {
		t.Error("NaN-injected embedding should not produce valid similarity >= 0.5")
	}
}

// TestAntiTamper_Embedding_InfinityInjection verifies that injecting Inf
// values into an embedding is handled.
func TestAntiTamper_Embedding_InfinityInjection(t *testing.T) {
	baseline := make([]float32, 512)
	for i := range baseline {
		baseline[i] = float32(math.Sin(float64(i) * 0.01))
	}

	infEmbedding := make([]float32, 512)
	copy(infEmbedding, baseline)
	infEmbedding[0] = float32(math.Inf(1))

	similarity, err := CosineSimilarity(baseline, infEmbedding)

	// Either returns error or returns an extreme value
	if err == nil && !math.IsNaN(float64(similarity)) && !math.IsInf(float64(similarity), 0) {
		// If it returns a normal number, it should not be a clean 1.0
		if math.Abs(float64(similarity)-1.0) < 0.001 {
			t.Error("Inf-injected embedding should not produce similarity = 1.0")
		}
	}
}

// =============================================================================
// Helpers
// =============================================================================

// createSecurityTestEmbedding creates a deterministic float32 embedding as bytes.
func createSecurityTestEmbedding(dimensions int) []byte {
	buf := new(bytes.Buffer)
	for i := 0; i < dimensions; i++ {
		value := float32(math.Sin(float64(i)))
		binary.Write(buf, binary.LittleEndian, value)
	}
	return buf.Bytes()
}
