// Package api: Tests for gallery enrichment — author rep + total size bytes (F-027, US-027-03)
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-03 (Backend — Gallery Response Enrichment)
// Purpose: Verify gallery items include author_peer_id + peer_rep, and response includes total_size_bytes

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// TestPortal_GalleryItem_HasAuthorPeerID seeds an asset and asserts the gallery
// response includes "author_peer_id" on each item.
func TestPortal_GalleryItem_HasAuthorPeerID(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	err := env.assetRepo.Create(ctx, &models.Asset{
		CID:           "cid-author-test",
		Filename:      "agent-config.claw-skill",
		Size:          2048,
		PeerID:        "12D3KooWTestPeer123",
		ManifestType:  "claw-skill",
		AnnouncedAt:   time.Now(),
		DownloadCount: 5,
	})
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data struct {
			Items []map[string]interface{} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(envelope.Data.Items) != 1 {
		t.Fatalf("items count = %d, want 1", len(envelope.Data.Items))
	}

	authorPeerID, ok := envelope.Data.Items[0]["author_peer_id"]
	if !ok {
		t.Fatal("items[0] missing 'author_peer_id' field — GalleryItem must include AuthorPeerID")
	}
	if authorPeerID != "12D3KooWTestPeer123" {
		t.Errorf("author_peer_id = %v, want '12D3KooWTestPeer123'", authorPeerID)
	}
}

// TestPortal_GalleryItem_HasPeerRep seeds an asset and a reputation record,
// then asserts the gallery response includes "peer_rep" reflecting the stored composite score.
func TestPortal_GalleryItem_HasPeerRep(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed reputation for the peer
	err := env.repRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "peer-with-rep",
		CompositeScore: 0.72,
		BandwidthScore: 0.8,
		QualityScore:   0.6,
		SecurityScore:  0.9,
		UpdatedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("seed reputation: %v", err)
	}

	// Seed asset from that peer
	err = env.assetRepo.Create(ctx, &models.Asset{
		CID:           "cid-rep-test",
		Filename:      "model.vec",
		Size:          4096,
		PeerID:        "peer-with-rep",
		ManifestType:  "vec",
		AnnouncedAt:   time.Now(),
		DownloadCount: 10,
	})
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

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

	if len(envelope.Data.Items) != 1 {
		t.Fatalf("items count = %d, want 1", len(envelope.Data.Items))
	}

	peerRep, ok := envelope.Data.Items[0]["peer_rep"]
	if !ok {
		t.Fatal("items[0] missing 'peer_rep' field — GalleryItem must include PeerRep")
	}
	// Composite 0.72 → display as int 72 (0-100 scale)
	if repVal, ok := peerRep.(float64); !ok || repVal != 72 {
		t.Errorf("peer_rep = %v, want 72 (composite 0.72 × 100)", peerRep)
	}
}

// TestPortal_GalleryItem_PeerRepDefaultsToZero verifies that when no reputation
// record exists for the author peer, peer_rep defaults to 0.
func TestPortal_GalleryItem_PeerRepDefaultsToZero(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed asset — no reputation record for this peer
	err := env.assetRepo.Create(ctx, &models.Asset{
		CID:          "cid-no-rep",
		Filename:     "data.bin",
		Size:         1024,
		PeerID:       "peer-no-rep",
		ManifestType: "raw",
		AnnouncedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	var envelope struct {
		Data struct {
			Items []map[string]interface{} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(envelope.Data.Items) != 1 {
		t.Fatalf("items count = %d, want 1", len(envelope.Data.Items))
	}

	peerRep, ok := envelope.Data.Items[0]["peer_rep"]
	if !ok {
		t.Fatal("items[0] missing 'peer_rep' field")
	}
	if repVal, ok := peerRep.(float64); !ok || repVal != 0 {
		t.Errorf("peer_rep = %v, want 0 (no reputation record)", peerRep)
	}
}

// TestPortal_GalleryResponse_HasTotalSizeBytes seeds multiple assets and asserts
// the gallery response includes "total_size_bytes" as the sum of all item sizes.
func TestPortal_GalleryResponse_HasTotalSizeBytes(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed 3 assets: 1024 + 2048 + 4096 = 7168 bytes
	sizes := []int64{1024, 2048, 4096}
	for i, sz := range sizes {
		err := env.assetRepo.Create(ctx, &models.Asset{
			CID:           fmt.Sprintf("cid-size-%d", i),
			Filename:      fmt.Sprintf("file-%d.bin", i),
			Size:          sz,
			PeerID:        "peer-1",
			ManifestType:  "raw",
			AnnouncedAt:   time.Now(),
			DownloadCount: int64(i),
		})
		if err != nil {
			t.Fatalf("seed asset %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/gallery/search", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data struct {
			TotalSizeBytes float64 `json:"total_size_bytes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if envelope.Data.TotalSizeBytes != 7168 {
		t.Errorf("total_size_bytes = %v, want 7168 (1024+2048+4096)", envelope.Data.TotalSizeBytes)
	}
}
