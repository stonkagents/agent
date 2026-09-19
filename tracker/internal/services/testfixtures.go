// Package: tracker/internal/services
// Purpose: Test fixtures for AssetService — reduces duplication across handler and service tests

package services

import (
	"github.com/stonkagents/agent/tracker/internal/embedding"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/search"
)

// DefaultHNSWOptions returns HNSW options suitable for tests (384 dims, all-MiniLM-L6-v2).
var DefaultHNSWOptions = search.HNSWOptions{
	Dimensions:  384,
	M:           16,
	EfConstruct: 200,
}

// NewTestAssetService returns an AssetService without embedding/HNSW (basic keyword search only).
func NewTestAssetService(repo repository.AssetRepository, pres presence.PresenceStore, availability repository.AvailabilityRepository) *AssetService {
	return NewAssetService(repo, pres, availability)
}

// NewTestAssetServiceWithSemanticSearch returns an AssetService with embedding + HNSW for semantic search.
// Uses default HNSW options. For custom opts, construct manually.
func NewTestAssetServiceWithSemanticSearch(
	repo repository.AssetRepository,
	pres presence.PresenceStore,
	availability repository.AvailabilityRepository,
) *AssetService {
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})
	hnswIndex := search.NewHNSWIndex(DefaultHNSWOptions)
	return NewAssetServiceWithSemanticSearch(repo, pres, availability, embGen, hnswIndex)
}
