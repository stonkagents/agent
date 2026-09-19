// Package: main
// Feature: F-013 (Credits & Identity)
// Story: TD-007 (Externalize hardcoded secrets)
// Purpose: Tests for tracker secret loading and STONKAGENTS_ENV fail-fast

package main

import (
	"os"
	"testing"
)

func clearSecretEnvVars(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"STONKAGENTS_ENV",
		"JWT_SIGNING_SECRET",
		"SOCIAL_CALLBACK_SECRET",
		"SOLANA_TREASURY_ADDRESS",
		"SOLANA_USE_STUBS",
		"SOLANA_RPC_URL",
		"LAUNCHPAD_PLATFORM_ID",
		"LAUNCHPAD_PROGRAM_ID",
		"LAUNCHPAD_CLUSTER",
		"LAUNCH_TREASURY_ADDRESS",
		"LAUNCH_FEE_USD",
		"LAUNCH_FEE_MIN_LAMPORTS",
		"LAUNCH_FEE_MAX_LAMPORTS",
		"PRICE_FEED_URL",
		"LAUNCH_TRANSFER_FEE_BPS",
		"LAUNCH_ONE_PER_WALLET",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

// setProductionBaseSecrets sets the pre-launchpad production secrets so tests can isolate launchpad checks.
func setProductionBaseSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("STONKAGENTS_ENV", "production")
	t.Setenv("JWT_SIGNING_SECRET", "production-jwt-secret-at-least-32-chars")
	t.Setenv("SOCIAL_CALLBACK_SECRET", "production-callback-secret")
	t.Setenv("SOLANA_TREASURY_ADDRESS", "ProdTreasuryAddr111111111111111111111111111")
	t.Setenv("SOLANA_USE_STUBS", "false")
	t.Setenv("SOLANA_RPC_URL", "https://api.mainnet-beta.solana.com")
}

func TestLoadTrackerSecrets_LaunchpadDevDefaults(t *testing.T) {
	clearSecretEnvVars(t)

	s, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if s.LaunchpadPlatformID != "" {
		t.Errorf("LaunchpadPlatformID = %q, want empty in dev", s.LaunchpadPlatformID)
	}
	if s.LaunchpadProgramID != "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj" {
		t.Errorf("LaunchpadProgramID = %q", s.LaunchpadProgramID)
	}
	if s.LaunchTreasuryAddress != devTreasuryAddress {
		t.Errorf("LaunchTreasuryAddress = %q, want fallback to SolanaTreasuryAddress", s.LaunchTreasuryAddress)
	}
	if s.LaunchFeeUSD != 0.50 || s.LaunchFeeMinLamports != 4_000_000 || s.LaunchFeeMaxLamports != 10_000_000 {
		t.Errorf("fee defaults = %v / %d / %d", s.LaunchFeeUSD, s.LaunchFeeMinLamports, s.LaunchFeeMaxLamports)
	}
	if s.PriceFeedURL != "https://lite-api.jup.ag/price/v3" || s.LaunchTransferFeeBps != 100 {
		t.Errorf("PriceFeedURL = %q, LaunchTransferFeeBps = %d", s.PriceFeedURL, s.LaunchTransferFeeBps)
	}
}

func TestLoadTrackerSecrets_LaunchpadOverrides(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("LAUNCHPAD_PLATFORM_ID", "PlatformCfg1111111111111111111111111111111")
	t.Setenv("LAUNCHPAD_PROGRAM_ID", "DRay6fNdQ5J82H7xV6uq2aV3mNrUZ1J4PgSKsWgptcm6")
	t.Setenv("LAUNCH_TREASURY_ADDRESS", "LaunchTreasury11111111111111111111111111111")
	t.Setenv("LAUNCH_FEE_USD", "0.75")
	t.Setenv("LAUNCH_FEE_MIN_LAMPORTS", "5000000")
	t.Setenv("LAUNCH_FEE_MAX_LAMPORTS", "20000000")
	t.Setenv("PRICE_FEED_URL", "http://localhost:9999/price")
	t.Setenv("LAUNCH_TRANSFER_FEE_BPS", "300")

	s, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if s.LaunchpadPlatformID != "PlatformCfg1111111111111111111111111111111" || s.LaunchpadProgramID != "DRay6fNdQ5J82H7xV6uq2aV3mNrUZ1J4PgSKsWgptcm6" ||
		s.LaunchTreasuryAddress != "LaunchTreasury11111111111111111111111111111" || s.LaunchFeeUSD != 0.75 ||
		s.LaunchFeeMinLamports != 5_000_000 || s.LaunchFeeMaxLamports != 20_000_000 ||
		s.PriceFeedURL != "http://localhost:9999/price" || s.LaunchTransferFeeBps != 300 {
		t.Errorf("launchpad overrides not applied: %+v", s)
	}
}

func TestLoadTrackerSecrets_LaunchpadInvalidValues(t *testing.T) {
	cases := map[string]string{
		"LAUNCH_FEE_USD":          "abc",
		"LAUNCH_FEE_MIN_LAMPORTS": "20000000", // > default max
		"LAUNCH_TRANSFER_FEE_BPS": "20000",
		"LAUNCHPAD_CLUSTER":       "testnet",
		"LAUNCH_ONE_PER_WALLET":   "nope",
	}
	for key, val := range cases {
		t.Run(key, func(t *testing.T) {
			clearSecretEnvVars(t)
			t.Setenv(key, val)
			if _, err := loadTrackerSecrets(); err == nil {
				t.Errorf("loadTrackerSecrets() with %s=%s error = nil, want error", key, val)
			}
		})
	}
}

func TestLoadTrackerSecrets_ProductionRequiresPlatformID(t *testing.T) {
	clearSecretEnvVars(t)
	setProductionBaseSecrets(t)

	if _, err := loadTrackerSecrets(); err == nil {
		t.Fatal("loadTrackerSecrets() error = nil, want error for production without LAUNCHPAD_PLATFORM_ID")
	}
	t.Setenv("LAUNCHPAD_PLATFORM_ID", "PlatformCfg1111111111111111111111111111111")
	if _, err := loadTrackerSecrets(); err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil once LAUNCHPAD_PLATFORM_ID is set", err)
	}
}

func TestLoadTrackerSecrets_DevelopmentDefaults(t *testing.T) {
	clearSecretEnvVars(t)

	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil", err)
	}
	if secrets.Env != "development" {
		t.Errorf("Env = %q, want %q", secrets.Env, "development")
	}
	if secrets.JWTSigningSecret != devJWTSecret {
		t.Errorf("JWTSigningSecret = %q, want dev default", secrets.JWTSigningSecret)
	}
	if secrets.SocialCallbackSecret != devCallbackSecret {
		t.Errorf("SocialCallbackSecret = %q, want dev default", secrets.SocialCallbackSecret)
	}
	if secrets.SolanaTreasuryAddress != devTreasuryAddress {
		t.Errorf("SolanaTreasuryAddress = %q, want dev default", secrets.SolanaTreasuryAddress)
	}
	if !secrets.UseSolanaStubs {
		t.Error("UseSolanaStubs = false, want true in development")
	}
}

func TestLoadTrackerSecrets_EnvVarsOverrideDefaults(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("JWT_SIGNING_SECRET", "real-jwt-secret-at-least-32-chars-long")
	t.Setenv("SOCIAL_CALLBACK_SECRET", "real-callback-secret")
	t.Setenv("SOLANA_TREASURY_ADDRESS", "RealTreasuryAddr111111111111111111111111111")

	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil", err)
	}
	if secrets.JWTSigningSecret != "real-jwt-secret-at-least-32-chars-long" {
		t.Errorf("JWTSigningSecret not overridden by env var")
	}
	if secrets.SocialCallbackSecret != "real-callback-secret" {
		t.Errorf("SocialCallbackSecret not overridden by env var")
	}
	if secrets.SolanaTreasuryAddress != "RealTreasuryAddr111111111111111111111111111" {
		t.Errorf("SolanaTreasuryAddress not overridden by env var")
	}
}

func TestLoadTrackerSecrets_ProductionFailsOnDefaults(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("STONKAGENTS_ENV", "production")

	_, err := loadTrackerSecrets()
	if err == nil {
		t.Fatal("loadTrackerSecrets() error = nil, want error for production with dev defaults")
	}
}

func TestLoadTrackerSecrets_ProductionPassesWithRealSecrets(t *testing.T) {
	clearSecretEnvVars(t)
	setProductionBaseSecrets(t)
	t.Setenv("LAUNCHPAD_PLATFORM_ID", "PlatformCfg1111111111111111111111111111111")

	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil", err)
	}
	if secrets.Env != "production" {
		t.Errorf("Env = %q, want %q", secrets.Env, "production")
	}
	if secrets.UseSolanaStubs {
		t.Error("UseSolanaStubs = true, want false in production")
	}
	if secrets.SolanaRPCURL != "https://api.mainnet-beta.solana.com" {
		t.Errorf("SolanaRPCURL = %q, want mainnet URL", secrets.SolanaRPCURL)
	}
}

func TestLoadTrackerSecrets_StagingFailsLikeProduction(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("STONKAGENTS_ENV", "staging")

	_, err := loadTrackerSecrets()
	if err == nil {
		t.Fatal("loadTrackerSecrets() error = nil, want error for staging with dev defaults")
	}
}

func TestLoadTrackerSecrets_ProductionRejectsStubs(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("STONKAGENTS_ENV", "production")
	t.Setenv("JWT_SIGNING_SECRET", "production-jwt-secret-at-least-32-chars")
	t.Setenv("SOCIAL_CALLBACK_SECRET", "production-callback-secret")
	t.Setenv("SOLANA_TREASURY_ADDRESS", "ProdTreasuryAddr111111111111111111111111111")
	t.Setenv("SOLANA_USE_STUBS", "true")

	_, err := loadTrackerSecrets()
	if err == nil {
		t.Fatal("loadTrackerSecrets() error = nil, want error for SOLANA_USE_STUBS=true in production")
	}
}

// Regression: tracker.dev.stonkagents.com runs as STONKAGENTS_ENV=dev and serves real
// users via real Solana RPC. Stubs must be rejected there so a misconfigured
// deploy doesn't silently use the hardcoded amount stub.
func TestLoadTrackerSecrets_DevRejectsStubs(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("STONKAGENTS_ENV", "dev")
	t.Setenv("SOLANA_USE_STUBS", "true")

	_, err := loadTrackerSecrets()
	if err == nil {
		t.Fatal("loadTrackerSecrets() error = nil, want error for SOLANA_USE_STUBS=true in dev")
	}
}

func TestLoadTrackerSecrets_DevAcceptsRealRPC(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("STONKAGENTS_ENV", "dev")
	t.Setenv("SOLANA_USE_STUBS", "false")
	t.Setenv("SOLANA_RPC_URL", "https://api.devnet.solana.com")

	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil for dev with real RPC", err)
	}
	if secrets.UseSolanaStubs {
		t.Error("UseSolanaStubs = true, want false in dev")
	}
}

func TestLoadTrackerSecrets_StubsDisabledExplicitly(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("SOLANA_USE_STUBS", "false")
	t.Setenv("SOLANA_RPC_URL", "https://api.devnet.solana.com")

	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil", err)
	}
	if secrets.UseSolanaStubs {
		t.Error("UseSolanaStubs = true, want false when SOLANA_USE_STUBS=false")
	}
	if secrets.SolanaRPCURL != "https://api.devnet.solana.com" {
		t.Errorf("SolanaRPCURL = %q, want devnet URL", secrets.SolanaRPCURL)
	}
}

func TestLoadTrackerSecrets_StubsDisabledRequiresRPCURL(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("SOLANA_USE_STUBS", "false")

	_, err := loadTrackerSecrets()
	if err == nil {
		t.Fatal("loadTrackerSecrets() error = nil, want error when stubs disabled without SOLANA_RPC_URL")
	}
}

func TestLoadTrackerSecrets_StrictStubsParsing(t *testing.T) {
	clearSecretEnvVars(t)
	// Values like "1", "yes", "TRUE" should NOT enable stubs in non-dev environments.
	setProductionBaseSecrets(t)
	t.Setenv("SOLANA_USE_STUBS", "yes")
	t.Setenv("LAUNCHPAD_PLATFORM_ID", "PlatformCfg1111111111111111111111111111111")

	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v, want nil", err)
	}
	// "yes" is not "true", so in production it should NOT enable stubs.
	if secrets.UseSolanaStubs {
		t.Error("UseSolanaStubs = true with SOLANA_USE_STUBS=yes, want false (strict parsing)")
	}
}

// TestLoadTrackerSecrets_LaunchpadCluster: LAUNCHPAD_CLUSTER wins; otherwise the cluster follows
// LAUNCHPAD_PROGRAM_ID (devnet program → devnet, default mainnet program → mainnet).
func TestLoadTrackerSecrets_LaunchpadCluster(t *testing.T) {
	cases := []struct{ name, cluster, program, want string }{
		{"defaults", "", "", "mainnet"},
		{"derived devnet", "", "DRay6fNdQ5J82H7xV6uq2aV3mNrUZ1J4PgSKsWgptcm6", "devnet"},
		{"derived mainnet", "", "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj", "mainnet"},
		{"explicit wins", "devnet", "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj", "devnet"},
		{"explicit case-insensitive", "MAINNET", "DRay6fNdQ5J82H7xV6uq2aV3mNrUZ1J4PgSKsWgptcm6", "mainnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearSecretEnvVars(t)
			if tc.cluster != "" {
				t.Setenv("LAUNCHPAD_CLUSTER", tc.cluster)
			}
			if tc.program != "" {
				t.Setenv("LAUNCHPAD_PROGRAM_ID", tc.program)
			}
			s, err := loadTrackerSecrets()
			if err != nil {
				t.Fatalf("loadTrackerSecrets() error = %v", err)
			}
			if s.LaunchpadCluster != tc.want {
				t.Errorf("LaunchpadCluster = %q, want %q", s.LaunchpadCluster, tc.want)
			}
		})
	}
}

func TestLoadTrackerSecrets_LaunchOnePerWallet(t *testing.T) {
	clearSecretEnvVars(t)
	s, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if !s.LaunchOnePerWallet {
		t.Errorf("LaunchOnePerWallet default = false, want true")
	}
	for val, want := range map[string]bool{"false": false, "0": false, "FALSE": false, "true": true, "1": true, " t ": true} {
		t.Run(val, func(t *testing.T) {
			clearSecretEnvVars(t)
			t.Setenv("LAUNCH_ONE_PER_WALLET", val)
			s, err := loadTrackerSecrets()
			if err != nil {
				t.Fatalf("loadTrackerSecrets() error = %v", err)
			}
			if s.LaunchOnePerWallet != want {
				t.Errorf("LAUNCH_ONE_PER_WALLET=%q -> %v, want %v", val, s.LaunchOnePerWallet, want)
			}
		})
	}
}

func TestLoadTrackerSecrets_PlatformPeerIDs(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("PLATFORM_PEER_IDS", "")
	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if len(secrets.PlatformPeerIDs) != 0 {
		t.Errorf("PlatformPeerIDs = %v, want none by default", secrets.PlatformPeerIDs)
	}
	t.Setenv("PLATFORM_PEER_IDS", " 12D3KooWAlpha, ,12D3KooWBeta,,")
	secrets, err = loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if len(secrets.PlatformPeerIDs) != 2 || secrets.PlatformPeerIDs[0] != "12D3KooWAlpha" || secrets.PlatformPeerIDs[1] != "12D3KooWBeta" {
		t.Errorf("PlatformPeerIDs = %v, want the two trimmed ids", secrets.PlatformPeerIDs)
	}
}

func TestLoadTrackerSecrets_PortalURL(t *testing.T) {
	clearSecretEnvVars(t)
	t.Setenv("PORTAL_URL", "")
	secrets, err := loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if secrets.PortalURL != "" {
		t.Errorf("PortalURL = %q, want empty by default", secrets.PortalURL)
	}
	t.Setenv("PORTAL_URL", " https://dev.stonkagents.com/ ")
	secrets, err = loadTrackerSecrets()
	if err != nil {
		t.Fatalf("loadTrackerSecrets() error = %v", err)
	}
	if secrets.PortalURL != "https://dev.stonkagents.com" {
		t.Errorf("PortalURL = %q, want the trimmed origin without a trailing slash", secrets.PortalURL)
	}
}
