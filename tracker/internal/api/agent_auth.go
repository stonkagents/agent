// Package api: Agent completions auth — resolve API key (peer or guest) to account_id.
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// ResolveAgentAccountID extracts the API key from r (X-API-Key or Authorization: Bearer)
// and resolves it to an account_id: first tries peer_api_keys -> accounts.GetByPeerID,
// then guest_api_keys -> account_id. Returns ("", ErrNotFound) if key is missing or unknown.
func ResolveAgentAccountID(
	ctx context.Context,
	r *http.Request,
	apiKeyRepo repository.PeerAPIKeyRepository,
	accountRepo repository.AccountRepository,
	guestKeyRepo repository.GuestKeyMappingRepository,
) (accountID string, err error) {
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if key == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.TrimSpace(auth), "Bearer ") {
			key = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}
	if key == "" {
		return "", models.ErrNotFound
	}

	// Try peer API key -> peer_id -> account
	if apiKeyRepo != nil {
		peerID, err := apiKeyRepo.GetByAPIKey(ctx, key)
		if err == nil && peerID != "" {
			acct, err := accountRepo.GetByPeerID(ctx, peerID)
			if err == nil {
				return acct.ID, nil
			}
		}
	}

	// Try guest API key -> account_id
	if guestKeyRepo != nil {
		accountID, err := guestKeyRepo.GetAccountIDByAPIKey(ctx, key)
		if err == nil {
			return accountID, nil
		}
	}

	return "", models.ErrNotFound
}
