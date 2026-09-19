package api

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// setupLaunchRoutes registers the StonkAgents launchpad routes on the portal (/api) subrouter.
//
// Public, rate limited by IP:
//
//	GET  /api/launch/config     launch parameters, live fee and raise sizing
//	POST /api/launch/metadata   server-side IPFS upload (multipart allowed via ContentTypeMiddleware)
//	POST /api/launch/record     record a confirmed LaunchLab launch (verified on chain)
//	GET  /api/launch/pending    unbound launches for a wallet
//	GET  /api/launch/by-wallet  every launch for a wallet, claimed or not
//	GET  /api/launch/{mint}     one launch record
//	GET  /api/launch/{mint}/trades   newest-first indexed trades (?limit=&cursor=)
//	GET  /api/launch/{mint}/candles  OHLC candles in quote (?interval=1m|5m|15m|1h|1d&limit=)
//	GET  /api/launches          paginated launch list
//	GET  /api/revenue           platform revenue summary
//	GET  /api/v1/agent-token/burnplan  network token burn plan (404 until AGENT_TOKEN_MINT is set)
//	GET  /api/v1/agent-token/payouts   keeper payouts (same gate)
//
// Internal (shared secret header X-Internal-Token, only registered when the secret is set):
//
//	POST /api/internal/revenue  append a platform_revenue entry (keeper)
//
// Authenticated (X-API-Key via the daemon proxy), rate limited by peer:
//
//	POST /api/launch/claim      bind a launch to the calling peer and grant the launch reward
//
// Static /launch/* paths are registered before /launch/{mint} so mux matches them first.
func (s *Server) setupLaunchRoutes(portal *mux.Router) {
	wrapIP := func(h http.Handler, cfg ratelimit.MiddlewareConfig) http.Handler {
		if s.limiter == nil {
			return h
		}
		return ratelimit.NewMiddleware(s.limiter, cfg).Wrap(h)
	}

	if s.launchConfig != nil {
		portal.Handle("/launch/config",
			wrapIP(http.HandlerFunc(s.launchConfig.HandleGetConfig), LaunchConfigRateLimitConfig())).Methods(http.MethodGet)
	}

	if s.launchMetadata != nil {
		portal.Handle("/launch/metadata",
			wrapIP(http.HandlerFunc(s.launchMetadata.HandleUpload), LaunchMetadataRateLimitConfig())).Methods(http.MethodPost)
	}

	readCfg := LaunchReadRateLimitConfig()
	if s.launch != nil {
		portal.Handle("/launch/record",
			wrapIP(http.HandlerFunc(s.launch.HandleRecord), LaunchRecordRateLimitConfig())).Methods(http.MethodPost)
		portal.Handle("/launch/pending", wrapIP(http.HandlerFunc(s.launch.HandlePending), readCfg)).Methods(http.MethodGet)
		portal.Handle("/launch/by-wallet", wrapIP(http.HandlerFunc(s.launch.HandleByWallet), readCfg)).Methods(http.MethodGet)
		if s.apiKeyRepo != nil {
			claim := wrapIP(http.HandlerFunc(s.launch.HandleClaim), LaunchClaimRateLimitConfig())
			portal.Handle("/launch/claim", RequireAPIKey(s.apiKeyRepo)(claim)).Methods(http.MethodPost)
		}
		portal.Handle("/launch/{mint}", wrapIP(http.HandlerFunc(s.launch.HandleGet), readCfg)).Methods(http.MethodGet)
		portal.Handle("/launches", wrapIP(http.HandlerFunc(s.launch.HandleList), readCfg)).Methods(http.MethodGet)
	}
	if s.launchTrades != nil {
		portal.Handle("/launch/{mint}/trades", wrapIP(http.HandlerFunc(s.launchTrades.HandleTrades), readCfg)).Methods(http.MethodGet)
		portal.Handle("/launch/{mint}/candles", wrapIP(http.HandlerFunc(s.launchTrades.HandleCandles), readCfg)).Methods(http.MethodGet)
	}
	if s.agentToken != nil {
		portal.Handle("/v1/agent-token/burnplan", wrapIP(http.HandlerFunc(s.agentToken.HandleBurnPlan), readCfg)).Methods(http.MethodGet)
		portal.Handle("/v1/agent-token/payouts", wrapIP(http.HandlerFunc(s.agentToken.HandlePayouts), readCfg)).Methods(http.MethodGet)
	}

	if s.revenue != nil {
		portal.Handle("/revenue", wrapIP(http.HandlerFunc(s.revenue.HandleSummary), readCfg)).Methods(http.MethodGet)
	}

	// Internal ledger write for the keeper. Registered only when INTERNAL_API_TOKEN is set,
	// so an unconfigured tracker answers 404 (the route does not exist) rather than 401.
	if s.internalRevenue.Enabled() {
		portal.Handle("/internal/revenue",
			wrapIP(http.HandlerFunc(s.internalRevenue.HandleRecord), InternalRevenueRateLimitConfig())).Methods(http.MethodPost)
	}
}
