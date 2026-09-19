// Package: pkg/vectorstore
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 Phase 4 (Vector Storage)
// Purpose: pgvector implementation for PostgreSQL-based vector storage

package vectorstore

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/pgvector/pgvector-go"
)

// validTableName matches valid SQL identifiers: starts with letter or underscore,
// followed by letters, digits, or underscores. Prevents SQL injection via table name.
var validTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// PgVectorStore implements VectorStore using PostgreSQL + pgvector extension
type PgVectorStore struct {
	db      *sql.DB
	table   string // Table name (default: "embeddings")
	modelID string // Model identifier (e.g., "all-MiniLM-L6-v2")
}

// PgVectorConfig holds configuration for PgVectorStore
type PgVectorConfig struct {
	DB      *sql.DB // PostgreSQL database connection
	Table   string  // Table name (default: "embeddings")
	ModelID string  // Model identifier (default: "all-MiniLM-L6-v2")
}

// NewPgVectorStore creates a new pgvector-based vector store
func NewPgVectorStore(config *PgVectorConfig) (*PgVectorStore, error) {
	// SECURITY: Validate config
	if config == nil {
		return nil, fmt.Errorf("pgvector store creation failed: config is nil")
	}

	// Set defaults
	table := config.Table
	if table == "" {
		table = "embeddings"
	}

	// SECURITY(TD-079): Validate table name to prevent SQL injection via fmt.Sprintf
	if !validTableName.MatchString(table) {
		return nil, fmt.Errorf("pgvector store creation failed: invalid table name: %q", table)
	}

	if config.DB == nil {
		return nil, fmt.Errorf("pgvector store creation failed: database connection is nil")
	}

	modelID := config.ModelID
	if modelID == "" {
		modelID = "all-MiniLM-L6-v2"
	}

	return &PgVectorStore{
		db:      config.DB,
		table:   table,
		modelID: modelID,
	}, nil
}

// Store saves an embedding with associated CID
func (s *PgVectorStore) Store(ctx context.Context, cid string, embedding []float32) error {
	// SECURITY: Validate inputs
	if cid == "" {
		return fmt.Errorf("vector store failed: invalid CID: empty string")
	}
	if len(embedding) == 0 {
		return fmt.Errorf("vector store failed: invalid embedding: empty slice")
	}

	// Convert []float32 to pgvector.Vector
	vector := pgvector.NewVector(embedding)

	// UPSERT: Insert or update if CID already exists
	query := fmt.Sprintf(`
		INSERT INTO %s (cid, embedding, model_id, created_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (cid)
		DO UPDATE SET
			embedding = EXCLUDED.embedding,
			model_id = EXCLUDED.model_id,
			created_at = NOW()
	`, s.table)

	_, err := s.db.ExecContext(ctx, query, cid, vector, s.modelID)
	if err != nil {
		return fmt.Errorf("vector store failed for CID %s: %w", cid, err)
	}

	return nil
}

// GetEmbedding retrieves embedding for a CID
func (s *PgVectorStore) GetEmbedding(ctx context.Context, cid string) ([]float32, error) {
	// SECURITY: Validate input
	if cid == "" {
		return nil, fmt.Errorf("get embedding failed: invalid CID: empty string")
	}

	query := fmt.Sprintf("SELECT embedding FROM %s WHERE cid = $1", s.table)

	var vector pgvector.Vector
	err := s.db.QueryRowContext(ctx, query, cid).Scan(&vector)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("get embedding failed: CID %s not found", cid)
	}
	if err != nil {
		return nil, fmt.Errorf("get embedding failed for CID %s: %w", cid, err)
	}

	return vector.Slice(), nil
}

// Search finds similar embeddings using cosine similarity
// Uses pgvector's <=> operator for cosine distance (1 - cosine_similarity)
func (s *PgVectorStore) Search(ctx context.Context, req VectorSearchRequest) ([]VectorSearchResult, error) {
	// SECURITY: Validate request
	if len(req.QueryEmbedding) == 0 {
		return nil, fmt.Errorf("vector search failed: invalid query embedding: empty slice")
	}
	if req.Limit <= 0 {
		req.Limit = 20 // Default limit
	}
	if req.MinSimilarity < 0 || req.MinSimilarity > 1 {
		req.MinSimilarity = 0.6 // Default minimum similarity
	}

	// Convert query embedding to pgvector.Vector
	queryVector := pgvector.NewVector(req.QueryEmbedding)

	// Build WHERE clause for filters
	whereClause := "WHERE 1=1"
	args := []interface{}{queryVector, req.MinSimilarity, req.Limit}
	argIndex := 4

	// Filter: Exclude specific CIDs
	if req.Filters != nil && len(req.Filters.CIDsToExclude) > 0 {
		placeholders := make([]string, len(req.Filters.CIDsToExclude))
		for i, cid := range req.Filters.CIDsToExclude {
			placeholders[i] = fmt.Sprintf("$%d", argIndex)
			args = append(args, cid)
			argIndex++
		}
		whereClause += fmt.Sprintf(" AND cid NOT IN (%s)", strings.Join(placeholders, ","))
	}

	// Filter: Model ID
	if req.Filters != nil && req.Filters.ModelID != "" {
		whereClause += fmt.Sprintf(" AND model_id = $%d", argIndex)
		args = append(args, req.Filters.ModelID)
		argIndex++
	}

	// pgvector cosine distance: <=> operator returns (1 - cosine_similarity)
	// So similarity = 1 - distance
	query := fmt.Sprintf(`
		SELECT cid, 1 - (embedding <=> $1) AS similarity
		FROM %s
		%s
		  AND 1 - (embedding <=> $1) >= $2
		ORDER BY embedding <=> $1
		LIMIT $3
	`, s.table, whereClause)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}
	defer rows.Close()

	results := make([]VectorSearchResult, 0, req.Limit)
	for rows.Next() {
		var result VectorSearchResult
		if err := rows.Scan(&result.CID, &result.Similarity); err != nil {
			return nil, fmt.Errorf("vector search failed: error scanning result: %w", err)
		}
		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vector search failed: error iterating results: %w", err)
	}

	return results, nil
}

// Delete removes embedding for a CID
func (s *PgVectorStore) Delete(ctx context.Context, cid string) error {
	// SECURITY: Validate input
	if cid == "" {
		return fmt.Errorf("delete embedding failed: invalid CID: empty string")
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE cid = $1", s.table)

	result, err := s.db.ExecContext(ctx, query, cid)
	if err != nil {
		return fmt.Errorf("delete embedding failed for CID %s: %w", cid, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete embedding failed: could not get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("delete embedding failed: CID %s not found", cid)
	}

	return nil
}

// Close closes the database connection
func (s *PgVectorStore) Close() error {
	// Note: We don't close the DB here because it's shared
	// The caller is responsible for closing the DB connection
	return nil
}
