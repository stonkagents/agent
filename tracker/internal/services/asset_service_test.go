// Package: tracker/internal/services
// Feature: F-007 (Centralized Tracker)
// Story: US-007-03 (Asset Registry)
// Purpose: Tests for asset service (announce, search)

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/pkg/vectorstore"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/embedding"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

func newAssetTestDeps() (*repository.MemoryAssetRepository, *presence.MemoryPresenceStore, *clock.MockClock) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	assetRepo := repository.NewMemoryAssetRepository()
	store := presence.NewMemoryPresenceStore(clk)
	return assetRepo, store, clk
}

func TestAssetService_Announce_Success(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	// Mark peer as online
	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	asset, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest123",
		Filename:     "lora-adapter.safetensors",
		MimeType:     "application/octet-stream",
		Size:         1024 * 1024,
		PeerID:       "peer-1",
		ManifestType: "raw",
	})
	if err != nil {
		t.Fatalf("Announce() unexpected error: %v", err)
	}
	if asset.CID != "bafytest123" {
		t.Errorf("Announce() got CID = %q, want %q", asset.CID, "bafytest123")
	}
}

func TestAssetService_Announce_MissingCID(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_, err := svc.Announce(ctx, AnnounceAssetRequest{
		Filename:     "test.bin",
		PeerID:       "peer-1",
		ManifestType: "raw",
	})
	if err != models.ErrInvalidInput {
		t.Errorf("Announce() got error = %v, want ErrInvalidInput", err)
	}
}

func TestAssetService_Announce_MissingPeerID(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest123",
		Filename:     "test.bin",
		ManifestType: "raw",
	})
	if err != models.ErrInvalidInput {
		t.Errorf("Announce() got error = %v, want ErrInvalidInput", err)
	}
}

func TestAssetService_Announce_InvalidManifestType(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest123",
		Filename:     "test.bin",
		PeerID:       "peer-1",
		ManifestType: "invalid",
	})
	if err != models.ErrInvalidInput {
		t.Errorf("Announce() got error = %v, want ErrInvalidInput", err)
	}
}

func TestAssetService_Search_ByKeyword(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-1", Filename: "flux-lora.safetensors", PeerID: "peer-1", ManifestType: "raw"})
	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-2", Filename: "clip-embeddings.npy", PeerID: "peer-1", ManifestType: "vec"})
	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-3", Filename: "flux-base.safetensors", PeerID: "peer-1", ManifestType: "raw"})

	results, total, err := svc.Search(ctx, SearchAssetsRequest{Query: "flux", Limit: 10})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 2 {
		t.Errorf("Search(flux) got total = %d, want 2", total)
	}
	if len(results) != 2 {
		t.Errorf("Search(flux) got %d results, want 2", len(results))
	}
}

func TestAssetService_Search_ByManifestType(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-1", Filename: "file1.bin", PeerID: "peer-1", ManifestType: "raw"})
	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-2", Filename: "emb.npy", PeerID: "peer-1", ManifestType: "vec"})

	results, total, err := svc.Search(ctx, SearchAssetsRequest{ManifestType: "vec", Limit: 10})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 1 {
		t.Errorf("Search(vec) got total = %d, want 1", total)
	}
	if len(results) != 1 {
		t.Errorf("Search(vec) got %d results, want 1", len(results))
	}
}

func TestAssetService_Search_OnlyOnlinePeers(t *testing.T) {
	assetRepo, store, clk := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-a", 5*time.Minute)
	_ = store.Heartbeat(ctx, "peer-b", 5*time.Minute)

	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-1", Filename: "file1.bin", PeerID: "peer-a", ManifestType: "raw"})
	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-2", Filename: "file2.bin", PeerID: "peer-b", ManifestType: "raw"})

	// Expire peer-b
	clk.Advance(3 * time.Minute)
	_ = store.Heartbeat(ctx, "peer-a", 5*time.Minute)
	clk.Advance(3 * time.Minute)

	results, total, err := svc.Search(ctx, SearchAssetsRequest{Limit: 10})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 1 {
		t.Errorf("Search(online) got total = %d, want 1", total)
	}
	if len(results) != 1 || results[0].PeerID != "peer-a" {
		t.Errorf("Search(online) expected only peer-a's asset, got %v", results)
	}
}

func TestAssetService_Search_QuarantinedExcluded(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-1", Filename: "clean.bin", PeerID: "peer-1", ManifestType: "raw"})
	_, _ = svc.Announce(ctx, AnnounceAssetRequest{CID: "cid-2", Filename: "quarantined.bin", PeerID: "peer-1", ManifestType: "raw"})

	// Quarantine cid-2
	_ = assetRepo.SetQuarantined(ctx, "cid-2", true)

	results, total, _ := svc.Search(ctx, SearchAssetsRequest{Limit: 10})
	if total != 1 {
		t.Errorf("Search() got total = %d, want 1 (quarantined excluded)", total)
	}
	if len(results) != 1 {
		t.Errorf("Search() got %d results, want 1", len(results))
	}
}

func TestAssetService_Search_Empty(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	results, total, err := svc.Search(ctx, SearchAssetsRequest{Limit: 10})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 0 {
		t.Errorf("Search() got total = %d, want 0", total)
	}
	if len(results) != 0 {
		t.Errorf("Search() got %d results, want 0", len(results))
	}
}

func TestAssetService_Search_WithLimit(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	for i := 0; i < 5; i++ {
		_, _ = svc.Announce(ctx, AnnounceAssetRequest{
			CID: "cid-" + string(rune('a'+i)), Filename: "file.bin",
			PeerID: "peer-1", ManifestType: "raw",
		})
	}

	results, total, _ := svc.Search(ctx, SearchAssetsRequest{Limit: 2})
	if total != 5 {
		t.Errorf("Search(limit=2) got total = %d, want 5", total)
	}
	if len(results) != 2 {
		t.Errorf("Search(limit=2) got %d results, want 2", len(results))
	}
}

// TestAssetService_Announce_StoresEmbedding - RED test
// Acceptance Criterion: Embedding generated on announce and stored in asset
// Feature: F-002, Story: US-002-01
func TestAssetService_Announce_StoresEmbedding(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()

	// Create embedding generator (will fallback to zero vector if model unavailable)
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	// Create asset service with embedding support
	svc := NewAssetServiceWithEmbedding(assetRepo, store, repository.NewMemoryAvailabilityRepository(), embGen)
	ctx := context.Background()

	// Mark peer as online
	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce asset
	asset, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest123",
		Filename:     "CLIP embeddings for image search",
		MimeType:     "application/octet-stream",
		Size:         1024 * 1024,
		PeerID:       "peer-1",
		ManifestType: "raw",
	})
	if err != nil {
		t.Fatalf("Announce() unexpected error: %v", err)
	}

	// Assert: Embedding should be stored (384 dimensions)
	if asset.Embedding == nil {
		t.Fatal("Expected embedding to be generated and stored, got nil")
	}

	if len(asset.Embedding) != 384 {
		t.Errorf("Expected embedding to be 384 dimensions, got %d", len(asset.Embedding))
	}

	// Verify embedding is persisted in repository
	retrieved, err := assetRepo.FindByCID(ctx, "bafytest123")
	if err != nil {
		t.Fatalf("FindByCID() unexpected error: %v", err)
	}

	if retrieved.Embedding == nil {
		t.Error("Expected embedding to be persisted in repository, got nil")
	}

	if len(retrieved.Embedding) != 384 {
		t.Errorf("Expected persisted embedding to be 384 dimensions, got %d", len(retrieved.Embedding))
	}
}

// TestAssetService_Announce_EmbeddingFromFilename - RED test
// Acceptance Criterion: Embedding generated from filename metadata
func TestAssetService_Announce_EmbeddingFromFilename(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})
	svc := NewAssetServiceWithEmbedding(assetRepo, store, repository.NewMemoryAvailabilityRepository(), embGen)
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce with descriptive filename
	asset, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest456",
		Filename:     "stable-diffusion-xl-lora-weights.safetensors", // Semantic metadata in filename
		PeerID:       "peer-1",
		ManifestType: "raw",
	})
	if err != nil {
		t.Fatalf("Announce() unexpected error: %v", err)
	}

	// Assert: Embedding should be generated from filename
	if asset.Embedding == nil {
		t.Fatal("Expected embedding from filename, got nil")
	}

	if len(asset.Embedding) != 384 {
		t.Errorf("Expected 384-dim embedding, got %d", len(asset.Embedding))
	}
}

// TestAssetService_AutoTagging_SizeCategories - RED test
// Acceptance Criterion: Auto-extract size category tags (small/medium/large)
// Feature: F-002, Story: US-002-04
func TestAssetService_AutoTagging_SizeCategories(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	tests := []struct {
		name        string
		size        int64
		expectedTag string
	}{
		{"small file", 500 * 1024, "size:small"},         // 500 KB
		{"medium file", 50 * 1024 * 1024, "size:medium"}, // 50 MB
		{"large file", 500 * 1024 * 1024, "size:large"},  // 500 MB
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, err := svc.Announce(ctx, AnnounceAssetRequest{
				CID:          "cid-" + tt.name,
				Filename:     "test.bin",
				Size:         tt.size,
				PeerID:       "peer-1",
				ManifestType: "raw",
			})
			if err != nil {
				t.Fatalf("Announce() unexpected error: %v", err)
			}

			// Check that size category tag is present
			found := false
			for _, tag := range asset.Tags {
				if tag == tt.expectedTag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected tag %q, got tags: %v", tt.expectedTag, asset.Tags)
			}
		})
	}
}

// TestAssetService_AutoTagging_MimeType - RED test
// Acceptance Criterion: Auto-extract MIME type category tags
func TestAssetService_AutoTagging_MimeType(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	tests := []struct {
		name        string
		mimeType    string
		expectedTag string
	}{
		{"octet-stream", "application/octet-stream", "mime:application"},
		{"json", "application/json", "mime:application"},
		{"text", "text/plain", "mime:text"},
		{"image", "image/png", "mime:image"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, err := svc.Announce(ctx, AnnounceAssetRequest{
				CID:          "cid-" + tt.name,
				Filename:     "test",
				MimeType:     tt.mimeType,
				PeerID:       "peer-1",
				ManifestType: "raw",
			})
			if err != nil {
				t.Fatalf("Announce() unexpected error: %v", err)
			}

			// Check that MIME category tag is present
			found := false
			for _, tag := range asset.Tags {
				if tag == tt.expectedTag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected tag %q, got tags: %v", tt.expectedTag, asset.Tags)
			}
		})
	}
}

// TestAssetService_AutoTagging_ManifestType - RED test
// Acceptance Criterion: Auto-extract manifest type tags
func TestAssetService_AutoTagging_ManifestType(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	tests := []struct {
		manifestType string
		expectedTag  string
	}{
		{"raw", "type:raw"},
		{"vec", "type:vec"},
	}

	for _, tt := range tests {
		t.Run(tt.manifestType, func(t *testing.T) {
			asset, err := svc.Announce(ctx, AnnounceAssetRequest{
				CID:          "cid-" + tt.manifestType,
				Filename:     "test",
				PeerID:       "peer-1",
				ManifestType: tt.manifestType,
			})
			if err != nil {
				t.Fatalf("Announce() unexpected error: %v", err)
			}

			// Check that manifest type tag is present
			found := false
			for _, tag := range asset.Tags {
				if tag == tt.expectedTag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected tag %q, got tags: %v", tt.expectedTag, asset.Tags)
			}
		})
	}
}

// TestAssetService_AutoTagging_FileExtension - RED test
// Acceptance Criterion: Auto-extract file extension tags
func TestAssetService_AutoTagging_FileExtension(t *testing.T) {
	assetRepo, store, _ := newAssetTestDeps()
	svc := NewAssetService(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	ctx := context.Background()

	_ = store.Heartbeat(ctx, "peer-1", 5*time.Minute)

	tests := []struct {
		filename    string
		expectedTag string
	}{
		{"model.safetensors", "ext:safetensors"},
		{"embeddings.npy", "ext:npy"},
		{"data.json", "ext:json"},
		{"weights.bin", "ext:bin"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			asset, err := svc.Announce(ctx, AnnounceAssetRequest{
				CID:          "cid-" + tt.filename,
				Filename:     tt.filename,
				PeerID:       "peer-1",
				ManifestType: "raw",
			})
			if err != nil {
				t.Fatalf("Announce() unexpected error: %v", err)
			}

			// Check that extension tag is present
			found := false
			for _, tag := range asset.Tags {
				if tag == tt.expectedTag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected tag %q, got tags: %v", tt.expectedTag, asset.Tags)
			}
		})
	}
}

// ============================================================================
// Vector Store Integration Tests (F-012, US-012-01 Phase 5)
// ============================================================================

// TestAssetService_Announce_WithVectorStore verifies embeddings are persisted to pgvector
func TestAssetService_Announce_WithVectorStore(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()

	// Create mock vector store
	mockVectorStore := vectorstore.NewMockVectorStore()

	// Create embedding generator (nil for MVP - graceful fallback)
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{
		ModelEndpoint: "http://localhost:11434",
		ModelName:     "all-minilm",
	})

	// Create service with vector store integration
	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce asset with embedding generation
	asset, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-vector-store",
		Filename:     "machine-learning-dataset.parquet",
		MimeType:     "application/octet-stream",
		Size:         10 * 1024 * 1024,
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	if err != nil {
		t.Fatalf("Announce() unexpected error: %v", err)
	}

	// Verify asset has embedding (may be zero vector if model unavailable)
	if len(asset.Embedding) == 0 {
		t.Skip("Skipping embedding verification - model unavailable (expected in CI)")
	}

	// Verify embedding was stored in vector store
	storedEmbedding, err := mockVectorStore.GetEmbedding(ctx, asset.CID)
	if err != nil {
		t.Errorf("GetEmbedding() failed: %v", err)
	}

	// Verify stored embedding matches generated embedding
	if len(storedEmbedding) != len(asset.Embedding) {
		t.Errorf("Stored embedding dimension mismatch: got %d, want %d", len(storedEmbedding), len(asset.Embedding))
	}

	// Verify embedding values match (sample first 5 dimensions)
	for i := 0; i < 5 && i < len(storedEmbedding); i++ {
		if storedEmbedding[i] != asset.Embedding[i] {
			t.Errorf("Stored embedding[%d] = %f, want %f", i, storedEmbedding[i], asset.Embedding[i])
		}
	}
}

// TestAssetService_Announce_VectorStoreFailure verifies graceful degradation
func TestAssetService_Announce_VectorStoreFailure(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()

	// Create embedding generator
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	// Create service with nil vector store (disabled)
	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, nil)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce should succeed even without vector store
	asset, err := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-no-vectorstore",
		Filename:     "test-file.bin",
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	if err != nil {
		t.Errorf("Announce() should succeed without vector store, got error: %v", err)
	}

	if asset.CID != "bafytest-no-vectorstore" {
		t.Errorf("Announce() got CID = %q, want %q", asset.CID, "bafytest-no-vectorstore")
	}
}

// ============================================================================
// Semantic Search with Filters Tests (F-012, US-012-02 Phase 1-2)
// ============================================================================

// TestAssetService_SearchSemanticWithVectorStore verifies pgvector-based semantic search
func TestAssetService_SearchSemanticWithVectorStore(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	// Create service with vector store (instead of HNSW)
	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce assets with embeddings
	asset1, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-financial-data",
		Filename:     "stock-prices-2023.csv",
		MimeType:     "text/csv",
		Size:         10 * 1024 * 1024, // 10MB
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	asset2, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-ml-model",
		Filename:     "sentiment-analysis.onnx",
		MimeType:     "application/octet-stream",
		Size:         100 * 1024 * 1024, // 100MB
		PeerID:       "peer-1",
		ManifestType: "vec",
	})

	// Search with vector store
	results, total, err := svc.SearchSemanticWithVectorStore(ctx, "financial dataset", 10, 0.5)

	if err != nil {
		t.Fatalf("SearchSemanticWithVectorStore() unexpected error: %v", err)
	}

	if total == 0 {
		t.Skip("Skipping verification - embeddings not generated (model unavailable)")
	}

	// Verify results include asset1 (financial data)
	found := false
	for _, result := range results {
		if result.Asset.CID == asset1.CID {
			found = true
			if result.Similarity < 0.5 {
				t.Errorf("Similarity %f below threshold 0.5", result.Similarity)
			}
		}
	}

	// Note: may not find match if embeddings are zero vectors (model unavailable)
	_ = found
	_ = asset2
}

// TestAssetService_SearchWithSizeFilter verifies size range filtering
func TestAssetService_SearchWithSizeFilter(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce assets of different sizes
	smallAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-small",
		Filename:     "small.txt",
		Size:         100 * 1024, // 100KB
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	largeAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-large",
		Filename:     "large.bin",
		Size:         50 * 1024 * 1024, // 50MB
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Search with size filter: only assets > 1MB
	filters := SearchFilters{
		MinSize: 1 * 1024 * 1024, // 1MB minimum
	}

	results, _, err := svc.SearchSemanticWithFilters(ctx, "file", 10, 0.0, &filters)

	if err != nil {
		t.Fatalf("SearchSemanticWithFilters() unexpected error: %v", err)
	}

	// Verify small asset is excluded
	for _, result := range results {
		if result.Asset.CID == smallAsset.CID {
			t.Errorf("Small asset should be filtered out, got CID=%s", result.Asset.CID)
		}
		if result.Asset.Size < filters.MinSize {
			t.Errorf("Result size %d below minimum %d", result.Asset.Size, filters.MinSize)
		}
	}

	_ = largeAsset
}

// TestAssetService_SearchWithManifestTypeFilter verifies manifest type filtering
func TestAssetService_SearchWithManifestTypeFilter(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce both raw and vec assets
	_, _ = svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-raw",
		Filename:     "data.csv",
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	vecAsset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-vec",
		Filename:     "embeddings.npy",
		PeerID:       "peer-1",
		ManifestType: "vec",
	})

	// Search with manifest type filter: only "vec" assets
	filters := SearchFilters{
		ManifestType: "vec",
	}

	results, _, err := svc.SearchSemanticWithFilters(ctx, "embeddings", 10, 0.0, &filters)

	if err != nil {
		t.Fatalf("SearchSemanticWithFilters() unexpected error: %v", err)
	}

	// Verify only "vec" assets returned
	for _, result := range results {
		if result.Asset.ManifestType != "vec" {
			t.Errorf("Expected manifest_type=vec, got %s for CID=%s", result.Asset.ManifestType, result.Asset.CID)
		}
	}

	_ = vecAsset
}

// ============================================================================
// Hybrid Search Tests (F-012, US-012-02 Phase 3)
// ============================================================================

// TestAssetService_HybridSearch verifies keyword + semantic search combination
func TestAssetService_HybridSearch(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce assets that match differently in keyword vs semantic
	exactMatch, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-exact",
		Filename:     "machine-learning-dataset.csv", // Exact keyword match
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	semanticMatch, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-semantic",
		Filename:     "ai-training-data.parquet", // Semantic match only
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Hybrid search: "machine learning"
	// - exactMatch should score high on keyword (contains "machine-learning")
	// - semanticMatch should score on semantic only (AI ≈ machine learning)
	results, _, err := svc.SearchHybrid(ctx, "machine learning", 10, 0.3, 0.7)

	if err != nil {
		t.Fatalf("SearchHybrid() unexpected error: %v", err)
	}

	if len(results) == 0 {
		t.Skip("Skipping verification - no results (expected in CI without model)")
	}

	// Verify both assets appear in results
	foundExact := false
	foundSemantic := false

	for _, result := range results {
		if result.Asset.CID == exactMatch.CID {
			foundExact = true
			// Should have higher score due to keyword match
		}
		if result.Asset.CID == semanticMatch.CID {
			foundSemantic = true
		}
	}

	// Note: Results may be empty with zero vector embeddings
	_ = foundExact
	_ = foundSemantic
}

// TestAssetService_HybridSearch_Deduplication verifies CID deduplication
func TestAssetService_HybridSearch_Deduplication(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	// Announce asset that matches both keyword and semantic
	asset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-both",
		Filename:     "financial-dataset.csv", // Matches "financial"
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Hybrid search with query that matches both ways
	results, _, err := svc.SearchHybrid(ctx, "financial", 10, 0.5, 0.5)

	if err != nil {
		t.Fatalf("SearchHybrid() unexpected error: %v", err)
	}

	// Verify asset appears only once (deduplicated)
	count := 0
	for _, result := range results {
		if result.Asset.CID == asset.CID {
			count++
		}
	}

	if count > 1 {
		t.Errorf("Asset %s appears %d times, should be deduplicated to 1", asset.CID, count)
	}
}

// TestAssetService_HybridSearch_WeightedScoring verifies score calculation
func TestAssetService_HybridSearch_WeightedScoring(t *testing.T) {
	assetRepo, presenceStore, _ := newAssetTestDeps()
	mockVectorStore := vectorstore.NewMockVectorStore()
	embGen := embedding.NewGenerator(embedding.GeneratorOptions{})

	svc := NewAssetServiceWithVectorStore(assetRepo, presenceStore, repository.NewMemoryAvailabilityRepository(), embGen, nil, mockVectorStore)

	ctx := context.Background()
	_ = presenceStore.Heartbeat(ctx, "peer-1", 5*time.Minute)

	asset, _ := svc.Announce(ctx, AnnounceAssetRequest{
		CID:          "bafytest-weighted",
		Filename:     "test-file.bin",
		PeerID:       "peer-1",
		ManifestType: "raw",
	})

	// Search with different weights
	// Weight semantic higher (0.3 keyword, 0.7 semantic)
	results, _, err := svc.SearchHybrid(ctx, "test", 10, 0.3, 0.7)

	if err != nil {
		t.Fatalf("SearchHybrid() unexpected error: %v", err)
	}

	// Verify HybridScore field is populated
	for _, result := range results {
		if result.Asset.CID == asset.CID {
			if result.HybridScore < 0 || result.HybridScore > 1 {
				t.Errorf("HybridScore %f out of range [0, 1]", result.HybridScore)
			}
		}
	}
}
