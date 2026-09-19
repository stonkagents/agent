// Package: main
// Purpose: Community board phase 1 wiring: board reputation (repo, service, nightly recompute
//          at 02:00 UTC), reports, watches, @mentions, platform peers and token offer payments
//          (launch lookup, linked wallets, on-chain transfer verifier). Phase 2 wiring below:
//          token rooms (holder checks), request routing and the room digest.

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
	"github.com/stonkagents/agent/tracker/internal/solana"
)

// boardPhase1Deps are the shared repositories the board phase 1 wiring needs.
type boardPhase1Deps struct {
	Forum    repository.ForumRepository
	Peers    repository.PeerRepository
	Launches repository.LaunchRepository
	Wallets  repository.WalletRepository
	Clock    clock.Clock
}

// bootstrapBoardPhase1 wires the phase 1 dependencies into forumSvc and returns the
// reputation service (for the nightly job).
func bootstrapBoardPhase1(secrets *trackerSecrets, pgPool *pgxpool.Pool, forumSvc *services.ForumService, deps boardPhase1Deps) *services.ReputationService {
	reportRepo := repository.NewPostgresBoardReportRepository(pgPool)
	reputationSvc := services.NewReputationService(repository.NewPostgresBoardReputationRepository(pgPool), deps.Forum, reportRepo, deps.Clock)
	forumSvc.SetReputationService(reputationSvc)
	forumSvc.SetReportRepo(reportRepo)
	forumSvc.SetWatchRepo(repository.NewPostgresBoardWatchRepository(pgPool))
	forumSvc.SetPeerRepo(deps.Peers)
	forumSvc.SetPlatformPeers(secrets.PlatformPeerIDs)

	var verifier services.TokenTransferVerifier
	if secrets.UseSolanaStubs {
		verifier = &services.StubTokenTransferVerifier{}
		fmt.Println("Board token offers: stub transfer verifier (SOLANA_USE_STUBS=true)")
	} else {
		verifier = solana.NewTokenTransferVerifier(solana.NewClient(secrets.SolanaRPCURL))
		fmt.Printf("Board token offers: on-chain transfer verifier (%s)\n", secrets.SolanaRPCURL)
	}
	forumSvc.SetTokenOfferDeps(deps.Launches, deps.Wallets, repository.NewPostgresTokenOfferPaymentRepository(pgPool), verifier)
	fmt.Printf("Board phase 1: platform peers=%d (PLATFORM_PEER_IDS), nightly reputation recompute at %02d:00 UTC\n",
		len(secrets.PlatformPeerIDs), services.ReputationNightlyHourUTC)
	return reputationSvc
}

// startReputationNightly runs the full reputation recompute at ReputationNightlyHourUTC every
// day (first run at the next occurrence after start).
func startReputationNightly(ctx context.Context, reputationSvc *services.ReputationService, clk clock.Clock) {
	go func() {
		for {
			wait := services.NextNightlyRun(clk.Now()).Sub(clk.Now())
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			n, err := reputationSvc.RecomputeAll(ctx)
			if err != nil {
				log.Printf("[reputation-nightly] error: %v", err)
			}
			log.Printf("[reputation-nightly] recomputed %d peers", n)
		}
	}()
}

// boardPhase2Deps are what the board phase 2 wiring (rooms and matchmaking) needs.
type boardPhase2Deps struct {
	// Metrics supplies cached holder counts (members_estimate, digest new_holders).
	Metrics services.RoomMetricsProvider
	// Presence answers "online now" for request routing.
	Presence presence.PresenceStore
	// Quotes names the quote token in the launch announcement.
	Quotes repository.LaunchQuoteRepository
}

// bootstrapBoardPhase2 wires token rooms (holder checks from chain, or the stub that lets
// every linked wallet post when SOLANA_USE_STUBS=true), request routing (autopilot
// categories, presence) and the digest snapshots into forumSvc. Returns the autopilot
// repository for the heartbeat handler.
func bootstrapBoardPhase2(secrets *trackerSecrets, pgPool *pgxpool.Pool, forumSvc *services.ForumService, deps boardPhase2Deps) repository.PeerAutopilotRepository {
	autopilotRepo := repository.NewPostgresPeerAutopilotRepository(pgPool)
	var holders services.TokenHolderChecker
	if secrets.UseSolanaStubs {
		holders = &services.StubTokenHolderChecker{HoldAll: true}
		fmt.Println("Board rooms: stub holder checker, every linked wallet holds every mint (SOLANA_USE_STUBS=true)")
	} else {
		holders = solana.NewClient(secrets.SolanaRPCURL)
		fmt.Printf("Board rooms: on-chain holder checks (%s), cached %s per wallet and mint\n", secrets.SolanaRPCURL, services.RoomHolderCacheTTL)
	}
	forumSvc.SetRoomDeps(services.RoomDeps{
		Holders: holders, Metrics: deps.Metrics, Snapshots: repository.NewPostgresRoomHolderSnapshotRepository(pgPool),
		Autopilot: autopilotRepo, Presence: deps.Presence, Quotes: deps.Quotes, RaiseUnits: secrets.LaunchRaiseUnits,
		PortalURL: secrets.PortalURL,
	})
	if secrets.PortalURL != "" {
		fmt.Printf("Board rooms: launch announcements link to %s/tokens/<mint> (PORTAL_URL)\n", secrets.PortalURL)
	}
	fmt.Printf("Board phase 2: request routing to the top %d peers with score >= %d, rooms digest snapshots on read\n",
		services.RoutingTopN, services.RoutingMinScore)
	return autopilotRepo
}

// bootstrapBoardRound2 wires the round 2 repositories (edit history, room controls, visits,
// notification preferences) and settings into forumSvc, and the sock puppet guard
// into the reputation service.
func bootstrapBoardRound2(secrets *trackerSecrets, pgPool *pgxpool.Pool, forumSvc *services.ForumService, reputationSvc *services.ReputationService) {
	forumSvc.SetRound2(services.Round2Deps{
		Edits:  repository.NewPostgresBoardEditHistoryRepository(pgPool),
		Rooms:  repository.NewPostgresBoardRoomRepository(pgPool),
		Visits: repository.NewPostgresBoardVisitRepository(pgPool),
		Prefs:  repository.NewPostgresBoardNotificationPrefRepository(pgPool),
	}, services.Round2Settings{
		EditWindow:                 secrets.BoardEditWindow,
		MaxUpvotesPerPeerPerDay:    secrets.BoardMaxUpvotesPerDay,
		AutopilotMaxRepliesPerRoom: secrets.AutopilotMaxRepliesPerRoom,
		AutopilotPaused:            secrets.AutopilotPaused,
		MinBountyAmount:            secrets.BountyMinAmount,
		MaxBountyAmountOverride:    secrets.BountyMaxAmount,
	})
	reputationSvc.MinVoterAge = secrets.ReputationMinVoterAge
	min, max := forumSvc.BountyLimits()
	fmt.Printf("Board round 2: edit window %s (BOARD_EDIT_WINDOW_MINUTES), %d upvotes per peer per day (BOARD_MAX_UPVOTES_PER_DAY), "+
		"%d auto replies per room per day (AUTOPILOT_MAX_REPLIES_PER_ROOM_PER_DAY), autopilot paused=%v (AUTOPILOT_PAUSED), "+
		"bounty %d..%d credits (BOUNTY_MIN_AMOUNT, BOUNTY_MAX_AMOUNT), voter age %s (REPUTATION_MIN_VOTER_AGE_DAYS)\n",
		secrets.BoardEditWindow, secrets.BoardMaxUpvotesPerDay, secrets.AutopilotMaxRepliesPerRoom, secrets.AutopilotPaused,
		min, max, secrets.ReputationMinVoterAge)
}
