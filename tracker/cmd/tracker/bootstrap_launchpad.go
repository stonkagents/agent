// Package: main
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Wiring for the launch record/claim flow and the revenue summary (kept out of
//          bootstrap.go so the launchpad branch merges cleanly and bootstrap.go stays < 500 lines).

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/api"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
	"github.com/stonkagents/agent/tracker/internal/solana"
)

// launchpadDeps are the shared repos the launchpad wiring reuses from the main bootstrap.
type launchpadDeps struct {
	Launches repository.LaunchRepository // optional; created from pgPool when nil
	Tokens   repository.TokenRepository
	Accounts repository.AccountRepository
	Wallets  repository.WalletRepository
	Credits  repository.CreditRepository
	// Quotes is the quote catalog used to inline quote details on launch views.
	Quotes repository.LaunchQuoteRepository
	// Metrics enriches launch views with market data (shared with the token handler).
	Metrics services.LaunchMetricsProvider
	// MetricsRefresh is the concrete metrics service the background refresher writes through
	// (nil = no refresher). Ctx bounds the refresher goroutine.
	MetricsRefresh *services.MetricsService
	// QuoteUsd prices quote mints in USD for the 24h volume (nil = implied from the card's own prices).
	QuoteUsd services.QuoteUsdSource
	// Peers resolves the bound agent display name on launch views (peer_display_name).
	Peers repository.PeerRepository
	// Reputation adds the bound agent's board tier to launch views (peer_reputation_tier); nil = omitted.
	Reputation *services.ReputationService
	// Announcer pins the launch announcement in the token room at claim time (board phase 2); nil = none.
	Announcer services.LaunchAnnouncer
	Ctx       context.Context
	Clock     clock.Clock
}

// launchpadHandlers are the HTTP handlers the launchpad wiring produces.
type launchpadHandlers struct {
	Launch  *api.LaunchHandler
	Revenue *api.RevenueHandler
	// InternalRevenue is nil unless INTERNAL_API_TOKEN is set; without it the
	// POST /api/internal/revenue route is never registered and answers 404.
	InternalRevenue *api.InternalRevenueHandler
	// Trades serves GET /api/launch/{mint}/trades and /candles.
	Trades *api.LaunchTradesHandler
	// AgentToken serves GET /api/v1/agent-token/burnplan and /payouts; nil (404) until AGENT_TOKEN_MINT is set.
	AgentToken *api.AgentTokenHandler
}

// bootstrapLaunchpad wires the StonkAgents LaunchLab launch record/claim flow, the public
// revenue summary and the keeper's ledger write endpoint.
//
// Env read here:
//
//	LAUNCH_TREASURY_ADDRESS          overrides SolanaTreasuryAddress when set
//	LAUNCHPAD_PLATFORM_ID            our LaunchLab PlatformConfig; when set, launches must reference it
//	LAUNCHPAD_CLUSTER                mainnet|devnet; derived from LAUNCHPAD_PROGRAM_ID when unset (envconfig.go)
//	LAUNCH_FEE_MIN_LAMPORTS          record-time fee floor; same value (and default) the fee
//	                                 pricing job uses, so a launch can never pay less than
//	                                 GET /api/launch/config quoted (parsed once in envconfig.go)
//	BUYBACK_WALLET_ADDRESS           optional; published on GET /api/revenue
//	TRANSFER_FEE_AUTHORITY_ADDRESS   optional; published on GET /api/revenue
//	SOLANA_EXPLORER_BASE_URL         optional; explorer links in the revenue feed
//	SOLANA_EXPLORER_CLUSTER          optional; e.g. "devnet"
//	INTERNAL_API_TOKEN               optional; when unset POST /api/internal/revenue is not registered
//	LAUNCH_ONE_PER_WALLET            boolean, default true: a creator wallet may record one launch;
//	                                 a second POST /api/launch/record answers 409 LAUNCH_EXISTS
//	                                 (parsed in envconfig.go)
//	INDEXER_ENABLED                  boolean, default true: poll every launch's pool for trades
//	                                 (off under SOLANA_USE_STUBS, which has no RPC)
//	INDEXER_INTERVAL                 poll interval, Go duration or seconds (default 30s)
//	AGENT_TOKEN_MINT                 network token mint; when set its burns are indexed and
//	                                 GET /api/v1/agent-token/burnplan + /payouts are served (else 404)
//	AGENT_TOKEN_SUPPLY, AGENT_BURN_* burn plan figures (see bootstrap_agent_token.go); invalid = error
func bootstrapLaunchpad(secrets *trackerSecrets, pgPool *pgxpool.Pool, deps launchpadDeps) (launchpadHandlers, error) {
	// Cluster detection: LAUNCHPAD_CLUSTER wins; otherwise derived from LAUNCHPAD_PROGRAM_ID
	// (DRay6f… = devnet, LanMV9… = mainnet). Quote reads and GET /api/launch/config are scoped to it.
	fmt.Printf("Launchpad: cluster=%s program=%s (LAUNCHPAD_CLUSTER=%q)\n",
		secrets.LaunchpadCluster, secrets.LaunchpadProgramID, os.Getenv("LAUNCHPAD_CLUSTER"))
	treasury := os.Getenv("LAUNCH_TREASURY_ADDRESS")
	if treasury == "" {
		treasury = secrets.SolanaTreasuryAddress
	}
	// The floor the fee pricing job clamps to is also the floor a recorded launch must have
	// paid: fee.lamports = max(LAUNCH_FEE_MIN_LAMPORTS, LAUNCH_FEE_USD at live SOL/USD).
	minFee := secrets.LaunchFeeMinLamports

	var verifier services.LaunchVerifier
	if secrets.UseSolanaStubs {
		verifier = &services.StubLaunchVerifier{}
		fmt.Println("Launchpad: stub launch verifier active (SOLANA_USE_STUBS=true)")
	} else {
		verifier = solana.NewLaunchVerifier(solana.NewClient(secrets.SolanaRPCURL)).WithProgramID(secrets.LaunchpadProgramID)
		fmt.Printf("Launchpad: on-chain launch verifier (%s), treasury=%q\n", secrets.SolanaRPCURL, treasury)
	}

	launchRepo := deps.Launches
	if launchRepo == nil {
		launchRepo = repository.NewPostgresLaunchRepository(pgPool)
	}
	revenueRepo := repository.NewPostgresRevenueRepository(pgPool)
	quotes := deps.Quotes
	if quotes == nil {
		quotes = repository.NewPostgresLaunchQuoteRepository(pgPool, secrets.LaunchpadCluster)
	}
	refresher := bootstrapMetricsRefresher(deps.Ctx, launchRepo, deps.MetricsRefresh)
	tradeRepo := repository.NewPostgresLaunchTradeRepository(pgPool)
	burnRepo := repository.NewPostgresLaunchBurnRepository(pgPool)
	tradeSvc := services.NewLaunchTradeService(tradeRepo, deps.QuoteUsd, deps.Clock, nil)
	agentMint := strings.TrimSpace(os.Getenv("AGENT_TOKEN_MINT"))
	bootstrapTradeIndexer(deps.Ctx, secrets, launchRepo, tradeRepo, burnRepo, quotes, agentMint)
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: launchRepo, Revenue: revenueRepo, Tokens: deps.Tokens, Accounts: deps.Accounts,
		Wallets: deps.Wallets, Credits: deps.Credits, Verifier: verifier, Clock: deps.Clock,
		Quotes: quotes, Metrics: deps.Metrics, Refresher: refresher, Trades: tradeSvc, Peers: deps.Peers,
		TreasuryAddress: treasury, PlatformID: os.Getenv("LAUNCHPAD_PLATFORM_ID"), MinFeeLamports: minFee,
		OnePerWallet: secrets.LaunchOnePerWallet,
		// TODO(pricing): Prices: a QuotePriceOracle (SOL/USD) so platform_revenue.amount_usd is filled.
	})
	launchSvc.SetReputationService(deps.Reputation)
	if deps.Announcer != nil {
		launchSvc.SetAnnouncer(deps.Announcer)
	}
	fmt.Printf("Launchpad: one launch per wallet=%v (LAUNCH_ONE_PER_WALLET)\n", secrets.LaunchOnePerWallet)
	revenueSvc := services.NewRevenueServiceWithConfig(revenueRepo, deps.Clock, services.RevenueConfig{
		TreasuryAddress:      treasury,
		BuybackWallet:        strings.TrimSpace(os.Getenv("BUYBACK_WALLET_ADDRESS")),
		TransferFeeAuthority: strings.TrimSpace(os.Getenv("TRANSFER_FEE_AUTHORITY_ADDRESS")),
		ExplorerBaseURL:      strings.TrimSpace(os.Getenv("SOLANA_EXPLORER_BASE_URL")),
		ExplorerCluster:      strings.TrimSpace(os.Getenv("SOLANA_EXPLORER_CLUSTER")),
	})

	out := launchpadHandlers{
		Launch:  api.NewLaunchHandler(launchSvc),
		Revenue: api.NewRevenueHandler(revenueSvc),
		Trades:  api.NewLaunchTradesHandler(tradeSvc, deps.Clock),
	}
	if agentMint != "" {
		agentCfg, err := loadAgentTokenConfig(agentMint)
		if err != nil {
			return out, err
		}
		out.AgentToken = api.NewAgentTokenHandler(services.NewAgentTokenServiceWithConfig(burnRepo, agentCfg, deps.Clock), deps.Clock)
		fmt.Printf("Launchpad: network token ledgers enabled for %s (GET /api/v1/agent-token/burnplan, /payouts); "+
			"supply=%v planPct=%v interval=%s start=%s amount=%v\n",
			agentMint, agentCfg.Supply, agentCfg.PlanTotalPct, agentCfg.BurnInterval, agentCfg.BurnStart.Format(time.RFC3339), agentCfg.BurnAmount)
	} else {
		fmt.Println("Launchpad: AGENT_TOKEN_MINT not set, GET /api/v1/agent-token/* disabled (404)")
	}
	if token := strings.TrimSpace(os.Getenv("INTERNAL_API_TOKEN")); token != "" {
		out.InternalRevenue = api.NewInternalRevenueHandler(revenueSvc, token)
		fmt.Println("Launchpad: internal revenue write enabled (POST /api/internal/revenue)")
	} else {
		fmt.Println("Launchpad: INTERNAL_API_TOKEN not set, POST /api/internal/revenue disabled (404)")
	}
	return out, nil
}

// bootstrapMetricsRefresher starts the background launch metrics refresher when the launchpad
// metrics source is configured. Returns a nil trigger (no-op for LaunchService) when disabled.
//
//	METRICS_REFRESH_INTERVAL     default 60s; "0" disables the refresher
//	METRICS_REFRESH_CONCURRENCY  default 4
func bootstrapMetricsRefresher(ctx context.Context, launches repository.LaunchRepository, metrics *services.MetricsService) services.LaunchMetricsTrigger {
	if ctx == nil || launches == nil || metrics == nil || !metrics.LaunchLabEnabled() {
		fmt.Println("Launchpad: metrics refresher disabled (launchpad metrics source not configured)")
		return nil
	}
	interval := services.DefaultMetricsRefreshInterval
	if v := strings.TrimSpace(os.Getenv("METRICS_REFRESH_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			if secs, serr := strconv.Atoi(v); serr == nil {
				d, err = time.Duration(secs)*time.Second, nil
			}
		}
		switch {
		case err != nil:
			log.Printf("WARNING: invalid METRICS_REFRESH_INTERVAL=%q, using %s", v, interval)
		case d <= 0:
			fmt.Println("Launchpad: metrics refresher disabled (METRICS_REFRESH_INTERVAL=0)")
			return nil
		default:
			interval = d
		}
	}
	concurrency := services.DefaultMetricsRefreshConcurrency
	if v := strings.TrimSpace(os.Getenv("METRICS_REFRESH_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		} else {
			log.Printf("WARNING: invalid METRICS_REFRESH_CONCURRENCY=%q, using %d", v, concurrency)
		}
	}
	r := services.NewLaunchMetricsRefresher(launches, metrics, services.LaunchMetricsRefresherConfig{
		Interval: interval, Concurrency: concurrency, MintTimeout: services.DefaultMetricsRefreshMintTimeout,
	})
	go r.Run(ctx)
	fmt.Printf("Launchpad: metrics refresher every %s, concurrency %d, per-mint timeout %s\n",
		interval, concurrency, services.DefaultMetricsRefreshMintTimeout)
	return r
}

// bootstrapTradeIndexer starts the background trade (and burn) indexer over the real RPC.
//
//	INDEXER_ENABLED   default true; "false" disables the poller (endpoints keep serving stored rows)
//	INDEXER_INTERVAL  default 30s (Go duration or seconds)
//	AGENT_TOKEN_MINT  when set, burns of that mint are indexed as well
func bootstrapTradeIndexer(ctx context.Context, secrets *trackerSecrets, launches repository.LaunchRepository,
	trades repository.LaunchTradeRepository, burns repository.LaunchBurnRepository, quotes repository.LaunchQuoteRepository, agentMint string) {
	enabled, err := envBool("INDEXER_ENABLED", true)
	if err != nil {
		log.Printf("WARNING: %v, indexer enabled", err)
		enabled = true
	}
	switch {
	case !enabled:
		fmt.Println("Launchpad: trade indexer disabled (INDEXER_ENABLED=false)")
		return
	case secrets.UseSolanaStubs:
		fmt.Println("Launchpad: trade indexer disabled (SOLANA_USE_STUBS=true, no RPC)")
		return
	case ctx == nil || secrets.SolanaRPCURL == "":
		fmt.Println("Launchpad: trade indexer disabled (no SOLANA_RPC_URL)")
		return
	}
	interval := services.DefaultIndexerInterval
	if v := strings.TrimSpace(os.Getenv("INDEXER_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			if secs, serr := strconv.Atoi(v); serr == nil {
				d, err = time.Duration(secs)*time.Second, nil
			}
		}
		if err != nil || d <= 0 {
			log.Printf("WARNING: invalid INDEXER_INTERVAL=%q, using %s", v, interval)
		} else {
			interval = d
		}
	}
	rpc := solana.NewClient(secrets.SolanaRPCURL)
	indexer := services.NewTradeIndexer(services.TradeIndexerDeps{
		Launches: launches, Trades: trades, Burns: burns, Quotes: quotes,
		Source: solana.NewTradeSource(rpc),
		Vaults: services.NewAccountPoolVaultSource(launchLabAccountReader{rpc: rpc}),
	}, services.TradeIndexerConfig{Interval: interval, AgentMint: agentMint})
	go indexer.Run(ctx)
	fmt.Printf("Launchpad: trade indexer every %s over %s (max %d pools/tick, burns for %q)\n",
		interval, secrets.SolanaRPCURL, services.DefaultIndexerMaxPools, agentMint)
}
