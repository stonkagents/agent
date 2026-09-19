// Package: tracker/internal/api
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: Tests for enriched HandlePortalPeers with batch queries, pagination, status/rank derivation

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// enrichedPortalEnv holds a test server and its repos for Slice 2+3+4 endpoint testing.
type enrichedPortalEnv struct {
	srv            *Server
	peerRepo       repository.PeerRepository
	assetRepo      repository.AssetRepository
	reputationRepo repository.ReputationRepository
	trustBlockRepo repository.PeerTrustBlockRepository
	apiKeyRepo     repository.PeerAPIKeyRepository
	peerEventRepo  repository.PeerEventRepository
	snapshotRepo   repository.ReputationSnapshotRepository
	store          presence.PresenceStore
	clk            *clock.MockClock
}

// newEnrichedPortalServer builds a Server with all deps for enriched peer list testing.
// Returns the server plus repos/stores so tests can seed data.
func newEnrichedPortalServer(t *testing.T) (
	*Server,
	repository.PeerRepository,
	repository.AssetRepository,
	repository.ReputationRepository,
	presence.PresenceStore,
	*clock.MockClock,
) {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	dmcaRepo := repository.NewMemoryDMCARepository()
	forumRepo := repository.NewMemoryForumRepository()
	trustBlockRepo := repository.NewMemoryPeerTrustBlockRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	reputationRepo := repository.NewMemoryReputationRepository()
	store := presence.NewMemoryPresenceStore(clk)

	peerSvc := services.NewPeerService(peerRepo, store, apiKeyRepo)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	dmcaSvc := services.NewDMCAService(assetRepo, dmcaRepo)
	forumSvc := services.NewForumService(forumRepo, nil, nil)

	guestKeyRepo := repository.NewMemoryGuestKeyRepository()
	geoResolver := &geo.StubResolver{}
	portalHandler := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, trustBlockRepo, apiKeyRepo, guestKeyRepo, reputationRepo, geoResolver, nil, nil, nil)

	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		AssetHandler:  NewAssetHandlerWithPeers(assetSvc, peerSvc),
		DMCAHandler:   NewDMCAHandler(dmcaSvc),
		PortalHandler: portalHandler,
		APIKeyRepo:    apiKeyRepo,
		Address:       ":7842",
	})
	return srv, peerRepo, assetRepo, reputationRepo, store, clk
}

// newEnrichedPortalEnv builds a test env returning all repos (including trustBlock + apiKey).
func newEnrichedPortalEnv(t *testing.T) *enrichedPortalEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	dmcaRepo := repository.NewMemoryDMCARepository()
	forumRepo := repository.NewMemoryForumRepository()
	trustBlockRepo := repository.NewMemoryPeerTrustBlockRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	reputationRepo := repository.NewMemoryReputationRepository()
	store := presence.NewMemoryPresenceStore(clk)

	peerSvc := services.NewPeerService(peerRepo, store, apiKeyRepo)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	dmcaSvc := services.NewDMCAService(assetRepo, dmcaRepo)
	forumSvc := services.NewForumService(forumRepo, nil, nil)

	guestKeyRepo := repository.NewMemoryGuestKeyRepository()
	peerEventRepo := repository.NewMemoryPeerEventRepository()
	snapshotRepo := repository.NewMemoryReputationSnapshotRepository()
	geoResolver := &geo.StubResolver{}
	portalHandler := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, trustBlockRepo, apiKeyRepo, guestKeyRepo, reputationRepo, geoResolver, peerEventRepo, snapshotRepo, nil)

	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		AssetHandler:  NewAssetHandlerWithPeers(assetSvc, peerSvc),
		DMCAHandler:   NewDMCAHandler(dmcaSvc),
		PortalHandler: portalHandler,
		APIKeyRepo:    apiKeyRepo,
		Address:       ":7842",
	})
	return &enrichedPortalEnv{
		srv:            srv,
		peerRepo:       peerRepo,
		assetRepo:      assetRepo,
		reputationRepo: reputationRepo,
		trustBlockRepo: trustBlockRepo,
		apiKeyRepo:     apiKeyRepo,
		peerEventRepo:  peerEventRepo,
		snapshotRepo:   snapshotRepo,
		store:          store,
		clk:            clk,
	}
}

// seedPeer creates a peer directly in the repo and presence store.
func seedPeer(t *testing.T, peerRepo repository.PeerRepository, store presence.PresenceStore, peer *models.Peer) {
	t.Helper()
	ctx := context.Background()
	if err := peerRepo.Upsert(ctx, peer); err != nil {
		t.Fatalf("seedPeer(%s): %v", peer.PeerID, err)
	}
	if err := store.Heartbeat(ctx, peer.PeerID, 5*time.Minute); err != nil {
		t.Fatalf("seedPeer heartbeat(%s): %v", peer.PeerID, err)
	}
}

func TestEnrichedPeers_ReturnsPaginatedEnvelope(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)
	ctx := context.Background()

	// Seed 3 peers
	for _, id := range []string{"peer-a", "peer-b", "peer-c"} {
		seedPeer(t, peerRepo, store, &models.Peer{
			PeerID:   id,
			LastSeen: time.Now(),
		})
	}
	_ = ctx

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/peers status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp PaginatedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Meta.Total != 3 {
		t.Errorf("meta.total = %d, want 3", resp.Meta.Total)
	}
	if resp.Meta.Limit != 20 {
		t.Errorf("meta.limit = %d, want 20 (default)", resp.Meta.Limit)
	}
	if resp.Meta.Offset != 0 {
		t.Errorf("meta.offset = %d, want 0", resp.Meta.Offset)
	}

	// Data should be an array of 3
	dataArr, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("data type = %T, want []interface{}", resp.Data)
	}
	if len(dataArr) != 3 {
		t.Errorf("data length = %d, want 3", len(dataArr))
	}
}

func TestEnrichedPeers_PaginationOffsetLimit(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	// Seed 5 peers
	for _, id := range []string{"peer-a", "peer-b", "peer-c", "peer-d", "peer-e"} {
		seedPeer(t, peerRepo, store, &models.Peer{
			PeerID:   id,
			LastSeen: time.Now(),
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peers?limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Meta.Total != 5 {
		t.Errorf("meta.total = %d, want 5", resp.Meta.Total)
	}
	if resp.Meta.Limit != 2 {
		t.Errorf("meta.limit = %d, want 2", resp.Meta.Limit)
	}
	if resp.Meta.Offset != 1 {
		t.Errorf("meta.offset = %d, want 1", resp.Meta.Offset)
	}

	dataArr, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("data type = %T, want []interface{}", resp.Data)
	}
	if len(dataArr) != 2 {
		t.Errorf("data length = %d, want 2", len(dataArr))
	}
}

func TestEnrichedPeers_RealReputationNotHardcoded(t *testing.T) {
	srv, peerRepo, _, reputationRepo, store, _ := newEnrichedPortalServer(t)
	ctx := context.Background()

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:             "peer-scored",
		LastSeen:           time.Now(),
		TotalUploadBytes:   1000,
		TotalDownloadBytes: 500,
	})

	// Seed reputation: composite = 0.72 → displayed as 72, tier = "gold"
	_ = reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:           "peer-scored",
		CompositeScore:   0.72,
		BandwidthScore:   0.8,
		QualityScore:     0.6,
		SecurityScore:    0.7,
		CitizenshipScore: 0.5,
		UpdatedAt:        time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(dataArr))
	}

	peer := dataArr[0].(map[string]interface{})
	rep := int(peer["reputation"].(float64))
	if rep != 72 {
		t.Errorf("reputation = %d, want 72 (composite*100)", rep)
	}
	tier := peer["tier"].(string)
	if tier != "gold" {
		t.Errorf("tier = %q, want %q", tier, "gold")
	}
}

func TestEnrichedPeers_RealSharedFilesCount(t *testing.T) {
	srv, peerRepo, assetRepo, _, store, _ := newEnrichedPortalServer(t)
	ctx := context.Background()

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:   "peer-sharer",
		LastSeen: time.Now(),
	})

	// Create 3 assets for this peer
	for _, cid := range []string{"cid-1", "cid-2", "cid-3"} {
		_ = assetRepo.Create(ctx, &models.Asset{
			CID:          cid,
			Filename:     "test-" + cid,
			PeerID:       "peer-sharer",
			AnnouncedAt:  time.Now(),
			ManifestType: "raw",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	peer := dataArr[0].(map[string]interface{})
	sharedFiles := int(peer["sharedFiles"].(float64))
	if sharedFiles != 3 {
		t.Errorf("sharedFiles = %d, want 3", sharedFiles)
	}
}

func TestEnrichedPeers_StatusDerived(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	now := time.Now()
	recentUpload := now.Add(-2 * time.Minute)
	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:       "peer-seeder",
		LastSeen:     now,
		LastUploadAt: &recentUpload,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	peer := dataArr[0].(map[string]interface{})
	status := peer["status"].(string)
	if status != "seeding" {
		t.Errorf("status = %q, want %q (recent upload)", status, "seeding")
	}
}

func TestEnrichedPeers_DisplayNameUsedForName(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:      "12D3KooWAbcdefghijklmnopqrstuvwxyz123456789",
		DisplayName: "Alice",
		LastSeen:    time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	peer := dataArr[0].(map[string]interface{})
	name := peer["name"].(string)
	if name != "Alice" {
		t.Errorf("name = %q, want %q (DisplayName should be used)", name, "Alice")
	}
}

func TestEnrichedPeers_MaskedPeerIDWhenNoDisplayName(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:   "12D3KooWAbcdefghijklmnopqrstuvwxyz123456789",
		LastSeen: time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	peer := dataArr[0].(map[string]interface{})
	name := peer["name"].(string)
	// Should be masked: first 6 + "..." + last 6
	expected := geo.MaskPeerID("12D3KooWAbcdefghijklmnopqrstuvwxyz123456789")
	if name != expected {
		t.Errorf("name = %q, want %q (masked peer ID)", name, expected)
	}
}

func TestEnrichedPeers_EmptyListReturnsPaginatedEmpty(t *testing.T) {
	srv, _, _, _, _, _ := newEnrichedPortalServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Meta.Total != 0 {
		t.Errorf("meta.total = %d, want 0", resp.Meta.Total)
	}

	dataArr, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("data type = %T, want []interface{}", resp.Data)
	}
	if len(dataArr) != 0 {
		t.Errorf("data length = %d, want 0", len(dataArr))
	}
}

func TestEnrichedPeers_SearchByDisplayName(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:      "peer-alice",
		DisplayName: "Alice",
		LastSeen:    time.Now(),
	})
	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:      "peer-bob",
		DisplayName: "Bob",
		LastSeen:    time.Now(),
	})
	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:      "peer-alicia",
		DisplayName: "Alicia",
		LastSeen:    time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers?q=ali", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 2 {
		t.Errorf("search 'ali' returned %d peers, want 2 (Alice + Alicia)", len(dataArr))
	}

	// Total should reflect filtered count, not total DB count
	if resp.Meta.Total != 2 {
		t.Errorf("meta.total = %d, want 2 (filtered count)", resp.Meta.Total)
	}
}

func TestEnrichedPeers_SearchByPeerID(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:   "12D3KooWXyZaBcDeFgHiJkLmNoPqRsTuVwXyZ12345",
		LastSeen: time.Now(),
	})
	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:   "peer-other",
		LastSeen: time.Now(),
	})

	// Search by partial peer ID (case-insensitive)
	req := httptest.NewRequest(http.MethodGet, "/api/peers?q=12d3koow", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 1 {
		t.Errorf("search '12d3koow' returned %d peers, want 1", len(dataArr))
	}
}

func TestEnrichedPeers_SearchEmptyQueryReturnsAll(t *testing.T) {
	srv, peerRepo, _, _, store, _ := newEnrichedPortalServer(t)

	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:   "peer-a",
		LastSeen: time.Now(),
	})
	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:   "peer-b",
		LastSeen: time.Now(),
	})

	// Empty q= should return all peers (same as no q param)
	req := httptest.NewRequest(http.MethodGet, "/api/peers?q=", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	if len(dataArr) != 2 {
		t.Errorf("empty search returned %d peers, want 2 (all)", len(dataArr))
	}
}

func TestEnrichedPeers_BootstrapRuleNewRank(t *testing.T) {
	srv, peerRepo, _, reputationRepo, store, _ := newEnrichedPortalServer(t)
	ctx := context.Background()

	// Peer with zero activity but default EigenTrust score (~0.45)
	seedPeer(t, peerRepo, store, &models.Peer{
		PeerID:             "peer-fresh",
		LastSeen:           time.Now(),
		TotalUploadBytes:   0,
		TotalDownloadBytes: 0,
	})

	_ = reputationRepo.Upsert(ctx, &reputation.ReputationRecord{
		PeerID:         "peer-fresh",
		CompositeScore: 0.45,
		UpdatedAt:      time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	dataArr := resp.Data.([]interface{})
	peer := dataArr[0].(map[string]interface{})
	tier := peer["tier"].(string)
	if tier != "new" {
		t.Errorf("tier = %q, want %q (bootstrap rule: zero activity = new)", tier, "new")
	}
}
