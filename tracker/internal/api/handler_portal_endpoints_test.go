// Package: tracker/internal/api
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: Tests for new portal endpoints: untrust, unblock, trusted/blocked lists, peer assets, peer reputation

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

// --- Task 13: Untrust + Unblock ---

func TestPortalUntrust_Success(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "target-peer", LastSeen: time.Now()})

	_ = env.trustBlockRepo.Trust(ctx, "actor-peer", "target-peer")

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/target-peer/trust", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /api/peers/{id}/trust status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Success bool   `json:"success"`
			PeerID  string `json:"peer_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Data.Success {
		t.Error("expected success=true")
	}
	if resp.Data.PeerID != "target-peer" {
		t.Errorf("peer_id = %q, want %q", resp.Data.PeerID, "target-peer")
	}

	// Verify trust was removed
	trusted, _ := env.trustBlockRepo.IsTrusted(ctx, "actor-peer", "target-peer")
	if trusted {
		t.Error("peer should no longer be trusted after untrust")
	}
}

func TestPortalUntrust_Unauthorized(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/target-peer/trust", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestPortalUntrust_Self(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/actor-peer/trust", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPortalUnblock_Success(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "target-peer", LastSeen: time.Now()})

	_ = env.trustBlockRepo.Block(ctx, "actor-peer", "target-peer")

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/target-peer/block", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /api/peers/{id}/block status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	blocked, _ := env.trustBlockRepo.IsBlocked(ctx, "actor-peer", "target-peer")
	if blocked {
		t.Error("peer should no longer be blocked after unblock")
	}
}

func TestPortalUnblock_Unauthorized(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/target-peer/block", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestPortalUnblock_Self(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodDelete, "/api/peers/actor-peer/block", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// --- Task 14: Trusted + Blocked lists ---

func TestPortalTrustedList_ReturnsPeerIDs(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	_ = env.trustBlockRepo.Trust(ctx, "actor-peer", "peer-a")
	_ = env.trustBlockRepo.Trust(ctx, "actor-peer", "peer-b")

	req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []string `json:"data"` // ADR-001: direct array
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Errorf("data length = %d, want 2", len(resp.Data))
	}
	if resp.Meta.Total != 2 {
		t.Errorf("meta.total = %d, want 2", resp.Meta.Total)
	}
}

func TestPortalTrustedList_EmptyReturnsEmptyArray(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data []string `json:"data"` // ADR-001: direct array
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Errorf("data should be empty array, got %v", resp.Data)
	}
	if resp.Meta.Total != 0 {
		t.Errorf("meta.total = %d, want 0", resp.Meta.Total)
	}
}

func TestPortalTrustedList_Unauthorized(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers/trusted", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestPortalBlockedList_ReturnsPeerIDs(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "actor-peer", LastSeen: time.Now()})

	_ = env.trustBlockRepo.Block(ctx, "actor-peer", "bad-peer")

	req := httptest.NewRequest(http.MethodGet, "/api/peers/blocked", nil)
	req.Header.Set("X-API-Key", actorKey)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []string `json:"data"` // ADR-001: direct array
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Errorf("data length = %d, want 1", len(resp.Data))
	}
	if resp.Meta.Total != 1 {
		t.Errorf("meta.total = %d, want 1", resp.Meta.Total)
	}
	if resp.Data[0] != "bad-peer" {
		t.Errorf("data[0] = %q, want %q", resp.Data[0], "bad-peer")
	}
}

func TestPortalBlockedList_Unauthorized(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers/blocked", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// --- Task 15: Peer Assets ---

func TestPortalPeerAssets_ReturnsTopByDownloads(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-sharer", LastSeen: time.Now()})

	for i, dc := range []int64{100, 50, 200} {
		_ = env.assetRepo.Create(ctx, &models.Asset{
			CID:           fmt.Sprintf("cid-%d", i),
			Filename:      fmt.Sprintf("file-%d.bin", i),
			Size:          int64(1024 * (i + 1)),
			PeerID:        "peer-sharer",
			ManifestType:  "raw",
			AnnouncedAt:   time.Now(),
			DownloadCount: dc,
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-sharer/assets", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			CID           string `json:"cid"`
			Filename      string `json:"filename"`
			FileType      string `json:"file_type"`
			SizeBytes     int64  `json:"size_bytes"`
			DownloadCount int64  `json:"download_count"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Data) != 3 {
		t.Fatalf("assets count = %d, want 3", len(resp.Data))
	}
	if resp.Data[0].DownloadCount != 200 {
		t.Errorf("first asset download_count = %d, want 200 (sorted DESC)", resp.Data[0].DownloadCount)
	}
}

func TestPortalPeerAssets_EmptyReturnsEmptyArray(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-empty", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-empty/assets", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data []interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Errorf("data length = %d, want 0", len(resp.Data))
	}
}

func TestPortalPeerAssets_LimitParam(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-many", LastSeen: time.Now()})

	for i := 0; i < 5; i++ {
		_ = env.assetRepo.Create(ctx, &models.Asset{
			CID:          fmt.Sprintf("cid-%d", i),
			Filename:     fmt.Sprintf("file-%d.bin", i),
			PeerID:       "peer-many",
			ManifestType: "raw",
			AnnouncedAt:  time.Now(),
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-many/assets?limit=2", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	var resp struct {
		Data []interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Errorf("data length = %d, want 2 (limited)", len(resp.Data))
	}
}

// --- Task 16: Peer Reputation ---

func TestPortalPeerReputation_ReturnsScores(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:             "peer-scored",
		LastSeen:           time.Now(),
		TotalUploadBytes:   5000,
		TotalDownloadBytes: 2000,
	})

	_ = env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:           "peer-scored",
		CompositeScore:   0.65,
		BandwidthScore:   0.70,
		QualityScore:     0.60,
		SecurityScore:    0.80,
		CitizenshipScore: 0.40,
		UpdatedAt:        time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-scored/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			CompositeScore   float64 `json:"composite_score"`
			BandwidthScore   float64 `json:"bandwidth_score"`
			QualityScore     float64 `json:"quality_score"`
			SecurityScore    float64 `json:"security_score"`
			CitizenshipScore float64 `json:"citizenship_score"`
			Tier             string  `json:"tier"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Data.CompositeScore != 0.65 {
		t.Errorf("composite_score = %v, want 0.65", resp.Data.CompositeScore)
	}
	if resp.Data.BandwidthScore != 0.70 {
		t.Errorf("bandwidth_score = %v, want 0.70", resp.Data.BandwidthScore)
	}
	if resp.Data.Tier != "gold" {
		t.Errorf("tier = %q, want %q", resp.Data.Tier, "gold")
	}
}

func TestPortalPeerReputation_NoRecord_ReturnsZeros(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-new", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-new/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			CompositeScore float64 `json:"composite_score"`
			Tier           string  `json:"tier"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Data.CompositeScore != 0 {
		t.Errorf("composite_score = %v, want 0", resp.Data.CompositeScore)
	}
	if resp.Data.Tier != "new" {
		t.Errorf("tier = %q, want %q", resp.Data.Tier, "new")
	}
}

func TestPortalPeerReputation_BootstrapRule(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:             "peer-fresh",
		LastSeen:           time.Now(),
		TotalUploadBytes:   0,
		TotalDownloadBytes: 0,
	})

	_ = env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "peer-fresh",
		CompositeScore: 0.45,
		UpdatedAt:      time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-fresh/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	var resp struct {
		Data struct {
			Tier string `json:"tier"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Data.Tier != "new" {
		t.Errorf("tier = %q, want %q (bootstrap rule: zero activity = new)", resp.Data.Tier, "new")
	}
}

// --- Task 20: Badges + WeeklyBonus in reputation response ---

func TestPortalPeerReputation_IncludesBadges(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	origLaunch := LaunchDate
	LaunchDate = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() { LaunchDate = origLaunch }()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:             "peer-badges",
		LastSeen:           time.Now(),
		FirstSeen:          LaunchDate.Add(3 * 24 * time.Hour), // within ogDays + earlyAdopterDays
		TotalUploadBytes:   5000,
		TotalDownloadBytes: 2000,
	})

	_ = env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "peer-badges",
		CompositeScore: 0.65,
		UpdatedAt:      time.Now(),
	})

	// Add some trusts for trusted_network badge
	for i := 0; i < 6; i++ {
		_ = env.trustBlockRepo.Trust(ctx, fmt.Sprintf("actor-%d", i), "peer-badges")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-badges/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Badges      []reputation.BadgeEntry `json:"badges"`
			WeeklyBonus int                     `json:"weekly_bonus"`
			Tier        string                  `json:"tier"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Data.Badges) != 10 {
		t.Errorf("badges count = %d, want 10", len(resp.Data.Badges))
	}

	// Verify early_adopter is earned (firstSeen within earlyAdopterDays of LaunchDate)
	for _, b := range resp.Data.Badges {
		if b.ID == "early_adopter" && b.Status != "earned" {
			t.Errorf("early_adopter = %q, want earned", b.Status)
		}
		if b.ID == "og_status" && b.Status != "earned" {
			t.Errorf("og_status = %q, want earned (firstSeen within ogDays)", b.Status)
		}
		if b.ID == "trusted_network" && b.Status != "earned" {
			t.Errorf("trusted_network = %q, want earned (6 trusts >= threshold)", b.Status)
		}
	}

	// Tier is gold (0.65, has activity), weekly_bonus for gold = 50
	if resp.Data.Tier != "gold" {
		t.Errorf("tier = %q, want gold", resp.Data.Tier)
	}
	if resp.Data.WeeklyBonus != 50 {
		t.Errorf("weekly_bonus = %d, want 50", resp.Data.WeeklyBonus)
	}
}

func TestPortalPeerReputation_Trend_WithSnapshot(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:             "peer-trending",
		LastSeen:           time.Now(),
		TotalUploadBytes:   5000,
		TotalDownloadBytes: 2000,
	})

	// Current score: 0.65
	_ = env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "peer-trending",
		CompositeScore: 0.65,
		UpdatedAt:      time.Now(),
	})

	// Previous snapshot: 0.45 (24 hours ago)
	_ = env.snapshotRepo.Upsert(ctx, &reputation.ReputationSnapshot{
		PeerID:         "peer-trending",
		CompositeScore: 0.45,
		SnappedAt:      time.Now().Add(-24 * time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-trending/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Trend *float64 `json:"trend"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Data.Trend == nil {
		t.Fatal("trend is nil, want ~0.20 (0.65 - 0.45)")
	}
	// Allow small float imprecision
	if *resp.Data.Trend < 0.19 || *resp.Data.Trend > 0.21 {
		t.Errorf("trend = %v, want ~0.20", *resp.Data.Trend)
	}
}

func TestPortalPeerReputation_Trend_NoSnapshot(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:             "peer-notrend",
		LastSeen:           time.Now(),
		TotalUploadBytes:   5000,
		TotalDownloadBytes: 2000,
	})

	_ = env.reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "peer-notrend",
		CompositeScore: 0.50,
		UpdatedAt:      time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-notrend/reputation", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Trend *float64 `json:"trend"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Data.Trend != nil {
		t.Errorf("trend = %v, want nil (no previous snapshot)", *resp.Data.Trend)
	}
}
