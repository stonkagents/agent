// Package: tracker/internal/services
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Tests for MetricsService source routing (pump.fun vs LaunchLab) and cross-source fallback

package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

type stubLaunchLab struct {
	metrics   *models.TokenMetrics
	err       error
	callCount atomic.Int32
	lastHint  LaunchLabHint
	lastMint  string
}

func (s *stubLaunchLab) GetCoinData(_ context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error) {
	s.callCount.Add(1)
	s.lastHint = hint
	s.lastMint = mint
	return s.metrics, s.err
}

func launchLabMetrics() *models.TokenMetrics {
	pool := "PoolABC"
	quote := StonkMint
	return &models.TokenMetrics{
		MarketCapUsd:        ptrFloat(2896),
		BondingCurvePercent: ptrInt(12),
		Complete:            ptrBool(false),
		PoolId:              &pool,
		QuoteMint:           &quote,
		QuoteRaised:         ptrFloat(3500),
		PriceQuote:          ptrFloat(0.00001),
	}
}

func TestRouting_PlainMint_GoesToLaunchLab(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", fxMint)
	pump := &stubPumpFun{metrics: bondingMetrics()}
	ll := &stubLaunchLab{metrics: launchLabMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{PumpFun: pump, LaunchLab: ll, TokenRepo: tokenRepo})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if ll.callCount.Load() != 1 || pump.callCount.Load() != 0 {
		t.Errorf("calls launchlab/pump = %d/%d, want 1/0", ll.callCount.Load(), pump.callCount.Load())
	}
	if m.Source != "launchlab" || m.PoolId == nil || *m.PoolId != "PoolABC" {
		t.Errorf("source/poolId = %s/%v", m.Source, m.PoolId)
	}
	if ll.lastMint != fxMint {
		t.Errorf("mint passed = %s", ll.lastMint)
	}
}

func TestRouting_PumpSuffix_GoesToPumpFun(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "7GCihgDB8fe6KNjn2MYtkzZcRjQy3t9GHdC8uHYmW2hrpump")
	pump := &stubPumpFun{metrics: bondingMetrics()}
	ll := &stubLaunchLab{metrics: launchLabMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{PumpFun: pump, LaunchLab: ll, TokenRepo: tokenRepo})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if pump.callCount.Load() != 1 || ll.callCount.Load() != 0 {
		t.Errorf("calls pump/launchlab = %d/%d, want 1/0", pump.callCount.Load(), ll.callCount.Load())
	}
	if m.Source != "pumpfun" || m.SolRaised == nil || *m.SolRaised != 3.2 {
		t.Errorf("source/solRaised = %s/%v", m.Source, m.SolRaised)
	}
}

func TestRouting_DefaultSourceOverride_PumpFun(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", fxMint)
	pump := &stubPumpFun{metrics: bondingMetrics()}
	ll := &stubLaunchLab{metrics: launchLabMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun: pump, LaunchLab: ll, TokenRepo: tokenRepo,
		DefaultSource: models.TokenSourcePumpFun, // TOKEN_METRICS_DEFAULT_SOURCE=pumpfun
	})
	if _, err := svc.GetTokenMetrics(context.Background(), "peer-1"); err != nil {
		t.Fatal(err)
	}
	if pump.callCount.Load() != 1 || ll.callCount.Load() != 0 {
		t.Errorf("calls pump/launchlab = %d/%d, want 1/0", pump.callCount.Load(), ll.callCount.Load())
	}
}

func TestRouting_LaunchLabNotFound_FallsBackToPumpFun(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "LegacyPreSuffixPumpMint111111111111111111111")
	pump := &stubPumpFun{metrics: bondingMetrics()}
	ll := &stubLaunchLab{metrics: nil, err: nil} // not a LaunchLab token
	svc := NewMetricsService(MetricsServiceDeps{PumpFun: pump, LaunchLab: ll, TokenRepo: tokenRepo})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if ll.callCount.Load() != 1 || pump.callCount.Load() != 1 {
		t.Errorf("calls launchlab/pump = %d/%d, want 1/1", ll.callCount.Load(), pump.callCount.Load())
	}
	if m.Source != "pumpfun" || m.MarketCapUsd == nil || *m.MarketCapUsd != 12400 {
		t.Errorf("fallback result = %+v", m)
	}
}

func TestRouting_LaunchLabError_PumpNotFound_Degraded(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", fxMint)
	pump := &stubPumpFun{metrics: nil, err: nil}
	ll := &stubLaunchLab{err: errors.New("raydium down")}
	moralis := &stubMoralis{price: ptrFloat(0.007)}
	svc := NewMetricsService(MetricsServiceDeps{PumpFun: pump, LaunchLab: ll, Moralis: moralis, TokenRepo: tokenRepo})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if m.MarketCapUsd != nil || m.BondingCurvePercent != nil {
		t.Errorf("expected degraded metrics, got %+v", m)
	}
	// Source failure (not a clean miss) → Moralis price fallback still applies
	if m.PriceUsd == nil || *m.PriceUsd != 0.007 {
		t.Errorf("PriceUsd = %v, want Moralis fallback 0.007", m.PriceUsd)
	}
	if moralis.holdersCalls.Load() != 0 {
		t.Error("holders must not be fetched for non-graduated/degraded tokens")
	}
}

func TestRouting_BothNotFound_EmptyNoError(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", fxMint)
	pump := &stubPumpFun{}
	ll := &stubLaunchLab{}
	moralis := &stubMoralis{price: ptrFloat(0.007)}
	svc := NewMetricsService(MetricsServiceDeps{PumpFun: pump, LaunchLab: ll, Moralis: moralis, TokenRepo: tokenRepo})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if m.PriceUsd != nil || m.MarketCapUsd != nil || m.Source != "" {
		t.Errorf("clean miss should be all-nulls without Moralis fallback, got %+v", m)
	}
	if moralis.priceCalls.Load() != 0 {
		t.Error("Moralis must not be called on a clean not-found")
	}
}

func TestRouting_OnlyLaunchLabConfigured_PumpMintStillServed(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "SomethingEndingInpump")
	ll := &stubLaunchLab{metrics: launchLabMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: ll, TokenRepo: tokenRepo}) // no pump.fun client

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if ll.callCount.Load() != 1 || m.Source != "launchlab" {
		t.Errorf("unconfigured primary should skip to the available source: calls=%d source=%s", ll.callCount.Load(), m.Source)
	}
}

func TestRouting_ResolverHintForwardedToLaunchLab(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", fxMint)
	ll := &stubLaunchLab{metrics: launchLabMetrics()}
	resolver := &DefaultTokenSourceResolver{Overrides: map[string]TokenSourceResolution{
		fxMint: {Source: models.TokenSourceLaunchLab, Hint: LaunchLabHint{PoolID: fxPool, QuoteMint: StonkMint}},
	}}
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: ll, Resolver: resolver, TokenRepo: tokenRepo})
	if _, err := svc.GetTokenMetrics(context.Background(), "peer-1"); err != nil {
		t.Fatal(err)
	}
	if ll.lastHint.PoolID != fxPool || ll.lastHint.QuoteMint != StonkMint {
		t.Errorf("hint = %+v", ll.lastHint)
	}
	res := svc.ResolveSource(context.Background(), &models.PeerToken{TokenContractAddress: fxMint})
	if res.Source != models.TokenSourceLaunchLab || res.Hint.PoolID != fxPool {
		t.Errorf("ResolveSource = %+v", res)
	}
}

func TestRouting_LaunchLabGraduated_MoralisHolders(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", fxMint)
	lm := launchLabMetrics()
	lm.Complete = ptrBool(true)
	lm.PriceUsd = ptrFloat(0.000003)
	ll := &stubLaunchLab{metrics: lm}
	moralis := &stubMoralis{holders: ptrInt(77), price: nil} // price missing → keep on-chain price
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: ll, Moralis: moralis, TokenRepo: tokenRepo})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Holders == nil || *m.Holders != 77 {
		t.Errorf("Holders = %v, want 77", m.Holders)
	}
	if m.PriceUsd == nil || *m.PriceUsd != 0.000003 {
		t.Errorf("PriceUsd = %v, want on-chain price retained when Moralis has none", m.PriceUsd)
	}
}
