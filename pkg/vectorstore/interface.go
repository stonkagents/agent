// Package: pkg/vectorstore
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 Phase 4 (Vector Storage)
// Purpose: Interface for vector database operations (pgvector, Qdrant, etc.)

package vectorstore

import (
	"context"
)

// VectorStore defines operations for storing and searching embeddings
type VectorStore interface {
	// Store saves an embedding with associated CID
	Store(ctx context.Context, cid string, embedding []float32) error

	// GetEmbedding retrieves embedding for a CID
	GetEmbedding(ctx context.Context, cid string) ([]float32, error)

	// Search finds similar embeddings using vector similarity
	Search(ctx context.Context, req VectorSearchRequest) ([]VectorSearchResult, error)

	// Delete removes embedding for a CID
	Delete(ctx context.Context, cid string) error

	// Close closes the vector store connection
	Close() error
}

// VectorSearchRequest defines parameters for vector similarity search
type VectorSearchRequest struct {
	QueryEmbedding []float32 // The embedding to search for
	Limit          int       // Maximum number of results (default: 20)
	MinSimilarity  float32   // Minimum cosine similarity (0.0-1.0, default: 0.6)
	Filters        *SearchFilters
}

// SearchFilters defines optional filters for search results
type SearchFilters struct {
	CIDsToExclude []string // Exclude specific CIDs (e.g., original CID for "related" search)
	ModelID       string   // Filter by embedding model (e.g., "all-MiniLM-L6-v2")
}

// VectorSearchResult represents a search result with similarity score
type VectorSearchResult struct {
	CID        string  // Content ID
	Similarity float32 // Cosine similarity score (0.0-1.0)
}
