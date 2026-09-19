// Package: main
// Feature: StonkAgents network token ($AGENT)
// Purpose: AGENT_TOKEN_SUPPLY / AGENT_BURN_* env parsing for the burn plan.

package main

import (
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/services"
)

func TestLoadAgentTokenConfig(t *testing.T) {
	for _, k := range []string{"AGENT_TOKEN_SUPPLY", "AGENT_BURN_TOTAL_PCT", "AGENT_BURN_INTERVAL", "AGENT_BURN_START", "AGENT_BURN_AMOUNT"} {
		t.Setenv(k, "")
	}
	cfg, err := loadAgentTokenConfig("Mint111")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.Mint != "Mint111" || cfg.Supply != services.AgentTokenTotalSupply || cfg.PlanTotalPct != 0 || cfg.BurnInterval != 0 || !cfg.BurnStart.IsZero() || cfg.BurnAmount != 0 {
		t.Errorf("defaults = %+v", cfg)
	}

	t.Setenv("AGENT_TOKEN_SUPPLY", "500000000")
	t.Setenv("AGENT_BURN_TOTAL_PCT", "12.5")
	t.Setenv("AGENT_BURN_INTERVAL", "168h")
	t.Setenv("AGENT_BURN_START", "2026-10-01T09:00:00Z")
	t.Setenv("AGENT_BURN_AMOUNT", "250000")
	cfg, err = loadAgentTokenConfig("Mint111")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if cfg.Supply != 500_000_000 || cfg.PlanTotalPct != 12.5 || cfg.BurnInterval != 168*time.Hour ||
		!cfg.BurnStart.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)) || cfg.BurnAmount != 250_000 {
		t.Errorf("configured = %+v", cfg)
	}

	for _, tc := range []struct{ key, val string }{
		{"AGENT_BURN_TOTAL_PCT", "101"},
		{"AGENT_BURN_TOTAL_PCT", "abc"},
		{"AGENT_BURN_INTERVAL", "0s"},
		{"AGENT_BURN_INTERVAL", "weekly"},
		{"AGENT_BURN_START", "2026-10-01"},
		{"AGENT_BURN_AMOUNT", "-1"},
		{"AGENT_TOKEN_SUPPLY", "0"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := loadAgentTokenConfig("Mint111"); err == nil {
				t.Errorf("%s=%q: want an error", tc.key, tc.val)
			}
		})
	}
}
