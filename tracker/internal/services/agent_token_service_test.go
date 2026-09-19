// Package: tracker/internal/services
// Feature: StonkAgents network token ($AGENT)
// Purpose: Burn plan figures — total is the supply, planTotal/next come from the configured
//          plan (null without one) and the ledger drives burned/next.

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

const agentTestMint = "AGENTmint11111111111111111111111111111111111"

func seedBurns(t *testing.T, burns *repository.MemoryLaunchBurnRepository, now time.Time) {
	t.Helper()
	_, err := burns.InsertBurns(context.Background(), []*models.LaunchBurn{
		{Mint: agentTestMint, Signature: "b1", Slot: 1, BlockTime: now.Add(-50 * time.Hour), Amount: 1_000_000, Burner: "x"},
		{Mint: agentTestMint, Signature: "b2", Slot: 2, BlockTime: now.Add(-2 * time.Hour), Amount: 250_000, Burner: "x"},
	})
	if err != nil {
		t.Fatalf("seed burns: %v", err)
	}
}

func TestAgentTokenBurnPlan_NoPlanConfigured(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	burns := repository.NewMemoryLaunchBurnRepository()
	seedBurns(t, burns, clk.Now())
	plan, err := NewAgentTokenService(burns, agentTestMint).BurnPlan(context.Background())
	if err != nil {
		t.Fatalf("BurnPlan: %v", err)
	}
	if plan.Mint != agentTestMint {
		t.Errorf("mint = %q, want the configured mint on the plan", plan.Mint)
	}
	if plan.Total != AgentTokenTotalSupply || plan.Burned != 1_250_000 || plan.Remaining != AgentTokenTotalSupply-1_250_000 || plan.Burns != 2 {
		t.Errorf("plan = %+v", plan)
	}
	if plan.PlanTotal != nil || plan.Next != nil {
		t.Errorf("planTotal/next = %v/%v, want both nil without a configured plan", plan.PlanTotal, plan.Next)
	}
	if len(plan.Recent) != 2 || plan.Recent[0].Sig != "b2" {
		t.Errorf("recent = %+v, want newest first", plan.Recent)
	}
}

func TestAgentTokenBurnPlan_PlanTotalAndNextFromLedger(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	burns := repository.NewMemoryLaunchBurnRepository()
	seedBurns(t, burns, clk.Now())
	svc := NewAgentTokenServiceWithConfig(burns, AgentTokenConfig{
		Mint: agentTestMint, PlanTotalPct: 5, BurnInterval: 24 * time.Hour, BurnAmount: 500_000,
		BurnStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, clk)
	plan, err := svc.BurnPlan(context.Background())
	if err != nil {
		t.Fatalf("BurnPlan: %v", err)
	}
	if plan.PlanTotal == nil || *plan.PlanTotal != 50_000_000 {
		t.Errorf("planTotal = %v, want 5%% of 1e9 = 50000000", plan.PlanTotal)
	}
	// The ledger wins over the start series: newest burn (now-2h) + 24h.
	wantNext := clk.Now().Add(22 * time.Hour)
	if plan.Next == nil || !plan.Next.At.Equal(wantNext) || plan.Next.Amount != 500_000 {
		t.Errorf("next = %+v, want %s / 500000", plan.Next, wantNext)
	}
}

func TestAgentTokenBurnPlan_NextFromStartSeriesAndPlanFloor(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	burns := repository.NewMemoryLaunchBurnRepository()
	_, _ = burns.InsertBurns(context.Background(), []*models.LaunchBurn{
		{Mint: agentTestMint, Signature: "big", Slot: 1, BlockTime: clk.Now().Add(-time.Hour), Amount: 30_000_000, Burner: "x"},
	})
	svc := NewAgentTokenServiceWithConfig(burns, AgentTokenConfig{Mint: agentTestMint, Supply: 100_000_000, PlanTotalPct: 10}, clk)
	plan, _ := svc.BurnPlan(context.Background())
	// 10% of 1e8 = 1e7 but 3e7 is already burned: planTotal floors at burned so the ratio caps at 100%.
	if plan.Total != 100_000_000 || plan.PlanTotal == nil || *plan.PlanTotal != 30_000_000 {
		t.Errorf("total/planTotal = %v/%v, want 1e8 / 3e7", plan.Total, plan.PlanTotal)
	}
	if plan.Next != nil {
		t.Errorf("next = %+v, want nil without an interval", plan.Next)
	}

	// No ledger burn, a weekly series started long ago: next is the first point still ahead.
	empty := repository.NewMemoryLaunchBurnRepository()
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC) // Tuesday 09:00
	svc = NewAgentTokenServiceWithConfig(empty, AgentTokenConfig{Mint: agentTestMint, BurnInterval: 7 * 24 * time.Hour, BurnStart: start}, clk)
	plan, _ = svc.BurnPlan(context.Background())
	if plan.Next == nil || !plan.Next.At.Equal(time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)) || plan.Next.Amount != 0 {
		t.Errorf("next = %+v, want 2026-09-15T09:00:00Z with amount 0", plan.Next)
	}
	// Interval without start or ledger: nothing to schedule from.
	svc = NewAgentTokenServiceWithConfig(empty, AgentTokenConfig{Mint: agentTestMint, BurnInterval: time.Hour}, clk)
	if plan, _ = svc.BurnPlan(context.Background()); plan.Next != nil {
		t.Errorf("next = %+v, want nil without a start or an indexed burn", plan.Next)
	}
}
