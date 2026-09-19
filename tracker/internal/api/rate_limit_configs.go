// Package: tracker/internal/api
// Feature: sprint-02
// Story: TD-001 (Wire rate-limit middleware to routes)
// Purpose: Exported rate-limit configs for registration and sensitive endpoints

package api

import (
	"net/http"
	"time"

	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// ChallengeRateLimitConfig returns the rate limit config for POST /challenge.
// 6 requests per hour per IP (prevents nonce exhaustion).
func ChallengeRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "challenge:",
		Limit:     6,
		Window:    1 * time.Hour,
		KeyFunc:   ratelimit.IPKey,
		Message:   "6 challenges per hour",
	}
}

// RegisterIdentityRateLimitConfig returns the rate limit config for POST /register/identity.
// 5 requests per day per IP (prevents mass account creation).
func RegisterIdentityRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "register-identity:",
		Limit:     5,
		Window:    24 * time.Hour,
		KeyFunc:   ratelimit.IPKey,
		Message:   "5 registrations per day from this address",
	}
}

// LegacyRegisterRateLimitConfig returns the rate limit config for POST /register (legacy).
// 30 requests per hour per IP (looser than identity registration, tighter than general API).
func LegacyRegisterRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "register-legacy:",
		Limit:     30,
		Window:    1 * time.Hour,
		KeyFunc:   ratelimit.IPKey,
		Message:   "30 registrations per hour from this address",
	}
}

// SpendTokenRateLimitConfig returns the rate limit config for POST /credits/spend-token.
// 60 requests per hour per IP (prevents credit burn abuse).
func SpendTokenRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "spend-token:",
		Limit:     60,
		Window:    1 * time.Hour,
		KeyFunc:   ratelimit.IPKey,
	}
}

// ProtocolEndpointRateLimitConfig returns the rate limit config for unauthenticated protocol POST endpoints.
// TD-090: goodbye, announce, availability, dmca, stats, downloads/complete — 100 requests per minute per IP.
// These are daemon-to-tracker protocol endpoints, not user-facing, but still need protection against abuse.
func ProtocolEndpointRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "protocol:",
		Limit:     100,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// GuestKeyRateLimitConfig returns the rate limit config for POST /guest-key.
// TD-087: 3 requests per hour per IP (prevents API key farming — each guest key grants 1000 credits).
func GuestKeyRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "guest-key:",
		Limit:     3,
		Window:    1 * time.Hour,
		KeyFunc:   ratelimit.IPKey,
	}
}

// PeerIDKeyFunc extracts the peer_id from request context (set by RequireAPIKey middleware).
// Falls back to IP if peer_id is not set (shouldn't happen if middleware is correctly ordered).
func PeerIDKeyFunc(r *http.Request) string {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID != "" {
		return peerID
	}
	return ratelimit.IPKey(r)
}

// TokenMetricsRateLimitConfig returns the rate limit config for GET /api/peers/{id}/token/metrics.
// 30 requests per minute per IP (prevents pump.fun/Moralis abuse).
func TokenMetricsRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "token-metrics:",
		Limit:     30,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// ProfileRateLimitConfig returns the rate limit config for GET /api/profile/me.
// TD-027: 30 requests per minute per peer_id (authenticated endpoint — 6 sequential DB calls
// per request means each request is expensive). Keyed by peer_id, not IP.
func ProfileRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "profile:",
		Limit:     30,
		Window:    1 * time.Minute,
		KeyFunc:   PeerIDKeyFunc,
	}
}

// TrustBlockMutationRateLimitConfig returns the rate limit config for POST/DELETE /api/peers/{id}/trust|block.
// F-032 AC-12: 10 requests per minute per API key (social graph manipulation should be infrequent to prevent spam).
func TrustBlockMutationRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "trust-block-mutation:",
		Limit:     10,
		Window:    1 * time.Minute,
		KeyFunc:   PeerIDKeyFunc,
	}
}

// TrustBlockListRateLimitConfig returns the rate limit config for GET /api/peers/trusted|blocked.
// F-032 AC-12: 30 requests per minute per API key (authenticated read, moderate limit).
func TrustBlockListRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "trust-block-list:",
		Limit:     30,
		Window:    1 * time.Minute,
		KeyFunc:   PeerIDKeyFunc,
	}
}

// PublicPeerDetailRateLimitConfig returns the rate limit config for GET /api/peers/{id}/reputation|assets|activity.
// F-032 AC-12: 60 requests per minute per IP (public read, moderate limit).
func PublicPeerDetailRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "public-peer-detail:",
		Limit:     60,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// PortalPeersRateLimitConfig returns the rate limit config for GET /api/peers.
// F-032 AC-12: 60 requests per minute per IP (enriched peer list with batch queries is heavier than before).
func PortalPeersRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "portal-peers:",
		Limit:     60,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// LaunchConfigRateLimitConfig returns the rate limit config for GET /api/launch/config.
// 60 requests per minute per IP (public read; may trigger a quote price fetch on cache miss).
func LaunchConfigRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "launch-config:",
		Limit:     60,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// AgentCompletionsRateLimitConfig returns the rate limit config for POST /api/v1/agents/completions.
// 120 requests per minute per IP (credits are deducted per request).
func AgentCompletionsRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "agent-completions:",
		Limit:     120,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// LaunchMetadataRateLimitConfig returns the rate limit config for POST /api/launch/metadata.
// StonkAgents launchpad: 10 requests per minute per IP (public, unauthenticated; each request
// costs two Pinata pins, so keep it tight — a launch needs exactly one call).
func LaunchMetadataRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "launch-metadata:",
		Limit:     10,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// LaunchRecordRateLimitConfig returns the rate limit config for POST /api/launch/record.
// Public, unauthenticated, and each call triggers a Solana getTransaction — 10 per minute per IP.
func LaunchRecordRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "launch-record:",
		Limit:     10,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// LaunchClaimRateLimitConfig returns the rate limit config for POST /api/launch/claim.
// Authenticated (peer_id key) — 10 per minute; a peer claims at most a handful of launches.
func LaunchClaimRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "launch-claim:",
		Limit:     10,
		Window:    1 * time.Minute,
		KeyFunc:   PeerIDKeyFunc,
	}
}

// LaunchReadRateLimitConfig returns the rate limit config for public launch/revenue reads
// (GET /api/launch/{mint}, /api/launches, /api/launch/pending, /api/revenue) — 60 per minute per IP.
func LaunchReadRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "launch-read:",
		Limit:     60,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// InternalRevenueRateLimitConfig returns the rate limit config for POST /api/internal/revenue.
// Authenticated by shared secret and written only by the keeper, but still bounded per IP so a
// leaked token cannot flood the ledger — 120 per minute covers a full claim/buyback/burn sweep.
func InternalRevenueRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "internal-revenue:",
		Limit:     120,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// FeedbackRateLimitConfig returns the rate limit config for POST /api/v1/feedback.
// Public, unauthenticated free text that is stored and forwarded to a chat — 5 per minute per IP.
func FeedbackRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "feedback:",
		Limit:     5,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// FeedbackAdminRateLimitConfig returns the rate limit config for GET /api/v1/admin/feedback.
// Guarded by the admin key, still bounded per IP so a leaked key cannot hammer the table.
func FeedbackAdminRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "feedback-admin:",
		Limit:     60,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// InterestRateLimitConfig returns the rate limit config for POST /api/v1/interest.
// Public, unauthenticated, stored and forwarded like feedback — 5 per minute per IP.
func InterestRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "interest:",
		Limit:     5,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// InterestAdminRateLimitConfig returns the rate limit config for GET /api/v1/admin/interest*.
func InterestAdminRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "interest-admin:",
		Limit:     60,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}

// Community board abuse limits, keyed by the calling peer (the API key), so one hostile agent
// cannot flood the board while a normal owner never notices them.
const (
	// BoardWriteLimit / BoardWriteWindow bound every board write of one peer: posts, replies
	// (asks included), upvotes, awards, extends, raises, accepts, watches and token offer
	// payments. 30 per 10 minutes is far above what a person or the autopilot (10 auto
	// replies per day) produces.
	BoardWriteLimit  = 30
	BoardWriteWindow = 10 * time.Minute
	// BoardReportLimit / BoardReportWindow bound the reports one peer can file: 10 per day.
	// Three reporters hide content, so unbounded reports are a censorship lever.
	BoardReportLimit  = 10
	BoardReportWindow = 24 * time.Hour
)

// BoardWriteRateLimitConfig returns the rate limit config shared by the board write routes.
func BoardWriteRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "board-write:",
		Limit:     BoardWriteLimit,
		Window:    BoardWriteWindow,
		KeyFunc:   PeerIDKeyFunc,
		Message:   "30 board writes per 10 minutes",
	}
}

// BoardReportRateLimitConfig returns the rate limit config for POST .../report.
func BoardReportRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "board-report:",
		Limit:     BoardReportLimit,
		Window:    BoardReportWindow,
		KeyFunc:   PeerIDKeyFunc,
		Message:   "10 reports per day",
	}
}

// DevDripRateLimitConfig returns the rate limit config for POST /api/dev/drip.
// 10 per minute per IP keeps a misbehaving client off the RPC; the service applies the
// real caps (per wallet per day, per IP per hour) from the dev_drips table.
func DevDripRateLimitConfig() ratelimit.MiddlewareConfig {
	return ratelimit.MiddlewareConfig{
		KeyPrefix: "dev-drip:",
		Limit:     10,
		Window:    1 * time.Minute,
		KeyFunc:   ratelimit.IPKey,
	}
}
