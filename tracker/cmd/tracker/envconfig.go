// Package: main
// Feature: F-013 (Credits & Identity)
// Story: TD-007 (Externalize hardcoded secrets)
// Purpose: Loads tracker secrets from env vars with dev fallbacks and production fail-fast

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	devJWTSecret       = "stonkagents-spend-token-dev-secret"
	devCallbackSecret  = "dev-callback-secret"
	devTreasuryAddress = "DevTreasuryAddress11111111111111111111111"

	// StonkAgents launchpad (Raydium LaunchLab) defaults.
	defaultLaunchpadProgramID   = "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj"
	defaultLaunchFeeUSD         = 0.50
	defaultLaunchFeeMinLamports = int64(4_000_000)
	defaultLaunchFeeMaxLamports = int64(10_000_000)
	defaultPriceFeedURL         = "https://lite-api.jup.ag/price/v3"
	defaultLaunchTransferFeeBps = 100
)

type trackerSecrets struct {
	JWTSigningSecret      string
	SocialCallbackSecret  string
	SolanaTreasuryAddress string
	SolanaRPCURL          string
	UseSolanaStubs        bool
	Env                   string
	// PumpFunAPIURL is the pump.fun API base (e.g. mainnet or devnet). Empty = mainnet. Set PUMP_FUN_API_URL or auto-set when SOLANA_RPC_URL contains "devnet".
	PumpFunAPIURL string

	// StonkAgents launchpad (Raydium LaunchLab).
	// LaunchpadPlatformID is our LaunchLab PlatformConfig pubkey (LAUNCHPAD_PLATFORM_ID). Empty in dev; required in production/staging.
	LaunchpadPlatformID string
	// LaunchpadProgramID is the LaunchLab program (LAUNCHPAD_PROGRAM_ID).
	LaunchpadProgramID string
	// LaunchpadCluster is the Solana cluster the launchpad runs on: LAUNCHPAD_CLUSTER (mainnet|devnet),
	// derived from LaunchpadProgramID when unset (the devnet LaunchLab program means devnet).
	LaunchpadCluster string
	// LaunchTreasuryAddress receives the launch fee (LAUNCH_TREASURY_ADDRESS, falls back to SolanaTreasuryAddress).
	LaunchTreasuryAddress string
	// LaunchFeeUSD is the USD-pegged launch fee (LAUNCH_FEE_USD).
	LaunchFeeUSD float64
	// LaunchQuoteMint pins the single launchable quote (LAUNCH_QUOTE_MINT); empty = the
	// cluster's $STONK quote (mainnet mint or the devnet stand-in). Staging launches against
	// $KNOTS this way.
	LaunchQuoteMint string
	// LaunchRaiseUnits fixes the graduation raise in whole quote tokens (LAUNCH_RAISE_UNITS);
	// 0 = size the raise so it is worth the default 85 SOL.
	LaunchRaiseUnits float64
	// LaunchFeeMinLamports / LaunchFeeMaxLamports clamp the SOL-denominated fee (LAUNCH_FEE_MIN_LAMPORTS / LAUNCH_FEE_MAX_LAMPORTS).
	LaunchFeeMinLamports int64
	LaunchFeeMaxLamports int64
	// PriceFeedURL is the Jupiter price API base (PRICE_FEED_URL).
	PriceFeedURL string
	// LaunchTransferFeeBps is the Token-2022 transfer fee for reward coins (LAUNCH_TRANSFER_FEE_BPS).
	LaunchTransferFeeBps int
	// LaunchOnePerWallet rejects a second recorded launch from the same creator wallet
	// (LAUNCH_ONE_PER_WALLET, boolean, default true).
	LaunchOnePerWallet bool

	// AutopilotMaxRepliesPerDay caps Agent Autopilot replies per peer per UTC day
	// (AUTOPILOT_MAX_REPLIES_PER_DAY, default 10, 0 = no daily cap).
	AutopilotMaxRepliesPerDay int

	// PlatformPeerIDs are the peers allowed to pin, hide and resolve reports on the community
	// board (PLATFORM_PEER_IDS, comma separated; empty = nobody).
	PlatformPeerIDs []string

	// PortalURL is the portal origin (PORTAL_URL, e.g. https://dev.stonkagents.com, no trailing
	// slash; empty = unset). The room launch announcement adds the absolute token page URL with it.
	PortalURL string

	// Board round 2.
	// BoardEditWindow is how long after creation an author may edit (BOARD_EDIT_WINDOW_MINUTES, default 30).
	BoardEditWindow time.Duration
	// BoardMaxUpvotesPerDay caps the upvotes one peer gives per UTC day (BOARD_MAX_UPVOTES_PER_DAY, default 50, -1 = no cap).
	BoardMaxUpvotesPerDay int
	// AutopilotMaxRepliesPerRoom caps one peer's auto replies per room per UTC day (AUTOPILOT_MAX_REPLIES_PER_ROOM_PER_DAY, default 5, -1 = no cap).
	AutopilotMaxRepliesPerRoom int
	// AutopilotPaused is the global circuit breaker: every auto write answers 503 AUTOPILOT_PAUSED (AUTOPILOT_PAUSED, default false).
	AutopilotPaused bool
	// BountyMinAmount / BountyMaxAmount bound a bounty in credits (BOUNTY_MIN_AMOUNT default 1, BOUNTY_MAX_AMOUNT default 10000).
	BountyMinAmount int
	BountyMaxAmount int
	// ReputationMinVoterAge is how old a peer must be for its upvotes and accepts to count
	// (REPUTATION_MIN_VOTER_AGE_DAYS, default 3, 0 = every event counts).
	ReputationMinVoterAge time.Duration
}

// loadBoardRound2Settings reads the round 2 env vars.
func loadBoardRound2Settings(s *trackerSecrets) error {
	editMinutes, err := envInt64("BOARD_EDIT_WINDOW_MINUTES", int64(services.DefaultEditWindow/time.Minute))
	if err != nil {
		return err
	}
	if editMinutes < 1 {
		return fmt.Errorf("BOARD_EDIT_WINDOW_MINUTES must be >= 1, got %d", editMinutes)
	}
	s.BoardEditWindow = time.Duration(editMinutes) * time.Minute
	upvotes, err := envInt64("BOARD_MAX_UPVOTES_PER_DAY", services.DefaultMaxUpvotesPerPeerPerDay)
	if err != nil {
		return err
	}
	if upvotes == 0 || upvotes < -1 {
		return fmt.Errorf("BOARD_MAX_UPVOTES_PER_DAY must be >= 1 or -1 (no cap), got %d", upvotes)
	}
	s.BoardMaxUpvotesPerDay = int(upvotes)
	perRoom, err := envInt64("AUTOPILOT_MAX_REPLIES_PER_ROOM_PER_DAY", services.DefaultAutopilotMaxRepliesPerRoomPerDay)
	if err != nil {
		return err
	}
	if perRoom == 0 || perRoom < -1 {
		return fmt.Errorf("AUTOPILOT_MAX_REPLIES_PER_ROOM_PER_DAY must be >= 1 or -1 (no cap), got %d", perRoom)
	}
	s.AutopilotMaxRepliesPerRoom = int(perRoom)
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTOPILOT_PAUSED"))) {
	case "", "0", "false", "no", "off":
		s.AutopilotPaused = false
	case "1", "true", "yes", "on":
		s.AutopilotPaused = true
	default:
		return fmt.Errorf("AUTOPILOT_PAUSED must be a boolean, got %q", os.Getenv("AUTOPILOT_PAUSED"))
	}
	minB, err := envInt64("BOUNTY_MIN_AMOUNT", services.DefaultMinBountyAmount)
	if err != nil {
		return err
	}
	maxB, err := envInt64("BOUNTY_MAX_AMOUNT", services.MaxBountyAmount)
	if err != nil {
		return err
	}
	if minB < 1 || maxB < minB {
		return fmt.Errorf("BOUNTY_MIN_AMOUNT / BOUNTY_MAX_AMOUNT must satisfy 1 <= min <= max, got %d / %d", minB, maxB)
	}
	s.BountyMinAmount, s.BountyMaxAmount = int(minB), int(maxB)
	voterDays, err := envInt64("REPUTATION_MIN_VOTER_AGE_DAYS", int64(services.DefaultReputationMinVoterAge/(24*time.Hour)))
	if err != nil {
		return err
	}
	if voterDays < 0 {
		return fmt.Errorf("REPUTATION_MIN_VOTER_AGE_DAYS must be >= 0, got %d", voterDays)
	}
	s.ReputationMinVoterAge = time.Duration(voterDays) * 24 * time.Hour
	return nil
}

// loadBoardSettings reads PLATFORM_PEER_IDS (comma separated peer ids, blanks ignored) and
// PORTAL_URL (trailing slash dropped).
func loadBoardSettings(s *trackerSecrets) {
	s.PortalURL = strings.TrimRight(strings.TrimSpace(os.Getenv("PORTAL_URL")), "/")
	s.PlatformPeerIDs = nil
	for _, id := range strings.Split(os.Getenv("PLATFORM_PEER_IDS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			s.PlatformPeerIDs = append(s.PlatformPeerIDs, id)
		}
	}
}

// loadAutopilotSettings reads AUTOPILOT_MAX_REPLIES_PER_DAY (default 10; 0 disables the daily cap).
func loadAutopilotSettings(s *trackerSecrets) error {
	n, err := envInt64("AUTOPILOT_MAX_REPLIES_PER_DAY", int64(services.DefaultAutopilotMaxRepliesPerDay))
	if err != nil {
		return err
	}
	if n < 0 {
		return fmt.Errorf("AUTOPILOT_MAX_REPLIES_PER_DAY must be >= 0, got %d", n)
	}
	s.AutopilotMaxRepliesPerDay = int(n)
	return nil
}

// loadLaunchpadSettings reads the launchpad env vars into secrets. Production/staging require LAUNCHPAD_PLATFORM_ID.
func loadLaunchpadSettings(s *trackerSecrets) error {
	var err error
	s.LaunchpadPlatformID = strings.TrimSpace(os.Getenv("LAUNCHPAD_PLATFORM_ID"))
	if s.LaunchpadProgramID = strings.TrimSpace(os.Getenv("LAUNCHPAD_PROGRAM_ID")); s.LaunchpadProgramID == "" {
		s.LaunchpadProgramID = defaultLaunchpadProgramID
	}
	if v := strings.TrimSpace(os.Getenv("LAUNCHPAD_CLUSTER")); v != "" && !models.IsValidLaunchCluster(strings.ToLower(v)) {
		return fmt.Errorf("LAUNCHPAD_CLUSTER=%q must be mainnet or devnet", v)
	}
	s.LaunchpadCluster = services.ResolveLaunchCluster(os.Getenv("LAUNCHPAD_CLUSTER"), s.LaunchpadProgramID)
	if s.LaunchTreasuryAddress = strings.TrimSpace(os.Getenv("LAUNCH_TREASURY_ADDRESS")); s.LaunchTreasuryAddress == "" {
		s.LaunchTreasuryAddress = s.SolanaTreasuryAddress
	}
	if s.LaunchFeeUSD, err = envFloat("LAUNCH_FEE_USD", defaultLaunchFeeUSD); err != nil {
		return err
	}
	s.LaunchQuoteMint = strings.TrimSpace(os.Getenv("LAUNCH_QUOTE_MINT"))
	if s.LaunchRaiseUnits, err = envFloat("LAUNCH_RAISE_UNITS", 0); err != nil {
		return err
	}
	if s.LaunchRaiseUnits < 0 {
		return fmt.Errorf("LAUNCH_RAISE_UNITS must be >= 0, got %v", s.LaunchRaiseUnits)
	}
	if s.LaunchFeeMinLamports, err = envInt64("LAUNCH_FEE_MIN_LAMPORTS", defaultLaunchFeeMinLamports); err != nil {
		return err
	}
	if s.LaunchFeeMaxLamports, err = envInt64("LAUNCH_FEE_MAX_LAMPORTS", defaultLaunchFeeMaxLamports); err != nil {
		return err
	}
	if s.PriceFeedURL = strings.TrimSpace(os.Getenv("PRICE_FEED_URL")); s.PriceFeedURL == "" {
		s.PriceFeedURL = defaultPriceFeedURL
	}
	bps, err := envInt64("LAUNCH_TRANSFER_FEE_BPS", int64(defaultLaunchTransferFeeBps))
	if err != nil {
		return err
	}
	s.LaunchTransferFeeBps = int(bps)
	if s.LaunchOnePerWallet, err = envBool("LAUNCH_ONE_PER_WALLET", true); err != nil {
		return err
	}

	if s.LaunchFeeUSD <= 0 {
		return fmt.Errorf("LAUNCH_FEE_USD must be > 0")
	}
	if s.LaunchFeeMinLamports <= 0 || s.LaunchFeeMaxLamports < s.LaunchFeeMinLamports {
		return fmt.Errorf("LAUNCH_FEE_MIN_LAMPORTS must be > 0 and <= LAUNCH_FEE_MAX_LAMPORTS")
	}
	if s.LaunchTransferFeeBps < 0 || s.LaunchTransferFeeBps > 10_000 {
		return fmt.Errorf("LAUNCH_TRANSFER_FEE_BPS must be within [0, 10000]")
	}
	if (s.Env == "production" || s.Env == "staging") && s.LaunchpadPlatformID == "" {
		return fmt.Errorf("STONKAGENTS_ENV=%s requires LAUNCHPAD_PLATFORM_ID", s.Env)
	}
	return nil
}

func envFloat(key string, def float64) (float64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return f, nil
}

// envBool parses key with strconv.ParseBool (1/t/true/0/f/false, any case); unset = def.
func envBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return b, nil
}

func envInt64(key string, def int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}

func loadTrackerSecrets() (*trackerSecrets, error) {
	env := os.Getenv("STONKAGENTS_ENV")
	if env == "" {
		env = "development"
	}

	jwtSecret := os.Getenv("JWT_SIGNING_SECRET")
	if jwtSecret == "" {
		jwtSecret = devJWTSecret
	}

	callbackSecret := os.Getenv("SOCIAL_CALLBACK_SECRET")
	if callbackSecret == "" {
		callbackSecret = devCallbackSecret
	}

	treasuryAddress := os.Getenv("SOLANA_TREASURY_ADDRESS")
	if treasuryAddress == "" {
		treasuryAddress = devTreasuryAddress
	}

	// Strict parsing for SOLANA_USE_STUBS:
	// - "true"  → stubs enabled (explicit opt-in)
	// - "false" → stubs disabled (explicit opt-out, requires SOLANA_RPC_URL)
	// - unset   → stubs enabled in development, caught by prod check below
	// Values like "1", "yes", "TRUE" are treated as unset (not recognized).
	stubsEnv := os.Getenv("SOLANA_USE_STUBS")
	var useStubs bool
	switch stubsEnv {
	case "true":
		useStubs = true
	case "false":
		useStubs = false
	default:
		useStubs = (env == "development")
	}

	solanaRPCURL := os.Getenv("SOLANA_RPC_URL")

	// Deployed environments (production, staging, dev) must use real on-chain
	// verification. Stubs are only acceptable for local development where the
	// real Solana RPC may be unreachable. tracker.dev.stonkagents.com runs as
	// STONKAGENTS_ENV=dev and serves real users, so it gets the same guards as
	// staging/production minus the secret-strength checks.
	deployed := env == "production" || env == "staging" || env == "dev"
	if deployed {
		var missing []string
		if env == "production" || env == "staging" {
			if jwtSecret == devJWTSecret {
				missing = append(missing, "JWT_SIGNING_SECRET")
			}
			if callbackSecret == devCallbackSecret {
				missing = append(missing, "SOCIAL_CALLBACK_SECRET")
			}
			if treasuryAddress == devTreasuryAddress {
				missing = append(missing, "SOLANA_TREASURY_ADDRESS")
			}
		}
		if useStubs {
			missing = append(missing, "SOLANA_USE_STUBS must not be 'true' in "+env)
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("STONKAGENTS_ENV=%s requires real values for: %s", env, strings.Join(missing, ", "))
		}
	}

	// When stubs are disabled, SOLANA_RPC_URL is required.
	if !useStubs && solanaRPCURL == "" {
		return nil, fmt.Errorf("SOLANA_RPC_URL is required when SOLANA_USE_STUBS is not 'true'")
	}

	// Pump.fun API: use PUMP_FUN_API_URL if set; otherwise mainnet (bootstrap defaults to mainnet).
	// Do not auto-set testnet URL: frontend-api-v3.testnetpump.fun does not resolve. Use mainnet for token metrics when on devnet RPC.
	pumpFunAPIURL := os.Getenv("PUMP_FUN_API_URL")

	secrets := &trackerSecrets{
		JWTSigningSecret:      jwtSecret,
		SocialCallbackSecret:  callbackSecret,
		SolanaTreasuryAddress: treasuryAddress,
		SolanaRPCURL:          solanaRPCURL,
		UseSolanaStubs:        useStubs,
		Env:                   env,
		PumpFunAPIURL:         pumpFunAPIURL,
	}
	if err := loadLaunchpadSettings(secrets); err != nil {
		return nil, err
	}
	if err := loadAutopilotSettings(secrets); err != nil {
		return nil, err
	}
	loadBoardSettings(secrets)
	if err := loadBoardRound2Settings(secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// --- StonkAgents launchpad: server-side token metadata (workstream D) ---

const (
	metadataUploaderPinata = "pinata"
	metadataUploaderFake   = "fake"
	// defaultMetadataMaxImageBytes is 2 MiB (METADATA_MAX_IMAGE_BYTES).
	defaultMetadataMaxImageBytes int64 = 2097152
	defaultMetadataGatewayURL          = "https://gateway.pinata.cloud/ipfs/"
)

// metadataConfig holds the IPFS upload settings for POST /api/launch/metadata.
type metadataConfig struct {
	// Uploader is "pinata" or "fake".
	Uploader string
	// PinataJWT is the server-side Pinata secret. Never logged.
	PinataJWT string
	// GatewayURL is the public HTTPS gateway prefix used to build URIs (trailing slash).
	GatewayURL string
	// MaxImageBytes caps the decoded image size.
	MaxImageBytes int64
}

// loadMetadataConfig reads PINATA_JWT, PINATA_GATEWAY_URL, METADATA_MAX_IMAGE_BYTES and
// METADATA_UPLOADER. Rules:
//   - METADATA_UPLOADER defaults to "pinata"; only "pinata" | "fake" are accepted.
//   - production/staging: uploader must be "pinata" and PINATA_JWT is required (fail-fast).
//   - development: an empty PINATA_JWT silently downgrades to the fake uploader so the
//     portal flow works locally without a Pinata account.
func loadMetadataConfig(env string) (*metadataConfig, error) {
	uploader := strings.ToLower(strings.TrimSpace(os.Getenv("METADATA_UPLOADER")))
	if uploader == "" {
		uploader = metadataUploaderPinata
	}
	if uploader != metadataUploaderPinata && uploader != metadataUploaderFake {
		return nil, fmt.Errorf("METADATA_UPLOADER must be %q or %q, got %q", metadataUploaderPinata, metadataUploaderFake, uploader)
	}

	jwt := strings.TrimSpace(os.Getenv("PINATA_JWT"))
	strict := env == "production" || env == "staging"
	if strict {
		if uploader != metadataUploaderPinata {
			return nil, fmt.Errorf("STONKAGENTS_ENV=%s requires METADATA_UPLOADER=pinata", env)
		}
		if jwt == "" {
			return nil, fmt.Errorf("STONKAGENTS_ENV=%s requires PINATA_JWT", env)
		}
	} else if uploader == metadataUploaderPinata && jwt == "" {
		uploader = metadataUploaderFake
	}

	gateway := strings.TrimSpace(os.Getenv("PINATA_GATEWAY_URL"))
	if gateway == "" {
		gateway = defaultMetadataGatewayURL
	}
	if !strings.HasPrefix(gateway, "https://") && !strings.HasPrefix(gateway, "http://") {
		return nil, fmt.Errorf("PINATA_GATEWAY_URL must be an http(s) URL, got %q", gateway)
	}
	gateway = strings.TrimRight(gateway, "/") + "/"

	maxImage := defaultMetadataMaxImageBytes
	if raw := strings.TrimSpace(os.Getenv("METADATA_MAX_IMAGE_BYTES")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("METADATA_MAX_IMAGE_BYTES must be a positive integer, got %q", raw)
		}
		maxImage = n
	}

	return &metadataConfig{
		Uploader:      uploader,
		PinataJWT:     jwt,
		GatewayURL:    gateway,
		MaxImageBytes: maxImage,
	}, nil
}

// --- Raydium LaunchLab metrics (StonkAgents) ---

// launchLabConfig holds env-driven settings for the LaunchLab metrics source.
//
//	RAYDIUM_API_URL              launch-mint API host (default https://launch-mint-v1.raydium.io)
//	RAYDIUM_TIMEOUT              HTTP timeout, Go duration (default 5s)
//	LAUNCHLAB_PROGRAM_ID         LaunchLab program (default mainnet LanMV9s…; devnet DRay6f…)
//	RAYDIUM_QUOTE_MINTS          comma-separated quote mints tried for pool PDA derivation when
//	                             the API is down and no pool id is known (default WSOL,$STONK)
//	JUPITER_PRICE_URL            quote USD price API (default https://lite-api.jup.ag/price/v3)
//	TOKEN_METRICS_DEFAULT_SOURCE "launchlab" | "pumpfun" for mints without a "pump" suffix
//	                             (default launchlab)
type launchLabConfig struct {
	APIURL        string
	Timeout       time.Duration
	ProgramID     string
	QuoteMints    []string
	JupiterURL    string
	DefaultSource models.TokenSource
}

func loadLaunchLabConfig() launchLabConfig {
	cfg := launchLabConfig{
		APIURL:        os.Getenv("RAYDIUM_API_URL"),
		Timeout:       5 * time.Second,
		ProgramID:     os.Getenv("LAUNCHLAB_PROGRAM_ID"),
		JupiterURL:    os.Getenv("JUPITER_PRICE_URL"),
		DefaultSource: models.TokenSourceLaunchLab,
	}
	if cfg.APIURL == "" {
		cfg.APIURL = services.RaydiumAPIURLDefault
	}
	if cfg.ProgramID == "" {
		cfg.ProgramID = services.LaunchLabProgramMainnet
	}
	if cfg.JupiterURL == "" {
		cfg.JupiterURL = services.JupiterPriceURLDefault
	}
	if v := os.Getenv("RAYDIUM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Timeout = d
		} else {
			fmt.Printf("RAYDIUM_TIMEOUT=%q invalid, using %s\n", v, cfg.Timeout)
		}
	}
	if v := os.Getenv("RAYDIUM_QUOTE_MINTS"); v != "" {
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				cfg.QuoteMints = append(cfg.QuoteMints, m)
			}
		}
	}
	if v := os.Getenv("TOKEN_METRICS_DEFAULT_SOURCE"); v != "" {
		if src, ok := models.ParseTokenSource(strings.ToLower(strings.TrimSpace(v))); ok {
			cfg.DefaultSource = src
		} else {
			fmt.Printf("TOKEN_METRICS_DEFAULT_SOURCE=%q invalid (pumpfun|launchlab), using %s\n", v, cfg.DefaultSource)
		}
	}
	return cfg
}
