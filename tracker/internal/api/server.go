// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: HTTP server with mux router and middleware chain

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// Server is the tracker HTTP server.
type Server struct {
	router       *mux.Router
	httpServer   *http.Server
	version      string
	peers        *PeerHandler
	reputation   *ReputationHandler
	portal       *PortalHandler
	assets       *AssetHandler
	dmca         *DMCAHandler
	relay        *RelayHandler
	stats        *StatsHandler
	downloads    *DownloadsHandler
	leaderboard  *LeaderboardHandler
	forum        *ForumHandler
	profile      *ProfileHandler
	apiKeyRepo   repository.PeerAPIKeyRepository // for forum RequireAPIKey middleware
	guestKeyRepo repository.GuestKeyRepository   // for HandleGuestKey (POST /api/v1/portal/guest-key)
	// F-013 handlers
	registration *RegistrationHandler
	credits      *CreditHandler
	social       *SocialHandler
	wallet       *WalletHandler
	purchase     *PurchaseHandler
	recovery     *RecoveryHandler
	// F-031: Token identity handler
	token *TokenHandler
	// Agent completions (guest/peer key -> account, deduct, proxy to LLM)
	agentCompletions *AgentCompletionsHandler
	// Agent chat relay (token chat: holder -> tracker -> owner's daemon uses local LLM)
	agentChatRelay *AgentChatRelayHandler
	// StonkAgents launchpad: GET /api/launch/config
	launchConfig *LaunchConfigHandler
	// TD-001: Rate limiter for registration + sensitive routes
	limiter        ratelimit.Limiter
	versionChecker *services.VersionChecker                     // F-025: latest release version for heartbeat responses
	healthProbe    func(context.Context) map[string]interface{} // optional: verbose health checks
	// StonkAgents launchpad: server-side token metadata upload (POST /api/launch/metadata)
	launchMetadata *LaunchMetadataHandler
	// StonkAgents launchpad: LaunchLab launch records + revenue summary
	launch  *LaunchHandler
	revenue *RevenueHandler
	// StonkAgents launchpad: keeper ledger write (POST /api/internal/revenue), nil = disabled
	internalRevenue *InternalRevenueHandler
	// StonkAgents launchpad: indexed trades (GET /api/launch/{mint}/trades, /candles), nil = not registered
	launchTrades *LaunchTradesHandler
	// StonkAgents network token ledgers (GET /api/v1/agent-token/*), nil = not registered (404)
	agentToken *AgentTokenHandler
	// StonkAgents portal feedback: POST /api/v1/feedback + GET /api/v1/admin/feedback, nil = not registered
	feedback *FeedbackHandler
	// StonkAgents roadmap interest: POST /api/v1/interest + GET /api/v1/admin/interest[/summary], nil = not registered
	interest *InterestHandler
	// StonkAgents devnet drip: POST /api/dev/drip, nil = not registered (no drip wallet or not devnet)
	devDrip *DevDripHandler
}

// ServerDeps holds the dependencies for creating a Server.
type ServerDeps struct {
	PeerHandler        *PeerHandler
	ReputationHandler  *ReputationHandler
	PortalHandler      *PortalHandler
	AssetHandler       *AssetHandler
	DMCAHandler        *DMCAHandler
	RelayHandler       *RelayHandler
	StatsHandler       *StatsHandler
	DownloadsHandler   *DownloadsHandler
	LeaderboardHandler *LeaderboardHandler
	ForumHandler       *ForumHandler
	ProfileHandler     *ProfileHandler
	APIKeyRepo         repository.PeerAPIKeyRepository // for forum mutation auth (RequireAPIKey)
	GuestKeyRepo       repository.GuestKeyRepository   // for HandleGuestKey
	// F-013 handlers
	RegistrationHandler *RegistrationHandler
	CreditHandler       *CreditHandler
	SocialHandler       *SocialHandler
	WalletHandler       *WalletHandler
	PurchaseHandler     *PurchaseHandler
	RecoveryHandler     *RecoveryHandler
	TokenHandler        *TokenHandler            // F-031: token identity handler
	AgentCompletions    *AgentCompletionsHandler // optional: POST /api/v1/agents/completions
	AgentChatRelay      *AgentChatRelayHandler   // optional: token chat relay (forward, pending, response, result)
	LaunchConfigHandler *LaunchConfigHandler     // optional: GET /api/launch/config (StonkAgents launchpad)
	Limiter             ratelimit.Limiter        // TD-001: nil = no rate limiting (tests without it still pass)
	VersionChecker      *services.VersionChecker // F-025: fetches latest release manifest, nil = no update checks
	// HealthProbe: when HEALTH_CHECKS_VERBOSE=true, called to add db/redis connectivity to /health response
	HealthProbe func(context.Context) map[string]interface{}
	Address     string
	Version     string
	// StonkAgents launchpad: optional server-side token metadata upload; nil = route not registered
	LaunchMetadataHandler *LaunchMetadataHandler
	// StonkAgents launchpad (optional): launch record/claim + revenue endpoints
	LaunchHandler  *LaunchHandler
	RevenueHandler *RevenueHandler
	// InternalRevenueHandler serves POST /api/internal/revenue for the keeper.
	// nil, or a handler with no shared secret, leaves the route unregistered (404).
	InternalRevenueHandler *InternalRevenueHandler
	// LaunchTradesHandler serves the indexed trade feed and candles; nil = routes not registered.
	LaunchTradesHandler *LaunchTradesHandler
	// AgentTokenHandler serves the network token burn plan / payouts; nil = 404 (AGENT_TOKEN_MINT unset).
	AgentTokenHandler *AgentTokenHandler
	// FeedbackHandler serves the portal feedback endpoints (see routes_feedback.go); nil = not registered.
	FeedbackHandler *FeedbackHandler
	// InterestHandler serves the roadmap interest endpoints (see routes_interest.go); nil = not registered.
	InterestHandler *InterestHandler
	// DevDripHandler serves POST /api/dev/drip (see routes_dev_drip.go); nil = not registered (404).
	DevDripHandler *DevDripHandler
}

// NewServer creates a new tracker HTTP server.
func NewServer(deps ServerDeps) *Server {
	version := deps.Version
	if version == "" {
		version = "dev"
	}
	s := &Server{
		router:           mux.NewRouter(),
		version:          version,
		peers:            deps.PeerHandler,
		reputation:       deps.ReputationHandler,
		portal:           deps.PortalHandler,
		assets:           deps.AssetHandler,
		dmca:             deps.DMCAHandler,
		relay:            deps.RelayHandler,
		stats:            deps.StatsHandler,
		downloads:        deps.DownloadsHandler,
		leaderboard:      deps.LeaderboardHandler,
		forum:            deps.ForumHandler,
		profile:          deps.ProfileHandler,
		apiKeyRepo:       deps.APIKeyRepo,
		guestKeyRepo:     deps.GuestKeyRepo,
		registration:     deps.RegistrationHandler,
		credits:          deps.CreditHandler,
		social:           deps.SocialHandler,
		wallet:           deps.WalletHandler,
		purchase:         deps.PurchaseHandler,
		recovery:         deps.RecoveryHandler,
		token:            deps.TokenHandler,
		agentCompletions: deps.AgentCompletions,
		agentChatRelay:   deps.AgentChatRelay,
		launchConfig:     deps.LaunchConfigHandler,
		limiter:          deps.Limiter,
		versionChecker:   deps.VersionChecker,
		healthProbe:      deps.HealthProbe,
		launchMetadata:   deps.LaunchMetadataHandler,
		launch:           deps.LaunchHandler,
		launchTrades:     deps.LaunchTradesHandler,
		agentToken:       deps.AgentTokenHandler,
		revenue:          deps.RevenueHandler,
		internalRevenue:  deps.InternalRevenueHandler,
		feedback:         deps.FeedbackHandler,
		interest:         deps.InterestHandler,
		devDrip:          deps.DevDripHandler,
	}

	// F-025: Wire version checker into PeerHandler for heartbeat update fields
	if deps.VersionChecker != nil && s.peers != nil {
		s.peers.SetVersionChecker(deps.VersionChecker)
	}

	s.setupRoutes()
	s.setupMiddleware()

	// CORS must wrap the entire router so OPTIONS preflight (no route match) gets 2xx + CORS headers.
	// Gorilla mux only runs Use() middleware when a route matches, so OPTIONS would otherwise hit NotFoundHandler (404).
	s.httpServer = &http.Server{
		Addr:           deps.Address,
		Handler:        CORSMiddleware(s.router),
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB max headers (DoS protection)
	}

	return s
}

func (s *Server) setupRoutes() {
	api := s.router.PathPrefix("/api/v1/tracker").Subrouter()

	// Peer endpoints (legacy register — rate limited via TD-001)
	var legacyRegisterHandler http.Handler = http.HandlerFunc(s.peers.HandleRegister)
	if s.limiter != nil {
		legacyRegisterHandler = ratelimit.NewMiddleware(s.limiter, LegacyRegisterRateLimitConfig()).Wrap(legacyRegisterHandler)
	}
	api.Handle("/register", legacyRegisterHandler).Methods(http.MethodPost)
	if s.reputation != nil {
		api.HandleFunc("/peers/{peer_id}/reputation", s.reputation.HandleGetPeerReputation).Methods(http.MethodGet)
	}
	if s.apiKeyRepo != nil {
		api.Handle("/heartbeat", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.peers.HandleHeartbeat))).Methods(http.MethodPost)
		api.Handle("/peers/me", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.peers.HandleUpdateMyWallet))).Methods(http.MethodPatch)
		api.Handle("/peers/{peer_id}/trust", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.peers.HandleTrust))).Methods(http.MethodPost)
		api.Handle("/peers/{peer_id}/block", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.peers.HandleBlock))).Methods(http.MethodPost)
	}
	api.HandleFunc("/peers/{peer_id}", s.peers.HandleGetPeer).Methods(http.MethodGet)
	api.HandleFunc("/peers", s.peers.HandleDiscover).Methods(http.MethodGet)
	// TD-090: Rate limit unauthenticated POST endpoints (100/min/IP — daemon-to-tracker protocol)
	wrapProtocol := func(h http.Handler) http.Handler { return h }
	if s.limiter != nil {
		rl := ratelimit.NewMiddleware(s.limiter, ProtocolEndpointRateLimitConfig())
		wrapProtocol = rl.Wrap
	}
	api.Handle("/goodbye", wrapProtocol(http.HandlerFunc(s.peers.HandleGoodbye))).Methods(http.MethodPost)

	// Asset endpoints
	api.Handle("/announce", wrapProtocol(http.HandlerFunc(s.assets.HandleAnnounce))).Methods(http.MethodPost)
	api.HandleFunc("/search", s.assets.HandleSearch).Methods(http.MethodGet)
	api.HandleFunc("/files/search", s.assets.HandleFileSearch).Methods(http.MethodGet)
	// Register static /assets/recent before /assets/{cid} so "recent" is not captured as a CID.
	api.HandleFunc("/assets/recent", s.assets.HandleRecentAssets).Methods(http.MethodGet)
	api.HandleFunc("/assets/{cid}", s.assets.HandleGetAsset).Methods(http.MethodGet)
	api.HandleFunc("/assets/{cid}/download", s.assets.HandleDownloadRoute).Methods(http.MethodGet)
	api.HandleFunc("/assets/{cid}/replication", s.assets.HandleReplicationStatus).Methods(http.MethodGet)
	api.HandleFunc("/assets/{cid}/related", s.assets.HandleRelatedAssets).Methods(http.MethodGet) // F-002, US-002-04
	api.HandleFunc("/assets/{cid}/peers", s.assets.HandleAssetPeers).Methods(http.MethodGet)
	api.Handle("/assets/{cid}/availability", wrapProtocol(http.HandlerFunc(s.assets.HandleAvailabilityUpdate))).Methods(http.MethodPost)

	// DMCA endpoint
	api.Handle("/dmca", wrapProtocol(http.HandlerFunc(s.dmca.HandleFileNotice))).Methods(http.MethodPost)

	// Relay info endpoint (returns relay PeerID + multiaddrs for daemon AutoRelay)
	if s.relay != nil {
		api.HandleFunc("/relay", s.relay.HandleGetRelayInfo).Methods(http.MethodGet)
	}

	// Analytics endpoints
	if s.stats != nil {
		api.Handle("/stats", wrapProtocol(http.HandlerFunc(s.stats.HandleReportStats))).Methods(http.MethodPost)
		api.HandleFunc("/stats", s.stats.HandleDashboardStats).Methods(http.MethodGet)
		api.HandleFunc("/stats/by-country", s.stats.HandleStatsByCountry).Methods(http.MethodGet)
	}
	if s.downloads != nil {
		api.Handle("/downloads/complete", wrapProtocol(http.HandlerFunc(s.downloads.HandleDownloadComplete))).Methods(http.MethodPost)
	}
	if s.leaderboard != nil {
		api.HandleFunc("/leaderboard/seeders", s.leaderboard.HandleTopSeeders).Methods(http.MethodGet)
		api.HandleFunc("/leaderboard/leechers", s.leaderboard.HandleTopLeechers).Methods(http.MethodGet)
	}
	api.HandleFunc("/trending", s.assets.HandleTrending).Methods(http.MethodGet)

	// Community board abuse limits (per calling peer, inside RequireAPIKey so the key names the
	// peer): every board write shares one budget, reports have their own. Nil limiter (tests
	// without one) leaves the handlers bare.
	boardWrite := func(h http.HandlerFunc) http.Handler {
		if s.limiter == nil {
			return h
		}
		return ratelimit.NewMiddleware(s.limiter, BoardWriteRateLimitConfig()).Wrap(h)
	}
	boardReport := func(h http.HandlerFunc) http.Handler {
		if s.limiter == nil {
			return h
		}
		return ratelimit.NewMiddleware(s.limiter, BoardReportRateLimitConfig()).Wrap(h)
	}

	// Forum endpoints (mutations require X-API-Key via RequireAPIKey middleware)
	if s.forum != nil {
		api.HandleFunc("/forum/posts", s.forum.HandleListPosts).Methods(http.MethodGet)
		api.HandleFunc("/forum/posts/{post_id}/replies", s.forum.HandleListReplies).Methods(http.MethodGet)
		api.HandleFunc("/forum/posts/{post_id}", s.forum.HandleGetPost).Methods(http.MethodGet)
		if s.apiKeyRepo != nil {
			api.Handle("/forum/posts", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.forum.HandleCreatePost))).Methods(http.MethodPost)
			api.Handle("/forum/posts/{post_id}/upvote", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.forum.HandleUpvote))).Methods(http.MethodPost)
			api.Handle("/forum/posts/{post_id}/replies", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.forum.HandleCreateReply))).Methods(http.MethodPost)
			// Agent Autopilot: the calling peer's auto replies and what came of them (last 30 days),
			// and the phase 3 totals.
			api.Handle("/autopilot/outcomes", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.forum.HandleAutopilotOutcomes))).Methods(http.MethodGet)
			api.Handle("/autopilot/outcomes/summary", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.forum.HandleAutopilotOutcomesSummary))).Methods(http.MethodGet)
		} else {
			// TD-089: Auth service not configured — return 503 instead of unprotected fallback.
			unavailable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Authentication service not configured")
			})
			api.Handle("/forum/posts", unavailable).Methods(http.MethodPost)
			api.Handle("/forum/posts/{post_id}/upvote", unavailable).Methods(http.MethodPost)
			api.Handle("/forum/posts/{post_id}/replies", unavailable).Methods(http.MethodPost)
		}
	}

	// Subrouter created unconditionally — portal, token, and profile routes all use /api prefix.
	portal := s.router.PathPrefix("/api").Subrouter()

	if s.portal != nil {
		portal.HandleFunc("/home", s.portal.HandleHome).Methods(http.MethodGet)
		// F-032 AC-12: Rate limit enriched peer list (60/min/IP — batch queries make it heavier)
		var portalPeersHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalPeers)
		if s.limiter != nil {
			portalPeersHandler = ratelimit.NewMiddleware(s.limiter, PortalPeersRateLimitConfig()).Wrap(portalPeersHandler)
		}
		portal.Handle("/peers", portalPeersHandler).Methods(http.MethodGet)
		portal.HandleFunc("/board/posts", s.portal.HandlePortalBoardPosts).Methods(http.MethodGet)
		portal.HandleFunc("/board/counts", s.portal.HandlePortalBoardCounts).Methods(http.MethodGet)
		// Board phase 1: @mention autocomplete (public). Registered before the /peers/{id} routes.
		portal.HandleFunc("/peers/display-names", s.portal.HandlePortalDisplayNames).Methods(http.MethodGet)
		portal.HandleFunc("/board/posts/{id}", s.portal.HandlePortalGetPost).Methods(http.MethodGet)
		portal.HandleFunc("/board/posts/{id}/replies", s.portal.HandlePortalBoardReplies).Methods(http.MethodGet)
		// Board phase 2: one token room with the viewer's can_post (public; /board/rooms?mine=1 needs a key, below).
		portal.HandleFunc("/board/rooms/{mint}", s.portal.HandlePortalRoom).Methods(http.MethodGet)
		// Round 2: the room's settings are public (the portal shows "routing off" and the minimum holding).
		portal.HandleFunc("/board/rooms/{mint}/settings", s.portal.HandlePortalRoomSettings).Methods(http.MethodGet)
		portal.HandleFunc("/gallery/search", s.portal.HandlePortalGallerySearch).Methods(http.MethodGet)
		portal.HandleFunc("/packs", s.portal.HandlePortalPacks).Methods(http.MethodGet)
		portal.HandleFunc("/activity/recent", s.portal.HandlePortalActivityRecent).Methods(http.MethodGet)

		// F-032: Public peer detail endpoints (no auth required, rate limited by IP)
		var peerReputationHandler http.Handler = http.HandlerFunc(s.portal.HandlePeerReputation)
		var peerAssetsHandler http.Handler = http.HandlerFunc(s.portal.HandlePeerAssets)
		var peerActivityHandler http.Handler = http.HandlerFunc(s.portal.HandlePeerActivity)
		// Agent activity view: the public per-agent board summary, same limit as the peer detail.
		var peerBoardSummaryHandler http.Handler = http.HandlerFunc(s.portal.HandlePeerBoardSummary)
		if s.limiter != nil {
			publicConfig := PublicPeerDetailRateLimitConfig()
			peerReputationHandler = ratelimit.NewMiddleware(s.limiter, publicConfig).Wrap(peerReputationHandler)
			peerAssetsHandler = ratelimit.NewMiddleware(s.limiter, publicConfig).Wrap(peerAssetsHandler)
			peerActivityHandler = ratelimit.NewMiddleware(s.limiter, publicConfig).Wrap(peerActivityHandler)
			peerBoardSummaryHandler = ratelimit.NewMiddleware(s.limiter, publicConfig).Wrap(peerBoardSummaryHandler)
		}
		portal.Handle("/peers/{id}/reputation", peerReputationHandler).Methods(http.MethodGet)
		portal.Handle("/peers/{id}/assets", peerAssetsHandler).Methods(http.MethodGet)
		portal.Handle("/peers/{id}/activity", peerActivityHandler).Methods(http.MethodGet)
		portal.Handle("/peers/{id}/board-summary", peerBoardSummaryHandler).Methods(http.MethodGet)
		if s.apiKeyRepo != nil {
			// F-032: Trusted/blocked lists — static paths registered before {id} routes
			// AC-12: Rate limited by peer_id (30 req/min)
			var trustedListHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalTrustedList)
			var blockedListHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalBlockedList)
			if s.limiter != nil {
				listConfig := TrustBlockListRateLimitConfig()
				trustedListHandler = ratelimit.NewMiddleware(s.limiter, listConfig).Wrap(trustedListHandler)
				blockedListHandler = ratelimit.NewMiddleware(s.limiter, listConfig).Wrap(blockedListHandler)
			}
			portal.Handle("/peers/trusted", RequireAPIKey(s.apiKeyRepo)(trustedListHandler)).Methods(http.MethodGet)
			portal.Handle("/peers/blocked", RequireAPIKey(s.apiKeyRepo)(blockedListHandler)).Methods(http.MethodGet)

			// F-032: Trust/block mutations — AC-12: rate limited by peer_id (10 req/min)
			var trustHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalTrust)
			var blockHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalBlock)
			var untrustHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalUntrust)
			var unblockHandler http.Handler = http.HandlerFunc(s.portal.HandlePortalUnblock)
			if s.limiter != nil {
				mutationConfig := TrustBlockMutationRateLimitConfig()
				trustHandler = ratelimit.NewMiddleware(s.limiter, mutationConfig).Wrap(trustHandler)
				blockHandler = ratelimit.NewMiddleware(s.limiter, mutationConfig).Wrap(blockHandler)
				untrustHandler = ratelimit.NewMiddleware(s.limiter, mutationConfig).Wrap(untrustHandler)
				unblockHandler = ratelimit.NewMiddleware(s.limiter, mutationConfig).Wrap(unblockHandler)
			}
			portal.Handle("/peers/{id}/trust", RequireAPIKey(s.apiKeyRepo)(trustHandler)).Methods(http.MethodPost)
			portal.Handle("/peers/{id}/block", RequireAPIKey(s.apiKeyRepo)(blockHandler)).Methods(http.MethodPost)
			// F-032: Untrust/unblock (DELETE mirrors trust/block POST)
			portal.Handle("/peers/{id}/trust", RequireAPIKey(s.apiKeyRepo)(untrustHandler)).Methods(http.MethodDelete)
			portal.Handle("/peers/{id}/block", RequireAPIKey(s.apiKeyRepo)(unblockHandler)).Methods(http.MethodDelete)
			portal.Handle("/board/posts", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.portal.HandlePortalCreatePost))).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/upvote", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.portal.HandlePortalUpvote))).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/replies", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.portal.HandlePortalCreateReply))).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/award", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.portal.HandlePortalAwardBounty))).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/bounty/extend", RequireAPIKey(s.apiKeyRepo)(boardWrite(s.portal.HandlePortalExtendBounty))).Methods(http.MethodPost)
			// Board activity feed (per-peer notifications); /activity/recent above stays public.
			portal.Handle("/activity", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.portal.HandlePortalActivity))).Methods(http.MethodGet)
			portal.Handle("/activity/read", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.portal.HandlePortalActivityRead))).Methods(http.MethodPost)
			// Board phase 1 (reputation and reach): accepted answers, reports, platform
			// moderation, watches, token offer payments and the caller's board identity.
			withKey := func(h http.HandlerFunc) http.Handler { return RequireAPIKey(s.apiKeyRepo)(h) }
			withKeyWrite := func(h http.HandlerFunc) http.Handler { return RequireAPIKey(s.apiKeyRepo)(boardWrite(h)) }
			portal.Handle("/peers/me", withKey(s.portal.HandlePortalPeersMe)).Methods(http.MethodGet)
			portal.Handle("/board/posts/{id}/accept", withKeyWrite(s.portal.HandlePortalAcceptReply)).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/report", RequireAPIKey(s.apiKeyRepo)(boardReport(s.portal.HandlePortalReportPost))).Methods(http.MethodPost)
			portal.Handle("/board/replies/{id}/report", RequireAPIKey(s.apiKeyRepo)(boardReport(s.portal.HandlePortalReportReply))).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/pin", withKey(s.portal.HandlePortalPinPost)).Methods(http.MethodPost, http.MethodDelete)
			portal.Handle("/board/posts/{id}/hide", withKey(s.portal.HandlePortalHidePost)).Methods(http.MethodPost, http.MethodDelete)
			portal.Handle("/board/replies/{id}/hide", withKey(s.portal.HandlePortalHideReply)).Methods(http.MethodPost, http.MethodDelete)
			portal.Handle("/board/reports", withKey(s.portal.HandlePortalListReports)).Methods(http.MethodGet)
			portal.Handle("/board/reports/{id}/uphold", withKey(s.portal.HandlePortalUpholdReport)).Methods(http.MethodPost)
			portal.Handle("/board/reports/{id}/dismiss", withKey(s.portal.HandlePortalDismissReport)).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/watch", withKeyWrite(s.portal.HandlePortalWatch)).Methods(http.MethodPost, http.MethodDelete)
			portal.Handle("/board/posts/{id}/token-offer/pay", withKeyWrite(s.portal.HandlePortalPayTokenOffer)).Methods(http.MethodPost)
			// Board phase 2 (rooms and matchmaking): the caller's rooms and the room digest for the token's agent.
			portal.Handle("/board/rooms", withKey(s.portal.HandlePortalRooms)).Methods(http.MethodGet)
			portal.Handle("/board/rooms/{mint}/digest", withKey(s.portal.HandlePortalRoomDigest)).Methods(http.MethodGet)
			// Board phase 3 (Autopilot v2): the author raises the bounty for the repliers who asked.
			portal.Handle("/board/posts/{id}/bounty/raise", withKeyWrite(s.portal.HandlePortalRaiseBounty)).Methods(http.MethodPost)
			// Board round 2: edits and deletes with history, bounty disputes, the reviewer
			// queue, room controls and notification preferences. Edits and deletes draw on the
			// board write budget; reads and preferences do not.
			// Edits answer on PATCH and on POST .../edit (the daemon proxy's CORS allowlist of
			// installed agents predates PATCH; the portal uses the POST form).
			portal.Handle("/board/posts/{id}", withKeyWrite(s.portal.HandlePortalEditPost)).Methods(http.MethodPatch)
			portal.Handle("/board/posts/{id}/edit", withKeyWrite(s.portal.HandlePortalEditPost)).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}", withKeyWrite(s.portal.HandlePortalDeletePost)).Methods(http.MethodDelete)
			portal.Handle("/board/posts/{id}/history", withKey(s.portal.HandlePortalPostHistory)).Methods(http.MethodGet)
			portal.Handle("/board/replies/{id}", withKeyWrite(s.portal.HandlePortalEditReply)).Methods(http.MethodPatch)
			portal.Handle("/board/replies/{id}/edit", withKeyWrite(s.portal.HandlePortalEditReply)).Methods(http.MethodPost)
			portal.Handle("/board/replies/{id}", withKeyWrite(s.portal.HandlePortalDeleteReply)).Methods(http.MethodDelete)
			portal.Handle("/board/replies/{id}/history", withKey(s.portal.HandlePortalReplyHistory)).Methods(http.MethodGet)
			portal.Handle("/board/posts/{id}/bounty/dispute", withKeyWrite(s.portal.HandlePortalDisputeBounty)).Methods(http.MethodPost)
			portal.Handle("/board/posts/{id}/bounty/dispute/resolve", withKey(s.portal.HandlePortalResolveDispute)).Methods(http.MethodPost)
			portal.Handle("/board/moderation/queue", withKey(s.portal.HandlePortalModerationQueue)).Methods(http.MethodGet)
			portal.Handle("/board/rooms/{mint}/settings", withKey(s.portal.HandlePortalRoomSettings)).Methods(http.MethodPut)
			portal.Handle("/board/rooms/{mint}/mutes", withKey(s.portal.HandlePortalRoomMutes)).Methods(http.MethodGet, http.MethodPost)
			portal.Handle("/board/rooms/{mint}/mutes/{peer}", withKey(s.portal.HandlePortalRoomUnmute)).Methods(http.MethodDelete)
			portal.Handle("/activity/prefs", withKey(s.portal.HandlePortalActivityPrefs)).Methods(http.MethodGet, http.MethodPut)
		}
		// TD-087: Rate limited — 3 per hour per IP (prevents API key farming)
		var guestKeyHandler http.Handler = http.HandlerFunc(s.portal.HandleGuestKey)
		if s.limiter != nil {
			guestKeyHandler = ratelimit.NewMiddleware(s.limiter, GuestKeyRateLimitConfig()).Wrap(guestKeyHandler)
		}
		portal.Handle("/v1/portal/guest-key", guestKeyHandler).Methods(http.MethodPost)
	}
	// Agent completions: deduct credits, proxy to LLM (rate limited by IP).
	// The user-facing paths are /api/v1/agents/* (see AgentCompletionsPaths).
	if s.agentCompletions != nil {
		var agentHandler http.Handler = http.HandlerFunc(s.agentCompletions.HandleCompletions)
		if s.limiter != nil {
			agentHandler = ratelimit.NewMiddleware(s.limiter, AgentCompletionsRateLimitConfig()).Wrap(agentHandler)
		}
		// OpenAI-compatible alias: clients using the OpenAI SDK pattern point baseUrl at
		// the prefix and the SDK appends "/chat/completions". Same handler, same auth,
		// same metering, same rate limit bucket for every path below.
		for _, p := range AgentCompletionsPaths {
			portal.Handle(p, agentHandler).Methods(http.MethodPost)
		}
	}
	// Agent chat relay: token chat — holder forwards to tracker; owner's daemon polls pending and posts response (uses owner's local LLM)
	if s.agentChatRelay != nil && s.apiKeyRepo != nil {
		portal.Handle("/v1/agent/chat/owner", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleGetOwner))).Methods(http.MethodGet)
		portal.Handle("/v1/agent/chat/forward", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleForward))).Methods(http.MethodPost)
		portal.Handle("/v1/agent/chat/pending", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandlePending))).Methods(http.MethodGet)
		portal.Handle("/v1/agent/chat/response", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleResponse))).Methods(http.MethodPost)
		portal.Handle("/v1/agent/chat/result/{request_id}", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleResult))).Methods(http.MethodGet)
		portal.Handle("/v1/agent/chat/history", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleGetHistory))).Methods(http.MethodGet)
		portal.Handle("/v1/agent/chat/history/append", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleAppendHistory))).Methods(http.MethodPost)
		portal.Handle("/v1/agent/chat/personal/sessions", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.agentChatRelay.HandleListPersonalSessions))).Methods(http.MethodGet)
	}

	// F-026: Profile endpoint (authenticated — requires X-API-Key)
	// TD-027: Rate limited by peer_id (30 req/min) — limiter wraps inside RequireAPIKey
	// so peer_id is already in context for PeerIDKeyFunc.
	if s.profile != nil && s.apiKeyRepo != nil {
		var profileHandler http.Handler = http.HandlerFunc(s.profile.HandleGetMyProfile)
		if s.limiter != nil {
			profileHandler = ratelimit.NewMiddleware(s.limiter, ProfileRateLimitConfig()).Wrap(profileHandler)
		}
		portal.Handle("/profile/me",
			RequireAPIKey(s.apiKeyRepo)(profileHandler),
		).Methods(http.MethodGet)
	}

	// F-031: Token identity endpoints
	if s.token != nil {
		portal.HandleFunc("/tokens", s.token.HandleListTokens).Methods(http.MethodGet)
		portal.HandleFunc("/peers/{id}/token", s.token.HandleGetPeerToken).Methods(http.MethodGet)
		// F-031 US-031-02: Token metrics (rate limited, public read)
		var metricsHandler http.Handler = http.HandlerFunc(s.token.HandleGetTokenMetrics)
		if s.limiter != nil {
			metricsHandler = ratelimit.NewMiddleware(s.limiter, TokenMetricsRateLimitConfig()).Wrap(metricsHandler)
		}
		portal.Handle("/peers/{id}/token/metrics", metricsHandler).Methods(http.MethodGet)
		if s.apiKeyRepo != nil {
			portal.Handle("/token", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.token.HandlePersistToken))).Methods(http.MethodPost)
		}
	}

	// F-013: Credits & Identity endpoints (rate limited via TD-001)
	if s.registration != nil {
		var challengeHandler http.Handler = http.HandlerFunc(s.registration.HandleChallenge)
		var registerIdentityHandler http.Handler = http.HandlerFunc(s.registration.HandleRegister)
		if s.limiter != nil {
			challengeHandler = ratelimit.NewMiddleware(s.limiter, ChallengeRateLimitConfig()).Wrap(challengeHandler)
			registerIdentityHandler = ratelimit.NewMiddleware(s.limiter, RegisterIdentityRateLimitConfig()).Wrap(registerIdentityHandler)
		}
		api.Handle("/challenge", challengeHandler).Methods(http.MethodPost)
		api.Handle("/register/identity", registerIdentityHandler).Methods(http.MethodPost)
	}
	if s.credits != nil && s.apiKeyRepo != nil {
		api.Handle("/credits/balance", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.credits.HandleGetBalance))).Methods(http.MethodGet)
		api.Handle("/credits/transactions", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.credits.HandleListTransactions))).Methods(http.MethodGet)
		var spendTokenHandler http.Handler = RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.credits.HandleIssueSpendToken))
		if s.limiter != nil {
			spendTokenHandler = ratelimit.NewMiddleware(s.limiter, SpendTokenRateLimitConfig()).Wrap(spendTokenHandler)
		}
		api.Handle("/credits/spend-token", spendTokenHandler).Methods(http.MethodPost)
	}
	if s.social != nil {
		api.HandleFunc("/social/confirm", s.social.HandleConfirm).Methods(http.MethodPost)
		if s.apiKeyRepo != nil {
			api.Handle("/social/connections", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.social.HandleListConnections))).Methods(http.MethodGet)
			api.Handle("/social/{platform}", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.social.HandleDisconnect))).Methods(http.MethodDelete)
		}
	}
	if s.wallet != nil && s.apiKeyRepo != nil {
		api.Handle("/wallet/link", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.wallet.HandleLinkWallet))).Methods(http.MethodPost)
	}
	if s.purchase != nil && s.apiKeyRepo != nil {
		api.Handle("/purchase/intent", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.purchase.HandleCreateIntent))).Methods(http.MethodPost)
		api.Handle("/purchase/verify", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.purchase.HandleVerifyPurchase))).Methods(http.MethodPost)
	}
	if s.recovery != nil && s.apiKeyRepo != nil {
		api.Handle("/account/recover", RequireAPIKey(s.apiKeyRepo)(http.HandlerFunc(s.recovery.HandleRecover))).Methods(http.MethodPost)
	}

	// StonkAgents launchpad routes (see routes_launch.go)
	s.setupLaunchRoutes(portal)
	// StonkAgents portal feedback routes (see routes_feedback.go)
	s.setupFeedbackRoutes(portal)
	// StonkAgents roadmap interest routes (see routes_interest.go)
	s.setupInterestRoutes(portal)
	// StonkAgents devnet drip route (see routes_dev_drip.go)
	s.setupDevDripRoutes(portal)

	// Health endpoint
	s.router.HandleFunc("/health", s.handleHealth).Methods(http.MethodGet)

	// Metrics endpoint (Prometheus)
	s.router.Handle("/metrics", HandleMetrics()).Methods(http.MethodGet)

	// 404 handler
	s.router.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Route not found")
	})
}

func (s *Server) setupMiddleware() {
	// CORS is applied at server level (wraps router) so OPTIONS preflight is handled for all paths.
	s.router.Use(CorrelationIDMiddleware)
	s.router.Use(ContentTypeMiddleware)
	s.router.Use(MetricsMiddleware)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"status":  "healthy",
		"version": s.version,
	}
	if s.healthProbe != nil && os.Getenv("HEALTH_CHECKS_VERBOSE") == "true" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if extra := s.healthProbe(ctx); extra != nil {
			for k, v := range extra {
				resp[k] = v
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// Router returns the underlying mux router (for testing).
func (s *Server) Router() *mux.Router {
	return s.router
}

// Start begins listening on the configured address.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
