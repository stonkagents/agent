// Package: main
// Feature: StonkAgents devnet drip
// Purpose: Wires POST /api/dev/drip — test SOL + test $STONK for wallets connecting to the dev
//          portal. Enabled only when the drip wallet key is present AND the launchpad cluster
//          is devnet; the tracker refuses to start with the key set on any other cluster.
//
// Env read here:
//
//	DEV_DRIP_SECRET_KEY    base58 64-byte ed25519 secret key of the drip wallet (AWS Secrets
//	                       Manager); absent = feature off, route unregistered (404). Never logged.
//	DEV_DRIP_SOL           SOL per drip, default 0.05
//	DEV_DRIP_STONK         $STONK per drip in whole units, default 25
//	DEV_DRIP_MINT          token mint, default the devnet $STONK stand-in (classic SPL Token)
//	DEV_DRIP_MINT_DECIMALS decimals of DEV_DRIP_MINT, default 6
//	SOLANA_EXPLORER_BASE_URL  optional; explorer links in the response (default https://solscan.io)

package main

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mr-tron/base58"
	"github.com/stonkagents/agent/tracker/internal/api"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
	"github.com/stonkagents/agent/tracker/internal/solana"
	"github.com/stonkagents/agent/tracker/internal/solana/tx"
)

// devDripSettings is the parsed drip configuration (key excluded).
type devDripSettings struct {
	SolLamports uint64
	StonkRaw    uint64
	Mint        tx.Pubkey
	Decimals    uint8
}

// loadDevDripSettings parses the amount env vars.
func loadDevDripSettings() (devDripSettings, error) {
	var s devDripSettings
	sol, err := envFloat("DEV_DRIP_SOL", services.DefaultDevDripSol)
	if err != nil {
		return s, err
	}
	stonk, err := envFloat("DEV_DRIP_STONK", services.DefaultDevDripStonk)
	if err != nil {
		return s, err
	}
	decimals, err := envInt64("DEV_DRIP_MINT_DECIMALS", services.DefaultDevDripMintDecimals)
	if err != nil {
		return s, err
	}
	if sol <= 0 || sol > 5 {
		return s, fmt.Errorf("DEV_DRIP_SOL must be within (0, 5], got %v", sol)
	}
	if stonk <= 0 {
		return s, fmt.Errorf("DEV_DRIP_STONK must be > 0, got %v", stonk)
	}
	if decimals < 0 || decimals > 18 {
		return s, fmt.Errorf("DEV_DRIP_MINT_DECIMALS must be within [0, 18], got %d", decimals)
	}
	mintStr := strings.TrimSpace(os.Getenv("DEV_DRIP_MINT"))
	if mintStr == "" {
		mintStr = services.DefaultDevDripMint
	}
	mint, err := tx.PubkeyFromBase58(mintStr)
	if err != nil {
		return s, fmt.Errorf("DEV_DRIP_MINT: %w", err)
	}
	s.SolLamports = uint64(math.Round(sol * 1_000_000_000))
	s.StonkRaw = uint64(math.Round(stonk * math.Pow10(int(decimals))))
	s.Mint = mint
	s.Decimals = uint8(decimals)
	return s, nil
}

// parseDevDripKey decodes a base58 64-byte ed25519 secret key.
func parseDevDripKey(raw string) (ed25519.PrivateKey, error) {
	b, err := base58.Decode(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("DEV_DRIP_SECRET_KEY is not base58")
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("DEV_DRIP_SECRET_KEY must decode to %d bytes, got %d", ed25519.PrivateKeySize, len(b))
	}
	return ed25519.PrivateKey(b), nil
}

// bootstrapDevDrip returns the drip handler, or nil when the feature is off. It returns an
// error (fail-fast) when the key is set on a non-devnet launchpad.
func bootstrapDevDrip(secrets *trackerSecrets, pgPool *pgxpool.Pool, clk clock.Clock) (*api.DevDripHandler, error) {
	raw := os.Getenv("DEV_DRIP_SECRET_KEY")
	if strings.TrimSpace(raw) == "" {
		fmt.Println("Dev drip: DEV_DRIP_SECRET_KEY not set, POST /api/dev/drip disabled (404)")
		return nil, nil
	}
	if secrets.LaunchpadCluster != models.LaunchClusterDevnet {
		return nil, fmt.Errorf("DEV_DRIP_SECRET_KEY is set but the launchpad cluster is %q; the dev drip runs on devnet only; unset DEV_DRIP_SECRET_KEY or set LAUNCHPAD_CLUSTER=devnet", secrets.LaunchpadCluster)
	}
	if secrets.UseSolanaStubs {
		fmt.Println("Dev drip: disabled (SOLANA_USE_STUBS=true, no RPC)")
		return nil, nil
	}
	key, err := parseDevDripKey(raw)
	if err != nil {
		return nil, err
	}
	settings, err := loadDevDripSettings()
	if err != nil {
		return nil, err
	}
	explorer := strings.TrimSpace(os.Getenv("SOLANA_EXPLORER_BASE_URL"))
	if explorer == "" {
		explorer = "https://solscan.io"
	}
	svc, err := services.NewDevDripService(services.DevDripDeps{
		Repo:   repository.NewPostgresDevDripRepository(pgPool),
		Chain:  solana.NewDripChain(solana.NewClient(secrets.SolanaRPCURL)),
		Key:    key,
		Clock:  clk,
		Logger: slog.Default(),
		Config: services.DevDripConfig{
			SolLamports: settings.SolLamports, StonkRaw: settings.StonkRaw, Mint: settings.Mint, Decimals: settings.Decimals,
			TokenProgram: tx.TokenProgramID, ExplorerBaseURL: explorer, ExplorerClusterSuffix: "?cluster=devnet",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("dev drip: %w", err)
	}
	fmt.Printf("Dev drip: enabled on devnet from %s, %v SOL + %v $STONK (%s) per wallet per 24h (POST /api/dev/drip)\n",
		svc.Wallet(), services.SolFloat(settings.SolLamports), services.TokenFloat(settings.StonkRaw, settings.Decimals), settings.Mint)
	return api.NewDevDripHandler(svc, slog.Default()), nil
}
