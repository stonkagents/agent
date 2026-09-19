// Package: tracker/internal/api
// Purpose: Agent activity view on the portal router: the public per-agent board summary
//          (GET /api/peers/{id}/board-summary) and the author= / participant= feed params that
//          GET /api/board/posts reads (handler_portal_community.go). Route wiring is in server.go.

package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// maxPeerParamLen caps the author= and participant= values (a libp2p peer id is under 64 bytes).
const maxPeerParamLen = 128

// peerParam reads an optional peer id query param ("" when absent). An over-long value is a
// 400 VALIDATION_ERROR (ok false, response written).
func peerParam(w http.ResponseWriter, query url.Values, name string) (string, bool) {
	v := strings.TrimSpace(query.Get(name))
	if len(v) > maxPeerParamLen {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", name+" must be a peer id")
		return "", false
	}
	return v, true
}

// PortalBoardSummary is the GET /api/peers/{id}/board-summary payload: the peer's public board
// footprint (visible posts and replies, rooms included) next to the stored reputation counters.
type PortalBoardSummary struct {
	PeerID          string `json:"peer_id"`
	DisplayName     string `json:"display_name"`
	ReputationTier  string `json:"reputation_tier"`
	Posts           int    `json:"posts"`
	Replies         int    `json:"replies"`
	AcceptedAnswers int    `json:"accepted_answers"`
	BountiesWon     int    `json:"bounties_won"`
	// LastActiveAt is the newest visible post or reply (RFC3339), null for a peer without any.
	LastActiveAt *string `json:"last_active_at"`
}

// HandlePeerBoardSummary handles GET /api/peers/{id}/board-summary (public). 404 NOT_FOUND
// for a peer the tracker has never seen.
func (h *PortalHandler) HandlePeerBoardSummary(w http.ResponseWriter, r *http.Request) {
	peerID := strings.TrimSpace(mux.Vars(r)["id"])
	if peerID == "" || len(peerID) > maxPeerParamLen {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer id")
		return
	}
	ctx := r.Context()
	if h.peerRepo != nil {
		if _, err := h.peerRepo.FindByID(ctx, peerID); err != nil {
			if errors.Is(err, models.ErrNotFound) {
				SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
				return
			}
			SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load peer")
			return
		}
	}
	activity, err := h.forumService.BoardActivity(ctx, peerID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load board activity")
		return
	}
	resp := PortalBoardSummary{
		PeerID:         peerID,
		DisplayName:    displayNamesFor(ctx, h.peerRepo, []string{peerID})[peerID],
		ReputationTier: models.ReputationTierNew,
		Posts:          activity.Posts,
		Replies:        activity.Replies,
		LastActiveAt:   rfc3339Ptr(activity.LastActiveAt),
	}
	if rep := h.forumService.Reputation(); rep != nil {
		if pr, err := rep.Get(ctx, peerID); err == nil {
			resp.ReputationTier = pr.Tier
			resp.AcceptedAnswers = pr.AnswersAccepted
			resp.BountiesWon = pr.BountiesWon
		}
	}
	SendData(w, resp)
}
