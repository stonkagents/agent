// Package: main
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Tests for loadMetadataConfig — defaults, dev fake fallback, production fail-fast.

package main

import (
	"os"
	"strings"
	"testing"
)

func clearMetadataEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PINATA_JWT", "PINATA_GATEWAY_URL", "METADATA_MAX_IMAGE_BYTES", "METADATA_UPLOADER"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadMetadataConfig_DevDefaultsToFakeWithoutJWT(t *testing.T) {
	clearMetadataEnv(t)
	cfg, err := loadMetadataConfig("development")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Uploader != metadataUploaderFake {
		t.Errorf("Uploader = %q, want fake", cfg.Uploader)
	}
	if cfg.GatewayURL != "https://gateway.pinata.cloud/ipfs/" {
		t.Errorf("GatewayURL = %q", cfg.GatewayURL)
	}
	if cfg.MaxImageBytes != 2097152 {
		t.Errorf("MaxImageBytes = %d", cfg.MaxImageBytes)
	}
}

func TestLoadMetadataConfig_DevUsesPinataWhenJWTSet(t *testing.T) {
	clearMetadataEnv(t)
	t.Setenv("PINATA_JWT", "jwt-value")
	t.Setenv("PINATA_GATEWAY_URL", "https://my-gw.mypinata.cloud/ipfs") // no trailing slash
	t.Setenv("METADATA_MAX_IMAGE_BYTES", "1048576")
	cfg, err := loadMetadataConfig("development")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Uploader != metadataUploaderPinata || cfg.PinataJWT != "jwt-value" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.GatewayURL != "https://my-gw.mypinata.cloud/ipfs/" {
		t.Errorf("GatewayURL = %q, want trailing slash normalised", cfg.GatewayURL)
	}
	if cfg.MaxImageBytes != 1048576 {
		t.Errorf("MaxImageBytes = %d", cfg.MaxImageBytes)
	}
}

func TestLoadMetadataConfig_ProductionRequiresJWT(t *testing.T) {
	for _, env := range []string{"production", "staging"} {
		clearMetadataEnv(t)
		_, err := loadMetadataConfig(env)
		if err == nil || !strings.Contains(err.Error(), "PINATA_JWT") {
			t.Fatalf("%s: err = %v, want PINATA_JWT required", env, err)
		}
	}
}

func TestLoadMetadataConfig_ProductionRejectsFake(t *testing.T) {
	clearMetadataEnv(t)
	t.Setenv("PINATA_JWT", "jwt")
	t.Setenv("METADATA_UPLOADER", "fake")
	if _, err := loadMetadataConfig("production"); err == nil {
		t.Fatal("expected error for METADATA_UPLOADER=fake in production")
	}
}

func TestLoadMetadataConfig_ProductionPassesWithJWT(t *testing.T) {
	clearMetadataEnv(t)
	t.Setenv("PINATA_JWT", "jwt")
	cfg, err := loadMetadataConfig("production")
	if err != nil || cfg.Uploader != metadataUploaderPinata {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestLoadMetadataConfig_InvalidValues(t *testing.T) {
	cases := map[string][2]string{
		"bad uploader":   {"METADATA_UPLOADER", "s3"},
		"bad max bytes":  {"METADATA_MAX_IMAGE_BYTES", "lots"},
		"zero max bytes": {"METADATA_MAX_IMAGE_BYTES", "0"},
		"bad gateway":    {"PINATA_GATEWAY_URL", "ftp://gw"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			clearMetadataEnv(t)
			t.Setenv(kv[0], kv[1])
			if _, err := loadMetadataConfig("development"); err == nil {
				t.Fatalf("expected error for %s=%s", kv[0], kv[1])
			}
		})
	}
}

func TestLoadMetadataConfig_ExplicitFakeInDev(t *testing.T) {
	clearMetadataEnv(t)
	t.Setenv("PINATA_JWT", "jwt")
	t.Setenv("METADATA_UPLOADER", "FAKE")
	cfg, err := loadMetadataConfig("development")
	if err != nil || cfg.Uploader != metadataUploaderFake {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}
