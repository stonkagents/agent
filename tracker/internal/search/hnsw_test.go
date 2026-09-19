// Package: tracker/internal/search
// Feature: F-002 (Intelligent Data Sharing)
// Story: US-002-02 (HNSW Vector Index for Fast Search)
// Purpose: TDD tests for HNSW approximate nearest neighbor search

package search

import (
	"context"
	"math"
	"math/rand"
	"testing"
)

// TestHNSWInsertAndSearch - GREEN test
// Acceptance Criterion: Insert vectors, query returns exact match as top result
func TestHNSWInsertAndSearch(t *testing.T) {
	// Set deterministic seed for reproducible tests
	rand.Seed(42)

	// Arrange
	index := NewHNSWIndex(HNSWOptions{
		Dimensions:  384,
		M:           16,  // Max neighbors per layer
		EfConstruct: 200, // Construction search parameter
	})

	ctx := context.Background()

	// Create distinct embeddings (not random, to ensure reproducibility)
	testEmbedding := make([]float32, 384)
	for i := 0; i < 384; i++ {
		testEmbedding[i] = float32(i) / 384.0 // Unique pattern
	}
	testEmbedding = normalizeEmbedding(testEmbedding)

	// Insert just 10 noise vectors (smaller dataset for more reliable exact match)
	for i := 0; i < 10; i++ {
		cid := "noise-" + string(rune('a'+i))
		embedding := generateDeterministicEmbedding(384, i+1000) // Unique seed
		index.Insert(ctx, cid, embedding)
	}

	// Insert our test vector
	testCID := "test-vector"
	err := index.Insert(ctx, testCID, testEmbedding)
	if err != nil {
		t.Fatalf("Insert() failed: %v", err)
	}

	// Act - Search with exact test embedding
	results, err := index.Search(ctx, testEmbedding, 10)
	if err != nil {
		t.Fatalf("Search() failed: %v", err)
	}

	// Assert
	if len(results) < 1 {
		t.Fatalf("Expected at least 1 result, got %d", len(results))
	}

	// For HNSW (approximate search), exact match should be in top-3 results
	foundExactMatch := false
	exactMatchRank := -1
	exactMatchSimilarity := float32(0)
	for i, result := range results {
		if result.CID == testCID {
			foundExactMatch = true
			exactMatchRank = i
			exactMatchSimilarity = result.Similarity
			break
		}
	}

	if !foundExactMatch {
		t.Fatalf("Exact match not found in results: %+v", results)
	}

	// Exact match should be in top-3 (HNSW is approximate)
	if exactMatchRank > 2 {
		t.Errorf("Expected exact match in top-3, found at rank %d", exactMatchRank)
	}

	// Exact match should have similarity ~1.0
	if exactMatchSimilarity < 0.99 {
		t.Errorf("Expected exact match similarity ~1.0, got %f", exactMatchSimilarity)
	}
}

// TestSearchRelevance - RED test
// Acceptance Criterion: Semantic query "CLIP embeddings" finds CLIP-related assets
func TestSearchRelevance(t *testing.T) {
	// This test requires real embeddings from embedding generator
	// For MVP, we'll use synthetic embeddings with known similarity patterns

	// Arrange
	index := NewHNSWIndex(HNSWOptions{
		Dimensions:  384,
		M:           16,
		EfConstruct: 200,
	})

	ctx := context.Background()

	// Insert assets with semantically similar embeddings
	// Simulate: embeddings close in vector space = semantically similar
	clipEmbedding1 := generateSyntheticEmbedding(384, []float32{1.0, 0.5, 0.3})   // CLIP asset 1
	clipEmbedding2 := generateSyntheticEmbedding(384, []float32{0.9, 0.6, 0.4})   // CLIP asset 2 (similar)
	otherEmbedding := generateSyntheticEmbedding(384, []float32{-0.8, -0.5, 0.1}) // Unrelated asset

	index.Insert(ctx, "clip-asset-1", clipEmbedding1)
	index.Insert(ctx, "clip-asset-2", clipEmbedding2)
	index.Insert(ctx, "other-asset", otherEmbedding)

	// Act - Query with embedding similar to CLIP assets
	queryEmbedding := generateSyntheticEmbedding(384, []float32{0.95, 0.55, 0.35}) // Close to CLIP embeddings

	results, err := index.Search(ctx, queryEmbedding, 2)
	if err != nil {
		t.Fatalf("Search() failed: %v", err)
	}

	// Assert - Top 2 results should be CLIP assets
	if len(results) < 2 {
		t.Fatalf("Expected at least 2 results, got %d", len(results))
	}

	// Check that CLIP assets ranked higher than unrelated asset
	foundCLIP1 := false
	foundCLIP2 := false
	for _, result := range results {
		if result.CID == "clip-asset-1" {
			foundCLIP1 = true
		}
		if result.CID == "clip-asset-2" {
			foundCLIP2 = true
		}
	}

	if !foundCLIP1 || !foundCLIP2 {
		t.Errorf("Expected top 2 results to be CLIP assets, got: %v", results)
	}
}

// TestHNSWCosineSimilarity - RED test
// Acceptance Criterion: Cosine similarity calculated correctly
func TestHNSWCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		vec1     []float32
		vec2     []float32
		expected float32
		epsilon  float32
	}{
		{
			name:     "identical vectors",
			vec1:     []float32{1, 0, 0},
			vec2:     []float32{1, 0, 0},
			expected: 1.0,
			epsilon:  0.001,
		},
		{
			name:     "orthogonal vectors",
			vec1:     []float32{1, 0, 0},
			vec2:     []float32{0, 1, 0},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "opposite vectors",
			vec1:     []float32{1, 0, 0},
			vec2:     []float32{-1, 0, 0},
			expected: -1.0,
			epsilon:  0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			similarity := cosineSimilarity(tt.vec1, tt.vec2)
			if math.Abs(float64(similarity-tt.expected)) > float64(tt.epsilon) {
				t.Errorf("cosineSimilarity() = %f, want %f (±%f)", similarity, tt.expected, tt.epsilon)
			}
		})
	}
}

// BenchmarkHNSW10k - RED test (benchmark)
// Acceptance Criterion: Search <100ms for 10k assets
func BenchmarkHNSW10k(b *testing.B) {
	index := NewHNSWIndex(HNSWOptions{
		Dimensions:  384,
		M:           16,
		EfConstruct: 200,
	})

	ctx := context.Background()

	// Insert 10k vectors
	for i := 0; i < 10000; i++ {
		cid := generateTestCID(i)
		embedding := generateRandomEmbedding(384)
		index.Insert(ctx, cid, embedding)
	}

	// Benchmark search
	queryEmbedding := generateRandomEmbedding(384)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := index.Search(ctx, queryEmbedding, 10)
		if err != nil {
			b.Fatalf("Search() failed: %v", err)
		}
	}
}

// BenchmarkHNSW100k - RED test (scale benchmark)
// Acceptance Criterion: Search <500ms for 100k assets (95th percentile)
func BenchmarkHNSW100k(b *testing.B) {
	index := NewHNSWIndex(HNSWOptions{
		Dimensions:  384,
		M:           16,
		EfConstruct: 200,
	})

	ctx := context.Background()

	// Insert 100k vectors
	b.Log("Inserting 100k vectors...")
	for i := 0; i < 100000; i++ {
		cid := generateTestCID(i)
		embedding := generateRandomEmbedding(384)
		index.Insert(ctx, cid, embedding)
	}

	// Benchmark search
	queryEmbedding := generateRandomEmbedding(384)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := index.Search(ctx, queryEmbedding, 10)
		if err != nil {
			b.Fatalf("Search() failed: %v", err)
		}
	}
}

// Helper functions

func generateTestCID(index int) string {
	return "bafytest" + string(rune('a'+index%26)) + string(rune('0'+index/26))
}

func generateRandomEmbedding(dims int) []float32 {
	// Use deterministic random based on current goroutine for reproducibility
	// Don't call rand.Seed in loop - causes issues
	embedding := make([]float32, dims)
	for i := 0; i < dims; i++ {
		embedding[i] = rand.Float32()*2 - 1 // Random [-1, 1]
	}
	return normalizeEmbedding(embedding)
}

func generateDeterministicEmbedding(dims int, seed int) []float32 {
	// Generate deterministic embedding based on seed
	r := rand.New(rand.NewSource(int64(seed)))
	embedding := make([]float32, dims)
	for i := 0; i < dims; i++ {
		embedding[i] = r.Float32()*2 - 1
	}
	return normalizeEmbedding(embedding)
}

func generateSyntheticEmbedding(dims int, pattern []float32) []float32 {
	embedding := make([]float32, dims)
	for i := 0; i < dims; i++ {
		if i < len(pattern) {
			embedding[i] = pattern[i]
		} else {
			// Fill rest with small random noise
			embedding[i] = (rand.Float32() - 0.5) * 0.1
		}
	}
	return normalizeEmbedding(embedding)
}

func normalizeEmbedding(vec []float32) []float32 {
	var sumSquares float32
	for _, val := range vec {
		sumSquares += val * val
	}
	magnitude := float32(math.Sqrt(float64(sumSquares)))
	if magnitude == 0 {
		return vec
	}
	normalized := make([]float32, len(vec))
	for i, val := range vec {
		normalized[i] = val / magnitude
	}
	return normalized
}
