// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Tests for in-memory asset repository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestAsset(cid, peerID string) *models.Asset {
	return &models.Asset{
		CID:          cid,
		Filename:     "test-file-" + cid + ".bin",
		MimeType:     "application/octet-stream",
		Size:         1024,
		PeerID:       peerID,
		AnnouncedAt:  time.Now(),
		ManifestType: "raw",
	}
}

func TestMemoryAssetRepo_Create_Success(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()
	asset := newTestAsset("cid-1", "peer-1")

	err := repo.Create(ctx, asset)
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}

	found, err := repo.FindByCID(ctx, "cid-1")
	if err != nil {
		t.Fatalf("FindByCID() unexpected error: %v", err)
	}
	if found.CID != "cid-1" {
		t.Errorf("FindByCID() got CID = %q, want %q", found.CID, "cid-1")
	}
	if found.PeerID != "peer-1" {
		t.Errorf("FindByCID() got PeerID = %q, want %q", found.PeerID, "peer-1")
	}
}

func TestMemoryAssetRepo_Create_DuplicateCID(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-1"))
	err := repo.Create(ctx, newTestAsset("cid-1", "peer-2"))

	if err != models.ErrAlreadyExists {
		t.Errorf("Create() got error = %v, want ErrAlreadyExists", err)
	}
}

func TestMemoryAssetRepo_FindByCID_NotFound(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_, err := repo.FindByCID(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("FindByCID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryAssetRepo_Search_ByFilename(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	a1 := newTestAsset("cid-1", "peer-1")
	a1.Filename = "flux-lora-adapter.safetensors"
	a2 := newTestAsset("cid-2", "peer-1")
	a2.Filename = "clip-embeddings.npy"
	a3 := newTestAsset("cid-3", "peer-1")
	a3.Filename = "flux-base-model.safetensors"

	_ = repo.Create(ctx, a1)
	_ = repo.Create(ctx, a2)
	_ = repo.Create(ctx, a3)

	results, total, err := repo.Search(ctx, SearchAssetsOptions{Query: "flux", Limit: 10})
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

func TestMemoryAssetRepo_Search_ByMimeType(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	a1 := newTestAsset("cid-1", "peer-1")
	a1.MimeType = "text/markdown"
	a2 := newTestAsset("cid-2", "peer-1")
	a2.MimeType = "application/json"

	_ = repo.Create(ctx, a1)
	_ = repo.Create(ctx, a2)

	results, total, err := repo.Search(ctx, SearchAssetsOptions{MimeType: "text/markdown", Limit: 10})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 1 {
		t.Errorf("Search(mime) got total = %d, want 1", total)
	}
	if len(results) != 1 || results[0].MimeType != "text/markdown" {
		t.Errorf("Search(mime) unexpected results")
	}
}

func TestMemoryAssetRepo_Search_ByManifestType(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	a1 := newTestAsset("cid-1", "peer-1")
	a1.ManifestType = "raw"
	a2 := newTestAsset("cid-2", "peer-1")
	a2.ManifestType = "vec"
	a3 := newTestAsset("cid-3", "peer-1")
	a3.ManifestType = "vec"

	_ = repo.Create(ctx, a1)
	_ = repo.Create(ctx, a2)
	_ = repo.Create(ctx, a3)

	results, total, err := repo.Search(ctx, SearchAssetsOptions{ManifestType: "vec", Limit: 10})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 2 {
		t.Errorf("Search(vec) got total = %d, want 2", total)
	}
	if len(results) != 2 {
		t.Errorf("Search(vec) got %d results, want 2", len(results))
	}
}

func TestMemoryAssetRepo_Search_WithLimit(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = repo.Create(ctx, newTestAsset("cid-"+string(rune('a'+i)), "peer-1"))
	}

	results, total, err := repo.Search(ctx, SearchAssetsOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 5 {
		t.Errorf("Search(limit=2) got total = %d, want 5", total)
	}
	if len(results) != 2 {
		t.Errorf("Search(limit=2) got %d results, want 2", len(results))
	}
}

func TestMemoryAssetRepo_Search_OnlyFromPeerIDs(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-a"))
	_ = repo.Create(ctx, newTestAsset("cid-2", "peer-b"))
	_ = repo.Create(ctx, newTestAsset("cid-3", "peer-c"))

	results, total, err := repo.Search(ctx, SearchAssetsOptions{
		PeerIDs: []string{"peer-a", "peer-c"},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if total != 2 {
		t.Errorf("Search(peerIDs) got total = %d, want 2", total)
	}
	if len(results) != 2 {
		t.Errorf("Search(peerIDs) got %d results, want 2", len(results))
	}
}

func TestMemoryAssetRepo_Delete_Success(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-1"))

	err := repo.Delete(ctx, "cid-1")
	if err != nil {
		t.Fatalf("Delete() unexpected error: %v", err)
	}

	_, err = repo.FindByCID(ctx, "cid-1")
	if err != models.ErrNotFound {
		t.Errorf("FindByCID() after delete got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryAssetRepo_SetQuarantined(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-1"))

	err := repo.SetQuarantined(ctx, "cid-1", true)
	if err != nil {
		t.Fatalf("SetQuarantined() unexpected error: %v", err)
	}

	found, _ := repo.FindByCID(ctx, "cid-1")
	if !found.Quarantined {
		t.Error("SetQuarantined(true) asset not quarantined")
	}

	// Search should exclude quarantined by default
	results, total, _ := repo.Search(ctx, SearchAssetsOptions{Limit: 10})
	if total != 0 {
		t.Errorf("Search() got total = %d, want 0 (quarantined excluded)", total)
	}
	if len(results) != 0 {
		t.Errorf("Search() got %d results, want 0 (quarantined excluded)", len(results))
	}
}

// =============================================================================
// F-032: CountByPeerIDs Batch Tests
// =============================================================================

func TestMemoryAssetRepo_CountByPeerIDs_VaryingCounts(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	// peer-a: 2 assets, peer-b: 1 asset, peer-c: 0 assets
	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-a"))
	_ = repo.Create(ctx, newTestAsset("cid-2", "peer-a"))
	_ = repo.Create(ctx, newTestAsset("cid-3", "peer-b"))

	result, err := repo.CountByPeerIDs(ctx, []string{"peer-a", "peer-b", "peer-c"})
	if err != nil {
		t.Fatalf("CountByPeerIDs() error: %v", err)
	}
	if result["peer-a"] != 2 {
		t.Errorf("CountByPeerIDs() peer-a = %d, want 2", result["peer-a"])
	}
	if result["peer-b"] != 1 {
		t.Errorf("CountByPeerIDs() peer-b = %d, want 1", result["peer-b"])
	}
	if _, exists := result["peer-c"]; exists {
		t.Error("CountByPeerIDs() peer-c should not be in result (0 assets)")
	}
}

func TestMemoryAssetRepo_CountByPeerIDs_ExcludesQuarantined(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-a"))
	_ = repo.Create(ctx, newTestAsset("cid-2", "peer-a"))
	_ = repo.SetQuarantined(ctx, "cid-2", true)

	result, err := repo.CountByPeerIDs(ctx, []string{"peer-a"})
	if err != nil {
		t.Fatalf("CountByPeerIDs() error: %v", err)
	}
	if result["peer-a"] != 1 {
		t.Errorf("CountByPeerIDs() peer-a = %d, want 1 (quarantined excluded)", result["peer-a"])
	}
}

func TestMemoryAssetRepo_CountByPeerIDs_EmptySlice(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-a"))

	result, err := repo.CountByPeerIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("CountByPeerIDs() error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("CountByPeerIDs(empty) got %d entries, want 0", len(result))
	}
}

// =============================================================================
// F-032: TopByDownloads Tests
// =============================================================================

func TestMemoryAssetRepo_TopByDownloads_OrderedByCount(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	a1 := newTestAsset("cid-1", "peer-a")
	a2 := newTestAsset("cid-2", "peer-a")
	a3 := newTestAsset("cid-3", "peer-a")
	_ = repo.Create(ctx, a1)
	_ = repo.Create(ctx, a2)
	_ = repo.Create(ctx, a3)

	// Set different download counts
	_ = repo.IncrementDownloadCount(ctx, "cid-2") // cid-2: 1
	_ = repo.IncrementDownloadCount(ctx, "cid-3") // cid-3: 1
	_ = repo.IncrementDownloadCount(ctx, "cid-3") // cid-3: 2

	results, err := repo.TopByDownloads(ctx, "peer-a", 2)
	if err != nil {
		t.Fatalf("TopByDownloads() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("TopByDownloads(limit=2) got %d results, want 2", len(results))
	}
	if results[0].CID != "cid-3" {
		t.Errorf("TopByDownloads()[0].CID = %q, want %q (highest downloads)", results[0].CID, "cid-3")
	}
	if results[1].CID != "cid-2" {
		t.Errorf("TopByDownloads()[1].CID = %q, want %q", results[1].CID, "cid-2")
	}
}

func TestMemoryAssetRepo_TopByDownloads_EmptyForPeer(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-a"))

	results, err := repo.TopByDownloads(ctx, "peer-b", 5)
	if err != nil {
		t.Fatalf("TopByDownloads() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("TopByDownloads(no assets) got %d results, want 0", len(results))
	}
}

func TestMemoryAssetRepo_TopByDownloads_DefaultLimit(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = repo.Create(ctx, newTestAsset("cid-"+string(rune('a'+i)), "peer-a"))
	}

	results, err := repo.TopByDownloads(ctx, "peer-a", 0)
	if err != nil {
		t.Fatalf("TopByDownloads() error: %v", err)
	}
	// limit=0 should default to 5
	if len(results) != 5 {
		t.Errorf("TopByDownloads(limit=0) got %d results, want 5 (default)", len(results))
	}
}

func TestMemoryAssetRepo_TopByDownloads_ExcludesQuarantined(t *testing.T) {
	repo := NewMemoryAssetRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestAsset("cid-1", "peer-a"))
	_ = repo.Create(ctx, newTestAsset("cid-2", "peer-a"))
	_ = repo.IncrementDownloadCount(ctx, "cid-1")
	_ = repo.IncrementDownloadCount(ctx, "cid-1")
	_ = repo.SetQuarantined(ctx, "cid-1", true)

	results, err := repo.TopByDownloads(ctx, "peer-a", 5)
	if err != nil {
		t.Fatalf("TopByDownloads() error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("TopByDownloads() got %d results, want 1 (quarantined excluded)", len(results))
	}
	if results[0].CID != "cid-2" {
		t.Errorf("TopByDownloads()[0].CID = %q, want %q", results[0].CID, "cid-2")
	}
}
