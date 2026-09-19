// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: Tests for reputation HTTP handler

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

func TestHandleGetPeerReputation_NotFound(t *testing.T) {
	repo := repository.NewMemoryReputationRepository()
	srv := NewServer(ServerDeps{
		PeerHandler:       &PeerHandler{},
		ReputationHandler: NewReputationHandler(repo),
		AssetHandler:      &AssetHandler{},
		DMCAHandler:       &DMCAHandler{},
		Address:           ":0",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracker/peers/nonexistent/reputation", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("HandleGetPeerReputation() status = %d, want 404", w.Code)
	}
}

func TestHandleGetPeerReputation_Success(t *testing.T) {
	repo := repository.NewMemoryReputationRepository()
	rec := &reputation.ReputationRecord{
		PeerID:           "12D3KooWpeer1",
		CompositeScore:   0.85,
		BandwidthScore:   0.9,
		QualityScore:     0.8,
		SecurityScore:    0.95,
		CitizenshipScore: 0.75,
		UpdatedAt:        time.Now(),
	}
	_ = repo.Upsert(context.Background(), rec)

	srv := NewServer(ServerDeps{
		PeerHandler:       &PeerHandler{},
		ReputationHandler: NewReputationHandler(repo),
		AssetHandler:      &AssetHandler{},
		DMCAHandler:       &DMCAHandler{},
		Address:           ":0",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracker/peers/12D3KooWpeer1/reputation", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleGetPeerReputation() status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var dto ReputationResponseDTO
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&dto); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if dto.PeerID != "12D3KooWpeer1" {
		t.Errorf("peer_id = %q, want 12D3KooWpeer1", dto.PeerID)
	}
	if dto.CompositeScore != 0.85 {
		t.Errorf("composite_score = %f, want 0.85", dto.CompositeScore)
	}
}
