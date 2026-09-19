// Minimal test to debug HNSW implementation

package search

import (
	"context"
	"testing"
)

func TestHNSW_MinimalCase(t *testing.T) {
	// Minimal test: 2 vectors, search for exact match
	index := NewHNSWIndex(HNSWOptions{
		Dimensions:  3, // Small for debugging
		M:           4,
		EfConstruct: 10,
	})

	ctx := context.Background()

	// Vector 1: [1, 0, 0]
	vec1 := []float32{1, 0, 0}
	index.Insert(ctx, "vec1", vec1)

	// Vector 2: [0, 1, 0]
	vec2 := []float32{0, 1, 0}
	index.Insert(ctx, "vec2", vec2)

	// Search for vec1 (exact match)
	results, err := index.Search(ctx, vec1, 1)
	if err != nil {
		t.Fatalf("Search() failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("No results returned")
	}

	t.Logf("Results: %+v", results)

	if results[0].CID != "vec1" {
		t.Errorf("Expected vec1, got %s (similarity: %f)", results[0].CID, results[0].Similarity)
	}

	if results[0].Similarity < 0.99 {
		t.Errorf("Expected similarity ~1.0, got %f", results[0].Similarity)
	}
}
