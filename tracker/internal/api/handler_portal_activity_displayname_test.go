// Package api: Tests for activity feed agentName enrichment (F-027, US-027-06)
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-06 (Backend — Activity Feed Peer Names)
// Purpose: Verify activity feed entries show "agent-XXXX shared file" instead of "file shared"

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// TestPortal_Activity_ShareTitleIncludesAgentName verifies share entries
// include an agent name prefix: "agent-XXXX shared filename" not just "filename shared".
func TestPortal_Activity_ShareTitleIncludesAgentName(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed an asset from a peer with a known ID
	err := env.assetRepo.Create(ctx, &models.Asset{
		CID:          "cid-display-name-test",
		Filename:     "agent-config.claw-skill",
		Size:         2048,
		PeerID:       "12D3KooWAbcDefGhiJkl",
		ManifestType: "claw-skill",
		AnnouncedAt:  time.Now().Add(-3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/activity/recent?limit=10", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Find the share entry
	var shareTitle string
	for _, entry := range envelope.Data {
		if entry["type"] == "share" {
			shareTitle, _ = entry["title"].(string)
			break
		}
	}

	if shareTitle == "" {
		t.Fatal("no share entry found in activity feed")
	}

	// Title format: "<display name> shared <filename>". Split rather than prefix-match so a
	// regression to "<filename> shared" cannot pass just because the filename also starts with "agent-".
	name, file, found := strings.Cut(shareTitle, " shared ")
	if !found {
		t.Fatalf("share title = %q, want format 'agent-XXXX shared filename'", shareTitle)
	}
	if want := agentName("12D3KooWAbcDefGhiJkl"); name != want {
		t.Errorf("share title display name = %q, want %q", name, want)
	}
	if !strings.HasPrefix(name, AgentNamePrefix) {
		t.Errorf("display name = %q, want prefix %q", name, AgentNamePrefix)
	}
	if file != "agent-config.claw-skill" {
		t.Errorf("share title filename = %q, want 'agent-config.claw-skill'", file)
	}
}

// TestPortal_Activity_HasPeerID verifies each activity entry includes a "peer_id" field.
func TestPortal_Activity_HasPeerID(t *testing.T) {
	env := newPortalTestEnv(t)
	ctx := context.Background()

	// Seed an asset
	err := env.assetRepo.Create(ctx, &models.Asset{
		CID:          "cid-peer-id-test",
		Filename:     "data.bin",
		Size:         1024,
		PeerID:       "12D3KooWPeerIDTest",
		ManifestType: "raw",
		AnnouncedAt:  time.Now().Add(-1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/activity/recent?limit=10", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Find the share entry
	for _, entry := range envelope.Data {
		if entry["type"] == "share" {
			peerID, ok := entry["peer_id"]
			if !ok {
				t.Fatal("share entry missing 'peer_id' field — ActivityEntry must include PeerID")
			}
			if peerID != "12D3KooWPeerIDTest" {
				t.Errorf("peer_id = %v, want '12D3KooWPeerIDTest'", peerID)
			}
			return
		}
	}
	t.Fatal("no share entry found")
}

// TestAgentName verifies the agentName helper produces "agent-XXXX" from a peer ID.
func TestAgentName(t *testing.T) {
	tests := []struct {
		peerID string
		want   string
	}{
		{"12D3KooWAbcDefGhiJkl", "agent-AbcD"},
		{"12D3KooW", "agent-12D3"},   // Edge: exactly 8 chars
		{"short", "agent-shor"},      // Edge: less than 12 chars
		{"12D3KooWXYZ", "agent-XYZ"}, // Edge: 11 chars total, suffix is "XYZ"
		{"", "agent-anon"},           // Edge: empty
	}
	for _, tc := range tests {
		got := agentName(tc.peerID)
		if got != tc.want {
			t.Errorf("agentName(%q) = %q, want %q", tc.peerID, got, tc.want)
		}
	}
}
