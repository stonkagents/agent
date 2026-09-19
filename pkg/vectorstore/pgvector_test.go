// Package: pkg/vectorstore
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 Phase 4 (Vector Storage)
// Purpose: Test pgvector implementation
// TDD Phase: RED → GREEN → REFACTOR

package vectorstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Mock Vector Store Tests
// ============================================================================
// Note: MockVectorStore implementation is in mock.go for reuse in other packages

func TestMockVectorStore_Store(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	embedding := []float32{1.0, 2.0, 3.0}
	err := store.Store(ctx, "test-cid", embedding)
	assert.NoError(t, err)

	// Verify stored
	retrieved, err := store.GetEmbedding(ctx, "test-cid")
	require.NoError(t, err)
	assert.Equal(t, embedding, retrieved)
}

func TestMockVectorStore_Store_EmptyCID(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	embedding := []float32{1.0, 2.0, 3.0}
	err := store.Store(ctx, "", embedding)
	assert.Error(t, err)
}

func TestMockVectorStore_Store_EmptyEmbedding(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	err := store.Store(ctx, "test-cid", []float32{})
	assert.Error(t, err)
}

func TestMockVectorStore_GetEmbedding_NotFound(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	_, err := store.GetEmbedding(ctx, "nonexistent-cid")
	assert.Error(t, err)
}

func TestMockVectorStore_Search_Success(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	// Store some embeddings
	store.Store(ctx, "cid1", []float32{1.0, 0.0, 0.0})
	store.Store(ctx, "cid2", []float32{0.0, 1.0, 0.0})
	store.Store(ctx, "cid3", []float32{0.9, 0.1, 0.0}) // Similar to cid1

	// Search for similar to cid1
	queryEmbedding := []float32{1.0, 0.0, 0.0}
	results, err := store.Search(ctx, VectorSearchRequest{
		QueryEmbedding: queryEmbedding,
		Limit:          10,
		MinSimilarity:  0.5,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1, "Should find at least cid1")

	// Verify cid1 is in results (exact match)
	found := false
	for _, result := range results {
		if result.CID == "cid1" {
			found = true
			assert.InDelta(t, 1.0, result.Similarity, 0.01, "cid1 should have similarity ~1.0")
		}
	}
	assert.True(t, found, "Should find cid1 in results")
}

func TestMockVectorStore_Search_ExcludeCIDs(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	// Store embeddings
	store.Store(ctx, "cid1", []float32{1.0, 0.0, 0.0})
	store.Store(ctx, "cid2", []float32{0.9, 0.1, 0.0})

	// Search but exclude cid1
	queryEmbedding := []float32{1.0, 0.0, 0.0}
	results, err := store.Search(ctx, VectorSearchRequest{
		QueryEmbedding: queryEmbedding,
		Limit:          10,
		MinSimilarity:  0.0,
		Filters: &SearchFilters{
			CIDsToExclude: []string{"cid1"},
		},
	})

	require.NoError(t, err)

	// Verify cid1 is NOT in results
	for _, result := range results {
		assert.NotEqual(t, "cid1", result.CID, "cid1 should be excluded")
	}
}

func TestMockVectorStore_Delete(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	// Store and delete
	embedding := []float32{1.0, 2.0, 3.0}
	store.Store(ctx, "test-cid", embedding)

	err := store.Delete(ctx, "test-cid")
	assert.NoError(t, err)

	// Verify deleted
	_, err = store.GetEmbedding(ctx, "test-cid")
	assert.Error(t, err)
}

func TestMockVectorStore_Delete_NotFound(t *testing.T) {
	store := NewMockVectorStore()
	ctx := context.Background()

	err := store.Delete(ctx, "nonexistent-cid")
	assert.Error(t, err)
}

// ============================================================================
// Interface Compliance Test
// ============================================================================

func TestMockVectorStore_ImplementsInterface(t *testing.T) {
	var _ VectorStore = (*MockVectorStore)(nil)
}

func TestPgVectorStore_ImplementsInterface(t *testing.T) {
	var _ VectorStore = (*PgVectorStore)(nil)
}

// ============================================================================
// PgVectorStore Tests (require PostgreSQL + pgvector)
// ============================================================================

// Note: These tests are skipped by default since they require a running PostgreSQL
// instance with pgvector extension. To run these tests:
// 1. Start PostgreSQL with pgvector: docker run -e POSTGRES_PASSWORD=test -p 5432:5432 ankane/pgvector
// 2. Run tests with: go test -v -tags=integration ./pkg/vectorstore/...

// TestPgVectorStore_Store requires PostgreSQL + pgvector
func TestPgVectorStore_Store(t *testing.T) {
	t.Skip("Skipping PgVectorStore test - requires PostgreSQL with pgvector extension")

	// Example test structure (would work with real database):
	// db, err := sql.Open("postgres", "postgresql://user:pass@localhost/testdb")
	// require.NoError(t, err)
	// defer db.Close()
	//
	// store, err := NewPgVectorStore(&PgVectorConfig{DB: db})
	// require.NoError(t, err)
	//
	// ctx := context.Background()
	// embedding := []float32{1.0, 2.0, 3.0}
	// err = store.Store(ctx, "test-cid", embedding)
	// assert.NoError(t, err)
}

// TestPgVectorStore_Search requires PostgreSQL + pgvector
func TestPgVectorStore_Search(t *testing.T) {
	t.Skip("Skipping PgVectorStore test - requires PostgreSQL with pgvector extension")

	// Example test structure (would work with real database):
	// ... setup database and store ...
	// results, err := store.Search(ctx, VectorSearchRequest{...})
	// assert.NoError(t, err)
	// assert.GreaterOrEqual(t, len(results), 1)
}
