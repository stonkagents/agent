// Package: pkg/security
// Feature: F-001 (Security-First Foundation)
// Story: US-001-03 (Cosine Similarity Spot-Checks)
// Purpose: Detect poisoned embeddings using cosine similarity validation

package security

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"gonum.org/v1/gonum/floats"
)

// AnomalyCheckResult contains the results of an embedding anomaly check
type AnomalyCheckResult struct {
	IsAnomalous bool    // True if embedding is flagged as anomalous
	Similarity  float32 // Cosine similarity to baseline
	Threshold   float32 // Threshold used for anomaly detection
	Model       string  // Model identifier
}

// CosineSimilarity calculates the cosine similarity between two vectors.
// Formula: cos(θ) = (A · B) / (||A|| ||B||)
//
// Returns a value between -1.0 (opposite) and 1.0 (identical).
// Returns error if vectors are nil, empty, different lengths, or zero vectors.
func CosineSimilarity(vec1, vec2 []float32) (float32, error) {
	if vec1 == nil || vec2 == nil {
		return 0, fmt.Errorf("vectors cannot be nil")
	}

	if len(vec1) == 0 || len(vec2) == 0 {
		return 0, fmt.Errorf("vectors cannot be empty")
	}

	if len(vec1) != len(vec2) {
		return 0, fmt.Errorf("vectors must have the same length: got %d and %d", len(vec1), len(vec2))
	}

	// Convert to float64 for gonum operations
	a := make([]float64, len(vec1))
	b := make([]float64, len(vec2))
	for i := range vec1 {
		a[i] = float64(vec1[i])
		b[i] = float64(vec2[i])
	}

	// Calculate dot product
	dotProduct := floats.Dot(a, b)

	// Calculate magnitudes
	magnitudeA := floats.Norm(a, 2)
	magnitudeB := floats.Norm(b, 2)

	// Check for zero vectors
	if magnitudeA == 0 || magnitudeB == 0 {
		return 0, fmt.Errorf("cannot calculate cosine similarity with zero vector")
	}

	// Calculate cosine similarity
	similarity := dotProduct / (magnitudeA * magnitudeB)

	return float32(similarity), nil
}

// BytesToFloat32Slice converts binary embedding data to a float32 slice.
// Assumes little-endian encoding (4 bytes per float32).
//
// Returns error if data length is not divisible by 4.
func BytesToFloat32Slice(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("data length %d is not divisible by 4 (float32 size)", len(data))
	}

	numFloats := len(data) / 4
	result := make([]float32, numFloats)

	buf := bytes.NewReader(data)
	for i := 0; i < numFloats; i++ {
		if err := binary.Read(buf, binary.LittleEndian, &result[i]); err != nil {
			return nil, fmt.Errorf("failed to read float32 at index %d: %w", i, err)
		}
	}

	return result, nil
}

// GenerateBaselineEmbedding generates a baseline embedding for a given model and prompt.
// This is a mock implementation that generates deterministic embeddings for testing.
//
// In production, this would:
// - Use actual model to generate embeddings
// - Cache baselines in ~/.stonkagents/baselines/
// - Support common models (CLIP, sentence-transformers)
//
// Returns a normalized (unit) vector.
func GenerateBaselineEmbedding(model, prompt string) ([]float32, error) {
	if model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}
	if prompt == "" {
		return nil, fmt.Errorf("prompt cannot be empty")
	}

	// Mock implementation: Generate deterministic baseline based on model/prompt hash
	// Use 512 dimensions (CLIP ViT-B/32 size)
	dimensions := 512

	embedding := make([]float32, dimensions)

	// Generate deterministic values based on model and prompt
	seed := int64(len(model) + len(prompt))
	for i := 0; i < dimensions; i++ {
		// Deterministic pattern based on index and seed
		value := math.Sin(float64(i)*0.01 + float64(seed)*0.001)
		embedding[i] = float32(value)
	}

	// Normalize to unit vector
	magnitude := float32(0.0)
	for _, val := range embedding {
		magnitude += val * val
	}
	magnitude = float32(math.Sqrt(float64(magnitude)))

	if magnitude > 0 {
		for i := range embedding {
			embedding[i] /= magnitude
		}
	}

	return embedding, nil
}

// CheckEmbeddingAnomaly checks if an embedding is anomalous compared to baseline.
// Threshold: cosine similarity < 0.5 triggers anomaly flag.
//
// Returns AnomalyCheckResult with similarity score and anomaly status.
func CheckEmbeddingAnomaly(model string, embedding []float32) (*AnomalyCheckResult, error) {
	if model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}
	if embedding == nil || len(embedding) == 0 {
		return nil, fmt.Errorf("embedding cannot be nil or empty")
	}

	const similarityThreshold = 0.5

	// Get baseline embedding for this model
	// Use a standard prompt for baseline comparison
	baseline, err := GenerateBaselineEmbedding(model, "baseline prompt")
	if err != nil {
		return nil, fmt.Errorf("failed to generate baseline: %w", err)
	}

	// Ensure embedding and baseline have same dimensions
	if len(embedding) != len(baseline) {
		return nil, fmt.Errorf("embedding dimensions %d do not match baseline dimensions %d", len(embedding), len(baseline))
	}

	// Calculate cosine similarity
	similarity, err := CosineSimilarity(embedding, baseline)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate cosine similarity: %w", err)
	}

	// Check if anomalous
	isAnomalous := similarity < similarityThreshold

	return &AnomalyCheckResult{
		IsAnomalous: isAnomalous,
		Similarity:  similarity,
		Threshold:   similarityThreshold,
		Model:       model,
	}, nil
}
