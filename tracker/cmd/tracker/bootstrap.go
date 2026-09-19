// Package: main
// Purpose: Tracker bootstrap — repository init, service init, handler wiring.
// Extracted from main.go to keep orchestration separate from wiring.

package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stonkagents/agent/tracker/internal/api"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/embedding"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/leaderboard"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/relay"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/reputation"
	"github.com/stonkagents/agent/tracker/internal/scheduler"
	"github.com/stonkagents/agent/tracker/internal/search"
	"github.com/stonkagents/agent/tracker/internal/services"
	"github.com/stonkagents/agent/tracker/internal/solana"
)

// BootstrapConfig holds inputs for bootstrap.
type BootstrapConfig struct {
	Addr           string
	PgPool         *pgxpool.Pool
	Secrets        *trackerSecrets
	Version        string
	PresenceTTL    time.Duration
	ShutdownCtx    context.Context
	ShutdownCancel context.CancelFunc
}

// BootstrapResult holds the HTTP server and cleanup resources.
type BootstrapResult struct {
	Server     *api.Server
	RelayHost  *relay.Host
	Redis      *redis.Client
	RecalcDone func()
	JobDone    func()
}

// Bootstrap wires repositories, services, handlers, and returns the HTTP server plus cleanup hooks.
func Bootstrap(cfg BootstrapConfig) (*BootstrapResult, error) {
	pgPool := cfg.PgPool

	// StonkAgents launchpad: validate PINATA_JWT/uploader config before any goroutines start
	// (production/staging fail fast on a missing secret).
	launchMetadataHandler, err := bootstrapLaunchMetadata(cfg.Secrets.Env)
	if err != nil {
		return nil, err
	}

	// Repositories
	peerRepo := repository.NewPostgresPeerRepository(pgPool)
	apiKeyRepo := repository.NewPostgresPeerAPIKeyRepository(pgPool)
	forumRepo := repository.NewPostgresForumRepository(pgPool)
	assetRepo := repository.NewPostgresAssetRepository(pgPool)
	replicationRepo := repository.NewPostgresReplicationRepository(pgPool)
	dmcaRepo := repository.NewPostgresDMCARepository(pgPool)
	availabilityRepo := repository.NewPostgresAvailabilityRepository(pgPool)
	store := presence.NewPostgresPresenceStore(pgPool, cfg.PresenceTTL)
	reputationRepo := repository.NewPostgresReputationRepository(pgPool)
	tokenRepo := repository.NewPostgresTokenRepository(pgPool)

	var redisClient *redis.Client
	var leaderboardStore leaderboard.Store
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		if opt, err := redis.ParseURL(redisURL); err == nil {
			redisClient = redis.NewClient(opt)
		} else {
			redisClient = redis.NewClient(&redis.Options{Addr: redisURL})
		}
		leaderboardStore = leaderboard.NewRedisStore(redisClient, peerRepo, assetRepo)
		fmt.Println("Tracker storage: PostgreSQL; leaderboard cache: Redis")
	} else {
		leaderboardStore = leaderboard.NewPostgresStore(peerRepo, assetRepo)
		fmt.Println("Tracker storage: PostgreSQL")
	}

	clk := clock.RealClock{}
	accountRepo := repository.NewPostgresAccountRepository(pgPool)
	creditRepo := repository.NewPostgresCreditRepository(pgPool)
	walletRepo := repository.NewPostgresWalletRepository(pgPool)
	settingsRepo := repository.NewPlatformSettingsRepository(pgPool)
	peerSvc := services.NewPeerServiceWithDeps(services.PeerServiceDeps{
		Repo: peerRepo, Presence: store, APIKeyRepo: apiKeyRepo,
		AccountRepo: accountRepo, CreditRepo: creditRepo, WalletRepo: walletRepo,
	})
	assetSvc := bootstrapAssetService(assetRepo, store, availabilityRepo)
	dmcaSvc := services.NewDMCAService(assetRepo, dmcaRepo)

	// F-013 repos (accountRepo, creditRepo, walletRepo already created above)
	nonceRepo := repository.NewPostgresNonceRepository(pgPool)
	blockRepo := repository.NewPostgresBlockRepository(pgPool)
	socialRepo := repository.NewPostgresSocialRepository(pgPool)
	purchaseRepo := repository.NewPostgresPurchaseRepository(pgPool)

	regSvc := services.NewRegistrationService(services.RegistrationServiceDeps{
		Accounts: accountRepo, Credits: creditRepo, Nonces: nonceRepo, Blocks: blockRepo,
		Peers: peerRepo, APIKeys: apiKeyRepo, Presence: store, Clock: clk,
	})
	creditSvc := services.NewCreditService(services.CreditServiceDeps{
		Credits: creditRepo, Accounts: accountRepo, Clock: clk, JWTSecret: cfg.Secrets.JWTSigningSecret,
	})
	socialSvc := services.NewSocialService(services.SocialServiceDeps{
		Social: socialRepo, Credits: creditRepo, Accounts: accountRepo, Clock: clk,
	})
	recoverySvc := services.NewRecoveryService(services.RecoveryServiceDeps{
		Accounts: accountRepo, Credits: creditRepo, Social: socialRepo,
		Wallets: walletRepo, APIKeys: apiKeyRepo, Clock: clk,
	})

	var solanaClient services.SolanaClient
	var txVerifier services.TransactionVerifier
	if cfg.Secrets.UseSolanaStubs {
		solanaClient = &devSolanaClient{}
		txVerifier = &devTxVerifier{}
		fmt.Println("Solana: dev stubs active (SOLANA_USE_STUBS=true)")
	} else {
		rpc := solana.NewClient(cfg.Secrets.SolanaRPCURL)
		solanaClient = rpc
		txVerifier = solana.NewVerifier(rpc)
		fmt.Printf("Solana: real RPC client (%s)\n", cfg.Secrets.SolanaRPCURL)
	}
	walletSvc := services.NewWalletService(services.WalletServiceDeps{
		Wallets: walletRepo, Credits: creditRepo, Accounts: accountRepo, Clock: clk, Solana: solanaClient,
	})
	purchaseSvc := services.NewPurchaseService(services.PurchaseServiceDeps{
		Purchases: purchaseRepo, Credits: creditRepo, Accounts: accountRepo, Settings: settingsRepo,
		Clock: clk, TxVerifier: txVerifier, TreasuryAddress: cfg.Secrets.SolanaTreasuryAddress,
	})
	fmt.Println("F-013 Credits & Identity: PostgreSQL repos (persistent)")

	// Reputation recalculator
	statsProvider := &peerStatsProvider{repo: peerRepo}
	recalc := reputation.NewRecalculator(statsProvider, reputationRepo, clock.RealClock{})
	recalcCtx, recalcCancel := context.WithCancel(context.Background())
	recalc.Start(recalcCtx, reputation.DefaultRecalcInterval)

	// Relay
	relayAddr := os.Getenv("RELAY_LISTEN_ADDR")
	if relayAddr == "" {
		relayAddr = "/ip4/0.0.0.0/tcp/9841"
	}
	relayHost, relayHandler := bootstrapRelay(relayAddr)

	// Forum
	forumSvc := services.NewForumService(forumRepo, accountRepo, creditRepo)
	forumSvc.AutopilotMaxRepliesPerDay = cfg.Secrets.AutopilotMaxRepliesPerDay
	forumSvc.SetClock(clk)
	forumSvc.SetActivityRepo(repository.NewPostgresBoardActivityRepository(pgPool)) // board activity feed (migration 022)
	forumSvc.SetCreditRefunder(creditSvc)                                           // escrow back to the poster on bounty expiry
	fmt.Printf("Autopilot: max auto replies per peer per day=%d (AUTOPILOT_MAX_REPLIES_PER_DAY, 0=off)\n", cfg.Secrets.AutopilotMaxRepliesPerDay)
	forumHandler := api.NewForumHandler(forumSvc, apiKeyRepo)
	forumHandler.SetPeerRepo(peerRepo) // author_display_name on posts and replies

	// Handlers
	assetHandler := api.NewAssetHandlerWithPeers(assetSvc, peerSvc)
	assetHandler.SetAssetRepo(assetRepo)
	assetHandler.SetReplicationRepo(replicationRepo)
	statsHandler := api.NewStatsHandler(peerSvc, assetSvc, peerRepo, assetRepo, store, cfg.PresenceTTL, leaderboardStore)
	downloadsHandler := api.NewDownloadsHandler(assetRepo, peerRepo)
	leaderboardHandler := api.NewLeaderboardHandler(leaderboardStore)

	trustBlockRepo := repository.NewPostgresPeerTrustBlockRepository(pgPool)
	peerHandler := api.NewPeerHandlerWithTrustBlock(peerSvc, trustBlockRepo)
	peerHandler.SetAPIKeyRepo(apiKeyRepo)
	peerHandler.SetPresenceStore(store)

	guestKeyMappingRepo := repository.NewPostgresGuestKeyMappingRepository(pgPool)
	guestKeySvc := services.NewGuestKeyService(services.GuestKeyServiceDeps{
		Peers: peerRepo, Accounts: accountRepo, Credits: creditRepo,
		GuestKeys: guestKeyMappingRepo, Clock: clk,
	})
	limiter := ratelimit.NewMemoryLimiter(clk)
	peerEventRepo := repository.NewPostgresPeerEventRepository(pgPool)
	snapshotRepo := repository.NewPostgresReputationSnapshotRepository(pgPool)
	geoResolver := bootstrapGeoResolver()
	peerHandler.SetGeoResolver(geoResolver)
	portalHandler := api.NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, trustBlockRepo, apiKeyRepo, guestKeySvc, reputationRepo, geoResolver, peerEventRepo, snapshotRepo, limiter)

	regHandler := api.NewRegistrationHandler(regSvc)
	creditHandler := api.NewCreditHandler(creditSvc, creditRepo, accountRepo)
	socialHandler := api.NewSocialHandler(socialSvc, accountRepo, cfg.Secrets.SocialCallbackSecret)
	walletHandler := api.NewWalletHandler(walletSvc, accountRepo)
	purchaseHandler := api.NewPurchaseHandler(purchaseSvc, accountRepo)
	recoveryHandler := api.NewRecoveryHandler(recoverySvc, accountRepo)

	profileHandler := api.NewProfileHandler(reputationRepo, peerRepo, assetRepo, store, trustBlockRepo)
	profileHandler.SetLibraryCounter(peerEventRepo)

	agentCompletionsCfg := api.AgentCompletionsConfig{
		LLMURL:      os.Getenv("AGENT_LLM_URL"),
		LLMAPIKey:   os.Getenv("AGENT_LLM_API_KEY"),
		LLMProvider: os.Getenv("AGENT_LLM_PROVIDER"),
		LLMModel:    os.Getenv("AGENT_LLM_MODEL"),
	}
	llmUsageRepo := repository.NewLLMUsageRepository(pgPool)
	agentCompletionsHandler := api.NewAgentCompletionsHandler(
		creditSvc, apiKeyRepo, accountRepo, guestKeyMappingRepo,
		settingsRepo, llmUsageRepo, agentCompletionsCfg,
	)

	// StonkAgents launchpad: recorded launches drive metrics routing and are shared with bootstrapLaunchpad.
	launchRepo := repository.NewPostgresLaunchRepository(pgPool)
	// Community board phase 1: reputation, reports, watches, mentions, token offer payments.
	reputationSvc := bootstrapBoardPhase1(cfg.Secrets, pgPool, forumSvc, boardPhase1Deps{
		Forum: forumRepo, Peers: peerRepo, Launches: launchRepo, Wallets: walletRepo, Clock: clk,
	})

	// One metrics service, shared by the token endpoints and the launch views.
	var holderCounter services.HolderCounter
	if rpc, ok := solanaClient.(*solana.Client); ok {
		holderCounter = rpc
	}
	launchLabClient := bootstrapLaunchLabClient(cfg.Secrets, loadLaunchLabConfig(), slog.Default())
	metricsSvc := bootstrapMetricsService(cfg.Secrets, tokenRepo, launchLabClient, redisClient, holderCounter)
	// The LaunchLab client also prices quote mints in USD (devnet fallback included) for 24h volume.
	var quoteUsd services.QuoteUsdSource
	if q, ok := launchLabClient.(services.QuoteUsdSource); ok {
		quoteUsd = q
	}
	tokenHandler := api.NewTokenHandler(tokenRepo)
	tokenHandler.SetMetricsService(metricsSvc)
	tokenHandler.SetPeerRepo(peerRepo)               // display_name on /api/tokens and /api/peers/{id}/token
	tokenHandler.SetReputationService(reputationSvc) // reputation_tier next to it

	agentChatRelayStore := api.NewAgentChatRelayStore()
	agentChatHistoryRepo := repository.NewPostgresAgentChatHistoryRepository(pgPool)
	agentChatRelayHandler := api.NewAgentChatRelayHandler(agentChatRelayStore, tokenRepo, apiKeyRepo, accountRepo, guestKeyMappingRepo, agentChatHistoryRepo, creditSvc, peerSvc)

	manifestURL := os.Getenv("RELEASE_MANIFEST_URL")
	if manifestURL == "" {
		manifestURL = "https://releases.stonkagents.com/manifest.json"
	}
	versionChecker := services.NewVersionChecker(manifestURL)
	versionChecker.Start(cfg.ShutdownCtx)

	healthProbe := buildHealthProbe(pgPool, redisClient)

	// StonkAgents launchpad: launch config + fee pricing job
	launchConfigHandler := bootstrapLaunchConfig(cfg.ShutdownCtx, pgPool, cfg.Secrets, clk)
	// StonkAgents launchpad: launch records, claim/bind, revenue ledger and keeper writes
	// (fails fast on an invalid AGENT_BURN_* burn-plan env)
	// Community board phase 2: token rooms, request routing, room digest. The forum service
	// announces claimed launches in their room (Announcer below).
	quoteRepo := repository.NewPostgresLaunchQuoteRepository(pgPool, cfg.Secrets.LaunchpadCluster)
	peerHandler.SetAutopilotRepo(bootstrapBoardPhase2(cfg.Secrets, pgPool, forumSvc, boardPhase2Deps{
		Metrics: metricsSvc, Presence: store, Quotes: quoteRepo,
	}))
	// Community board round 2: edits and deletes, disputes, room controls, visits, preferences.
	bootstrapBoardRound2(cfg.Secrets, pgPool, forumSvc, reputationSvc)
	launchpad, err := bootstrapLaunchpad(cfg.Secrets, pgPool, launchpadDeps{
		Launches: launchRepo, Tokens: tokenRepo, Accounts: accountRepo, Wallets: walletRepo, Credits: creditRepo,
		Quotes: quoteRepo, Metrics: metricsSvc, Clock: clk,
		MetricsRefresh: metricsSvc, Ctx: cfg.ShutdownCtx, QuoteUsd: quoteUsd, Peers: peerRepo, Reputation: reputationSvc,
		Announcer: forumSvc,
	})
	if err != nil {
		recalcCancel()
		return nil, err
	}

	// StonkAgents devnet drip: refuses to start with a drip key on a non-devnet cluster.
	devDripHandler, err := bootstrapDevDrip(cfg.Secrets, pgPool, clk)
	if err != nil {
		recalcCancel()
		return nil, err
	}
	// GET /api/launch/config advertises the drip so the portal can hide it instead of 404ing.
	launchConfigHandler.SetDevDripEnabled(devDripHandler != nil)

	srv := api.NewServer(api.ServerDeps{
		PeerHandler:         peerHandler,
		PortalHandler:       portalHandler,
		ReputationHandler:   api.NewReputationHandler(reputationRepo),
		AssetHandler:        assetHandler,
		DMCAHandler:         api.NewDMCAHandler(dmcaSvc),
		RelayHandler:        relayHandler,
		StatsHandler:        statsHandler,
		DownloadsHandler:    downloadsHandler,
		LeaderboardHandler:  leaderboardHandler,
		ForumHandler:        forumHandler,
		ProfileHandler:      profileHandler,
		TokenHandler:        tokenHandler,
		AgentCompletions:    agentCompletionsHandler,
		AgentChatRelay:      agentChatRelayHandler,
		LaunchConfigHandler: launchConfigHandler,
		APIKeyRepo:          apiKeyRepo,
		GuestKeyRepo:        guestKeySvc,
		RegistrationHandler: regHandler,
		CreditHandler:       creditHandler,
		SocialHandler:       socialHandler,
		WalletHandler:       walletHandler,
		PurchaseHandler:     purchaseHandler,
		RecoveryHandler:     recoveryHandler,
		Limiter:             limiter,
		VersionChecker:      versionChecker,
		HealthProbe:         healthProbe,
		Address:             cfg.Addr,
		Version:             cfg.Version,
		// StonkAgents launchpad: server-side token metadata upload
		LaunchMetadataHandler:  launchMetadataHandler,
		LaunchHandler:          launchpad.Launch,
		RevenueHandler:         launchpad.Revenue,
		InternalRevenueHandler: launchpad.InternalRevenue,
		LaunchTradesHandler:    launchpad.Trades,
		AgentTokenHandler:      launchpad.AgentToken,
		// StonkAgents portal feedback
		FeedbackHandler: bootstrapFeedback(pgPool, clk),
		// StonkAgents roadmap interest
		InterestHandler: bootstrapInterest(pgPool, clk),
		// StonkAgents devnet drip
		DevDripHandler: devDripHandler,
	})

	bootstrapBackgroundJobs(cfg.ShutdownCtx, cfg.ShutdownCancel, blockRepo, creditSvc, regSvc, purchaseSvc, forumSvc, statsProvider, reputationRepo, snapshotRepo, accountRepo, creditRepo, clk, limiter)
	startReputationNightly(cfg.ShutdownCtx, reputationSvc, clk)

	return &BootstrapResult{
		Server:     srv,
		RelayHost:  relayHost,
		Redis:      redisClient,
		RecalcDone: recalcCancel,
		JobDone:    cfg.ShutdownCancel,
	}, nil
}

func bootstrapAssetService(assetRepo repository.AssetRepository, store presence.PresenceStore, availabilityRepo repository.AvailabilityRepository) *services.AssetService {
	if os.Getenv("SEMANTIC_SEARCH_ENABLED") == "true" {
		embEndpoint := os.Getenv("EMBEDDING_ENDPOINT")
		if embEndpoint == "" {
			embEndpoint = "http://localhost:11434"
		}
		embGen := embedding.NewGenerator(embedding.GeneratorOptions{ModelEndpoint: embEndpoint})
		hnswIndex := search.NewHNSWIndex(search.HNSWOptions{Dimensions: 384, M: 16, EfConstruct: 200})
		fmt.Printf("Semantic search: enabled (embedding endpoint %s)\n", embEndpoint)
		return services.NewAssetServiceWithSemanticSearch(assetRepo, store, availabilityRepo, embGen, hnswIndex)
	}
	return services.NewAssetService(assetRepo, store, availabilityRepo)
}

func bootstrapRelay(addr string) (*relay.Host, *api.RelayHandler) {
	host, err := relay.NewHost(addr)
	if err != nil {
		log.Printf("WARNING: Failed to start relay host: %v", err)
		return nil, nil
	}
	h := api.NewRelayHandler(host)
	fmt.Printf("Relay host started: PeerID=%s\n", host.ID().String())
	for _, a := range host.Multiaddrs() {
		fmt.Printf("  Relay addr: %s\n", a)
	}
	if publicAddr := os.Getenv("RELAY_PUBLIC_ADDR"); publicAddr != "" {
		peerID := host.ID().String()
		h.SetPublicMultiaddrs([]string{publicAddr + "/p2p/" + peerID})
	}
	return host, h
}

func buildHealthProbe(pgPool *pgxpool.Pool, redisClient *redis.Client) func(context.Context) map[string]interface{} {
	return func(ctx context.Context) map[string]interface{} {
		out := make(map[string]interface{})
		if pgPool != nil {
			if err := pgPool.Ping(ctx); err != nil {
				out["database"] = "unhealthy: " + err.Error()
			} else {
				out["database"] = "ok"
			}
		}
		if redisClient != nil {
			if err := redisClient.Ping(ctx).Err(); err != nil {
				out["redis"] = "unhealthy: " + err.Error()
			} else {
				out["redis"] = "ok"
			}
		}
		return out
	}
}

func bootstrapGeoResolver() geo.Resolver {
	if k := os.Getenv("IPDATA_API_KEY"); k != "" {
		fmt.Println("GeoIP: ipdata.co resolver active")
		return geo.NewIPDataResolver(k)
	}
	fmt.Println("GeoIP: stub resolver (set IPDATA_API_KEY for ipdata.co)")
	return &geo.StubResolver{}
}

func bootstrapMetricsService(secrets *trackerSecrets, tokenRepo repository.TokenRepository, launchLab services.LaunchLabClient, redis *redis.Client, holders services.HolderCounter) *services.MetricsService {
	slogLogger := slog.Default()
	pumpFunBaseURL := secrets.PumpFunAPIURL
	if pumpFunBaseURL == "" {
		pumpFunBaseURL = services.PumpFunBaseURLMainnet
	}
	var moralis services.MoralisClient
	if k := os.Getenv("MORALIS_API_KEY"); k != "" {
		moralis = services.NewMoralisClient(k, slogLogger)
	}
	llCfg := loadLaunchLabConfig()
	return services.NewMetricsService(services.MetricsServiceDeps{
		PumpFun:       services.NewPumpFunClientWithBaseURL(slogLogger, pumpFunBaseURL),
		LaunchLab:     launchLab,
		DefaultSource: llCfg.DefaultSource,
		Moralis:       moralis,
		Holders:       holders,
		TokenRepo:     tokenRepo,
		Redis:         redis,
		Logger:        slogLogger,
	})
}

func bootstrapBackgroundJobs(ctx context.Context, cancel context.CancelFunc, blockRepo repository.BlockRepository, creditSvc *services.CreditService, regSvc *services.RegistrationService, purchaseSvc *services.PurchaseService, forumSvc *services.ForumService, statsProvider *peerStatsProvider, reputationRepo repository.ReputationRepository, snapshotRepo repository.ReputationSnapshotRepository, accountRepo repository.AccountRepository, creditRepo repository.CreditRepository, clk clock.Clock, limiter ratelimit.Limiter) {
	velocityDetector := ratelimit.NewVelocityDetector(ratelimit.VelocityDetectorDeps{Limiter: limiter, Blocks: blockRepo, Clock: clk})
	startJob(ctx, "velocity-scan", 5*time.Minute, func() error {
		n, err := velocityDetector.Scan(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Printf("[velocity-scan] blocked %d abusive subnets", n)
		}
		return nil
	})
	startJob(ctx, "block-cleanup", 1*time.Hour, func() error {
		n, _ := blockRepo.CleanExpired(ctx, clk.Now())
		if n > 0 {
			log.Printf("[block-cleanup] removed %d expired blocks", n)
		}
		return nil
	})
	startJob(ctx, "credit-expiry", 1*time.Hour, func() error {
		n, _ := creditSvc.ExpireStaleCredits(ctx)
		if n > 0 {
			log.Printf("[credit-expiry] expired %d account free balances", n)
		}
		return nil
	})
	startJob(ctx, "bounty-expiry", 10*time.Minute, func() error {
		n, err := forumSvc.ExpireBounties(ctx)
		if n > 0 {
			log.Printf("[bounty-expiry] expired %d bounties", n)
		}
		return err
	})
	startJob(ctx, "presence-grant", 1*time.Minute, func() error {
		n, _ := regSvc.CheckPresenceGrants(ctx)
		if n > 0 {
			log.Printf("[presence-grant] granted %d presence bonuses", n)
		}
		return nil
	})
	startJob(ctx, "intent-cleanup", 5*time.Minute, func() error {
		n, _ := purchaseSvc.ExpireStaleIntents(ctx)
		if n > 0 {
			log.Printf("[intent-cleanup] expired %d purchase intents", n)
		}
		return nil
	})
	cronScheduler := scheduler.New(scheduler.Config{
		StatsProvider: statsProvider,
		RepStore:      reputationRepo,
		SnapshotRepo:  snapshotRepo,
		AccountRepo:   accountRepo,
		CreditRepo:    creditRepo,
		Clock:         clk,
	})
	startJob(ctx, "daily-snapshot", 24*time.Hour, func() error {
		return cronScheduler.RunDailySnapshot(ctx)
	})
	startJob(ctx, "weekly-bonus", 7*24*time.Hour, func() error {
		return cronScheduler.RunWeeklyBonus(ctx)
	})
	fmt.Println("F-013 background jobs started")
}

// peerStatsProvider adapts PeerRepository to reputation.StatsProvider.
type peerStatsProvider struct {
	repo repository.PeerRepository
}

func (p *peerStatsProvider) AllPeerIDs(ctx context.Context) ([]string, error) {
	peers, err := p.repo.List(ctx, repository.ListPeersOptions{Limit: 50000, Offset: 0})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(peers))
	for _, peer := range peers {
		ids = append(ids, peer.PeerID)
	}
	return ids, nil
}

func (p *peerStatsProvider) GetPeerStats(ctx context.Context, peerID string) (*reputation.PeerStats, error) {
	peer, err := p.repo.FindByID(ctx, peerID)
	if err != nil {
		return nil, err
	}
	return &reputation.PeerStats{
		PeerID:               peerID,
		UploadBytes:          peer.TotalUploadBytes,
		DownloadBytes:        peer.TotalDownloadBytes,
		TotalDownloads:       0,
		VerificationFailures: 0,
		DMCAFlagged:          false,
		DMCAReportsFiled:     0,
	}, nil
}

func startJob(ctx context.Context, name string, interval time.Duration, fn func() error) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				if err := fn(); err != nil {
					log.Printf("[%s] error: %v", name, err)
				}
			}
		}
	}()
}

// devSolanaClient is a stub for local development (SOLANA_USE_STUBS=true).
type devSolanaClient struct{}

func (d *devSolanaClient) GetBalance(_ context.Context, _ string) (int64, error) {
	return 500_000_000, nil
}
func (d *devSolanaClient) GetFirstTransactionTime(_ context.Context, _ string) (*time.Time, error) {
	t := time.Now().Add(-30 * 24 * time.Hour)
	return &t, nil
}
func (d *devSolanaClient) VerifyWalletSignature(_ string, _, _ []byte) bool {
	return true
}

// devTxVerifier is a stub for local development (SOLANA_USE_STUBS=true).
// Mirrors the requested AmountLamports so any test/dev intent passes the
// purchase service's "amount mismatch" check regardless of tier values.
// The real verifier ignores AmountLamports entirely — it derives the on-chain
// delta from pre/post balances — so this stub behavior never leaks into prod.
type devTxVerifier struct{}

func (d *devTxVerifier) VerifyTransaction(_ context.Context, _ string, params services.VerifyParams) (*services.TxVerifyResult, error) {
	return &services.TxVerifyResult{
		Success:        true,
		ActualLamports: params.AmountLamports,
		MemoMatch:      true,
	}, nil
}

// --- Raydium LaunchLab metrics (StonkAgents) ---

// launchLabAccountReader adapts solana.Client to services.AccountReader.
type launchLabAccountReader struct{ rpc *solana.Client }

func (a launchLabAccountReader) GetMultipleAccounts(ctx context.Context, addrs []string) ([]*services.RPCAccount, error) {
	infos, err := a.rpc.GetMultipleAccounts(ctx, addrs)
	if err != nil {
		return nil, err
	}
	out := make([]*services.RPCAccount, len(infos))
	for i, info := range infos {
		if info != nil {
			out[i] = &services.RPCAccount{Owner: info.Owner, Data: info.Data}
		}
	}
	return out, nil
}

// bootstrapLaunchLabClient builds the LaunchLab metrics source. Under SOLANA_USE_STUBS
// a deterministic stub is used; otherwise the Raydium API client with an on-chain
// PoolState fallback over a dedicated RPC client (own circuit breaker).
func bootstrapLaunchLabClient(secrets *trackerSecrets, cfg launchLabConfig, logger *slog.Logger) services.LaunchLabClient {
	if secrets.UseSolanaStubs {
		fmt.Println("LaunchLab metrics: stub client (SOLANA_USE_STUBS=true)")
		return services.NewStubRaydiumClient()
	}
	var rpc services.AccountReader
	if secrets.SolanaRPCURL != "" {
		rpc = launchLabAccountReader{rpc: solana.NewClient(secrets.SolanaRPCURL)}
	}
	fmt.Printf("LaunchLab metrics: Raydium API %s (default source: %s, RPC fallback: %v)\n",
		cfg.APIURL, cfg.DefaultSource, rpc != nil)
	return services.NewRaydiumClient(services.RaydiumClientConfig{
		APIBaseURL: cfg.APIURL,
		Timeout:    cfg.Timeout,
		ProgramID:  cfg.ProgramID,
		QuoteMints: cfg.QuoteMints,
		Prices:     services.NewJupiterPriceClient(cfg.JupiterURL, cfg.Timeout, logger),
		RPC:        rpc,
		Logger:     logger,
	})
}
