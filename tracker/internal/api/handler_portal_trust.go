// Package: tracker/internal/api
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Trust & Block Actions)
// Purpose: Trust, block, untrust, unblock, and trusted/blocked list handlers (split from handler_portal.go, TD-060)

package api

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// HandlePortalTrust handles POST /api/peers/:id/trust. Requires X-API-Key.
func (h *PortalHandler) HandlePortalTrust(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	if actorID == peerID {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Cannot trust self")
		return
	}
	if err := h.trustBlockRepo.Trust(r.Context(), actorID, peerID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to trust peer")
		return
	}
	// F-032 AC-1 (US-032-02): Log trusted_by event for the target peer's activity feed
	if h.peerEventRepo != nil {
		_ = h.peerEventRepo.Insert(r.Context(), &models.PeerEvent{
			PeerID:    peerID,
			Action:    "trusted_by",
			Details:   geo.MaskPeerID(actorID),
			CreatedAt: time.Now(),
		})
	}
	peer, err := h.peerService.FindByID(r.Context(), peerID)
	resp := map[string]interface{}{"success": true, "peer_id": peerID}
	if err == nil {
		resp["peer"] = PortalPeer{
			ID: peer.PeerID, Name: peer.PeerID, PeerID: peer.PeerID,
			Status:   reputation.DeriveStatus(peer.LastSeen, peer.LastUploadAt, peer.LastDownloadAt, time.Now()),
			LastSeen: peer.LastSeen.Format(time.RFC3339),
		}
	}
	SendData(w, resp)
}

// HandlePortalBlock handles POST /api/peers/:id/block. Requires X-API-Key.
func (h *PortalHandler) HandlePortalBlock(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	if actorID == peerID {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Cannot block self")
		return
	}
	if err := h.trustBlockRepo.Block(r.Context(), actorID, peerID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to block peer")
		return
	}
	peer, err := h.peerService.FindByID(r.Context(), peerID)
	resp := map[string]interface{}{"success": true, "peer_id": peerID}
	if err == nil {
		resp["peer"] = PortalPeer{
			ID: peer.PeerID, Name: peer.PeerID, PeerID: peer.PeerID,
			Status:   reputation.DeriveStatus(peer.LastSeen, peer.LastUploadAt, peer.LastDownloadAt, time.Now()),
			LastSeen: peer.LastSeen.Format(time.RFC3339),
		}
	}
	SendData(w, resp)
}

// HandlePortalUntrust handles DELETE /api/peers/:id/trust. Requires X-API-Key. Idempotent.
func (h *PortalHandler) HandlePortalUntrust(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	if actorID == peerID {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Cannot untrust self")
		return
	}
	if err := h.trustBlockRepo.Untrust(r.Context(), actorID, peerID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to untrust peer")
		return
	}
	SendData(w, map[string]interface{}{"success": true, "peer_id": peerID})
}

// HandlePortalUnblock handles DELETE /api/peers/:id/block. Requires X-API-Key. Idempotent.
func (h *PortalHandler) HandlePortalUnblock(w http.ResponseWriter, r *http.Request) {
	peerID := mux.Vars(r)["id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	if actorID == peerID {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Cannot unblock self")
		return
	}
	if err := h.trustBlockRepo.Unblock(r.Context(), actorID, peerID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to unblock peer")
		return
	}
	SendData(w, map[string]interface{}{"success": true, "peer_id": peerID})
}

// HandlePortalTrustedList handles GET /api/peers/trusted. Requires X-API-Key.
func (h *PortalHandler) HandlePortalTrustedList(w http.ResponseWriter, r *http.Request) {
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	peerIDs, err := h.trustBlockRepo.TrustedByActor(r.Context(), actorID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to fetch trusted peers")
		return
	}
	if peerIDs == nil {
		peerIDs = []string{}
	}
	// ADR-001: Return { data: [], meta: { total } } envelope
	SendDataWithMeta(w, peerIDs, len(peerIDs), len(peerIDs), 0)
}

// HandlePortalBlockedList handles GET /api/peers/blocked. Requires X-API-Key.
func (h *PortalHandler) HandlePortalBlockedList(w http.ResponseWriter, r *http.Request) {
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	peerIDs, err := h.trustBlockRepo.BlockedByActor(r.Context(), actorID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to fetch blocked peers")
		return
	}
	if peerIDs == nil {
		peerIDs = []string{}
	}
	// ADR-001: Return { data: [], meta: { total } } envelope
	SendDataWithMeta(w, peerIDs, len(peerIDs), len(peerIDs), 0)
}
