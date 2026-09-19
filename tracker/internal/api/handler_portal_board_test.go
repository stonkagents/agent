// Package api: Tests for portal board endpoints (rich posts, bounty, token offer, CID, view count).
// Feature: F-031 (Token Data Persistence)
// Story: US-031-05 (Community Board End-to-End)
// Purpose: TDD tests for rich post creation, listing with pagination, single-post with view count

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/repository"
)

// createAuthenticatedPostRequest creates a POST /api/board/posts request with API key auth context.
func createAuthenticatedPostRequest(t *testing.T, srv *Server, body interface{}) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// seedAPIKey creates a peer API key and returns (peerID, apiKey).
func seedAPIKey(t *testing.T, apiKeyRepo repository.PeerAPIKeyRepository, peerID string) string {
	t.Helper()
	key, err := apiKeyRepo.Create(context.Background(), peerID)
	if err != nil {
		t.Fatalf("seed API key: %v", err)
	}
	return key
}

// --- Create post with rich fields ---

func TestPortalBoard_CreatePostWithBounty_ReturnsBountyInResponse(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-bounty-author")

	body := map[string]interface{}{
		"body":     "Looking for help with AI training dataset\nNeed someone to curate 1000 samples",
		"category": "bounty",
		"tags":     []string{"ai", "dataset"},
		"bounty": map[string]interface{}{
			"amount":   500,
			"currency": "credits",
			"days":     7,
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/board/posts status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope DataEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := envelope.Data.(map[string]interface{})

	// Verify bounty is present
	bounty, ok := data["bounty"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing bounty object")
	}
	if int(bounty["amount"].(float64)) != 500 {
		t.Errorf("bounty.amount = %v, want 500", bounty["amount"])
	}
	if bounty["currency"] != "credits" {
		t.Errorf("bounty.currency = %v, want credits", bounty["currency"])
	}
	if int(bounty["daysRemaining"].(float64)) < 6 {
		t.Errorf("bounty.daysRemaining = %v, want >= 6", bounty["daysRemaining"])
	}

	// Verify category
	if data["category"] != "bounty" {
		t.Errorf("category = %v, want bounty", data["category"])
	}

	// Verify tags
	tags, ok := data["tags"].([]interface{})
	if !ok || len(tags) != 2 {
		t.Errorf("tags = %v, want [ai, dataset]", data["tags"])
	}
}

func TestPortalBoard_CreatePostWithTokenOffer_ReturnsTokenOfferInResponse(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-token-offer")

	body := map[string]interface{}{
		"body":     "Offering tokens for quality dataset reviews",
		"category": "token-offer",
		"tags":     []string{"review"},
		"tokenOffer": map[string]interface{}{
			"amount": 100,
			"token":  "STNK",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var envelope DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	data := envelope.Data.(map[string]interface{})

	tokenOffer, ok := data["tokenOffer"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing tokenOffer object")
	}
	if int(tokenOffer["amount"].(float64)) != 100 {
		t.Errorf("tokenOffer.amount = %v, want 100", tokenOffer["amount"])
	}
	if tokenOffer["token"] != "STNK" {
		t.Errorf("tokenOffer.token = %v, want STNK", tokenOffer["token"])
	}
}

func TestPortalBoard_CreatePostWithCID_ReturnsCIDInResponse(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-cid-post")

	cid := "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG"
	body := map[string]interface{}{
		"body":     "Sharing my AI training dataset",
		"category": "discovery",
		"cid":      cid,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}

	var envelope DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	data := envelope.Data.(map[string]interface{})

	if data["cid"] != cid {
		t.Errorf("cid = %v, want %v", data["cid"], cid)
	}
}

func TestPortalBoard_CreatePostInvalidCategory_Returns400(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-bad-cat")

	body := map[string]interface{}{
		"body":     "test post",
		"category": "nonexistent-category",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

// --- List posts with pagination ---

func TestPortalBoard_ListPosts_ReturnsPaginationMeta(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-list-author")

	// Create 3 posts
	for i := 0; i < 3; i++ {
		body := map[string]interface{}{
			"body":     "Test post content " + string(rune('A'+i)),
			"category": "general",
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", apiKey)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("create post %d: status = %d; body: %s", i, w.Code, w.Body.String())
		}
	}

	// List with limit=2
	req := httptest.NewRequest(http.MethodGet, "/api/board/posts?limit=2&offset=0", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/board/posts status = %d; body: %s", w.Code, w.Body.String())
	}

	var result struct {
		Data []interface{}          `json:"data"`
		Meta map[string]interface{} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Data) != 2 {
		t.Errorf("data length = %d, want 2", len(result.Data))
	}
	if result.Meta == nil {
		t.Fatal("meta is nil, want pagination metadata")
	}
	if int(result.Meta["total"].(float64)) != 3 {
		t.Errorf("meta.total = %v, want 3", result.Meta["total"])
	}
	if int(result.Meta["limit"].(float64)) != 2 {
		t.Errorf("meta.limit = %v, want 2", result.Meta["limit"])
	}
}

func TestPortalBoard_ListPosts_RichFieldsInResponse(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-rich-list")

	// Create a bounty post
	body := map[string]interface{}{
		"body":     "Bounty post for listing",
		"category": "bounty",
		"tags":     []string{"test"},
		"bounty": map[string]interface{}{
			"amount":   250,
			"currency": "credits",
			"days":     14,
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: status = %d; body: %s", w.Code, w.Body.String())
	}

	// List posts
	req = httptest.NewRequest(http.MethodGet, "/api/board/posts?tab=recent", nil)
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d; body: %s", w.Code, w.Body.String())
	}

	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Data) == 0 {
		t.Fatal("no posts returned")
	}

	post := result.Data[0]
	if post["category"] != "bounty" {
		t.Errorf("category = %v, want bounty", post["category"])
	}
	bounty, ok := post["bounty"].(map[string]interface{})
	if !ok {
		t.Fatal("list post missing bounty")
	}
	if int(bounty["amount"].(float64)) != 250 {
		t.Errorf("bounty.amount = %v, want 250", bounty["amount"])
	}
}

// --- Single post with view count ---

func TestPortalBoard_GetPost_IncrementsViewCount(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-view-count")

	// Create a post
	body := map[string]interface{}{
		"body":     "View count test post",
		"category": "general",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create: status = %d; body: %s", w.Code, w.Body.String())
	}

	var createEnv DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &createEnv)
	createData := createEnv.Data.(map[string]interface{})
	postID := createData["id"].(string)

	// GET /api/board/posts/:id — first request
	req = httptest.NewRequest(http.MethodGet, "/api/board/posts/"+postID, nil)
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET post 1st: status = %d; body: %s", w.Code, w.Body.String())
	}

	var env1 DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env1)
	data1 := env1.Data.(map[string]interface{})
	if int(data1["viewCount"].(float64)) != 1 {
		t.Errorf("viewCount after 1st GET = %v, want 1", data1["viewCount"])
	}

	// GET again — second request
	req = httptest.NewRequest(http.MethodGet, "/api/board/posts/"+postID, nil)
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	var env2 DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env2)
	data2 := env2.Data.(map[string]interface{})
	if int(data2["viewCount"].(float64)) != 2 {
		t.Errorf("viewCount after 2nd GET = %v, want 2", data2["viewCount"])
	}
}

func TestPortalBoard_GetPost_NotFound_Returns404(t *testing.T) {
	srv := newTestServerWithPortal(t)

	req := httptest.NewRequest(http.MethodGet, "/api/board/posts/nonexistent-id", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET nonexistent post status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

// --- Default category ---

func TestPortalBoard_CreatePostNoCategory_DefaultsToGeneral(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-default-cat")

	body := map[string]interface{}{
		"body": "A post without category",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}

	var envelope DataEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	data := envelope.Data.(map[string]interface{})

	if data["category"] != "general" {
		t.Errorf("category = %v, want general", data["category"])
	}
}

// --- Service-level tests ---

func TestForumService_CreatePost_ValidatesCategory(t *testing.T) {
	srv := newTestServerWithPortal(t)
	apiKey := seedAPIKey(t, srv.apiKeyRepo, "peer-val-cat")

	tests := []struct {
		name     string
		category string
		wantOK   bool
	}{
		{"general", "general", true},
		{"bounty", "bounty", true},
		{"token-offer", "token-offer", true},
		{"request", "request", true},
		{"discovery", "discovery", true},
		{"invalid", "spam", false},
		{"empty defaults to general", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]interface{}{
				"body":     "Testing category " + tt.category,
				"category": tt.category,
			}
			b, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-API-Key", apiKey)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if tt.wantOK && w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}
			if !tt.wantOK && w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body: %s", w.Code, w.Body.String())
			}
		})
	}
}
