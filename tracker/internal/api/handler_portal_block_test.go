/**
 * Feature: F-032 (Peers & Reputation)
 * Story: US-032-03 (Trust/Block Actions)
 * Purpose: RED tests for POST /api/peers/{id}/block endpoint
 */

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

// TestPortalBlock_Success verifies actor can block a peer.
func TestPortalBlock_Success(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")

	// Create target peer
	now := time.Now()
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "target-peer",
		LastSeen: now.Add(-2 * time.Minute),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/peers/target-peer/block", nil)
	req = req.WithContext(WithForumPeerID(req.Context(), "actor-peer"))
	req.Header.Set("X-API-Key", actorKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/peers/{id}/block status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	// Verify block was recorded
	blocked, _ := env.trustBlockRepo.IsBlocked(ctx, "actor-peer", "target-peer")
	if !blocked {
		t.Error("peer should be blocked after block action")
	}
}

// TestPortalBlock_Unauthorized verifies endpoint requires X-API-Key.
func TestPortalBlock_Unauthorized(t *testing.T) {
	env := newEnrichedPortalEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/peers/target-peer/block", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/peers/{id}/block without auth status = %d, want 401", w.Code)
	}
}

// TestPortalBlock_Self verifies cannot block self.
func TestPortalBlock_Self(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")

	req := httptest.NewRequest(http.MethodPost, "/api/peers/actor-peer/block", nil)
	req = req.WithContext(WithForumPeerID(req.Context(), "actor-peer"))
	req.Header.Set("X-API-Key", actorKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/peers/{id}/block (self) status = %d, want 400", w.Code)
	}

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error.Code != "VALIDATION_ERROR" {
		t.Errorf("error code = %s, want VALIDATION_ERROR", resp.Error.Code)
	}
}

// TestPortalBlock_Idempotent verifies blocking already-blocked peer succeeds.
func TestPortalBlock_Idempotent(t *testing.T) {
	env := newEnrichedPortalEnv(t)
	ctx := context.Background()

	actorKey, _ := env.apiKeyRepo.Create(ctx, "actor-peer")

	// Create target peer
	now := time.Now()
	seedPeer(t, env.peerRepo, env.store, &models.Peer{
		PeerID:   "target-peer",
		LastSeen: now.Add(-2 * time.Minute),
	})

	// Block once
	env.trustBlockRepo.Block(ctx, "actor-peer", "target-peer")

	// Block again (idempotent)
	req := httptest.NewRequest(http.MethodPost, "/api/peers/target-peer/block", nil)
	req = req.WithContext(WithForumPeerID(req.Context(), "actor-peer"))
	req.Header.Set("X-API-Key", actorKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /api/peers/{id}/block (idempotent) status = %d, want 200", w.Code)
	}
}
