// Package: pkg/embeddings
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-01 Phase 3 (Embedding Validation)
// Purpose: Validate embeddings to detect poisoning attacks
// Security: Multi-layer validation (basic + spot-check + outlier detection)

package embeddings

import (
	"fmt"
	"math"
)

// ============================================================================
// Basic Validation (Layer 1): NaN/Inf/Norm Checks
// ============================================================================

// ValidateEmbedding performs basic validation on embedding
// Returns error if embedding is invalid
// SECURITY: Prevents NaN, Inf, zero, and malformed embeddings
func ValidateEmbedding(embedding []float32) error {
	if len(embedding) == 0 {
		return fmt.Errorf("embedding validation failed: empty embedding")
	}

	// Check not all zeros first (trivial embedding)
	// This must come before norm check since all-zero has norm 0.0
	allZero := true
	for _, val := range embedding {
		if val != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return fmt.Errorf("embedding validation failed: all-zero embedding (trivial)")
	}

	// Check for NaN or Inf values
	for i, val := range embedding {
		if math.IsNaN(float64(val)) {
			return fmt.Errorf("embedding validation failed: NaN at index %d", i)
		}
		if math.IsInf(float64(val), 0) {
			return fmt.Errorf("embedding validation failed: Inf at index %d", i)
		}
	}

	// Calculate L2 norm
	norm := calculateNorm(embedding)

	// Check norm is within reasonable range (0.8 to 1.2 for normalized embeddings)
	if norm < 0.8 || norm > 1.2 {
		return fmt.Errorf("embedding validation failed: L2 norm %.4f outside valid range [0.8, 1.2]", norm)
	}

	return nil
}

// ============================================================================
// Spot-Check Validation (Layer 2): Similarity to Known Good Embeddings
// ============================================================================

// SpotCheckConfig holds configuration for spot-check validation
type SpotCheckConfig struct {
	// MinAverageSimilarity is minimum avg cosine similarity to sample embeddings
	// Default: 0.1 (embeddings should not be complete outliers)
	MinAverageSimilarity float32

	// PopularQueries are queries that attackers might try to exploit
	// Spam detection: embeddings suspiciously similar to popular queries
	PopularQueries []string

	// SpamSimilarityThreshold is max similarity to popular queries
	// Default: 0.95 (embedding shouldn't be nearly identical to popular query)
	SpamSimilarityThreshold float32
}

// DefaultSpotCheckConfig returns default configuration
func DefaultSpotCheckConfig() *SpotCheckConfig {
	return &SpotCheckConfig{
		MinAverageSimilarity: 0.1,
		PopularQueries: []string{
			"machine learning model",
			"financial dataset",
			"image classification",
			"time series data",
			"training data",
		},
		SpamSimilarityThreshold: 0.95,
	}
}

// SpotCheckEmbedding validates embedding against sample embeddings
// SECURITY: Detects outliers and potential spam embeddings
func SpotCheckEmbedding(embedding []float32, sampleEmbeddings [][]float32, config *SpotCheckConfig) error {
	if config == nil {
		config = DefaultSpotCheckConfig()
	}

	// Skip if no samples available
	if len(sampleEmbeddings) == 0 {
		return nil // Can't perform spot-check without samples
	}

	// Calculate average similarity to sample embeddings
	avgSimilarity := calculateAverageSimilarity(embedding, sampleEmbeddings)

	// Flag if too dissimilar (outlier)
	if avgSimilarity < config.MinAverageSimilarity {
		return fmt.Errorf("embedding outlier detected: avg_similarity=%.4f (threshold: %.4f)",
			avgSimilarity, config.MinAverageSimilarity)
	}

	return nil
}

// DetectSemanticSpam checks if embedding is suspiciously similar to popular queries
// SECURITY: Prevents keyword-stuffing attacks where attackers craft descriptions
// to generate embeddings similar to popular search queries
func DetectSemanticSpam(embedding []float32, text string, generator EmbeddingGenerator, config *SpotCheckConfig) error {
	if config == nil {
		config = DefaultSpotCheckConfig()
	}

	// For each popular query, check if embedding is suspiciously similar
	for _, query := range config.PopularQueries {
		// Generate embedding for popular query (cached in production)
		queryEmbedding, err := generator.Generate(nil, query)
		if err != nil {
			continue // Skip if can't generate query embedding
		}

		// Calculate similarity
		similarity := cosineSimilarity(embedding, queryEmbedding)

		// Check if suspiciously high AND text doesn't contain the query keywords
		if similarity > config.SpamSimilarityThreshold {
			// If text actually contains the query, it's legitimate
			if containsKeywords(text, query) {
				continue
			}

			// Otherwise, it's potential spam
			return fmt.Errorf("semantic spam detected: similarity=%.4f to query='%s' without keyword match",
				similarity, query)
		}
	}

	return nil
}

// ============================================================================
// Helper Functions: Similarity Calculations
// ============================================================================

// calculateNorm calculates L2 norm of embedding
func calculateNorm(embedding []float32) float32 {
	var sumSquares float64
	for _, val := range embedding {
		sumSquares += float64(val * val)
	}
	return float32(math.Sqrt(sumSquares))
}

// cosineSimilarity calculates cosine similarity between two embeddings
// Returns value in range [-1, 1], where 1 = identical, 0 = orthogonal, -1 = opposite
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0 // Incompatible dimensions
	}

	// Calculate dot product and norms
	var dotProduct float64
	var normA, normB float64

	for i := 0; i < len(a); i++ {
		dotProduct += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}

	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)

	// Avoid division by zero
	if normA < 1e-6 || normB < 1e-6 {
		return 0
	}

	return float32(dotProduct / (normA * normB))
}

// calculateAverageSimilarity calculates average cosine similarity to sample embeddings
func calculateAverageSimilarity(embedding []float32, samples [][]float32) float32 {
	if len(samples) == 0 {
		return 0
	}

	var sumSimilarity float64
	for _, sample := range samples {
		similarity := cosineSimilarity(embedding, sample)
		sumSimilarity += float64(similarity)
	}

	return float32(sumSimilarity / float64(len(samples)))
}

// containsKeywords checks if text contains keywords from query (case-insensitive)
// Simple check: split query into words and verify at least 50% appear in text
func containsKeywords(text, query string) bool {
	// Normalize both to lowercase
	textLower := toLower(text)
	queryLower := toLower(query)

	// Split query into words
	words := splitWords(queryLower)
	if len(words) == 0 {
		return false
	}

	// Count how many query words appear in text
	matchCount := 0
	for _, word := range words {
		if contains(textLower, word) {
			matchCount++
		}
	}

	// Require at least 50% of query words to appear in text
	return float64(matchCount)/float64(len(words)) >= 0.5
}

// ============================================================================
// Helper Functions: String Operations
// ============================================================================

// toLower converts string to lowercase (simple ASCII)
func toLower(s string) string {
	result := make([]rune, len(s))
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			result[i] = r + ('a' - 'A')
		} else {
			result[i] = r
		}
	}
	return string(result)
}

// splitWords splits string into words (simple whitespace split)
func splitWords(s string) []string {
	var words []string
	var current []rune

	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if len(current) > 0 {
				words = append(words, string(current))
				current = nil
			}
		} else {
			current = append(current, r)
		}
	}

	if len(current) > 0 {
		words = append(words, string(current))
	}

	return words
}

// contains checks if string contains substring (simple search)
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}

	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}

	return false
}

// ============================================================================
// Batch Validation
// ============================================================================

// ValidateEmbeddings validates multiple embeddings
// Returns error on first invalid embedding
func ValidateEmbeddings(embeddings [][]float32) error {
	for i, embedding := range embeddings {
		if err := ValidateEmbedding(embedding); err != nil {
			return fmt.Errorf("batch validation failed at index %d: %w", i, err)
		}
	}
	return nil
}
