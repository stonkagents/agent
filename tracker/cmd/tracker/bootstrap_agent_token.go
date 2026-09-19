// Package: main
// Feature: StonkAgents network token ($AGENT)
// Purpose: Parses the network token burn-plan env into services.AgentTokenConfig for
//          GET /api/v1/agent-token/burnplan. $AGENT lives on stonk.fun's LaunchLab platform,
//          so nothing here needs LAUNCHPAD_PLATFORM_ID or a tracker launch record.
//
// Env read here:
//
//	AGENT_TOKEN_MINT        network token mint; unset = GET /api/v1/agent-token/* disabled (404)
//	AGENT_TOKEN_SUPPLY      whole-token supply, default 1,000,000,000 (`total` on the burn plan)
//	AGENT_BURN_TOTAL_PCT    percent of the supply the plan burns in total; unset = `planTotal` null
//	AGENT_BURN_INTERVAL     Go duration between scheduled burns (e.g. 168h); unset = `next` null
//	AGENT_BURN_START        RFC 3339 time of the first scheduled burn; with AGENT_BURN_INTERVAL
//	                        it seeds `next` until the ledger has an indexed burn
//	AGENT_BURN_AMOUNT       whole tokens per scheduled burn; unset = `next.amount` 0

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// loadAgentTokenConfig reads the env for mint. Invalid values fail fast.
func loadAgentTokenConfig(mint string) (services.AgentTokenConfig, error) {
	cfg := services.AgentTokenConfig{Mint: mint}
	var err error
	if cfg.Supply, err = envFloat("AGENT_TOKEN_SUPPLY", services.AgentTokenTotalSupply); err != nil {
		return cfg, err
	}
	if cfg.PlanTotalPct, err = envFloat("AGENT_BURN_TOTAL_PCT", 0); err != nil {
		return cfg, err
	}
	if cfg.BurnAmount, err = envFloat("AGENT_BURN_AMOUNT", 0); err != nil {
		return cfg, err
	}
	if v := strings.TrimSpace(os.Getenv("AGENT_BURN_INTERVAL")); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil || d <= 0 {
			return cfg, fmt.Errorf("AGENT_BURN_INTERVAL must be a positive Go duration, got %q", v)
		}
		cfg.BurnInterval = d
	}
	if v := strings.TrimSpace(os.Getenv("AGENT_BURN_START")); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return cfg, fmt.Errorf("AGENT_BURN_START must be RFC 3339, got %q", v)
		}
		cfg.BurnStart = t.UTC()
	}
	switch {
	case cfg.Supply <= 0:
		return cfg, fmt.Errorf("AGENT_TOKEN_SUPPLY must be > 0")
	case cfg.PlanTotalPct < 0 || cfg.PlanTotalPct > 100:
		return cfg, fmt.Errorf("AGENT_BURN_TOTAL_PCT must be within [0, 100]")
	case cfg.BurnAmount < 0:
		return cfg, fmt.Errorf("AGENT_BURN_AMOUNT must be >= 0")
	}
	return cfg, nil
}
