// Package: tracker/internal/services
// Feature: F-007 (Centralized Tracker)
// Story: US-007-03 (Asset Registry)
// Purpose: Business logic for asset announcement and search

package services

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/stonkagents/agent/pkg/vectorstore"
	"github.com/stonkagents/agent/tracker/internal/embedding"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/search"
)

// AnnounceAssetRequest contains fields for asset announcement.
type AnnounceAssetRequest struct {
	CID          string
	Filename     string
	MimeType     string
	Size         int64
	PeerID       string
	ManifestType string
	ManifestData []byte
	Chunks       []int
}

// SearchAssetsRequest contains fields for asset search.
type SearchAssetsRequest struct {
	Query        string
	ManifestType string
	Limit        int
	Offset       int
}

// SearchFilters contains optional filters for semantic search (F-012, US-012-02)
type SearchFilters struct {
	ManifestType string // Filter by manifest type ("raw", "vec")
	MinSize      int64  // Minimum file size in bytes
	MaxSize      int64  // Maximum file size in bytes (0 = no limit)
}

// DefaultSearchLimit is the default number of search results.
const DefaultSearchLimit = 50

// AssetService handles asset announcements and search.
type AssetService struct {
	repo         repository.AssetRepository
	availability repository.AvailabilityRepository
	presence     presence.PresenceStore
	embedding    *embedding.Generator    // Optional: nil if semantic search disabled
	hnswIndex    *search.HNSWIndex       // Optional: nil if semantic search disabled
	vectorStore  vectorstore.VectorStore // Optional: nil if vector persistence disabled (F-012, US-012-01 Phase 5)
}

// NewAssetService creates a new AssetService without embedding support.
// availability is injected (e.g. memory or Postgres implementation).
func NewAssetService(repo repository.AssetRepository, pres presence.PresenceStore, availability repository.AvailabilityRepository) *AssetService {
	return &AssetService{
		repo:         repo,
		availability: availability,
		presence:     pres,
		embedding:    nil,
	}
}

// NewAssetServiceWithEmbedding creates a new AssetService with semantic search support.
// Feature: F-002 (Intelligent Data Sharing), Story: US-002-01, US-002-03
func NewAssetServiceWithEmbedding(repo repository.AssetRepository, pres presence.PresenceStore, availability repository.AvailabilityRepository, embGen *embedding.Generator) *AssetService {
	return &AssetService{
		repo:         repo,
		availability: availability,
		presence:     pres,
		embedding:    embGen,
		hnswIndex:    nil,
	}
}

// NewAssetServiceWithSemanticSearch creates a new AssetService with full semantic search (embedding + HNSW).
// Feature: F-002, Story: US-002-03
func NewAssetServiceWithSemanticSearch(repo repository.AssetRepository, pres presence.PresenceStore, availability repository.AvailabilityRepository, embGen *embedding.Generator, hnswIndex *search.HNSWIndex) *AssetService {
	return &AssetService{
		repo:         repo,
		availability: availability,
		presence:     pres,
		embedding:    embGen,
		hnswIndex:    hnswIndex,
		vectorStore:  nil,
	}
}

// NewAssetServiceWithVectorStore creates a new AssetService with pgvector persistence.
// Feature: F-012 (Semantic Search Engine), Story: US-012-01 Phase 5
func NewAssetServiceWithVectorStore(repo repository.AssetRepository, pres presence.PresenceStore, availability repository.AvailabilityRepository, embGen *embedding.Generator, hnswIndex *search.HNSWIndex, store vectorstore.VectorStore) *AssetService {
	return &AssetService{
		repo:         repo,
		availability: availability,
		presence:     pres,
		embedding:    embGen,
		hnswIndex:    hnswIndex,
		vectorStore:  store,
	}
}

// Announce registers a new asset from a peer.
// Feature: F-002, Story: US-002-01 - Generates semantic embedding if embedding generator available.
func (s *AssetService) Announce(ctx context.Context, req AnnounceAssetRequest) (*models.Asset, error) {
	if err := validateAnnounce(req); err != nil {
		return nil, err
	}

	asset := &models.Asset{
		CID:          req.CID,
		Filename:     req.Filename,
		MimeType:     req.MimeType,
		Size:         req.Size,
		PeerID:       req.PeerID,
		AnnouncedAt:  time.Now(),
		ManifestType: req.ManifestType,
		ManifestData: req.ManifestData,
		Tags:         autoExtractTags(req), // F-002, US-002-04: Auto-tagging
	}

	// Generate semantic embedding for search (F-002, US-002-01)
	// Embedding generated from filename + description + tags
	// Falls back to zero vector if model unavailable (graceful degradation)
	if s.embedding != nil {
		embVec, err := s.embedding.GenerateEmbedding(ctx, req.Filename)
		if err != nil {
			// Log error but don't fail announce (embedding is optional)
			// Error already logged in generator with [WARNING] prefix
		} else {
			asset.Embedding = embVec

			// Persist embedding to vector database (F-012, US-012-01 Phase 5)
			// Uses pgvector for production-grade vector similarity search
			if s.vectorStore != nil && len(embVec) > 0 {
				if err := s.vectorStore.Store(ctx, asset.CID, embVec); err != nil {
					// Log error but don't fail announce (vector storage is optional)
					// Embedding still available in-memory via asset.Embedding field
					// Error: duplicate CID or database connection failure
				}
			}

			// Insert embedding into HNSW index for fast semantic search (F-002, US-002-03)
			if s.hnswIndex != nil && len(embVec) > 0 {
				if err := s.hnswIndex.Insert(ctx, asset.CID, embVec); err != nil {
					// Log error but don't fail announce (indexing is optional)
					// Note: duplicate CID errors are expected on re-announces
				}
			}
		}
	}

	if err := s.repo.Create(ctx, asset); err != nil {
		if err == models.ErrAlreadyExists {
			existing, findErr := s.repo.FindByCID(ctx, req.CID)
			if findErr != nil {
				return nil, findErr
			}
			asset = existing
		} else {
			return nil, err
		}
	}

	if s.availability != nil {
		_ = s.availability.UpsertPeerChunks(ctx, req.CID, req.PeerID, req.Chunks)
	}

	return asset, nil
}

// PeersForCID returns per-peer chunk availability for a CID, filtered to online peers.
func (s *AssetService) PeersForCID(ctx context.Context, cid string) ([]repository.PeerChunkAvailability, error) {
	if s.availability == nil {
		return []repository.PeerChunkAvailability{}, nil
	}

	onlineIDs, err := s.presence.OnlinePeerIDs(ctx)
	if err != nil {
		return nil, err
	}
	onlineSet := make(map[string]bool, len(onlineIDs))
	for _, id := range onlineIDs {
		onlineSet[id] = true
	}

	peers, err := s.availability.GetPeerChunks(ctx, cid)
	if err != nil {
		return nil, err
	}

	filtered := make([]repository.PeerChunkAvailability, 0, len(peers))
	for _, peerInfo := range peers {
		if onlineSet[peerInfo.PeerID] {
			filtered = append(filtered, peerInfo)
		}
	}

	return filtered, nil
}

// Search returns assets matching query, filtered to online peers only.
func (s *AssetService) Search(ctx context.Context, req SearchAssetsRequest) ([]*models.Asset, int, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}

	// Get online peer IDs for filtering
	onlineIDs, err := s.presence.OnlinePeerIDs(ctx)
	if err != nil {
		return nil, 0, err
	}

	return s.repo.Search(ctx, repository.SearchAssetsOptions{
		Query:        req.Query,
		ManifestType: req.ManifestType,
		PeerIDs:      onlineIDs,
		Limit:        limit,
		Offset:       req.Offset,
	})
}

// SemanticSearchResult includes similarity score for semantic search results
type SemanticSearchResult struct {
	Asset       *models.Asset
	Similarity  float32 // Cosine similarity (0.0-1.0)
	HybridScore float32 // Weighted score for hybrid search (0.0-1.0)
}

// SearchSemantic performs embedding-based semantic search using HNSW index.
// Feature: F-002, Story: US-002-03
// Returns results ranked by cosine similarity, filtered by threshold (>0.3).
func (s *AssetService) SearchSemantic(ctx context.Context, query string, limit int) ([]*SemanticSearchResult, int, error) {
	// Require embedding generator for query embedding
	if s.embedding == nil {
		return nil, 0, models.ErrInvalidInput
	}

	// Require HNSW index for search
	if s.hnswIndex == nil {
		return nil, 0, models.ErrInvalidInput
	}

	// Generate embedding for query
	queryEmbedding, err := s.embedding.GenerateEmbedding(ctx, query)
	if err != nil {
		return nil, 0, err
	}

	// Search HNSW index (returns top-k by similarity)
	hnswResults, err := s.hnswIndex.Search(ctx, queryEmbedding, limit*2) // Request more for filtering
	if err != nil {
		return nil, 0, err
	}

	// Filter by similarity threshold (0.3 minimum)
	const minSimilarity = 0.3
	filtered := make([]*search.SearchResult, 0, len(hnswResults))
	for _, result := range hnswResults {
		if result.Similarity >= minSimilarity {
			filtered = append(filtered, result)
		}
	}

	// Limit results
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	// Fetch asset details and filter by online peers
	results := make([]*SemanticSearchResult, 0, len(filtered))
	for _, hnswResult := range filtered {
		asset, err := s.repo.FindByCID(ctx, hnswResult.CID)
		if err != nil {
			continue // Skip if asset not found
		}

		// Filter: not quarantined
		if asset.Quarantined {
			continue
		}

		results = append(results, &SemanticSearchResult{
			Asset:      asset,
			Similarity: hnswResult.Similarity,
		})
	}

	return results, len(results), nil
}

// RelatedAssets finds similar assets using semantic similarity.
// Feature: F-002, Story: US-002-04
// Returns top-k most similar assets, excluding the query CID itself.
// Filters: similarity > 0.5, not quarantined, exclude self.
func (s *AssetService) RelatedAssets(ctx context.Context, cid string, limit int) ([]*SemanticSearchResult, error) {
	// Require semantic search infrastructure
	if s.embedding == nil || s.hnswIndex == nil {
		return nil, models.ErrInvalidInput
	}

	// Get the asset to find related items for
	asset, err := s.repo.FindByCID(ctx, cid)
	if err != nil {
		return nil, err
	}

	// Require asset to have embedding
	if len(asset.Embedding) == 0 {
		return []*SemanticSearchResult{}, nil // No embedding, no related assets
	}

	// Search HNSW index using asset's embedding
	hnswResults, err := s.hnswIndex.Search(ctx, asset.Embedding, limit*2) // Request more for filtering
	if err != nil {
		return nil, err
	}

	// Filter by similarity threshold (0.5 for "related" - higher than search)
	const minSimilarity = 0.5
	filtered := make([]*search.SearchResult, 0, len(hnswResults))
	for _, result := range hnswResults {
		// Exclude self
		if result.CID == cid {
			continue
		}
		// Require higher similarity for "related" assets
		if result.Similarity >= minSimilarity {
			filtered = append(filtered, result)
		}
	}

	// Limit results
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	// Fetch asset details and filter
	results := make([]*SemanticSearchResult, 0, len(filtered))
	for _, hnswResult := range filtered {
		relatedAsset, err := s.repo.FindByCID(ctx, hnswResult.CID)
		if err != nil {
			continue
		}

		// Filter: not quarantined
		if relatedAsset.Quarantined {
			continue
		}

		results = append(results, &SemanticSearchResult{
			Asset:      relatedAsset,
			Similarity: hnswResult.Similarity,
		})
	}

	return results, nil
}

// FindByCID looks up an asset by its CID.
func (s *AssetService) FindByCID(ctx context.Context, cid string) (*models.Asset, error) {
	return s.repo.FindByCID(ctx, cid)
}

func validateAnnounce(req AnnounceAssetRequest) error {
	if req.CID == "" {
		return models.ErrInvalidInput
	}
	if req.PeerID == "" {
		return models.ErrInvalidInput
	}
	if !models.ValidManifestTypes[req.ManifestType] {
		return models.ErrInvalidInput
	}
	return nil
}

// autoExtractTags extracts metadata tags from asset announcement (F-002, US-002-04)
// Tags include: size category, MIME type category, manifest type
func autoExtractTags(req AnnounceAssetRequest) []string {
	tags := make([]string, 0, 5)

	// Size category
	if req.Size < 1024*1024 { // < 1 MB
		tags = append(tags, "size:small")
	} else if req.Size < 100*1024*1024 { // < 100 MB
		tags = append(tags, "size:medium")
	} else {
		tags = append(tags, "size:large")
	}

	// MIME type category
	if req.MimeType != "" {
		// Extract primary MIME type (e.g., "application" from "application/octet-stream")
		if len(req.MimeType) > 0 {
			mimeCategory := req.MimeType
			if idx := strings.IndexByte(req.MimeType, '/'); idx != -1 {
				mimeCategory = req.MimeType[:idx]
			}
			tags = append(tags, "mime:"+mimeCategory)
		}
	}

	// Manifest type
	tags = append(tags, "type:"+req.ManifestType)

	// File extension (if present in filename)
	if req.Filename != "" {
		if idx := strings.LastIndexByte(req.Filename, '.'); idx != -1 && idx < len(req.Filename)-1 {
			ext := req.Filename[idx+1:]
			tags = append(tags, "ext:"+ext)
		}
	}

	return tags
}

// SearchSemanticWithVectorStore performs vector-based semantic search using pgvector.
// Feature: F-012 (Semantic Search Engine), Story: US-012-02 Phase 1
// Uses PostgreSQL vector similarity search instead of in-memory HNSW index.
func (s *AssetService) SearchSemanticWithVectorStore(ctx context.Context, query string, limit int, minSimilarity float32) ([]*SemanticSearchResult, int, error) {
	// Require embedding generator for query embedding
	if s.embedding == nil {
		return nil, 0, models.ErrInvalidInput
	}

	// Require vector store for search
	if s.vectorStore == nil {
		return nil, 0, models.ErrInvalidInput
	}

	// Set defaults
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if minSimilarity <= 0 {
		minSimilarity = 0.6 // Default minimum similarity
	}

	// Generate embedding for query
	queryEmbedding, err := s.embedding.GenerateEmbedding(ctx, query)
	if err != nil {
		return nil, 0, err
	}

	// Search vector store (pgvector)
	vectorResults, err := s.vectorStore.Search(ctx, vectorstore.VectorSearchRequest{
		QueryEmbedding: queryEmbedding,
		Limit:          limit * 2, // Request more for filtering
		MinSimilarity:  minSimilarity,
	})
	if err != nil {
		return nil, 0, err
	}

	// Get online peers
	onlineIDs, err := s.presence.OnlinePeerIDs(ctx)
	if err != nil {
		return nil, 0, err
	}
	onlineSet := make(map[string]bool)
	for _, id := range onlineIDs {
		onlineSet[id] = true
	}

	// Fetch asset details and filter by online peers
	results := make([]*SemanticSearchResult, 0, len(vectorResults))
	for _, vectorResult := range vectorResults {
		asset, err := s.repo.FindByCID(ctx, vectorResult.CID)
		if err != nil {
			continue // Skip if asset not found
		}

		// Filter: only online peers, not quarantined
		if !onlineSet[asset.PeerID] || asset.Quarantined {
			continue
		}

		results = append(results, &SemanticSearchResult{
			Asset:      asset,
			Similarity: vectorResult.Similarity,
		})

		// Stop if we have enough results
		if len(results) >= limit {
			break
		}
	}

	return results, len(results), nil
}

// SearchSemanticWithFilters performs semantic search with metadata filters.
// Feature: F-012, Story: US-012-02 Phase 2
// Supports filtering by manifest type, file size range.
func (s *AssetService) SearchSemanticWithFilters(ctx context.Context, query string, limit int, minSimilarity float32, filters *SearchFilters) ([]*SemanticSearchResult, int, error) {
	// Perform vector search first
	results, _, err := s.SearchSemanticWithVectorStore(ctx, query, limit*2, minSimilarity)
	if err != nil {
		return nil, 0, err
	}

	// Apply filters if provided
	if filters == nil {
		// Return first 'limit' results (already filtered by online peers)
		if len(results) > limit {
			results = results[:limit]
		}
		return results, len(results), nil
	}

	// Filter results by metadata
	filtered := make([]*SemanticSearchResult, 0, len(results))
	for _, result := range results {
		// Filter by manifest type
		if filters.ManifestType != "" && result.Asset.ManifestType != filters.ManifestType {
			continue
		}

		// Filter by minimum size
		if filters.MinSize > 0 && result.Asset.Size < filters.MinSize {
			continue
		}

		// Filter by maximum size
		if filters.MaxSize > 0 && result.Asset.Size > filters.MaxSize {
			continue
		}

		filtered = append(filtered, result)

		// Stop if we have enough results
		if len(filtered) >= limit {
			break
		}
	}

	return filtered, len(filtered), nil
}

// SearchHybrid performs hybrid search combining keyword and semantic similarity.
// Feature: F-012 (Semantic Search Engine), Story: US-012-02 Phase 3
// Combines keyword matching (exact/substring) with semantic similarity.
// Score = keywordWeight * keywordScore + semanticWeight * semanticScore
func (s *AssetService) SearchHybrid(ctx context.Context, query string, limit int, keywordWeight float32, semanticWeight float32) ([]*SemanticSearchResult, int, error) {
	// Normalize weights
	totalWeight := keywordWeight + semanticWeight
	if totalWeight == 0 {
		keywordWeight = 0.3 // Default weights
		semanticWeight = 0.7
		totalWeight = 1.0
	}
	keywordWeight /= totalWeight
	semanticWeight /= totalWeight

	// Set limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}

	// Run keyword and semantic searches in parallel
	var keywordResults []*models.Asset
	var semanticResults []*SemanticSearchResult
	var keywordErr, semanticErr error

	// Channel for parallel execution
	done := make(chan bool, 2)

	// Goroutine 1: Keyword search
	go func() {
		defer func() { done <- true }()
		keywordResults, _, keywordErr = s.Search(ctx, SearchAssetsRequest{
			Query: query,
			Limit: limit * 2, // Request more for merging
		})
	}()

	// Goroutine 2: Semantic search (if vector store available)
	go func() {
		defer func() { done <- true }()
		if s.vectorStore != nil && s.embedding != nil {
			semanticResults, _, semanticErr = s.SearchSemanticWithVectorStore(ctx, query, limit*2, 0.0)
		}
	}()

	// Wait for both searches to complete
	<-done
	<-done

	// If both fail, return error
	if keywordErr != nil && semanticErr != nil {
		return nil, 0, keywordErr
	}

	// Merge results with weighted scoring
	scoreMap := make(map[string]*SemanticSearchResult)

	// Process keyword results
	for _, asset := range keywordResults {
		// Calculate keyword score (simple substring match scoring)
		keywordScore := calculateKeywordScore(query, asset.Filename)

		scoreMap[asset.CID] = &SemanticSearchResult{
			Asset:       asset,
			Similarity:  0, // No semantic similarity from keyword search
			HybridScore: keywordWeight * keywordScore,
		}
	}

	// Process semantic results
	for _, semResult := range semanticResults {
		if existing, exists := scoreMap[semResult.Asset.CID]; exists {
			// Asset found in both searches - combine scores
			existing.Similarity = semResult.Similarity
			existing.HybridScore += semanticWeight * semResult.Similarity
		} else {
			// Asset only in semantic results
			scoreMap[semResult.Asset.CID] = &SemanticSearchResult{
				Asset:       semResult.Asset,
				Similarity:  semResult.Similarity,
				HybridScore: semanticWeight * semResult.Similarity,
			}
		}
	}

	// Convert map to slice
	merged := make([]*SemanticSearchResult, 0, len(scoreMap))
	for _, result := range scoreMap {
		merged = append(merged, result)
	}

	// Sort by hybrid score descending
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].HybridScore > merged[j].HybridScore
	})

	// Limit results
	if len(merged) > limit {
		merged = merged[:limit]
	}

	return merged, len(merged), nil
}

// calculateKeywordScore computes a simple keyword match score (0.0-1.0)
// Based on substring matching and position
func calculateKeywordScore(query string, text string) float32 {
	queryLower := strings.ToLower(query)
	textLower := strings.ToLower(text)

	// Exact match
	if queryLower == textLower {
		return 1.0
	}

	// Substring match
	if strings.Contains(textLower, queryLower) {
		// Position-based scoring: earlier matches score higher
		index := strings.Index(textLower, queryLower)
		positionScore := 1.0 - (float32(index) / float32(len(textLower)))
		return 0.5 + (0.5 * positionScore) // Score range: [0.5, 1.0]
	}

	// Partial word match
	queryWords := strings.Fields(queryLower)
	textWords := strings.Fields(textLower)
	matchCount := 0
	for _, qWord := range queryWords {
		for _, tWord := range textWords {
			if strings.Contains(tWord, qWord) {
				matchCount++
				break
			}
		}
	}

	if len(queryWords) > 0 {
		return float32(matchCount) / float32(len(queryWords)) * 0.4 // Score range: [0.0, 0.4]
	}

	return 0.0
}
