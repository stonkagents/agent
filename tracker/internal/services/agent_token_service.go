// Package: tracker/internal/services
// Feature: StonkAgents network token ($AGENT)
// Purpose: The token's public ledgers as the portal panel reads them — the burn plan from
//          indexed launch_burns rows and the (still empty) keeper payouts.
//
// $AGENT is launched on stonk.fun (a Raydium LaunchLab pool under stonkfun's platform config,
// quoted in SOL), not on our platform: nothing here assumes a tracker launch record or our
// LAUNCHPAD_PLATFORM_ID. Burns are indexed by mint (TradeIndexer.indexBurns walks the mint's
// own signatures) and metrics resolve the pool from the mint (Raydium API row, then the
// ["pool", mintA, mintB] PDA under the LaunchLab program), so any platform's pool works.

package services

import (
	"context"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// AgentTokenTotalSupply is the whole-token supply the network token launched with; the burn
// plan's `remaining` counts down from it.
const AgentTokenTotalSupply = 1_000_000_000

// burnPlanRecentLimit is how many executed burns the plan lists.
const burnPlanRecentLimit = 5

// BurnEntry is one executed burn: when, how much (whole tokens) and the transaction.
type BurnEntry struct {
	At     time.Time `json:"at"`
	Amount float64   `json:"amount"`
	Sig    string    `json:"sig"`
}

// BurnScheduleEntry is the next scheduled burn. Amount is 0 when the schedule names no
// per-burn amount (AGENT_BURN_AMOUNT unset).
type BurnScheduleEntry struct {
	At     time.Time `json:"at"`
	Amount float64   `json:"amount"`
}

// BurnPlan is the body of GET /api/v1/agent-token/burnplan (amounts in whole tokens).
//
//	mint       the mint the ledger is for (AGENT_TOKEN_MINT), so a portal built for another
//	           mint can tell the two apart instead of mixing its chain reads with this ledger
//	total      the token SUPPLY (not the plan) — `remaining` = total - burned
//	planTotal  tokens the burn plan burns in total; null when no plan is configured
//	burned     sum of indexed burns; burns = their count
//	next       next scheduled burn from the configured schedule; null when none is configured
//	schedule   the configured cadence (interval in seconds, first burn, tokens per burn); null when
//	           no interval is configured, so the portal can draw the plan as a track
type BurnPlan struct {
	Mint      string             `json:"mint"`
	Total     float64            `json:"total"`
	PlanTotal *float64           `json:"planTotal"`
	Burned    float64            `json:"burned"`
	Remaining float64            `json:"remaining"`
	Burns     int                `json:"burns"`
	Next      *BurnScheduleEntry `json:"next"`
	Schedule  *BurnSchedule      `json:"schedule"`
	Recent    []BurnEntry        `json:"recent"`
}

// BurnSchedule is the configured cadence behind `next` (AGENT_BURN_INTERVAL, AGENT_BURN_START,
// AGENT_BURN_AMOUNT). StartAt is null when no start is configured (the first indexed burn
// then anchors the schedule); Amount is 0 when unknown.
type BurnSchedule struct {
	IntervalSeconds int64      `json:"intervalSeconds"`
	StartAt         *time.Time `json:"startAt"`
	Amount          float64    `json:"amount"`
}

// PayoutEntry is one keeper payout (whole quote tokens).
type PayoutEntry struct {
	Kind        string    `json:"kind"`
	Amount      float64   `json:"amount"`
	QuoteSymbol string    `json:"quoteSymbol"`
	Recipients  int       `json:"recipients"`
	Signature   string    `json:"signature"`
	At          time.Time `json:"at"`
}

// Payouts is the body of GET /api/v1/agent-token/payouts.
type Payouts struct {
	Payouts []PayoutEntry `json:"payouts"`
}

// AgentTokenConfig describes the network token and its burn plan (env in bootstrap_agent_token.go).
type AgentTokenConfig struct {
	// Mint is the network token mint (AGENT_TOKEN_MINT).
	Mint string
	// Supply is the whole-token supply; 0 = AgentTokenTotalSupply.
	Supply float64
	// PlanTotalPct is the percent of Supply the plan burns in total (AGENT_BURN_TOTAL_PCT);
	// 0 = no plan configured (planTotal null).
	PlanTotalPct float64
	// BurnInterval and BurnStart define the burn schedule (AGENT_BURN_INTERVAL, AGENT_BURN_START).
	// `next` is null unless both the interval is > 0 and a start (or an indexed burn) is known.
	BurnInterval time.Duration
	BurnStart    time.Time
	// BurnAmount is the whole tokens burned per scheduled burn (AGENT_BURN_AMOUNT); 0 = unknown.
	BurnAmount float64
}

// AgentTokenService serves the network token's ledgers.
type AgentTokenService struct {
	burns repository.LaunchBurnRepository
	cfg   AgentTokenConfig
	clock clock.Clock
}

// NewAgentTokenService creates the service for mint (AGENT_TOKEN_MINT) with no burn plan configured.
func NewAgentTokenService(burns repository.LaunchBurnRepository, mint string) *AgentTokenService {
	return NewAgentTokenServiceWithConfig(burns, AgentTokenConfig{Mint: mint}, nil)
}

// NewAgentTokenServiceWithConfig creates the service with a burn plan; clk may be nil.
func NewAgentTokenServiceWithConfig(burns repository.LaunchBurnRepository, cfg AgentTokenConfig, clk clock.Clock) *AgentTokenService {
	if cfg.Supply <= 0 {
		cfg.Supply = AgentTokenTotalSupply
	}
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &AgentTokenService{burns: burns, cfg: cfg, clock: clk}
}

// Mint returns the network token mint.
func (s *AgentTokenService) Mint() string { return s.cfg.Mint }

// BurnPlan sums the indexed burns, lists the newest five and derives the plan figures.
func (s *AgentTokenService) BurnPlan(ctx context.Context) (*BurnPlan, error) {
	sum, err := s.burns.Summary(ctx, s.cfg.Mint)
	if err != nil {
		return nil, err
	}
	recent, err := s.burns.Recent(ctx, s.cfg.Mint, burnPlanRecentLimit)
	if err != nil {
		return nil, err
	}
	plan := &BurnPlan{Mint: s.cfg.Mint, Total: s.cfg.Supply, Burned: sum.Burned, Burns: sum.Burns, Recent: make([]BurnEntry, 0, len(recent))}
	plan.Remaining = plan.Total - plan.Burned
	if plan.Remaining < 0 {
		plan.Remaining = 0
	}
	plan.PlanTotal = s.planTotal(plan.Burned)
	var lastBurn *time.Time
	for i, b := range recent {
		at := b.BlockTime.UTC()
		if i == 0 {
			lastBurn = &at // Recent is newest first
		}
		plan.Recent = append(plan.Recent, BurnEntry{At: at, Amount: b.Amount, Sig: b.Signature})
	}
	plan.Next = s.nextBurn(lastBurn)
	plan.Schedule = s.schedule()
	return plan, nil
}

// planTotal is Supply * PlanTotalPct / 100, never below what is already burned so the
// portal's "burned / planTotal" cannot exceed 100%; nil when no plan is configured.
func (s *AgentTokenService) planTotal(burned float64) *float64 {
	if s.cfg.PlanTotalPct <= 0 {
		return nil
	}
	total := s.cfg.Supply * s.cfg.PlanTotalPct / 100
	if burned > total {
		total = burned
	}
	return &total
}

// schedule is the configured cadence, or nil when no interval is set.
func (s *AgentTokenService) schedule() *BurnSchedule {
	if s.cfg.BurnInterval <= 0 {
		return nil
	}
	out := &BurnSchedule{IntervalSeconds: int64(s.cfg.BurnInterval / time.Second), Amount: s.cfg.BurnAmount}
	if !s.cfg.BurnStart.IsZero() {
		start := s.cfg.BurnStart.UTC()
		out.StartAt = &start
	}
	return out
}

// nextBurn is the first scheduled burn after now: the interval added to the newest indexed
// burn when there is one, otherwise the first point of the start + k*interval series that is
// still ahead. nil without an interval, or without both a start and an indexed burn.
func (s *AgentTokenService) nextBurn(lastBurn *time.Time) *BurnScheduleEntry {
	if s.cfg.BurnInterval <= 0 {
		return nil
	}
	now := s.clock.Now().UTC()
	var next time.Time
	switch {
	case lastBurn != nil:
		next = lastBurn.Add(s.cfg.BurnInterval)
	case !s.cfg.BurnStart.IsZero():
		next = s.cfg.BurnStart.UTC()
	default:
		return nil
	}
	if !next.After(now) {
		// Skip the missed points in one step (a tight interval and an old start must not spin).
		missed := now.Sub(next)/s.cfg.BurnInterval + 1
		next = next.Add(missed * s.cfg.BurnInterval)
	}
	return &BurnScheduleEntry{At: next, Amount: s.cfg.BurnAmount}
}

// Payouts is empty until the keeper pays.
func (s *AgentTokenService) Payouts(_ context.Context) (*Payouts, error) {
	return &Payouts{Payouts: []PayoutEntry{}}, nil
}
