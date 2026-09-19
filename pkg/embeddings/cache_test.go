// Package: pkg/embeddings
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-02 Phase 4 (Query Embedding Cache)
// Purpose: Test query embedding cache
// TDD Phase: RED → GREEN → REFACTOR

package embeddings

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestEmbeddingCache_PutAndGet verifies basic cache operations
func TestEmbeddingCache_PutAndGet(t *testing.T) {
	cache := NewEmbeddingCache(100, time.Hour)

	embedding := []float32{1.0, 2.0, 3.0}
	query := "test query"

	// Put embedding in cache
	cache.Put(query, embedding)

	// Get embedding from cache
	retrieved, found := cache.Get(query)
	assert.True(t, found, "Embedding should be found in cache")
	assert.Equal(t, embedding, retrieved, "Retrieved embedding should match stored embedding")
}

// TestEmbeddingCache_CacheMiss verifies cache miss behavior
func TestEmbeddingCache_CacheMiss(t *testing.T) {
	cache := NewEmbeddingCache(100, time.Hour)

	_, found := cache.Get("nonexistent query")
	assert.False(t, found, "Non-existent query should not be found")
}

// TestEmbeddingCache_Expiration verifies TTL expiration
func TestEmbeddingCache_Expiration(t *testing.T) {
	cache := NewEmbeddingCache(100, 50*time.Millisecond) // Short TTL

	embedding := []float32{1.0, 2.0, 3.0}
	query := "expiring query"

	// Put embedding
	cache.Put(query, embedding)

	// Should be found immediately
	_, found := cache.Get(query)
	assert.True(t, found, "Embedding should be found before expiration")

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired now
	_, found = cache.Get(query)
	assert.False(t, found, "Embedding should be expired after TTL")
}

// TestEmbeddingCache_LRUEviction verifies LRU eviction policy
func TestEmbeddingCache_LRUEviction(t *testing.T) {
	cache := NewEmbeddingCache(3, time.Hour) // Small cache for testing

	// Fill cache to capacity
	cache.Put("query1", []float32{1.0})
	cache.Put("query2", []float32{2.0})
	cache.Put("query3", []float32{3.0})

	// All should be present
	assert.Equal(t, 3, cache.Size(), "Cache should have 3 entries")

	// Add fourth entry - should evict query1 (LRU)
	cache.Put("query4", []float32{4.0})

	// Verify eviction
	assert.Equal(t, 3, cache.Size(), "Cache should still have 3 entries")

	_, found := cache.Get("query1")
	assert.False(t, found, "query1 should be evicted (LRU)")

	_, found = cache.Get("query4")
	assert.True(t, found, "query4 should be present")
}

// TestEmbeddingCache_LRUUpdateOnAccess verifies LRU updates on cache hit
func TestEmbeddingCache_LRUUpdateOnAccess(t *testing.T) {
	cache := NewEmbeddingCache(3, time.Hour)

	// Fill cache
	cache.Put("query1", []float32{1.0})
	cache.Put("query2", []float32{2.0})
	cache.Put("query3", []float32{3.0})

	// Access query1 - should move to end of LRU list
	cache.Get("query1")

	// Add fourth entry - should evict query2 (now LRU)
	cache.Put("query4", []float32{4.0})

	// Verify query1 is still present (accessed recently)
	_, found := cache.Get("query1")
	assert.True(t, found, "query1 should be present (accessed recently)")

	// Verify query2 was evicted
	_, found = cache.Get("query2")
	assert.False(t, found, "query2 should be evicted (LRU)")
}

// TestEmbeddingCache_Clear verifies cache clearing
func TestEmbeddingCache_Clear(t *testing.T) {
	cache := NewEmbeddingCache(100, time.Hour)

	// Add entries
	cache.Put("query1", []float32{1.0})
	cache.Put("query2", []float32{2.0})

	assert.Equal(t, 2, cache.Size(), "Cache should have 2 entries")

	// Clear cache
	cache.Clear()

	assert.Equal(t, 0, cache.Size(), "Cache should be empty after Clear()")

	_, found := cache.Get("query1")
	assert.False(t, found, "query1 should not be found after Clear()")
}

// TestEmbeddingCache_ConcurrentAccess verifies thread safety
func TestEmbeddingCache_ConcurrentAccess(t *testing.T) {
	cache := NewEmbeddingCache(1000, time.Hour)

	// Run concurrent Put and Get operations
	done := make(chan bool, 100)

	for i := 0; i < 50; i++ {
		go func(id int) {
			defer func() { done <- true }()
			query := "query" + string(rune(id))
			embedding := []float32{float32(id)}
			cache.Put(query, embedding)
		}(i)
	}

	for i := 0; i < 50; i++ {
		go func(id int) {
			defer func() { done <- true }()
			query := "query" + string(rune(id))
			cache.Get(query)
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}

	// Verify cache is consistent (no panics or race conditions)
	assert.True(t, cache.Size() > 0, "Cache should have entries after concurrent access")
}

// TestEmbeddingCache_HashQuery verifies query hashing
func TestEmbeddingCache_HashQuery(t *testing.T) {
	// Same query should produce same hash
	hash1 := hashQuery("test query")
	hash2 := hashQuery("test query")
	assert.Equal(t, hash1, hash2, "Same query should produce same hash")

	// Different queries should produce different hashes
	hash3 := hashQuery("different query")
	assert.NotEqual(t, hash1, hash3, "Different queries should produce different hashes")

	// Hash should be SHA256 (64 hex characters)
	assert.Len(t, hash1, 64, "SHA256 hash should be 64 characters")
}

// TestEmbeddingCache_DefaultValues verifies default configuration
func TestEmbeddingCache_DefaultValues(t *testing.T) {
	// Create cache with invalid values - should use defaults
	cache := NewEmbeddingCache(0, 0)

	// Should use default max size (1000)
	// Fill cache beyond default size to verify
	for i := 0; i < 1500; i++ {
		cache.Put("query"+string(rune(i)), []float32{float32(i)})
	}

	assert.LessOrEqual(t, cache.Size(), 1000, "Cache should not exceed default max size (1000)")
}
