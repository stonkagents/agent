// Package: pkg/embeddings
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-02 Phase 4 (Query Embedding Cache)
// Purpose: LRU cache for query embeddings to reduce latency

package embeddings

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// CacheEntry represents a cached embedding with expiration
type CacheEntry struct {
	Embedding []float32
	ExpiresAt time.Time
}

// EmbeddingCache provides LRU caching for query embeddings
type EmbeddingCache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
	maxSize int
	ttl     time.Duration
	lruList []string // Tracks access order for LRU eviction
}

// NewEmbeddingCache creates a new LRU cache for embeddings
func NewEmbeddingCache(maxSize int, ttl time.Duration) *EmbeddingCache {
	if maxSize <= 0 {
		maxSize = 1000 // Default: 1000 entries
	}
	if ttl <= 0 {
		ttl = time.Hour // Default: 1 hour TTL
	}

	return &EmbeddingCache{
		entries: make(map[string]*CacheEntry),
		maxSize: maxSize,
		ttl:     ttl,
		lruList: make([]string, 0, maxSize),
	}
}

// Get retrieves a cached embedding by query text
func (c *EmbeddingCache) Get(query string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := hashQuery(query)
	entry, exists := c.entries[key]

	if !exists {
		return nil, false
	}

	// Check expiration
	if time.Now().After(entry.ExpiresAt) {
		// Expired - remove from cache
		delete(c.entries, key)
		c.removeFromLRU(key)
		return nil, false
	}

	// Cache hit - update LRU position (move to end)
	c.removeFromLRU(key)
	c.lruList = append(c.lruList, key)

	return entry.Embedding, true
}

// Put stores an embedding in the cache
func (c *EmbeddingCache) Put(query string, embedding []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := hashQuery(query)

	// Create cache entry with expiration
	entry := &CacheEntry{
		Embedding: embedding,
		ExpiresAt: time.Now().Add(c.ttl),
	}

	// If cache is full, evict LRU entry
	if len(c.entries) >= c.maxSize && !c.exists(key) {
		c.evictLRU()
	}

	// Store entry
	c.entries[key] = entry

	// Update LRU list
	c.removeFromLRU(key) // Remove if exists
	c.lruList = append(c.lruList, key)
}

// Clear removes all entries from the cache
func (c *EmbeddingCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*CacheEntry)
	c.lruList = make([]string, 0, c.maxSize)
}

// Size returns the current number of cached entries
func (c *EmbeddingCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}

// evictLRU removes the least recently used entry
func (c *EmbeddingCache) evictLRU() {
	if len(c.lruList) == 0 {
		return
	}

	// Remove oldest entry (first in LRU list)
	lruKey := c.lruList[0]
	delete(c.entries, lruKey)
	c.lruList = c.lruList[1:]
}

// removeFromLRU removes a key from the LRU list
func (c *EmbeddingCache) removeFromLRU(key string) {
	for i, k := range c.lruList {
		if k == key {
			c.lruList = append(c.lruList[:i], c.lruList[i+1:]...)
			return
		}
	}
}

// exists checks if a key exists in the cache
func (c *EmbeddingCache) exists(key string) bool {
	_, exists := c.entries[key]
	return exists
}

// hashQuery generates SHA256 hash of query text for cache key
func hashQuery(query string) string {
	hash := sha256.Sum256([]byte(query))
	return hex.EncodeToString(hash[:])
}
