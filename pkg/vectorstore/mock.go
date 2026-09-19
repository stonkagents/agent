// Package: pkg/vectorstore
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 Phase 4 (Vector Storage)
// Purpose: Mock vector store for testing without PostgreSQL

package vectorstore

import (
	"context"
	"strings"
	"sync"

	"github.com/stretchr/testify/assert"
)

// MockVectorStore is an in-memory implementation for testing
type MockVectorStore struct {
	mu         sync.RWMutex
	embeddings map[string][]float32 // cid → embedding
}

// NewMockVectorStore creates a new mock vector store
func NewMockVectorStore() *MockVectorStore {
	return &MockVectorStore{
		embeddings: make(map[string][]float32),
	}
}

// Store saves an embedding
func (m *MockVectorStore) Store(ctx context.Context, cid string, embedding []float32) error {
	if cid == "" || strings.TrimSpace(cid) == "" {
		return assert.AnError
	}
	if len(embedding) == 0 {
		return assert.AnError
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddings[cid] = embedding
	return nil
}

// GetEmbedding retrieves an embedding
func (m *MockVectorStore) GetEmbedding(ctx context.Context, cid string) ([]float32, error) {
	if cid == "" {
		return nil, assert.AnError
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	embedding, exists := m.embeddings[cid]
	if !exists {
		return nil, assert.AnError
	}
	return embedding, nil
}

// Search finds similar embeddings (simplified mock implementation)
func (m *MockVectorStore) Search(ctx context.Context, req VectorSearchRequest) ([]VectorSearchResult, error) {
	if len(req.QueryEmbedding) == 0 {
		return nil, assert.AnError
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	results := []VectorSearchResult{}

	// Calculate similarity to all stored embeddings
	for cid, embedding := range m.embeddings {
		// Skip excluded CIDs
		if req.Filters != nil {
			excluded := false
			for _, excludeCID := range req.Filters.CIDsToExclude {
				if cid == excludeCID {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}

		// Calculate cosine similarity
		similarity := cosineSimilarity(req.QueryEmbedding, embedding)

		// Filter by minimum similarity
		if similarity >= req.MinSimilarity {
			results = append(results, VectorSearchResult{
				CID:        cid,
				Similarity: similarity,
			})
		}
	}

	// Sort by similarity (descending) and limit
	// For simplicity, just return first N results
	if len(results) > req.Limit {
		results = results[:req.Limit]
	}

	return results, nil
}

// Delete removes an embedding
func (m *MockVectorStore) Delete(ctx context.Context, cid string) error {
	if cid == "" {
		return assert.AnError
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.embeddings[cid]; !exists {
		return assert.AnError
	}
	delete(m.embeddings, cid)
	return nil
}

// Close does nothing for mock
func (m *MockVectorStore) Close() error {
	return nil
}

// Size returns the number of stored embeddings (for testing/monitoring)
func (m *MockVectorStore) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.embeddings)
}

// ============================================================================
// Helper Functions
// ============================================================================

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float32
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (sqrt(normA) * sqrt(normB))
}

func sqrt(x float32) float32 {
	if x == 0 {
		return 0
	}
	// Simple iterative square root (Newton's method)
	result := x
	for i := 0; i < 10; i++ {
		result = (result + x/result) / 2
	}
	return result
}
