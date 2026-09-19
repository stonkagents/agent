// Package api: Forum API key authentication (X-API-Key header).
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type forumContextKey string

const forumPeerIDKey forumContextKey = "forum_peer_id"

// RequireAPIKey returns a middleware that resolves X-API-Key to peer_id and sets it in context.
// If the header is missing or invalid, it returns 401 and does not call next.
func RequireAPIKey(apiKeyRepo repository.PeerAPIKeyRepository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimSpace(r.Header.Get("X-API-Key"))
			if key == "" {
				SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required. Set X-API-Key header.")
				return
			}
			peerID, err := apiKeyRepo.GetByAPIKey(r.Context(), key)
			if err != nil || peerID == "" {
				if err == models.ErrNotFound {
					SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid API key.")
					return
				}
				SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to validate API key")
				return
			}
			ctx := context.WithValue(r.Context(), forumPeerIDKey, peerID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ForumPeerIDFromContext returns the peer_id set by RequireAPIKey middleware, or "" if not set.
func ForumPeerIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(forumPeerIDKey).(string)
	return v
}
