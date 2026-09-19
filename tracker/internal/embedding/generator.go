// Package: tracker/internal/embedding
// Feature: F-002 (Intelligent Data Sharing)
// Story: US-002-01 (Embedding Generation & Vector Storage)
// Purpose: Generate embeddings for semantic search with graceful fallback

package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"
)

const (
	// EmbeddingDimensions is the size of embedding vectors (all-MiniLM-L6-v2: 384 dims)
	EmbeddingDimensions = 384

	// DefaultTimeout for embedding API calls
	DefaultTimeout = 30 * time.Second
)

// GeneratorOptions configures the embedding generator
type GeneratorOptions struct {
	// ModelEndpoint is the HTTP endpoint for embedding generation (e.g., Ollama API)
	// Default: http://localhost:11434 (Ollama default port)
	ModelEndpoint string

	// ModelName is the embedding model to use
	// Default: all-minilm (lightweight, 384 dimensions)
	ModelName string

	// Timeout for HTTP requests
	Timeout time.Duration
}

// Generator generates embeddings for text using an external model or fallback
type Generator struct {
	endpoint string
	model    string
	timeout  time.Duration
	client   *http.Client
}

// NewGenerator creates a new embedding generator with fallback support
func NewGenerator(opts GeneratorOptions) *Generator {
	endpoint := opts.ModelEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434" // Ollama default
	}

	model := opts.ModelName
	if model == "" {
		model = "all-minilm" // Lightweight embedding model
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	return &Generator{
		endpoint: endpoint,
		model:    model,
		timeout:  timeout,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// GenerateEmbedding generates a 384-dim embedding vector for the given text.
// Falls back to zero vector if model unavailable (graceful degradation).
func (g *Generator) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	// Empty text → zero vector (no error)
	if text == "" {
		return make([]float32, EmbeddingDimensions), nil
	}

	// Try to generate embedding from model
	embedding, err := g.callEmbeddingAPI(ctx, text)
	if err != nil {
		// FALLBACK: Log warning and return zero vector (graceful degradation)
		slog.Warn("[Generator.GenerateEmbedding] model unavailable, using zero vector fallback", "error", err)
		return make([]float32, EmbeddingDimensions), nil
	}

	// Normalize embedding to unit length (L2 normalization)
	normalized := normalizeVector(embedding)

	return normalized, nil
}

// callEmbeddingAPI calls the embedding model HTTP API
func (g *Generator) callEmbeddingAPI(ctx context.Context, text string) ([]float32, error) {
	// Ollama API format: POST /api/embeddings
	// Request: { "model": "all-minilm", "prompt": "text" }
	// Response: { "embedding": [0.1, 0.2, ...] }

	reqBody := map[string]interface{}{
		"model":  g.model,
		"prompt": text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build API endpoint URL
	url := fmt.Sprintf("%s/api/embeddings", g.endpoint)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call embedding API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp struct {
		Embedding []float32 `json:"embedding"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned from API")
	}

	// Pad or truncate to exactly 384 dimensions
	embedding := make([]float32, EmbeddingDimensions)
	copy(embedding, apiResp.Embedding)

	return embedding, nil
}

// normalizeVector performs L2 normalization (unit length)
func normalizeVector(vec []float32) []float32 {
	// Calculate L2 norm (magnitude)
	var sumSquares float32
	for _, val := range vec {
		sumSquares += val * val
	}

	magnitude := float32(math.Sqrt(float64(sumSquares)))

	// If magnitude is zero, return as-is (avoid divide by zero)
	if magnitude == 0 {
		return vec
	}

	// Normalize to unit length
	normalized := make([]float32, len(vec))
	for i, val := range vec {
		normalized[i] = val / magnitude
	}

	return normalized
}

// GenerateEmbeddingForAsset generates embedding from asset metadata (filename + description + tags)
// This is a helper for asset announcement flow
func (g *Generator) GenerateEmbeddingForAsset(ctx context.Context, filename, description string, tags []string) ([]float32, error) {
	// Concatenate metadata into single text
	text := filename

	if description != "" {
		text += " " + description
	}

	for _, tag := range tags {
		text += " " + tag
	}

	return g.GenerateEmbedding(ctx, text)
}
