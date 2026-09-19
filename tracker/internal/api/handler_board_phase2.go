// Package: tracker/internal/api
// Purpose: Community board phase 2 (rooms and matchmaking) on the portal router: the rooms a
//          viewer can post in, one room with the viewer's can_post, the room digest for the
//          token's agent, and the room error mapping shared by create post and reply. Route
//          wiring is in server.go.

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// PortalRoom is one token room (GET /api/board/rooms and GET /api/board/rooms/{mint}).
type PortalRoom struct {
	Mint             string `json:"mint"`
	Symbol           string `json:"symbol"`
	Name             string `json:"name"`
	ImageURL         string `json:"image_url"`
	AgentPeerID      string `json:"agent_peer_id"`
	AgentDisplayName string `json:"agent_display_name"`
	Posts7d          int    `json:"posts_7d"`
	// MembersEstimate is the holder count from the cached launch metrics, null when unknown.
	MembersEstimate *int    `json:"members_estimate"`
	LastPostAt      *string `json:"last_post_at"`
	// Role is the viewer's role in the room: agent or holder ("" when the viewer cannot post).
	Role string `json:"role"`
	// CanPost is only set on the single room read (viewer specific, false without a key).
	CanPost *bool `json:"can_post,omitempty"`
	// Unread (round 2, the mine=1 list only) counts the room's posts newer than the viewer's
	// last visit (the last 7 days for a room never opened).
	Unread *int `json:"unread,omitempty"`
}

// sendRoomError maps the room errors; false when err is not one of them.
func sendRoomError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, services.ErrRoomUnknownMint):
		SendError(w, http.StatusBadRequest, "ROOM_UNKNOWN_MINT", "room_mint is not a token launched on this platform")
	case errors.Is(err, services.ErrRoomNotHolder):
		SendError(w, http.StatusForbidden, "ROOM_NOT_HOLDER", "Only the token's agent and holders of the token can post in this room")
	case errors.Is(err, services.ErrRoomCheckUnavailable):
		SendError(w, http.StatusServiceUnavailable, "ROOM_CHECK_UNAVAILABLE", "Could not verify the token balance right now, try again")
	case errors.Is(err, services.ErrRoomNotAgent):
		SendError(w, http.StatusForbidden, "ROOM_NOT_AGENT", "Only the token's agent can do this")
	default:
		return false
	}
	return true
}

// toPortalRooms builds the DTOs with the agents' display names in one lookup.
func (h *PortalHandler) toPortalRooms(r *http.Request, rooms []*services.RoomRecord) []PortalRoom {
	ids := make([]string, 0, len(rooms))
	for _, room := range rooms {
		ids = append(ids, room.AgentPeerID)
	}
	names := displayNamesFor(r.Context(), h.peerRepo, ids)
	out := make([]PortalRoom, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, PortalRoom{
			Mint: room.Mint, Symbol: room.Symbol, Name: room.Name, ImageURL: room.ImageURL,
			AgentPeerID: room.AgentPeerID, AgentDisplayName: names[room.AgentPeerID],
			Posts7d: room.Posts7d, MembersEstimate: room.MembersEstimate, LastPostAt: rfc3339Ptr(room.LastPostAt),
			Role: room.Role,
		})
	}
	return out
}

// HandlePortalRooms handles GET /api/board/rooms?mine=1 (API key): the rooms the caller can
// post in (agent of the token, or holder of it), most recently active first.
func (h *PortalHandler) HandlePortalRooms(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	if mine := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mine"))); mine != "1" && mine != "true" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "mine=1 is required")
		return
	}
	rooms, err := h.forumService.ListRooms(r.Context(), peerID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list rooms")
		return
	}
	dtos := h.toPortalRooms(r, rooms)
	mints := make([]string, 0, len(rooms))
	for _, room := range rooms {
		mints = append(mints, room.Mint)
	}
	unread := h.forumService.UnreadByScope(r.Context(), peerID, mints)
	for i := range dtos {
		n := unread[dtos[i].Mint]
		dtos[i].Unread = &n
	}
	SendData(w, dtos)
}

// HandlePortalRoom handles GET /api/board/rooms/{mint} (public): the room plus can_post for
// the viewer (false without a key).
func (h *PortalHandler) HandlePortalRoom(w http.ResponseWriter, r *http.Request) {
	mint := strings.TrimSpace(mux.Vars(r)["mint"])
	if mint == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing room mint")
		return
	}
	room, err := h.forumService.GetRoom(r.Context(), mint, h.viewerPeerID(r))
	if errors.Is(err, services.ErrRoomUnknownMint) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Room not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load room")
		return
	}
	dto := h.toPortalRooms(r, []*services.RoomRecord{room})[0]
	canPost := room.Role == models.RoomRoleAgent || room.Role == models.RoomRoleHolder
	dto.CanPost = &canPost
	SendData(w, dto)
}

// PortalRoomDigestThread is one top thread of the digest.
type PortalRoomDigestThread struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Upvotes int    `json:"upvotes"`
	Replies int    `json:"replies"`
}

// PortalRoomDigest is the GET /api/board/rooms/{mint}/digest payload.
type PortalRoomDigest struct {
	Posts           int                      `json:"posts"`
	Replies         int                      `json:"replies"`
	BountiesAwarded int                      `json:"bounties_awarded"`
	CreditsAwarded  int                      `json:"credits_awarded"`
	TopThreads      []PortalRoomDigestThread `json:"top_threads"`
	// NewHolders is the holder count delta over the period, null when unknown.
	NewHolders  *int   `json:"new_holders"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}

// HandlePortalRoomDigest handles GET /api/board/rooms/{mint}/digest?days=7 (API key, the
// token's agent only; days 1..30, default 7).
func (h *PortalHandler) HandlePortalRoomDigest(w http.ResponseWriter, r *http.Request) {
	peerID, ok := requirePeer(w, r)
	if !ok {
		return
	}
	mint := strings.TrimSpace(mux.Vars(r)["mint"])
	if mint == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing room mint")
		return
	}
	days := services.DefaultDigestDays
	if d := strings.TrimSpace(r.URL.Query().Get("days")); d != "" {
		parsed, err := strconv.Atoi(d)
		if err != nil || parsed <= 0 || parsed > services.MaxDigestDays {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "days must be between 1 and 30")
			return
		}
		days = parsed
	}
	digest, err := h.forumService.RoomDigestFor(r.Context(), mint, peerID, days)
	if errors.Is(err, services.ErrRoomUnknownMint) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Room not found")
		return
	}
	if sendRoomError(w, err) {
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to build digest")
		return
	}
	threads := make([]PortalRoomDigestThread, 0, len(digest.TopThreads))
	for _, t := range digest.TopThreads {
		threads = append(threads, PortalRoomDigestThread{ID: t.ID, Title: t.Title, Upvotes: t.Upvotes, Replies: t.Replies})
	}
	SendData(w, PortalRoomDigest{
		Posts: digest.Posts, Replies: digest.Replies, BountiesAwarded: digest.BountiesAwarded, CreditsAwarded: digest.CreditsAwarded,
		TopThreads: threads, NewHolders: digest.NewHolders,
		PeriodStart: digest.PeriodStart.UTC().Format(time.RFC3339), PeriodEnd: digest.PeriodEnd.UTC().Format(time.RFC3339),
	})
}
