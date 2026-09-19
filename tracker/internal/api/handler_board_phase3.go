// Package: tracker/internal/api
// Purpose: Community board phase 3 (Autopilot v2) on the portal router: bounty negotiation.
//          The reply ask rides on POST /api/board/posts/{id}/replies (handler_portal_community.go);
//          this file has POST /api/board/posts/{id}/bounty/raise. Route wiring is in server.go.

package api

import (
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// PortalRaiseBountyBody is the POST .../bounty/raise body: the new bounty amount (credits).
type PortalRaiseBountyBody struct {
	Amount int `json:"amount"`
}

// HandlePortalRaiseBounty handles POST /api/board/posts/{id}/bounty/raise {amount} (API key,
// author only). Raises the open bounty to amount, or creates one with the default 7 day
// deadline on a post without a bounty, escrowing the difference from the author's credits.
// Every replier with an ask gets a bounty_raised activity. Returns the post.
func (h *PortalHandler) HandlePortalRaiseBounty(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	postID, ok := pathID(w, r, "post")
	if !ok {
		return
	}
	var body PortalRaiseBountyBody
	if !decodeSmallBody(w, r, &body) {
		return
	}
	if body.Amount <= 0 || body.Amount > services.MaxBountyAmount {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "amount must be 1 to 10000 credits and above the current bounty")
		return
	}
	post, err := h.forumService.RaiseBounty(r.Context(), peerID, postID, body.Amount)
	switch err {
	case nil:
		SendData(w, h.singlePost(r.Context(), post, "", peerID))
	case models.ErrInvalidInput:
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "amount must be 1 to 10000 credits and above the current bounty")
	case services.ErrBountyNotAuthor:
		SendError(w, http.StatusForbidden, "BOUNTY_NOT_AUTHOR", "Only the post author can raise the bounty")
	case services.ErrBountyNotOpen:
		SendError(w, http.StatusConflict, "BOUNTY_NOT_OPEN", "Only an open bounty can be raised")
	case services.ErrInsufficientCredits:
		SendError(w, http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", "Not enough credits to escrow the raise")
	case models.ErrNotFound:
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Post not found")
	default:
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to raise bounty")
	}
}
