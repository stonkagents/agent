// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: TokenSourceResolver backed by the token_launches table.
//
// A token recorded through POST /api/launch/record is a LaunchLab launch by definition,
// and the record carries the pool id and quote mint, so the metrics client can read the
// pool directly instead of discovering it. Anything not in the table falls through to the
// default resolver (pump.fun suffix rule, then TOKEN_METRICS_DEFAULT_SOURCE).

package services

import (
	"context"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// LaunchRecordSourceResolver resolves the metrics source from recorded launches first.
type LaunchRecordSourceResolver struct {
	launches repository.LaunchRepository
	fallback TokenSourceResolver
}

// NewLaunchRecordSourceResolver wraps fallback with a token_launches lookup.
// A nil fallback uses NewDefaultTokenSourceResolver(launchlab).
func NewLaunchRecordSourceResolver(launches repository.LaunchRepository, fallback TokenSourceResolver) *LaunchRecordSourceResolver {
	if fallback == nil {
		fallback = NewDefaultTokenSourceResolver(models.TokenSourceLaunchLab)
	}
	return &LaunchRecordSourceResolver{launches: launches, fallback: fallback}
}

// Resolve implements TokenSourceResolver.
func (r *LaunchRecordSourceResolver) Resolve(ctx context.Context, token *models.PeerToken) TokenSourceResolution {
	if token != nil && r.launches != nil {
		if l, err := r.launches.GetByMint(ctx, token.TokenContractAddress); err == nil && l != nil {
			return TokenSourceResolution{
				Source: models.TokenSourceLaunchLab,
				Hint:   LaunchLabHint{PoolID: l.PoolID, QuoteMint: l.QuoteMint},
			}
		}
	}
	return r.fallback.Resolve(ctx, token)
}
