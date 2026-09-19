// Package api: Tests for activity feed endpoint (F-027, US-027-06)
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-06 (Backend — Tracker Activity Feed Endpoint)
// Purpose: Verify GET /api/activity/recent returns merged activity entries

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// TestPortal_ActivityRecent_ReturnsMergedFeed seeds download events, asset
// announcements, and new peers, then verifies the activity feed merges all 3 types.
func TestPortal_ActivityRecent_ReturnsMergedFeed(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed an asset (recent announcement → "share" type)
	err := env.assetRepo.Create(ctx, &models.Asset{
		CID:          "cid-shared",
		Filename:     "shared-file.bin",
		Size:         4096,
		PeerID:       "peer-sharer",
		ManifestType: "raw",
		AnnouncedAt:  time.Now().Add(-5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	// Record a download event for a different asset → "install" type
	err = env.assetRepo.Create(ctx, &models.Asset{
		CID:          "cid-installed",
		Filename:     "installed-file.vec",
		Size:         2048,
		PeerID:       "peer-installer",
		ManifestType: "vec",
		AnnouncedAt:  time.Now().Add(-30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("seed asset 2: %v", err)
	}
	if err := env.assetRepo.RecordDownloadEvent(ctx, "cid-installed"); err != nil {
		t.Fatalf("record download: %v", err)
	}

	// Seed a recently-joined peer → "join" type
	err = env.peerRepo.Create(ctx, &models.Peer{
		PeerID:    "peer-new-joiner",
		PublicKey: "pubkey-new",
		FirstSeen: time.Now().Add(-2 * time.Minute),
		LastSeen:  time.Now().Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	// Hit the activity feed
	req := httptest.NewRequest(http.MethodGet, "/api/activity/recent?limit=10", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/activity/recent status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// Parse response
	var envelope struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Should have at least 3 entries (1 share + 1 install + 1 join)
	if len(envelope.Data) < 3 {
		t.Fatalf("activity entries = %d, want >= 3", len(envelope.Data))
	}

	// Verify each entry has ALL required fields (type, title, time_ago, occurred_at, color)
	for i, entry := range envelope.Data {
		for _, field := range []string{"type", "title", "time_ago", "occurred_at", "color"} {
			if _, ok := entry[field]; !ok {
				t.Errorf("entry[%d] missing '%s' field", i, field)
			}
		}
		// occurred_at must be a parseable RFC3339 timestamp
		if ts, ok := entry["occurred_at"].(string); ok {
			if _, err := time.Parse(time.RFC3339, ts); err != nil {
				t.Errorf("entry[%d] occurred_at=%q not valid RFC3339: %v", i, ts, err)
			}
		}
	}

	// All 3 types must be present
	types := make(map[string]bool)
	for _, entry := range envelope.Data {
		if tp, ok := entry["type"].(string); ok {
			types[tp] = true
		}
	}
	for _, want := range []string{"share", "install", "join"} {
		if !types[want] {
			t.Errorf("activity feed missing '%s' type entry", want)
		}
	}
}

// TestPortal_ActivityRecent_EmptyReturnsEmptyArray verifies empty state returns [].
func TestPortal_ActivityRecent_EmptyReturnsEmptyArray(t *testing.T) {
	env := newPortalTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/activity/recent", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/activity/recent status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data []interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if envelope.Data == nil {
		t.Fatal("data is null, want empty array []")
	}
	if len(envelope.Data) != 0 {
		t.Errorf("data length = %d, want 0 (no activity)", len(envelope.Data))
	}
}

// --- F-032, Task 22: Per-peer activity endpoint ---

func TestPeerActivity_ReturnsEvents(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-active", LastSeen: time.Now()})

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	_ = env.peerEventRepo.Insert(ctx, &models.PeerEvent{ID: "e1", PeerID: "peer-active", Action: "share", Details: "shared file.vec", CreatedAt: now.Add(-2 * time.Hour)})
	_ = env.peerEventRepo.Insert(ctx, &models.PeerEvent{ID: "e2", PeerID: "peer-active", Action: "install", Details: "installed model.bin", CreatedAt: now.Add(-1 * time.Hour)})
	_ = env.peerEventRepo.Insert(ctx, &models.PeerEvent{ID: "e3", PeerID: "peer-active", Action: "trust", Details: "trusted peer-b", CreatedAt: now})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-active/activity", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			Action  string `json:"action"`
			Details string `json:"details"`
			Time    string `json:"time"`
		} `json:"data"`
		Meta struct {
			Total  int `json:"total"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Data) != 3 {
		t.Errorf("data len = %d, want 3", len(resp.Data))
	}
	if resp.Meta.Total != 3 {
		t.Errorf("meta.total = %d, want 3", resp.Meta.Total)
	}
	// Most recent first
	if len(resp.Data) > 0 && resp.Data[0].Action != "trust" {
		t.Errorf("data[0].action = %q, want trust (most recent)", resp.Data[0].Action)
	}
}

func TestPeerActivity_Empty(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-empty", LastSeen: time.Now()})

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-empty/activity", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Data) != 0 {
		t.Errorf("data len = %d, want 0", len(resp.Data))
	}
	if resp.Meta.Total != 0 {
		t.Errorf("meta.total = %d, want 0", resp.Meta.Total)
	}
}

func TestPeerActivity_Pagination(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	seedPeer(t, env.peerRepo, env.store, &models.Peer{PeerID: "peer-paginated", LastSeen: time.Now()})

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = env.peerEventRepo.Insert(ctx, &models.PeerEvent{
			ID:        "e" + string(rune('a'+i)),
			PeerID:    "peer-paginated",
			Action:    "share",
			Details:   "event",
			CreatedAt: now.Add(time.Duration(i) * time.Hour),
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peers/peer-paginated/activity?limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []interface{} `json:"data"`
		Meta struct {
			Total  int `json:"total"`
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Data) != 2 {
		t.Errorf("data len = %d, want 2", len(resp.Data))
	}
	if resp.Meta.Total != 5 {
		t.Errorf("meta.total = %d, want 5", resp.Meta.Total)
	}
	if resp.Meta.Limit != 2 {
		t.Errorf("meta.limit = %d, want 2", resp.Meta.Limit)
	}
	if resp.Meta.Offset != 1 {
		t.Errorf("meta.offset = %d, want 1", resp.Meta.Offset)
	}
}

// TestFormatTimeAgo verifies the human-readable relative time formatting.
func TestFormatTimeAgo(t *testing.T) {
	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-1 * time.Minute), "1 min ago"},
		{now.Add(-15 * time.Minute), "15 mins ago"},
		{now.Add(-1 * time.Hour), "1 hour ago"},
		{now.Add(-3 * time.Hour), "3 hours ago"},
		{now.Add(-24 * time.Hour), "1 day ago"},
		{now.Add(-72 * time.Hour), "3 days ago"},
	}
	for _, tc := range tests {
		got := formatTimeAgo(tc.at, now)
		if got != tc.want {
			t.Errorf("formatTimeAgo(%v, %v) = %q, want %q", tc.at, now, got, tc.want)
		}
	}
}
