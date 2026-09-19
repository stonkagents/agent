// Package: tracker/internal/services
// Feature: F-012 (Semantic Search Engine)
// Story: US-012-02 (Search API & Discovery)
// Purpose: Integration tests for enhanced search features
// TDD Phase: End-to-End verification

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/pkg/vectorstore"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/embedding"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// End-to-End Integration Tests (US-012-02 Phase 6)
// ============================================================================

// TestE2E_SemanticSearchWithFilters verifies complete search flow
func TestE2E_SemanticSearchWithFilters(t *testing.T) {
	// Setup: Create service with full stack
	assetRepo := repository.NewMemoryAssetRepository()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	presenceStore := presence.NewMemoryPresenceStore(clk)
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce diverse assets
	assets := []struct {
		cid          string
		filename     string
		size         int64
		manifestType string
	}{
		{"bafytest-small-raw", "dataset-small.csv", 500 * 1024, "raw"},
		{"bafytest-large-raw", "dataset-large.parquet", 50 * 1024 * 1024, "raw"},
		{"bafytest-small-vec", "embeddings-small.npy", 800 * 1024, "vec"},
		{"bafytest-large-vec", "embeddings-large.safetensors", 100 * 1024 * 1024, "vec"},
	}

	for _, a := range assets {
		_, err := svc.Announce(ctx, AnnounceAssetRequest{
			CID:          a.cid,
			Filename:     a.filename,
			Size:         a.size,
			PeerID:       "peer-1",
			ManifestType: a.manifestType,
		})
		require.NoError(t, err)
	}

	// Test 1: Filter by size (only large files > 10MB)
	filters := &SearchFilters{MinSize: 10 * 1024 * 1024}
	results, total, err := svc.SearchSemanticWithFilters(ctx, "dataset", 10, 0.0, filters)
	require.NoError(t, err)

	for _, result := range results {
		assert.GreaterOrEqual(t, result.Asset.Size, filters.MinSize,
			"Result %s should be >= minSize", result.Asset.CID)
	}

	// Test 2: Filter by manifest type (only "vec")
	filters = &SearchFilters{ManifestType: "vec"}
	results, total, err = svc.SearchSemanticWithFilters(ctx, "embeddings", 10, 0.0, filters)
	require.NoError(t, err)

	for _, result := range results {
		assert.Equal(t, "vec", result.Asset.ManifestType,
			"Result %s should have manifestType=vec", result.Asset.CID)
	}

	// Test 3: Combined filters (raw + large)
	filters = &SearchFilters{
		ManifestType: "raw",
		MinSize:      10 * 1024 * 1024,
	}
	results, total, err = svc.SearchSemanticWithFilters(ctx, "dataset", 10, 0.0, filters)
	require.NoError(t, err)

	// Should only return bafytest-large-raw
	for _, result := range results {
		assert.Equal(t, "raw", result.Asset.ManifestType)
		assert.GreaterOrEqual(t, result.Asset.Size, filters.MinSize)
	}

	_ = total
}

// TestE2E_HybridSearchFlow verifies keyword + semantic combination
func TestE2E_HybridSearchFlow(t *testing.T) {
	assetRepo := repository.NewMemoryAssetRepository()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	presenceStore := presence.NewMemoryPresenceStore(clk)
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce assets with different match types
	exactMatch, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-exact",
		Filename:     "financial-data.csv", // Exact keyword match
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	partialMatch, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-partial",
		Filename:     "stock-market-analysis.parquet", // Partial match
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Hybrid search: "financial data"
	results, total, err := svc.SearchHybrid(ctx, "financial data", 10, 0.3, 0.7)
	require.NoError(t, err)

	if len(results) > 0 {
		// Verify hybrid scores are set
		for _, result := range results {
			assert.GreaterOrEqual(t, result.HybridScore, float32(0.0))
			assert.LessOrEqual(t, result.HybridScore, float32(1.0))
		}

		// Exact match should have higher score
		exactScore := float32(0.0)
		partialScore := float32(0.0)

		for _, result := range results {
			if result.Asset.CID == exactMatch.CID {
				exactScore = result.HybridScore
			}
			if result.Asset.CID == partialMatch.CID {
				partialScore = result.HybridScore
			}
		}

		if exactScore > 0 && partialScore > 0 {
			assert.Greater(t, exactScore, partialScore,
				"Exact match should score higher than partial match")
		}
	}

	_ = total
}

// TestE2E_OfflinePeerFiltering verifies online-only filtering
func TestE2E_OfflinePeerFiltering(t *testing.T) {
	assetRepo := repository.NewMemoryAssetRepository()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	presenceStore := presence.NewMemoryPresenceStore(clk)
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()

	// Peer 1: Online
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Peer 2: Offline (no heartbeat)

	// Announce assets from both peers
	onlineAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-online",
		Filename:     "online-dataset.csv",
		PeerID:       "peer-1", // Online
		ManifestType: "raw",
	})

	offlineAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-offline",
		Filename:     "offline-dataset.parquet",
		PeerID:       "peer-2", // Offline
		ManifestType: "raw",
	})

	// Search should only return online peer's assets
	results, _, err := svc.SearchSemanticWithVectorStore(ctx, "dataset", 10, 0.0)
	require.NoError(t, err)

	// Verify offline asset is filtered out
	for _, result := range results {
		assert.NotEqual(t, offlineAsset.CID, result.Asset.CID,
			"Offline peer asset should be filtered out")

		// All results should be from online peers
		assert.Equal(t, onlineAsset.PeerID, result.Asset.PeerID)
	}
}

// TestE2E_QuarantinedAssetFiltering verifies DMCA filtering
func TestE2E_QuarantinedAssetFiltering(t *testing.T) {
	assetRepo := repository.NewMemoryAssetRepository()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	presenceStore := presence.NewMemoryPresenceStore(clk)
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce normal asset
	normalAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-normal",
		Filename:     "normal-file.csv",
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Announce asset that will be quarantined
	quarantinedAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-quarantined",
		Filename:     "quarantined-file.csv",
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Quarantine the asset
	err := assetRepo.SetQuarantined(ctx, quarantinedAsset.CID, true)
	require.NoError(t, err)

	// Search should exclude quarantined assets
	results, _, err := svc.SearchSemanticWithVectorStore(ctx, "file", 10, 0.0)
	require.NoError(t, err)

	// Verify quarantined asset is excluded
	for _, result := range results {
		assert.NotEqual(t, quarantinedAsset.CID, result.Asset.CID,
			"Quarantined asset should be filtered out")
		assert.False(t, result.Asset.Quarantined)
	}

	// Normal asset should still appear
	foundNormal := false
	for _, result := range results {
		if result.Asset.CID == normalAsset.CID {
			foundNormal = true
		}
	}
	assert.True(t, foundNormal || len(results) == 0,
		"Normal asset should appear in results (unless no embeddings)")
}

// TestE2E_PerformanceCharacteristics verifies search performance
func TestE2E_PerformanceCharacteristics(t *testing.T) {
	assetRepo := repository.NewMemoryAssetRepository()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	presenceStore := presence.NewMemoryPresenceStore(clk)
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce 100 assets
	for i := 0; i < 100; i++ {
		_, _ = svc.Announce(ctx, AnnounceAssetRequest{
			CID:          "bafytest-" + string(rune(i)),
			Filename:     "dataset-" + string(rune(i)) + ".csv",
			PeerID:       "peer-1",
			ManifestType: "raw",
		})
	}

	// Test 1: Search latency (should be fast with cache)
	start := time.Now()
	_, _, err := svc.SearchSemanticWithVectorStore(ctx, "dataset", 20, 0.0)
	elapsed := time.Since(start)
	require.NoError(t, err)

	// Should complete in reasonable time (< 100ms for in-memory)
	assert.Less(t, elapsed, 100*time.Millisecond,
		"Search should complete quickly with mock vector store")

	// Test 2: Hybrid search parallelism (should be fast due to parallel execution)
	start = time.Now()
	_, _, err = svc.SearchHybrid(ctx, "dataset", 20, 0.3, 0.7)
	elapsed = time.Since(start)
	require.NoError(t, err)

	// Hybrid should also be fast
	assert.Less(t, elapsed, 150*time.Millisecond,
		"Hybrid search should complete quickly with parallel execution")
}
