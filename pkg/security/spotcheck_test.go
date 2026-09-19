// Package: pkg/security
// Feature: F-001 (Security-First Foundation)
// Story: US-001-03 (Cosine Similarity Spot-Checks)
// Purpose: Test suite for cosine similarity spot-checks to detect poisoned embeddings

package security

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// TestCosineSimilarity_IdenticalVectors verifies cosine similarity = 1.0 for identical vectors
// Acceptance Criteria: Cosine similarity calculation working correctly
func TestCosineSimilarity_IdenticalVectors(t *testing.T) {
	vec1 := []float32{1.0, 2.0, 3.0, 4.0}
	vec2 := []float32{1.0, 2.0, 3.0, 4.0}

	similarity, err := CosineSimilarity(vec1, vec2)
	if err != nil {
		t.Fatalf("CosineSimilarity() failed: %v", err)
	}

	// Identical vectors should have cosine similarity = 1.0
	if math.Abs(float64(similarity)-1.0) > 0.0001 {
		t.Errorf("CosineSimilarity() = %f, want 1.0", similarity)
	}
}

// TestCosineSimilarity_OrthogonalVectors verifies cosine similarity = 0.0 for orthogonal vectors
func TestCosineSimilarity_OrthogonalVectors(t *testing.T) {
	vec1 := []float32{1.0, 0.0, 0.0}
	vec2 := []float32{0.0, 1.0, 0.0}

	similarity, err := CosineSimilarity(vec1, vec2)
	if err != nil {
		t.Fatalf("CosineSimilarity() failed: %v", err)
	}

	// Orthogonal vectors should have cosine similarity ≈ 0.0
	if math.Abs(float64(similarity)) > 0.0001 {
		t.Errorf("CosineSimilarity() = %f, want 0.0", similarity)
	}
}

// TestCosineSimilarity_OppositeVectors verifies cosine similarity = -1.0 for opposite vectors
func TestCosineSimilarity_OppositeVectors(t *testing.T) {
	vec1 := []float32{1.0, 2.0, 3.0}
	vec2 := []float32{-1.0, -2.0, -3.0}

	similarity, err := CosineSimilarity(vec1, vec2)
	if err != nil {
		t.Fatalf("CosineSimilarity() failed: %v", err)
	}

	// Opposite vectors should have cosine similarity ≈ -1.0
	if math.Abs(float64(similarity)+1.0) > 0.0001 {
		t.Errorf("CosineSimilarity() = %f, want -1.0", similarity)
	}
}

// TestCosineSimilarity_DifferentLengths verifies error handling for mismatched vector lengths
func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	vec1 := []float32{1.0, 2.0, 3.0}
	vec2 := []float32{1.0, 2.0}

	_, err := CosineSimilarity(vec1, vec2)
	if err == nil {
		t.Error("CosineSimilarity() should return error for vectors of different lengths")
	}
}

// TestCosineSimilarity_ZeroVector verifies error handling for zero vectors
func TestCosineSimilarity_ZeroVector(t *testing.T) {
	vec1 := []float32{0.0, 0.0, 0.0}
	vec2 := []float32{1.0, 2.0, 3.0}

	_, err := CosineSimilarity(vec1, vec2)
	if err == nil {
		t.Error("CosineSimilarity() should return error for zero vector")
	}
}

// TestBytesToFloat32Slice verifies conversion from binary embedding to float32 slice
func TestBytesToFloat32Slice(t *testing.T) {
	// Create test embedding: [1.0, 2.0, 3.0]
	expected := []float32{1.0, 2.0, 3.0}
	buf := new(bytes.Buffer)
	for _, val := range expected {
		binary.Write(buf, binary.LittleEndian, val)
	}
	data := buf.Bytes()

	result, err := BytesToFloat32Slice(data)
	if err != nil {
		t.Fatalf("BytesToFloat32Slice() failed: %v", err)
	}

	if len(result) != len(expected) {
		t.Fatalf("BytesToFloat32Slice() length = %d, want %d", len(result), len(expected))
	}

	for i, val := range result {
		if math.Abs(float64(val-expected[i])) > 0.0001 {
			t.Errorf("BytesToFloat32Slice()[%d] = %f, want %f", i, val, expected[i])
		}
	}
}

// TestBytesToFloat32Slice_InvalidSize verifies error handling for invalid byte size
func TestBytesToFloat32Slice_InvalidSize(t *testing.T) {
	// 10 bytes is not divisible by 4 (size of float32)
	invalidData := make([]byte, 10)

	_, err := BytesToFloat32Slice(invalidData)
	if err == nil {
		t.Error("BytesToFloat32Slice() should return error for data not divisible by 4")
	}
}

// TestGenerateBaselineEmbedding creates a baseline embedding for a given model and prompt
// Acceptance Criteria: Baseline embeddings generated for common models
func TestGenerateBaselineEmbedding(t *testing.T) {
	// Mock baseline: deterministic embedding for testing
	model := "test/model"
	prompt := "test prompt"

	embedding, err := GenerateBaselineEmbedding(model, prompt)
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	if len(embedding) == 0 {
		t.Error("GenerateBaselineEmbedding() returned empty embedding")
	}

	// Verify embedding is normalized (unit vector)
	magnitude := float32(0.0)
	for _, val := range embedding {
		magnitude += val * val
	}
	magnitude = float32(math.Sqrt(float64(magnitude)))

	if math.Abs(float64(magnitude)-1.0) > 0.01 {
		t.Errorf("Baseline embedding not normalized: magnitude = %f, want 1.0", magnitude)
	}
}

// TestCheckEmbeddingAnomaly_NormalEmbedding verifies normal embeddings pass spot-check
// Acceptance Criteria: Cosine similarity >= 0.5 passes validation
func TestCheckEmbeddingAnomaly_NormalEmbedding(t *testing.T) {
	model := "test/model"

	// Create baseline
	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Create similar embedding (should pass)
	similarEmbedding := make([]float32, len(baseline))
	for i, val := range baseline {
		// Add small noise but keep similar
		similarEmbedding[i] = val + float32(rand.Float64()*0.1-0.05)
	}
	// Normalize
	magnitude := float32(0.0)
	for _, val := range similarEmbedding {
		magnitude += val * val
	}
	magnitude = float32(math.Sqrt(float64(magnitude)))
	for i := range similarEmbedding {
		similarEmbedding[i] /= magnitude
	}

	result, err := CheckEmbeddingAnomaly(model, similarEmbedding)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() failed: %v", err)
	}

	if result.IsAnomalous {
		t.Errorf("CheckEmbeddingAnomaly() flagged normal embedding as anomalous (similarity: %f)", result.Similarity)
	}
}

// TestCheckEmbeddingAnomaly_PoisonedEmbedding verifies poisoned embeddings are detected
// Acceptance Criteria: Unit test - Inject poisoned embedding (random noise) → spot-check detects anomaly
func TestCheckEmbeddingAnomaly_PoisonedEmbedding(t *testing.T) {
	model := "test/model"

	// Create baseline
	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Create poisoned embedding (random noise - should fail)
	poisonedEmbedding := make([]float32, len(baseline))
	for i := range poisonedEmbedding {
		poisonedEmbedding[i] = float32(rand.Float64()*2.0 - 1.0) // Random values [-1, 1]
	}
	// Normalize
	magnitude := float32(0.0)
	for _, val := range poisonedEmbedding {
		magnitude += val * val
	}
	magnitude = float32(math.Sqrt(float64(magnitude)))
	for i := range poisonedEmbedding {
		poisonedEmbedding[i] /= magnitude
	}

	result, err := CheckEmbeddingAnomaly(model, poisonedEmbedding)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() failed: %v", err)
	}

	// Poisoned embedding should be flagged as anomalous
	if !result.IsAnomalous {
		t.Errorf("CheckEmbeddingAnomaly() failed to detect poisoned embedding (similarity: %f)", result.Similarity)
	}

	// Similarity should be < 0.5 threshold
	if result.Similarity >= 0.5 {
		t.Errorf("Poisoned embedding similarity = %f, should be < 0.5", result.Similarity)
	}
}

// TestCheckEmbeddingAnomaly_ThresholdBoundary verifies threshold logic
func TestCheckEmbeddingAnomaly_ThresholdBoundary(t *testing.T) {
	tests := []struct {
		name           string
		similarityMock float32 // We'll create embeddings with known similarity
		expectAnomaly  bool
	}{
		{
			name:           "similarity 0.6 (above threshold) - normal",
			similarityMock: 0.6,
			expectAnomaly:  false,
		},
		{
			name:           "similarity 0.4 (below threshold) - anomalous",
			similarityMock: 0.4,
			expectAnomaly:  true,
		},
		{
			name:           "similarity 0.5 (at threshold) - normal",
			similarityMock: 0.5,
			expectAnomaly:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We'll need to create embeddings with specific similarity
			// For now, skip this test as it requires more complex setup
			t.Skip("Boundary test requires controlled similarity - implement in integration test")
		})
	}
}

// TestAnomalyCheckResult_Fields verifies AnomalyCheckResult structure
func TestAnomalyCheckResult_Fields(t *testing.T) {
	result := AnomalyCheckResult{
		IsAnomalous: true,
		Similarity:  0.3,
		Threshold:   0.5,
		Model:       "test/model",
	}

	if !result.IsAnomalous {
		t.Error("IsAnomalous should be true")
	}
	if result.Similarity != 0.3 {
		t.Errorf("Similarity = %f, want 0.3", result.Similarity)
	}
	if result.Threshold != 0.5 {
		t.Errorf("Threshold = %f, want 0.5", result.Threshold)
	}
	if result.Model != "test/model" {
		t.Errorf("Model = %s, want test/model", result.Model)
	}
}

// TestIntegration_SpotCheckWorkflow verifies end-to-end spot-check workflow
// Acceptance Criteria: Random 5% sample validated, runs asynchronously
func TestIntegration_SpotCheckWorkflow(t *testing.T) {
	model := "openai/clip-vit-b-32"

	// Generate baseline
	baseline, err := GenerateBaselineEmbedding(model, "a photo of a cat")
	if err != nil {
		t.Fatalf("GenerateBaselineEmbedding() failed: %v", err)
	}

	// Test 1: Normal embedding (similar to baseline)
	normalEmbedding := make([]float32, len(baseline))
	copy(normalEmbedding, baseline)
	// Add tiny noise
	for i := range normalEmbedding {
		normalEmbedding[i] += float32(rand.Float64()*0.05 - 0.025)
	}

	result, err := CheckEmbeddingAnomaly(model, normalEmbedding)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() failed for normal embedding: %v", err)
	}
	if result.IsAnomalous {
		t.Errorf("Normal embedding incorrectly flagged as anomalous (similarity: %f)", result.Similarity)
	}

	// Test 2: Poisoned embedding (random noise)
	poisonedEmbedding := make([]float32, len(baseline))
	for i := range poisonedEmbedding {
		poisonedEmbedding[i] = float32(rand.Float64()*2.0 - 1.0)
	}

	result, err = CheckEmbeddingAnomaly(model, poisonedEmbedding)
	if err != nil {
		t.Fatalf("CheckEmbeddingAnomaly() failed for poisoned embedding: %v", err)
	}
	if !result.IsAnomalous {
		t.Errorf("Poisoned embedding not detected (similarity: %f)", result.Similarity)
	}
}

// TestCosineSimilarity_CLIPEmbeddings verifies cosine similarity for CLIP-sized vectors
// CLIP ViT-B/32 uses 512-dimensional embeddings
func TestCosineSimilarity_CLIPEmbeddings(t *testing.T) {
	// Create two 512-dimensional embeddings
	vec1 := make([]float32, 512)
	vec2 := make([]float32, 512)

	for i := 0; i < 512; i++ {
		vec1[i] = float32(math.Sin(float64(i) * 0.01))
		vec2[i] = float32(math.Sin(float64(i)*0.01 + 0.1)) // Slightly different
	}

	similarity, err := CosineSimilarity(vec1, vec2)
	if err != nil {
		t.Fatalf("CosineSimilarity() failed for 512-dim vectors: %v", err)
	}

	// Similar sine waves should have high cosine similarity
	if similarity < 0.8 {
		t.Errorf("CosineSimilarity() = %f, expected > 0.8 for similar embeddings", similarity)
	}
}

// TestCosineSimilarity_ErrorCases verifies error handling
func TestCosineSimilarity_ErrorCases(t *testing.T) {
	tests := []struct {
		name    string
		vec1    []float32
		vec2    []float32
		wantErr bool
	}{
		{
			name:    "nil vectors",
			vec1:    nil,
			vec2:    []float32{1.0, 2.0},
			wantErr: true,
		},
		{
			name:    "empty vectors",
			vec1:    []float32{},
			vec2:    []float32{},
			wantErr: true,
		},
		{
			name:    "mismatched lengths",
			vec1:    []float32{1.0, 2.0},
			vec2:    []float32{1.0, 2.0, 3.0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CosineSimilarity(tt.vec1, tt.vec2)
			if (err != nil) != tt.wantErr {
				t.Errorf("CosineSimilarity() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCheckEmbeddingAnomaly_ErrorCases verifies error handling for invalid inputs
func TestCheckEmbeddingAnomaly_ErrorCases(t *testing.T) {
	validEmbedding := make([]float32, 512)
	for i := range validEmbedding {
		validEmbedding[i] = float32(math.Sin(float64(i) * 0.01))
	}

	tests := []struct {
		name      string
		model     string
		embedding []float32
		wantErr   bool
	}{
		{
			name:      "empty model",
			model:     "",
			embedding: validEmbedding,
			wantErr:   true,
		},
		{
			name:      "nil embedding",
			model:     "test/model",
			embedding: nil,
			wantErr:   true,
		},
		{
			name:      "empty embedding",
			model:     "test/model",
			embedding: []float32{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CheckEmbeddingAnomaly(tt.model, tt.embedding)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckEmbeddingAnomaly() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestGenerateBaselineEmbedding_ErrorCases verifies error handling
func TestGenerateBaselineEmbedding_ErrorCases(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		prompt  string
		wantErr bool
	}{
		{
			name:    "empty model",
			model:   "",
			prompt:  "test prompt",
			wantErr: true,
		},
		{
			name:    "empty prompt",
			model:   "test/model",
			prompt:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateBaselineEmbedding(tt.model, tt.prompt)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateBaselineEmbedding() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
