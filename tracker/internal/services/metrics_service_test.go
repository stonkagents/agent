// Package: tracker/internal/services
// Feature: F-031 (Token Data Persistence)
// Story: US-031-02 (Backend Token Metrics Aggregation)
// Purpose: Tests for MetricsService — caching, singleflight, Moralis gating, degraded cache

package services

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// --- Stub PumpFunClient ---

type stubPumpFun struct {
	metrics   *models.TokenMetrics
	err       error
	callCount atomic.Int32
}

func (s *stubPumpFun) GetCoinData(_ context.Context, _ string) (*models.TokenMetrics, error) {
	s.callCount.Add(1)
	return s.metrics, s.err
}

// slowPumpFun adds a delay to simulate real API latency (for singleflight testing).
type slowPumpFun struct {
	metrics   *models.TokenMetrics
	err       error
	delay     time.Duration
	callCount atomic.Int32
}

func (s *slowPumpFun) GetCoinData(ctx context.Context, _ string) (*models.TokenMetrics, error) {
	s.callCount.Add(1)
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.metrics, s.err
}

// --- Stub MoralisClient ---

type stubMoralis struct {
	holders      *int
	holdersErr   error
	price        *float64
	priceErr     error
	holdersCalls atomic.Int32
	priceCalls   atomic.Int32
}

func (s *stubMoralis) GetTopHolders(_ context.Context, _ string) (*int, error) {
	s.holdersCalls.Add(1)
	return s.holders, s.holdersErr
}

func (s *stubMoralis) GetTokenPrice(_ context.Context, _ string) (*float64, error) {
	s.priceCalls.Add(1)
	return s.price, s.priceErr
}

// --- Helpers ---

func seedToken(t *testing.T, repo *repository.MemoryTokenRepository, peerID, contractAddr string) {
	t.Helper()
	err := repo.Create(context.Background(), &models.PeerToken{
		PeerID:               peerID,
		TokenContractAddress: contractAddr,
		TokenTicker:          "TEST",
		TokenName:            "TestCoin",
		LaunchedAt:           time.Now(),
	})
	if err != nil {
		t.Fatalf("seedToken: %v", err)
	}
}

func ptrFloat(v float64) *float64 { return &v }
func ptrInt(v int) *int           { return &v }
func ptrBool(v bool) *bool        { return &v }
func ptrStr(v string) *string     { return &v }

func bondingMetrics() *models.TokenMetrics {
	return &models.TokenMetrics{
		MarketCapUsd:        ptrFloat(12400),
		SolRaised:           ptrFloat(3.2),
		BondingCurvePercent: ptrInt(4),
		Complete:            ptrBool(false),
		ImageUrl:            ptrStr("https://example.com/img.png"),
	}
}

func graduatedMetrics() *models.TokenMetrics {
	return &models.TokenMetrics{
		MarketCapUsd:        ptrFloat(50000),
		SolRaised:           ptrFloat(85),
		BondingCurvePercent: ptrInt(99),
		Complete:            ptrBool(true),
		ImageUrl:            ptrStr("https://example.com/img.png"),
	}
}

// --- Tests ---

func TestMetrics_BasicFetch_ReturnsPumpData(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: bondingMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if m.MarketCapUsd == nil || *m.MarketCapUsd != 12400 {
		t.Errorf("MarketCapUsd = %v, want 12400", m.MarketCapUsd)
	}
	if m.Complete == nil || *m.Complete != false {
		t.Errorf("Complete = %v, want false", m.Complete)
	}
	if pump.callCount.Load() != 1 {
		t.Errorf("pump.fun calls = %d, want 1", pump.callCount.Load())
	}
}

func TestMetrics_PeerNotFound_ReturnsError(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	pump := &stubPumpFun{metrics: bondingMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	_, err := svc.GetTokenMetrics(context.Background(), "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMetrics_PumpFun404_ReturnsAllNulls(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	// nil metrics + nil error = 404 (not indexed)
	pump := &stubPumpFun{metrics: nil, err: nil}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if m.MarketCapUsd != nil || m.SolRaised != nil || m.Complete != nil {
		t.Errorf("expected all-nulls, got: mcap=%v sol=%v complete=%v", m.MarketCapUsd, m.SolRaised, m.Complete)
	}
}

func TestMetrics_MoralisPostGraduation_BothCalled(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: graduatedMetrics()}
	moralis := &stubMoralis{
		holders: ptrInt(42),
		price:   ptrFloat(0.005),
	}

	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		Moralis:   moralis,
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if moralis.holdersCalls.Load() != 1 {
		t.Errorf("Moralis holders calls = %d, want 1", moralis.holdersCalls.Load())
	}
	if moralis.priceCalls.Load() != 1 {
		t.Errorf("Moralis price calls = %d, want 1", moralis.priceCalls.Load())
	}
	if m.Holders == nil || *m.Holders != 42 {
		t.Errorf("Holders = %v, want 42", m.Holders)
	}
	if m.PriceUsd == nil || *m.PriceUsd != 0.005 {
		t.Errorf("PriceUsd = %v, want 0.005", m.PriceUsd)
	}
}

func TestMetrics_MoralisBondingPhase_NoCalls(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: bondingMetrics()} // complete=false
	moralis := &stubMoralis{holders: ptrInt(10), price: ptrFloat(0.01)}

	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		Moralis:   moralis,
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if moralis.holdersCalls.Load() != 0 {
		t.Errorf("Moralis holders calls = %d, want 0 (bonding phase)", moralis.holdersCalls.Load())
	}
	if moralis.priceCalls.Load() != 0 {
		t.Errorf("Moralis price calls = %d, want 0 (bonding phase)", moralis.priceCalls.Load())
	}
	if m.Holders != nil {
		t.Errorf("Holders = %v, want nil (bonding phase)", m.Holders)
	}
}

func TestMetrics_MoralisNilClient_NoFailure(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: graduatedMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		Moralis:   nil, // explicitly nil
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if m.Holders != nil {
		t.Errorf("Holders = %v, want nil (Moralis disabled)", m.Holders)
	}
	if m.PriceUsd != nil {
		t.Errorf("PriceUsd = %v, want nil (Moralis disabled)", m.PriceUsd)
	}
}

func TestMetrics_MoralisFailure_NullFields(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: graduatedMetrics()}
	moralis := &stubMoralis{
		holdersErr: context.DeadlineExceeded,
		priceErr:   context.DeadlineExceeded,
	}

	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		Moralis:   moralis,
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	// pump.fun data should still be present
	if m.MarketCapUsd == nil {
		t.Error("MarketCapUsd should be present from pump.fun")
	}
	// Moralis fields should be null
	if m.Holders != nil {
		t.Errorf("Holders = %v, want nil (Moralis failed)", m.Holders)
	}
	if m.PriceUsd != nil {
		t.Errorf("PriceUsd = %v, want nil (Moralis failed)", m.PriceUsd)
	}
}

func TestMetrics_Singleflight_DedupeOneExternalCall(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &slowPumpFun{metrics: bondingMetrics(), delay: 50 * time.Millisecond}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	var wg sync.WaitGroup
	const goroutines = 5
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = svc.GetTokenMetrics(context.Background(), "peer-1")
		}()
	}
	wg.Wait()

	calls := pump.callCount.Load()
	if calls != 1 {
		t.Errorf("pump.fun calls = %d, want 1 (singleflight should dedupe)", calls)
	}
}

func TestMetrics_PopulatesImageUrl_WhenNull(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: bondingMetrics()} // has imageUrl
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	_, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}

	// Give the fire-and-forget goroutine time to complete
	time.Sleep(100 * time.Millisecond)

	token, err := tokenRepo.GetByPeerID(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetByPeerID: %v", err)
	}
	if token.TokenImageURL != "https://example.com/img.png" {
		t.Errorf("TokenImageURL = %q, want https://example.com/img.png", token.TokenImageURL)
	}
}

func TestMetrics_SkipsImageUrl_WhenAlreadySet(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")
	// Pre-set the image URL
	_ = tokenRepo.UpdateImageURL(context.Background(), "peer-1", "https://old.com/old.png")

	pump := &stubPumpFun{metrics: bondingMetrics()} // has different imageUrl
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
	})

	_, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	token, err := tokenRepo.GetByPeerID(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetByPeerID: %v", err)
	}
	// Should keep the old URL — UpdateImageURL uses WHERE token_image_url IS NULL
	if token.TokenImageURL != "https://old.com/old.png" {
		t.Errorf("TokenImageURL = %q, want https://old.com/old.png (should not overwrite)", token.TokenImageURL)
	}
}

func TestMetrics_PumpFunFailed_MoralisFallback(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: nil, err: context.DeadlineExceeded}
	moralis := &stubMoralis{price: ptrFloat(0.003)}

	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		Moralis:   moralis,
		TokenRepo: tokenRepo,
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	// Moralis price should be used as fallback
	if m.PriceUsd == nil || *m.PriceUsd != 0.003 {
		t.Errorf("PriceUsd = %v, want 0.003 (Moralis fallback)", m.PriceUsd)
	}
	// Holders should NOT be called (not graduated, just fallback)
	if moralis.holdersCalls.Load() != 0 {
		t.Errorf("Moralis holders calls = %d, want 0 (fallback only fetches price)", moralis.holdersCalls.Load())
	}
}

// --- US-031-05: BatchGetCachedMetrics tests ---

func TestBatchGetCachedMetrics_RedisNil_ReturnsEmptyMap(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	pump := &stubPumpFun{metrics: bondingMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
		Redis:     nil, // no Redis
	})

	result := svc.BatchGetCachedMetrics(context.Background(), []string{"contract-1", "contract-2"})
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0 (no Redis)", len(result))
	}
}

func TestBatchGetCachedMetrics_EmptySlice_ReturnsEmptyMap(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	pump := &stubPumpFun{metrics: bondingMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
		Redis:     nil,
	})

	result := svc.BatchGetCachedMetrics(context.Background(), []string{})
	if result == nil {
		t.Error("result should be non-nil empty map, got nil")
	}
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0", len(result))
	}
}

func TestMetrics_CacheRedisNil_NoPanic(t *testing.T) {
	tokenRepo := repository.NewMemoryTokenRepository()
	seedToken(t, tokenRepo, "peer-1", "CONTRACT1")

	pump := &stubPumpFun{metrics: bondingMetrics()}
	svc := NewMetricsService(MetricsServiceDeps{
		PumpFun:   pump,
		TokenRepo: tokenRepo,
		Redis:     nil, // explicitly nil
	})

	m, err := svc.GetTokenMetrics(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("GetTokenMetrics: %v", err)
	}
	if m.MarketCapUsd == nil {
		t.Error("MarketCapUsd should be present even without Redis")
	}
}
