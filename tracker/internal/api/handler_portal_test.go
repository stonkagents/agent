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
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// newTestServerWithPortal builds a Server with PortalHandler for testing portal routes.
func newTestServerWithPortal(t *testing.T) *Server {
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
	reputationRepo := repository.NewMemoryReputationRepository()
	portalHandler := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, trustBlockRepo, apiKeyRepo, guestKeyRepo, reputationRepo, &geo.StubResolver{}, nil, nil, nil)

	srv := NewServer(ServerDeps{
		PeerHandler:   NewPeerHandler(peerSvc),
		AssetHandler:  NewAssetHandlerWithPeers(assetSvc, peerSvc),
		DMCAHandler:   NewDMCAHandler(dmcaSvc),
		PortalHandler: portalHandler,
		APIKeyRepo:    apiKeyRepo,
		Address:       ":7842",
	})
	return srv
}

func TestPortal_HandleHome_Returns200AndDataEnvelope(t *testing.T) {
	srv := newTestServerWithPortal(t)

	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/home status = %d, want %d", w.Code, http.StatusOK)
	}

	var envelope DataEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data == nil {
		t.Error("response envelope data is nil")
	}

	// HomeResponse shape
	data, ok := envelope.Data.(map[string]interface{})
	if !ok {
		t.Errorf("response data type = %T, want map", envelope.Data)
		return
	}
	if _, ok := data["visionStats"]; !ok {
		t.Error("response data missing visionStats")
	}
	if _, ok := data["trendingAssets"]; !ok {
		t.Error("response data missing trendingAssets")
	}
	if _, ok := data["mostInstalled"]; !ok {
		t.Error("response data missing mostInstalled")
	}
	if _, ok := data["recentlyShared"]; !ok {
		t.Error("response data missing recentlyShared")
	}
}

func TestPortal_HandleHome_EmptyTrendingReturnsZeroLengthArrays(t *testing.T) {
	srv := newTestServerWithPortal(t)

	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/home status = %d, want %d", w.Code, http.StatusOK)
	}

	var envelope DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	data := envelope.Data.(map[string]interface{})

	trending := data["trendingAssets"].([]interface{})
	if len(trending) != 0 {
		t.Errorf("trendingAssets length = %d, want 0 (no assets announced)", len(trending))
	}
	recentlyShared := data["recentlyShared"].([]interface{})
	if len(recentlyShared) != 0 {
		t.Errorf("recentlyShared length = %d, want 0", len(recentlyShared))
	}
}

func TestPortal_HandleHome_VisionStatsIncludesAvgReputation(t *testing.T) {
	srv := newTestServerWithPortal(t)

	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/home status = %d, want %d", w.Code, http.StatusOK)
	}

	var envelope DataEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	data := envelope.Data.(map[string]interface{})
	stats := data["visionStats"].([]interface{})

	if len(stats) != 4 {
		t.Fatalf("visionStats length = %d, want 4", len(stats))
	}

	fourth := stats[3].(map[string]interface{})
	if fourth["label"] != "Avg Reputation" {
		t.Errorf("4th stat label = %q, want %q", fourth["label"], "Avg Reputation")
	}
	if fourth["value"] != "0.0" {
		t.Errorf("4th stat value = %q, want %q (no reputation records)", fourth["value"], "0.0")
	}
}

func TestPortal_HandlePeers_Returns200AndDataEnvelope(t *testing.T) {
	srv := newTestServerWithPortal(t)

	req := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/peers status = %d, want %d", w.Code, http.StatusOK)
	}

	var envelope DataEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data == nil {
		t.Error("response envelope data is nil")
	}
}

func TestPortal_HandleBoardPosts_UpvotedByMeWithAPIKey(t *testing.T) {
	srv := newTestServerWithPortal(t)

	ctx := context.Background()

	// Access internal repos and services through the portal handler.
	ph := srv.portal
	forumSvc := ph.forumService
	apiKeyRepo := ph.apiKeyRepo

	// Create two posts.
	postA, err := forumSvc.CreatePost(ctx, "peer-alice", services.CreatePostInput{Title: "Post A", Description: "First post body"})
	if err != nil {
		t.Fatalf("CreatePost A: %v", err)
	}
	postB, err := forumSvc.CreatePost(ctx, "peer-bob", services.CreatePostInput{Title: "Post B", Description: "Second post body"})
	if err != nil {
		t.Fatalf("CreatePost B: %v", err)
	}

	// peer-alice upvotes postB (not postA).
	if _, _, err := forumSvc.ToggleUpvote(ctx, "peer-alice", postB.ID); err != nil {
		t.Fatalf("ToggleUpvote: %v", err)
	}

	// Create an API key for peer-alice.
	apiKey, err := apiKeyRepo.Create(ctx, "peer-alice")
	if err != nil {
		t.Fatalf("Create API key: %v", err)
	}

	// GET /api/board/posts with X-API-Key header.
	req := httptest.NewRequest(http.MethodGet, "/api/board/posts?tab=recent", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/board/posts status = %d, want %d", w.Code, http.StatusOK)
	}

	var envelope DataEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	posts, ok := envelope.Data.([]interface{})
	if !ok {
		t.Fatalf("response data type = %T, want []interface{}", envelope.Data)
	}
	if len(posts) != 2 {
		t.Fatalf("posts length = %d, want 2", len(posts))
	}

	// Build a map of post ID -> upvotedByMe from the response.
	upvotedByID := make(map[string]bool)
	for _, raw := range posts {
		p := raw.(map[string]interface{})
		id := p["id"].(string)
		// omitempty means the field may be absent (false) or present (true).
		upvoted, _ := p["upvotedByMe"].(bool)
		upvotedByID[id] = upvoted
	}

	if upvotedByID[postA.ID] {
		t.Errorf("postA.upvotedByMe = true, want false (alice did not upvote postA)")
	}
	if !upvotedByID[postB.ID] {
		t.Errorf("postB.upvotedByMe = false, want true (alice upvoted postB)")
	}
}

func TestPortal_HandleBoardPosts_NoAPIKeyOmitsUpvotedByMe(t *testing.T) {
	srv := newTestServerWithPortal(t)

	ctx := context.Background()
	ph := srv.portal

	// Create a post and upvote it.
	post, err := ph.forumService.CreatePost(ctx, "peer-alice", services.CreatePostInput{Title: "Post X", Description: "Body X"})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if _, _, err := ph.forumService.ToggleUpvote(ctx, "peer-bob", post.ID); err != nil {
		t.Fatalf("ToggleUpvote: %v", err)
	}

	// GET without X-API-Key — upvotedByMe should be absent (omitempty).
	req := httptest.NewRequest(http.MethodGet, "/api/board/posts?tab=recent", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/board/posts status = %d, want %d", w.Code, http.StatusOK)
	}

	// Parse raw JSON to check the field is literally absent.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	var posts []map[string]json.RawMessage
	if err := json.Unmarshal(raw["data"], &posts); err != nil {
		t.Fatalf("decode data array: %v", err)
	}
	if len(posts) == 0 {
		t.Fatal("expected at least one post")
	}
	for i, p := range posts {
		if _, exists := p["upvotedByMe"]; exists {
			t.Errorf("post[%d] has upvotedByMe field, want omitted when no API key", i)
		}
	}
}

func TestPortal_HandleGuestKey_Returns200AndApiKey(t *testing.T) {
	srv := newTestServerWithPortal(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/portal/guest-key", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /api/v1/portal/guest-key status = %d, want %d", w.Code, http.StatusOK)
	}

	var envelope DataEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data == nil {
		t.Fatal("response envelope data is nil")
	}
	data, ok := envelope.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("response data type = %T, want map", envelope.Data)
	}
	apiKey, _ := data["apiKey"].(string)
	if apiKey == "" || len(apiKey) < 32 {
		t.Errorf("response data.apiKey = %q, want non-empty key (64 hex chars)", apiKey)
	}
}
