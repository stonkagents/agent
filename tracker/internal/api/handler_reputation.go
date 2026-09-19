// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-04 (EigenTrust Reputation System)
// Purpose: HTTP handler for peer reputation (EigenTrust scores)

package api

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// reputationFinder is the minimal interface for reputation lookup.
type reputationFinder interface {
	FindByPeerID(ctx context.Context, peerID string) (*reputation.ReputationRecord, error)
}

// ReputationHandler handles reputation HTTP endpoints.
type ReputationHandler struct {
	repo reputationFinder
}

// NewReputationHandler creates a new ReputationHandler.
func NewReputationHandler(repo reputationFinder) *ReputationHandler {
	return &ReputationHandler{repo: repo}
}

// ReputationResponseDTO is the response shape for GET /peers/{peer_id}/reputation.
type ReputationResponseDTO struct {
	PeerID           string  `json:"peer_id"`
	CompositeScore   float64 `json:"composite_score"`
	BandwidthScore   float64 `json:"bandwidth_score"`
	QualityScore     float64 `json:"quality_score"`
	SecurityScore    float64 `json:"security_score"`
	CitizenshipScore float64 `json:"citizenship_score"`
	UpdatedAt        string  `json:"updated_at"`
}

// HandleGetPeerReputation handles GET /api/v1/tracker/peers/{peer_id}/reputation.
func (h *ReputationHandler) HandleGetPeerReputation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	peerID := mux.Vars(r)["peer_id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer_id")
		return
	}

	rec, err := h.repo.FindByPeerID(r.Context(), peerID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Reputation not found for peer")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get reputation")
		return
	}

	SendJSON(w, http.StatusOK, ReputationResponseDTO{
		PeerID:           rec.PeerID,
		CompositeScore:   rec.CompositeScore,
		BandwidthScore:   rec.BandwidthScore,
		QualityScore:     rec.QualityScore,
		SecurityScore:    rec.SecurityScore,
		CitizenshipScore: rec.CitizenshipScore,
		UpdatedAt:        rec.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}
