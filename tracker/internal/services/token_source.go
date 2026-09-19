// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Decides which launchpad (pump.fun | LaunchLab) a peer token's metrics come from
//
// peer_tokens has no source column (no migration in this workstream). Routing therefore
// uses a TokenSourceResolver: the default implementation recognises pump.fun mints by
// their vanity "pump" suffix and sends everything else to the configured default
// (TOKEN_METRICS_DEFAULT_SOURCE, launchlab). A future resolver backed by the
// token_launches table can return the exact source plus pool id / quote mint.

package services

import (
	"context"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// TokenSourceResolution is the routing decision for one token.
type TokenSourceResolution struct {
	Source models.TokenSource
	// Hint is passed to the LaunchLab client (pool id / quote mint when known).
	Hint LaunchLabHint
}

// TokenSourceResolver picks the metrics source for a peer token.
type TokenSourceResolver interface {
	Resolve(ctx context.Context, token *models.PeerToken) TokenSourceResolution
}

// DefaultTokenSourceResolver routes by mint suffix with a configurable default.
type DefaultTokenSourceResolver struct {
	// Default is used for mints that are not recognisably pump.fun. Empty = launchlab.
	Default models.TokenSource
	// Overrides maps a contract address to an explicit resolution (optional).
	Overrides map[string]TokenSourceResolution
}

// NewDefaultTokenSourceResolver creates a resolver with the given default source.
func NewDefaultTokenSourceResolver(def models.TokenSource) *DefaultTokenSourceResolver {
	if def == "" {
		def = models.TokenSourceLaunchLab
	}
	return &DefaultTokenSourceResolver{Default: def}
}

// Resolve implements TokenSourceResolver.
func (r *DefaultTokenSourceResolver) Resolve(_ context.Context, token *models.PeerToken) TokenSourceResolution {
	if token == nil {
		return TokenSourceResolution{Source: r.defaultSource()}
	}
	if res, ok := r.Overrides[token.TokenContractAddress]; ok {
		return res
	}
	if IsPumpFunMint(token.TokenContractAddress) {
		return TokenSourceResolution{Source: models.TokenSourcePumpFun}
	}
	return TokenSourceResolution{Source: r.defaultSource()}
}

func (r *DefaultTokenSourceResolver) defaultSource() models.TokenSource {
	if r.Default == "" {
		return models.TokenSourceLaunchLab
	}
	return r.Default
}

// IsPumpFunMint reports whether a mint carries pump.fun's vanity suffix
// (all pump.fun mints since mid-2024 end in "pump").
func IsPumpFunMint(mint string) bool {
	return strings.HasSuffix(mint, "pump")
}

// otherSource returns the fallback launchpad for a primary source.
func otherSource(s models.TokenSource) models.TokenSource {
	if s == models.TokenSourcePumpFun {
		return models.TokenSourceLaunchLab
	}
	return models.TokenSourcePumpFun
}
