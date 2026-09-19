// Package api: Tests for gallery enrichment (F-027, US-027-03)
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-03 (Backend — Gallery Response Enrichment)
// Purpose: Verify gallery search returns download_count on items and correct total from repo

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// portalTestEnv holds a test server and its repos for seeding data.
type portalTestEnv struct {
	srv       *Server
	assetRepo *repository.MemoryAssetRepository
	peerRepo  *repository.MemoryPeerRepository
	repRepo   *repository.MemoryReputationRepository
	store     *presence.MemoryPresenceStore
}

// newPortalTestEnv builds a test server and returns exposed repos for seeding.
func newPortalTestEnv(t *testing.T) *portalTestEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	dmcaRepo := repository.NewMemoryDMCARepository()
	forumRepo := repository.NewMemoryForumRepository()
	trustBlockRepo := repository.NewMemoryPeerTrustBlockRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	store := presence.NewMemoryPresenceStore(clk)

	peerSvc := services.NewPeerService(peerRepo, store, apiKeyRepo)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	dmcaSvc := services.NewDMCAService(assetRepo, dmcaRepo)
	forumSvc := services.NewForumService(forumRepo, nil, nil)

	guestKeyRepo := repository.NewMemoryGuestKeyRepository()
	repRepo := repository.NewMemoryReputationRepository()
	portalHandler := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, trustBlockRepo, apiKeyRepo, guestKeyRepo, repRepo, &geo.StubResolver{}, nil, nil, nil)

	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		AssetHandler:  NewAssetHandlerWithPeers(assetSvc, peerSvc),
		DMCAHandler:   NewDMCAHandler(dmcaSvc),
		PortalHandler: portalHandler,
		APIKeyRepo:    apiKeyRepo,
		Address:       ":7842",
	})

	return &portalTestEnv{srv: srv, assetRepo: assetRepo, peerRepo: peerRepo, repRepo: repRepo, store: store}
}

// TestPortal_GalleryTrending_HasDownloadCount seeds assets with DownloadCount and
// asserts the gallery response includes the "download_count" JSON field on each item.
func TestPortal_GalleryTrending_HasDownloadCount(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed 2 assets with non-zero download counts
	for i, dc := range []int64{42, 7} {
		err := env.assetRepo.Create(ctx, &models.Asset{
			CID:           fmt.Sprintf("cid-%d", i),
			Filename:      fmt.Sprintf("file-%d.bin", i),
			Size:          int64(1024 * (i + 1)),
			PeerID:        "peer-1",
			ManifestType:  "raw",
			AnnouncedAt:   time.Now(),
			DownloadCount: dc,
		})
		if err != nil {
			t.Fatalf("seed asset %d: %v", i, err)
		}
	}

	// Hit trending (no query) — uses assetRepo.ListTrending directly (no online-peer filter)
	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/gallery/search status = %d, want 200", w.Code)
	}

	// Parse as raw JSON to check field presence
	var envelope struct {
		Data struct {
			Items []map[string]interface{} `json:"items"`
			Total float64                  `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(envelope.Data.Items) != 2 {
		t.Fatalf("items count = %d, want 2", len(envelope.Data.Items))
	}

	// Assert download_count field exists on first item
	dc, ok := envelope.Data.Items[0]["download_count"]
	if !ok {
		t.Fatal("items[0] missing 'download_count' field — GalleryItem must include DownloadCount")
	}
	// First item should be cid-0 (DownloadCount=42, sorted DESC)
	if dcVal, ok := dc.(float64); !ok || dcVal != 42 {
		t.Errorf("items[0].download_count = %v, want 42", dc)
	}

	// Second item should have download_count=7
	dc2, ok := envelope.Data.Items[1]["download_count"]
	if !ok {
		t.Fatal("items[1] missing 'download_count' field")
	}
	if dcVal, ok := dc2.(float64); !ok || dcVal != 7 {
		t.Errorf("items[1].download_count = %v, want 7", dc2)
	}
}

// TestPortal_GalleryTrending_TotalIsRepoCount seeds 25 assets (exceeding the
// handler's limit=20) and asserts that response.total reflects the real repo count (25),
// not the capped len(items) (20).
func TestPortal_GalleryTrending_TotalIsRepoCount(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed 25 assets (handler limits to 20 items)
	const totalAssets = 25
	for i := 0; i < totalAssets; i++ {
		err := env.assetRepo.Create(ctx, &models.Asset{
			CID:           fmt.Sprintf("cid-%03d", i),
			Filename:      fmt.Sprintf("file-%03d.bin", i),
			Size:          1024,
			PeerID:        "peer-1",
			ManifestType:  "raw",
			AnnouncedAt:   time.Now(),
			DownloadCount: int64(totalAssets - i), // descending so sort is deterministic
		})
		if err != nil {
			t.Fatalf("seed asset %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/gallery/search status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data struct {
			Items []map[string]interface{} `json:"items"`
			Total float64                  `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Handler limits items to 20
	if len(envelope.Data.Items) != 20 {
		t.Errorf("items count = %d, want 20 (handler limit)", len(envelope.Data.Items))
	}

	// Total MUST be 25 (the real repo count), NOT 20 (len(items))
	if envelope.Data.Total != float64(totalAssets) {
		t.Errorf("total = %v, want %d (repo count, not len(items))", envelope.Data.Total, totalAssets)
	}
}

// TestPortal_GallerySearch_TypeFilter seeds assets with different ManifestTypes and
// asserts that ?type=vec returns only vec assets (trending path) and ?q=...&type=raw
// returns only raw assets (search path).
func TestPortal_GallerySearch_TypeFilter(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed assets: 2 vec, 1 raw, 1 traj
	assets := []struct {
		cid      string
		filename string
		mtype    string
	}{
		{"cid-vec-1", "model-a.vec", "vec"},
		{"cid-vec-2", "model-b.vec", "vec"},
		{"cid-raw-1", "data.bin", "raw"},
		{"cid-traj-1", "path.traj", "traj"},
	}
	for _, a := range assets {
		err := env.assetRepo.Create(ctx, &models.Asset{
			CID:           a.cid,
			Filename:      a.filename,
			Size:          1024,
			PeerID:        "peer-1",
			ManifestType:  a.mtype,
			AnnouncedAt:   time.Now(),
			DownloadCount: 10,
		})
		if err != nil {
			t.Fatalf("seed asset %s: %v", a.cid, err)
		}
	}

	t.Run("trending with type=vec returns only vec assets", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/gallery/search?type=vec", nil)
		w := httptest.NewRecorder()
		env.srv.Router().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var envelope struct {
			Data struct {
				Items []map[string]interface{} `json:"items"`
				Total float64                  `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(envelope.Data.Items) != 2 {
			t.Fatalf("items count = %d, want 2 (only vec)", len(envelope.Data.Items))
		}
		for i, item := range envelope.Data.Items {
			if item["type"] != "vec" {
				t.Errorf("items[%d].type = %v, want vec", i, item["type"])
			}
		}
	})

	t.Run("search with type=vec excludes non-vec matches", func(t *testing.T) {
		// "model" matches model-a.vec AND model-b.vec (both vec).
		// Without type filter, Search returns both. With type=vec, should still return 2.
		// But to test the filter actually works, also seed a raw asset matching "model".
		err := env.assetRepo.Create(ctx, &models.Asset{
			CID:           "cid-raw-model",
			Filename:      "model-weights.bin",
			Size:          2048,
			PeerID:        "peer-1",
			ManifestType:  "raw",
			AnnouncedAt:   time.Now(),
			DownloadCount: 5,
		})
		if err != nil {
			t.Fatalf("seed extra: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/gallery/search?q=model&type=vec", nil)
		w := httptest.NewRecorder()
		env.srv.Router().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var envelope struct {
			Data struct {
				Items []map[string]interface{} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// Without type filter "model" matches 3 assets (2 vec + 1 raw).
		// With type=vec, only the 2 vec assets should be returned.
		if len(envelope.Data.Items) != 2 {
			t.Fatalf("items count = %d, want 2 (only vec matching 'model')", len(envelope.Data.Items))
		}
		for i, item := range envelope.Data.Items {
			if item["type"] != "vec" {
				t.Errorf("items[%d].type = %v, want vec", i, item["type"])
			}
		}
	})

	t.Run("no type filter returns all assets", func(t *testing.T) {
		// By this point 5 assets exist (4 initial + 1 added in search subtest)
		req := httptest.NewRequest(http.MethodGet, "/api/gallery/search", nil)
		w := httptest.NewRecorder()
		env.srv.Router().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}

		var envelope struct {
			Data struct {
				Items []map[string]interface{} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(envelope.Data.Items) < 4 {
			t.Errorf("items count = %d, want >= 4 (all assets)", len(envelope.Data.Items))
		}
	})
}
