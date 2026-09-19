// Package: main
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Wires launch config repos, price feed, service, handler and the fee pricing job.

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/api"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	launchFeeJobName     = "launch-fee-price"
	launchFeeJobInterval = 30 * time.Second
	launchFeeBootTimeout = 10 * time.Second
)

// bootstrapLaunchConfig builds the launch config service and handler, prices the fee once at boot
// and schedules the 5-minute price refresh job. Uses the stub price feed when Solana stubs are on.
func bootstrapLaunchConfig(ctx context.Context, pgPool *pgxpool.Pool, secrets *trackerSecrets, clk clock.Clock) *api.LaunchConfigHandler {
	var feed services.PriceFeed
	if secrets.UseSolanaStubs {
		feed = services.NewStubPriceFeed()
		fmt.Println("Launchpad price feed: dev stub (SOLANA_USE_STUBS=true)")
	} else {
		feed = services.NewJupiterPriceFeed(secrets.PriceFeedURL)
		fmt.Printf("Launchpad price feed: Jupiter (%s)\n", secrets.PriceFeedURL)
	}

	services.PinLaunchQuote(secrets.LaunchQuoteMint)
	if secrets.LaunchQuoteMint != "" {
		fmt.Println("Launchpad quote pinned to " + secrets.LaunchQuoteMint)
	}
	if secrets.LaunchRaiseUnits > 0 {
		fmt.Println("Launchpad raise fixed at", secrets.LaunchRaiseUnits, "quote tokens")
	}
	svc := services.NewLaunchConfigService(services.LaunchConfigServiceDeps{
		Settings: repository.NewPostgresLaunchSettingsRepository(pgPool),
		Quotes:   repository.NewPostgresLaunchQuoteRepository(pgPool, secrets.LaunchpadCluster),
		Prices:   feed,
		Clock:    clk,
		Config: services.LaunchConfigSettings{
			Cluster:        secrets.LaunchpadCluster,
			ProgramID:      secrets.LaunchpadProgramID,
			PlatformID:     secrets.LaunchpadPlatformID,
			Treasury:       secrets.LaunchTreasuryAddress,
			TransferFeeBps: secrets.LaunchTransferFeeBps,
			FeeUSD:         secrets.LaunchFeeUSD,
			QuoteMint:      secrets.LaunchQuoteMint,
			RaiseUnits:     secrets.LaunchRaiseUnits,
			MinFeeLamports: secrets.LaunchFeeMinLamports,
			MaxFeeLamports: secrets.LaunchFeeMaxLamports,
		},
	})

	refresh := func() error {
		jobCtx, cancel := context.WithTimeout(ctx, launchFeeBootTimeout)
		defer cancel()
		fee, err := svc.RefreshFee(jobCtx)
		if err != nil {
			return err
		}
		log.Printf("[%s] fee=%d lamports (%.2f USD @ %.2f SOL/USD, source=%s)", launchFeeJobName, fee.FeeLamports, fee.FeeUSD, fee.SolUSD, fee.Source)
		return nil
	}
	// Price once at boot so the endpoint is live immediately; on failure the last persisted value
	// keeps serving (flagged stale after 15 minutes) until the job succeeds.
	if err := refresh(); err != nil {
		log.Printf("[%s] boot pricing failed, serving last persisted fee: %v", launchFeeJobName, err)
	}
	startJob(ctx, launchFeeJobName, launchFeeJobInterval, refresh)

	if secrets.LaunchpadPlatformID == "" {
		fmt.Println("Launchpad: LAUNCHPAD_PLATFORM_ID not set (dev only; required in production)")
	}
	return api.NewLaunchConfigHandler(svc)
}
