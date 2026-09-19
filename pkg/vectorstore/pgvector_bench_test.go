// Package: pkg/vectorstore
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-04 (Performance Optimization)
// Purpose: Benchmark vector search performance
// TDD Phase: Benchmark-driven optimization

package vectorstore

import (
	"context"
	"testing"
)

// ============================================================================
// Performance Benchmarks (US-012-04)
// ============================================================================

// Acceptance Criteria:
// - Search latency < 100ms for 10K vectors (p95)
// - Search latency < 200ms for 100K vectors (p95)
// - Cache hit latency < 1ms
// - Batch operations 10x faster than sequential

// BenchmarkVectorStore_Search_10K benchmarks search with 10K vectors
func BenchmarkVectorStore_Search_10K(b *testing.B) {
	store := NewMockVectorStore()
	ctx := context.Background()

	// Pre-populate with 10K vectors
	for i := 0; i < 10000; i++ {
		embedding := make([]float32, 384)
		for j := range embedding {
			embedding[j] = float32(i%100) / 100.0
		}
		cid := "bafytest-" + string(rune(i))
		_ = store.Store(ctx, cid, embedding)
	}

	// Query embedding
	queryEmbedding := make([]float32, 384)
	for i := range queryEmbedding {
		queryEmbedding[i] = 0.5
	}

	// Benchmark search
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Search(ctx, VectorSearchRequest{
			QueryEmbedding: queryEmbedding,
			Limit:          20,
			MinSimilarity:  0.6,
		})
	}
}

// BenchmarkVectorStore_Search_100K benchmarks search with 100K vectors
func BenchmarkVectorStore_Search_100K(b *testing.B) {
	store := NewMockVectorStore()
	ctx := context.Background()

	// Pre-populate with 100K vectors
	b.Log("Populating 100K vectors...")
	for i := 0; i < 100000; i++ {
		embedding := make([]float32, 384)
		for j := range embedding {
			embedding[j] = float32(i%1000) / 1000.0
		}
		cid := "bafytest-" + string(rune(i))
		_ = store.Store(ctx, cid, embedding)
	}

	queryEmbedding := make([]float32, 384)
	for i := range queryEmbedding {
		queryEmbedding[i] = 0.5
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Search(ctx, VectorSearchRequest{
			QueryEmbedding: queryEmbedding,
			Limit:          20,
			MinSimilarity:  0.6,
		})
	}
}

// BenchmarkVectorStore_Store benchmarks embedding storage
func BenchmarkVectorStore_Store(b *testing.B) {
	store := NewMockVectorStore()
	ctx := context.Background()

	embedding := make([]float32, 384)
	for i := range embedding {
		embedding[i] = 0.5
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cid := "bafytest-" + string(rune(i))
		_ = store.Store(ctx, cid, embedding)
	}
}

// BenchmarkCosineSimilarity benchmarks similarity calculation
func BenchmarkCosineSimilarity(b *testing.B) {
	a := make([]float32, 384)
	b2 := make([]float32, 384)

	for i := range a {
		a[i] = float32(i) / 384.0
		b2[i] = float32(384-i) / 384.0
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cosineSimilarity(a, b2)
	}
}

// BenchmarkVectorStore_Search_ParallelQueries benchmarks concurrent searches
func BenchmarkVectorStore_Search_ParallelQueries(b *testing.B) {
	store := NewMockVectorStore()
	ctx := context.Background()

	// Pre-populate with 10K vectors
	for i := 0; i < 10000; i++ {
		embedding := make([]float32, 384)
		for j := range embedding {
			embedding[j] = float32(i%100) / 100.0
		}
		_ = store.Store(ctx, "bafytest-"+string(rune(i)), embedding)
	}

	queryEmbedding := make([]float32, 384)
	for i := range queryEmbedding {
		queryEmbedding[i] = 0.5
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = store.Search(ctx, VectorSearchRequest{
				QueryEmbedding: queryEmbedding,
				Limit:          20,
				MinSimilarity:  0.6,
			})
		}
	})
}
