// Package: tracker/internal/api
// Purpose: One batched peer_id -> display_name lookup for the public payloads that
//          reference peers (forum authors, leaderboard entries, tokens, launches), so a
//          page costs one query and never N+1. Missing names are simply absent.

package api

import (
	"context"
	"log/slog"

	"github.com/stonkagents/agent/tracker/internal/repository"
)

// displayNamesFor resolves display names for peerIDs (duplicates and blanks ignored).
// A nil repo or a lookup error degrades to an empty map: the payload still renders,
// only without names.
func displayNamesFor(ctx context.Context, repo repository.PeerRepository, peerIDs []string) map[string]string {
	if repo == nil {
		return map[string]string{}
	}
	unique := make([]string, 0, len(peerIDs))
	seen := make(map[string]struct{}, len(peerIDs))
	for _, id := range peerIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return map[string]string{}
	}
	names, err := repo.DisplayNamesByIDs(ctx, unique)
	if err != nil {
		slog.Warn("[api] display name lookup degraded", "peers", len(unique), "error", err)
		return map[string]string{}
	}
	return names
}
