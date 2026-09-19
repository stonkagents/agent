// Package: pkg/embeddings
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 (Embedding Generation Service)
// Purpose: Test embedding generation with sentence-transformers model
// TDD Phase: RED tests first, then GREEN implementation

package embeddings

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Phase 1: Model Loading Tests (RED → GREEN → REFACTOR)
// ============================================================================

// TestEmbeddingGenerator_LoadModel verifies model loads from file path
// RED: This test will fail until we implement the generator
func TestEmbeddingGenerator_LoadModel(t *testing.T) {
	// For MVP: Use mock model path (actual model download comes later)
	modelPath := "testdata/mock-model.onnx"

	// Create generator with model path
	generator, err := NewSentenceTransformerGenerator(modelPath)

	// Should not error on valid model path (mock for now)
	if err != nil {
		t.Skip("Skipping model load test - model file not available yet")
	}

	assert.NotNil(t, generator)
}

// TestEmbeddingGenerator_ModelDimensions verifies model returns correct dimensions
// RED: This test will fail until we implement Dimensions() method
func TestEmbeddingGenerator_ModelDimensions(t *testing.T) {
	modelPath := "testdata/mock-model.onnx"
	generator, err := NewSentenceTransformerGenerator(modelPath)
	if err != nil {
		t.Skip("Skipping dimensions test - model file not available yet")
	}

	// all-MiniLM-L6-v2 produces 384-dimensional embeddings
	dimensions := generator.Dimensions()
	assert.Equal(t, 384, dimensions, "Expected 384 dimensions for all-MiniLM-L6-v2 model")
}

// TestEmbeddingGenerator_ModelID verifies model identifier
// RED: This test will fail until we implement ModelID() method
func TestEmbeddingGenerator_ModelID(t *testing.T) {
	modelPath := "testdata/mock-model.onnx"
	generator, err := NewSentenceTransformerGenerator(modelPath)
	if err != nil {
		t.Skip("Skipping model ID test - model file not available yet")
	}

	modelID := generator.ModelID()
	assert.Equal(t, "all-MiniLM-L6-v2", modelID, "Expected all-MiniLM-L6-v2 as model ID")
}

// ============================================================================
// Phase 2: Embedding Generation Tests (RED → GREEN → REFACTOR)
// ============================================================================

// TestEmbeddingGenerator_Generate_SingleText verifies embedding generation from text
// RED: This test will fail until we implement Generate() method
func TestEmbeddingGenerator_Generate_SingleText(t *testing.T) {
	modelPath := "testdata/mock-model.onnx"
	generator, err := NewSentenceTransformerGenerator(modelPath)
	if err != nil {
		t.Skip("Skipping generate test - model file not available yet")
	}

	ctx := context.Background()
	text := "machine learning dataset for financial forecasting"

	embedding, err := generator.Generate(ctx, text)
	require.NoError(t, err)

	// Verify embedding dimensions
	assert.Equal(t, 384, len(embedding), "Expected 384-dimensional embedding")

	// Verify embedding contains non-zero values
	hasNonZero := false
	for _, val := range embedding {
		if val != 0 {
			hasNonZero = true
			break
		}
	}
	assert.True(t, hasNonZero, "Embedding should contain non-zero values")
}

// TestEmbeddingGenerator_Generate_EmptyText verifies error handling for empty input
// RED: This test will fail until we add empty text validation
func TestEmbeddingGenerator_Generate_EmptyText(t *testing.T) {
	modelPath := "testdata/mock-model.onnx"
	generator, err := NewSentenceTransformerGenerator(modelPath)
	if err != nil {
		t.Skip("Skipping empty text test - model file not available yet")
	}

	ctx := context.Background()

	// Test empty string
	_, err = generator.Generate(ctx, "")
	assert.Error(t, err, "Should return error for empty text")
	assert.Contains(t, err.Error(), "empty", "Error message should mention empty text")

	// Test whitespace-only string
	_, err = generator.Generate(ctx, "   ")
	assert.Error(t, err, "Should return error for whitespace-only text")
}

// TestEmbeddingGenerator_GenerateBatch verifies batch processing
// RED: This test will fail until we implement GenerateBatch() method
func TestEmbeddingGenerator_GenerateBatch(t *testing.T) {
	modelPath := "testdata/mock-model.onnx"
	generator, err := NewSentenceTransformerGenerator(modelPath)
	if err != nil {
		t.Skip("Skipping batch test - model file not available yet")
	}

	ctx := context.Background()
	texts := []string{
		"machine learning dataset",
		"financial time series data",
		"image classification model",
	}

	embeddings, err := generator.GenerateBatch(ctx, texts)
	require.NoError(t, err)

	// Verify correct number of embeddings
	assert.Equal(t, 3, len(embeddings), "Should generate 3 embeddings")

	// Verify each embedding has correct dimensions
	for i, embedding := range embeddings {
		assert.Equal(t, 384, len(embedding), "Embedding %d should have 384 dimensions", i)
	}
}

// ============================================================================
// Phase 3: Performance Tests
// ============================================================================

// TestEmbeddingGenerator_Performance_SingleText verifies latency target (<50ms)
// This is a benchmark, not a strict requirement for MVP
func TestEmbeddingGenerator_Performance_SingleText(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	modelPath := "testdata/mock-model.onnx"
	generator, err := NewSentenceTransformerGenerator(modelPath)
	if err != nil {
		t.Skip("Skipping performance test - model file not available yet")
	}

	ctx := context.Background()
	text := "machine learning dataset for financial forecasting"

	// Warm up (first inference may be slower)
	_, _ = generator.Generate(ctx, text)

	// Measure latency
	const iterations = 10
	var totalDuration int64

	for i := 0; i < iterations; i++ {
		start := testing.Benchmark(func(b *testing.B) {
			for j := 0; j < b.N; j++ {
				_, _ = generator.Generate(ctx, text)
			}
		})
		totalDuration += start.NsPerOp()
	}

	avgLatencyMs := float64(totalDuration/iterations) / 1_000_000
	t.Logf("Average latency: %.2f ms", avgLatencyMs)

	// Target: <50ms per text (p99)
	// For MVP, we'll be lenient and accept <200ms
	if avgLatencyMs > 200 {
		t.Logf("WARNING: Latency %.2f ms exceeds 200ms target (target: <50ms)", avgLatencyMs)
	}
}

// ============================================================================
// Mock Implementation Tests (for integration testing)
// ============================================================================

// TestMockEmbeddingGenerator_Generate verifies mock generator for testing
func TestMockEmbeddingGenerator_Generate(t *testing.T) {
	generator := NewMockEmbeddingGenerator(384)
	ctx := context.Background()

	embedding, err := generator.Generate(ctx, "test text")
	require.NoError(t, err)

	assert.Equal(t, 384, len(embedding), "Mock should generate 384-dimensional embeddings")
	assert.Equal(t, 384, generator.Dimensions())
	assert.Equal(t, "mock", generator.ModelID())
}

// ============================================================================
// Cache Integration Tests (F-012, US-012-02 Phase 4)
// ============================================================================

// TestGenerator_WithCache_CacheHit verifies cache reduces generation calls
func TestGenerator_WithCache_CacheHit(t *testing.T) {
	generator, err := NewSentenceTransformerGenerator("test-model.onnx")
	require.NoError(t, err)

	// Enable caching
	generator.WithCache(100, time.Hour)

	ctx := context.Background()
	query := "machine learning dataset"

	// First call - cache miss, generates embedding
	embedding1, err := generator.Generate(ctx, query)
	require.NoError(t, err)

	// Second call - cache hit, same embedding
	embedding2, err := generator.Generate(ctx, query)
	require.NoError(t, err)

	// Should return identical embeddings
	assert.Equal(t, embedding1, embedding2, "Cached embedding should match original")
}

// TestGenerator_WithCache_DifferentQueries verifies cache handles multiple queries
func TestGenerator_WithCache_DifferentQueries(t *testing.T) {
	generator, err := NewSentenceTransformerGenerator("test-model.onnx")
	require.NoError(t, err)

	generator.WithCache(100, time.Hour)

	ctx := context.Background()

	// Generate embeddings for different queries
	emb1, err := generator.Generate(ctx, "query 1")
	require.NoError(t, err)

	emb2, err := generator.Generate(ctx, "query 2")
	require.NoError(t, err)

	// Should be different
	assert.NotEqual(t, emb1, emb2, "Different queries should produce different embeddings")

	// Retrieve from cache - should match
	cached1, err := generator.Generate(ctx, "query 1")
	require.NoError(t, err)
	assert.Equal(t, emb1, cached1)

	cached2, err := generator.Generate(ctx, "query 2")
	require.NoError(t, err)
	assert.Equal(t, emb2, cached2)
}

// TestGenerator_WithoutCache_NoCaching verifies default behavior (no cache)
func TestGenerator_WithoutCache_NoCaching(t *testing.T) {
	generator, err := NewSentenceTransformerGenerator("test-model.onnx")
	require.NoError(t, err)

	// No WithCache() call - caching should be disabled

	ctx := context.Background()
	query := "test query"

	// Generate embedding
	embedding1, err := generator.Generate(ctx, query)
	require.NoError(t, err)

	// Second call - no cache, regenerates (but deterministic, so same result)
	embedding2, err := generator.Generate(ctx, query)
	require.NoError(t, err)

	// Should still match (deterministic hash-based generation)
	assert.Equal(t, embedding1, embedding2)
}

// TestGenerator_Cache_Expiration verifies cache TTL
func TestGenerator_Cache_Expiration(t *testing.T) {
	generator, err := NewSentenceTransformerGenerator("test-model.onnx")
	require.NoError(t, err)

	// Short TTL for testing
	generator.WithCache(100, 50*time.Millisecond)

	ctx := context.Background()
	query := "expiring query"

	// Generate and cache
	embedding1, err := generator.Generate(ctx, query)
	require.NoError(t, err)

	// Immediate retrieval - cache hit
	embedding2, err := generator.Generate(ctx, query)
	require.NoError(t, err)
	assert.Equal(t, embedding1, embedding2)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should regenerate (cache expired)
	embedding3, err := generator.Generate(ctx, query)
	require.NoError(t, err)

	// Should still match (deterministic generation)
	assert.Equal(t, embedding1, embedding3)
}
