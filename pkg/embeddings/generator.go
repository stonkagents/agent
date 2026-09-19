// Package: pkg/embeddings
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 (Embedding Generation Service)
// Purpose: Generate semantic embeddings from text using sentence-transformers
// Model: all-MiniLM-L6-v2 (384-dimensional embeddings)

package embeddings

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

// ============================================================================
// Interface: EmbeddingGenerator
// ============================================================================

// EmbeddingGenerator generates semantic embeddings from text
type EmbeddingGenerator interface {
	// Generate creates embedding from text (filename + description)
	Generate(ctx context.Context, text string) ([]float32, error)

	// GenerateBatch creates embeddings for multiple texts (optimized for bulk operations)
	GenerateBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns embedding dimensions (e.g., 384 for all-MiniLM-L6-v2)
	Dimensions() int

	// ModelID returns model identifier (e.g., "all-MiniLM-L6-v2")
	ModelID() string
}

// ============================================================================
// SentenceTransformerGenerator: ONNX-based implementation
// ============================================================================

// SentenceTransformerGenerator implements EmbeddingGenerator using ONNX Runtime
// Model: all-MiniLM-L6-v2 from sentence-transformers
// For MVP: Uses deterministic hash-based embeddings until ONNX integration complete
type SentenceTransformerGenerator struct {
	modelPath  string
	dimensions int
	modelID    string
	cache      *EmbeddingCache // Optional: nil if caching disabled (F-012, US-012-02 Phase 4)
}

// NewSentenceTransformerGenerator creates a new embedding generator
// For MVP: Returns mock generator (ONNX Runtime integration is future work)
func NewSentenceTransformerGenerator(modelPath string) (*SentenceTransformerGenerator, error) {
	// SECURITY: Validate model path
	if modelPath == "" {
		return nil, fmt.Errorf("embedding generator failed: invalid model path: empty string")
	}

	// For MVP: Create generator without loading actual model
	// TODO(Sprint 6): Integrate ONNX Runtime for production model inference
	return &SentenceTransformerGenerator{
		modelPath:  modelPath,
		dimensions: 384, // all-MiniLM-L6-v2 dimensions
		modelID:    "all-MiniLM-L6-v2",
		cache:      nil, // Caching disabled by default
	}, nil
}

// WithCache enables query embedding caching with LRU eviction
// Feature: F-012, US-012-02 Phase 4
// Reduces latency for repeat queries (e.g., popular searches)
func (g *SentenceTransformerGenerator) WithCache(maxSize int, ttl time.Duration) *SentenceTransformerGenerator {
	g.cache = NewEmbeddingCache(maxSize, ttl)
	return g
}

// Generate creates embedding from text
// For MVP: Uses deterministic hash-based embedding (normalized 384-dim vector)
// PRODUCTION: Will use ONNX Runtime inference with actual model
// Feature: F-012, US-012-02 Phase 4 - Uses LRU cache to reduce latency for repeat queries
func (g *SentenceTransformerGenerator) Generate(ctx context.Context, text string) ([]float32, error) {
	// SECURITY: Validate input
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("embedding generation failed: invalid text: empty string after trimming")
	}

	// Check cache first (if enabled)
	if g.cache != nil {
		if cached, found := g.cache.Get(trimmed); found {
			return cached, nil
		}
	}

	// Check context cancellation
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("embedding generation cancelled: %w", ctx.Err())
	default:
	}

	// MVP: Generate deterministic embedding from text hash
	// This allows semantic search to work end-to-end while ONNX integration is in progress
	embedding := g.generateHashBasedEmbedding(text)

	// SECURITY: Validate generated embedding
	if !isValidEmbedding(embedding) {
		return nil, fmt.Errorf("embedding generation failed: generated invalid embedding")
	}

	// Store in cache (if enabled)
	if g.cache != nil {
		g.cache.Put(trimmed, embedding)
	}

	return embedding, nil
}

// GenerateBatch creates embeddings for multiple texts
// For MVP: Calls Generate() sequentially
// PRODUCTION: Will use batched ONNX inference for better performance
func (g *SentenceTransformerGenerator) GenerateBatch(ctx context.Context, texts []string) ([][]float32, error) {
	// SECURITY: Validate input
	if len(texts) == 0 {
		return nil, fmt.Errorf("batch embedding generation failed: invalid texts: empty slice")
	}

	embeddings := make([][]float32, 0, len(texts))

	for i, text := range texts {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("batch embedding generation cancelled at index %d: %w", i, ctx.Err())
		default:
		}

		embedding, err := g.Generate(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("batch embedding generation failed at index %d: %w", i, err)
		}

		embeddings = append(embeddings, embedding)
	}

	return embeddings, nil
}

// Dimensions returns embedding dimensions (384 for all-MiniLM-L6-v2)
func (g *SentenceTransformerGenerator) Dimensions() int {
	return g.dimensions
}

// ModelID returns model identifier
func (g *SentenceTransformerGenerator) ModelID() string {
	return g.modelID
}

// ============================================================================
// Helper Functions: Hash-based Embedding (MVP Placeholder)
// ============================================================================

// generateHashBasedEmbedding creates deterministic embedding from text hash
// This is a placeholder until ONNX Runtime integration is complete
// Properties:
// - Deterministic: same text → same embedding
// - Normalized: L2 norm ≈ 1.0
// - Distributed: values spread across [-1, 1]
// - Unique: different texts → different embeddings (high probability)
//
// NOTE: This is NOT semantically meaningful - similar texts won't have similar embeddings
// For MVP testing only. PRODUCTION will use actual sentence-transformers model.
func (g *SentenceTransformerGenerator) generateHashBasedEmbedding(text string) []float32 {
	// Lowercase and trim for basic normalization
	normalized := strings.ToLower(strings.TrimSpace(text))

	// Generate SHA-256 hash (32 bytes = 256 bits)
	hash := sha256.Sum256([]byte(normalized))

	// Convert hash bytes to 384 float32 values
	embedding := make([]float32, g.dimensions)

	// Use hash bytes to seed pseudo-random values
	// Divide hash into chunks and generate values deterministically
	for i := 0; i < g.dimensions; i++ {
		// Use hash bytes cyclically
		byteIndex := (i * 4) % len(hash)

		// Convert 4 bytes to uint32
		var seed uint32
		if byteIndex+4 <= len(hash) {
			seed = binary.BigEndian.Uint32(hash[byteIndex : byteIndex+4])
		} else {
			// Wrap around for last few dimensions
			seed = uint32(hash[byteIndex%len(hash)])
		}

		// Convert to float in range [-1, 1]
		embedding[i] = (float32(seed)/float32(math.MaxUint32))*2.0 - 1.0
	}

	// Normalize to unit length (L2 norm = 1.0)
	embedding = normalizeEmbedding(embedding)

	return embedding
}

// normalizeEmbedding normalizes embedding to unit length (L2 norm = 1.0)
func normalizeEmbedding(embedding []float32) []float32 {
	// Calculate L2 norm
	var sumSquares float64
	for _, val := range embedding {
		sumSquares += float64(val * val)
	}
	norm := math.Sqrt(sumSquares)

	// Avoid division by zero
	if norm < 1e-6 {
		return embedding
	}

	// Normalize
	normalized := make([]float32, len(embedding))
	for i, val := range embedding {
		normalized[i] = float32(float64(val) / norm)
	}

	return normalized
}

// ============================================================================
// Mock Implementation: For Testing
// ============================================================================

// MockEmbeddingGenerator is a mock implementation for testing
// Generates random embeddings with correct dimensions
type MockEmbeddingGenerator struct {
	dimensions int
}

// NewMockEmbeddingGenerator creates a new mock generator
func NewMockEmbeddingGenerator(dimensions int) *MockEmbeddingGenerator {
	return &MockEmbeddingGenerator{
		dimensions: dimensions,
	}
}

// Generate creates a mock embedding
func (m *MockEmbeddingGenerator) Generate(ctx context.Context, text string) ([]float32, error) {
	// Validate input
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("mock embedding generation failed: empty text")
	}

	// Generate deterministic embedding from text hash (same as sentence transformer MVP)
	hash := sha256.Sum256([]byte(text))
	embedding := make([]float32, m.dimensions)

	for i := 0; i < m.dimensions; i++ {
		byteIndex := (i * 4) % len(hash)
		var seed uint32
		if byteIndex+4 <= len(hash) {
			seed = binary.BigEndian.Uint32(hash[byteIndex : byteIndex+4])
		} else {
			seed = uint32(hash[byteIndex%len(hash)])
		}
		embedding[i] = (float32(seed)/float32(math.MaxUint32))*2.0 - 1.0
	}

	// Normalize
	return normalizeEmbedding(embedding), nil
}

// GenerateBatch creates mock embeddings for multiple texts
func (m *MockEmbeddingGenerator) GenerateBatch(ctx context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, 0, len(texts))
	for _, text := range texts {
		embedding, err := m.Generate(ctx, text)
		if err != nil {
			return nil, err
		}
		embeddings = append(embeddings, embedding)
	}
	return embeddings, nil
}

// Dimensions returns embedding dimensions
func (m *MockEmbeddingGenerator) Dimensions() int {
	return m.dimensions
}

// ModelID returns mock model identifier
func (m *MockEmbeddingGenerator) ModelID() string {
	return "mock"
}

// ============================================================================
// Validation: isValidEmbedding (Phase 3)
// ============================================================================

// isValidEmbedding checks if embedding is valid (no NaN/Inf, reasonable norm)
// SECURITY: Prevents poisoned embeddings from entering the system
func isValidEmbedding(embedding []float32) bool {
	if len(embedding) == 0 {
		return false
	}

	// Check for NaN or Inf values
	for _, val := range embedding {
		if math.IsNaN(float64(val)) || math.IsInf(float64(val), 0) {
			return false
		}
	}

	// Calculate L2 norm
	var sumSquares float64
	for _, val := range embedding {
		sumSquares += float64(val * val)
	}
	norm := math.Sqrt(sumSquares)

	// Check norm is within reasonable range (0.8 to 1.2 for normalized embeddings)
	// Allow some tolerance for floating-point precision
	if norm < 0.8 || norm > 1.2 {
		return false
	}

	// Check not all zeros (trivial embedding)
	allZero := true
	for _, val := range embedding {
		if val != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return false
	}

	return true
}
