package services

import (
	"context"
	"errors"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
)

type fakeHolderCounter struct {
	byProgram map[string]int
	err       error
	calls     []string
}

func (f *fakeHolderCounter) CountTokenHolders(_ context.Context, _ string, program string) (int, error) {
	f.calls = append(f.calls, program)
	if f.err != nil {
		return 0, f.err
	}
	return f.byProgram[program], nil
}

type holdersLaunchLab struct{ m *models.TokenMetrics }

func (h holdersLaunchLab) GetCoinData(context.Context, string, LaunchLabHint) (*models.TokenMetrics, error) {
	return h.m, nil
}

func TestOnCurveHoldersComeFromChain(t *testing.T) {
	counter := &fakeHolderCounter{byProgram: map[string]int{tokenProgram2022: 16}}
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: holdersLaunchLab{&models.TokenMetrics{}}, Holders: counter, DefaultSource: models.TokenSourceLaunchLab})
	m, err := svc.fetchMetrics(context.Background(), "2161TxLYzfZabXowZGHeE5oNMYC3HsQ2SKqRQfdc23ki", models.TokenSourceLaunchLab, LaunchLabHint{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Holders == nil || *m.Holders != 16 {
		t.Fatalf("want 16 holders from chain, got %v", m.Holders)
	}
	if len(counter.calls) != 1 || counter.calls[0] != tokenProgram2022 {
		t.Fatalf("Token-2022 first, got %v", counter.calls)
	}
}

func TestOnCurveHoldersFallBackToClassicProgram(t *testing.T) {
	counter := &fakeHolderCounter{byProgram: map[string]int{tokenProgram2022: 0, tokenProgramClassic: 7}}
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: holdersLaunchLab{&models.TokenMetrics{}}, Holders: counter, DefaultSource: models.TokenSourceLaunchLab})
	m, _ := svc.fetchMetrics(context.Background(), "HzJh3iPHf8u5pauuTD8Am6MMCNDqHeiwm36aCRSwF6MU", models.TokenSourceLaunchLab, LaunchLabHint{})
	if m.Holders == nil || *m.Holders != 7 {
		t.Fatalf("want 7 from the classic program, got %v", m.Holders)
	}
}

func TestServedHoldersAreKeptAndErrorsLeaveNil(t *testing.T) {
	served := 42
	counter := &fakeHolderCounter{byProgram: map[string]int{tokenProgram2022: 1}}
	svc := NewMetricsService(MetricsServiceDeps{LaunchLab: holdersLaunchLab{&models.TokenMetrics{Holders: &served}}, Holders: counter, DefaultSource: models.TokenSourceLaunchLab})
	m, _ := svc.fetchMetrics(context.Background(), "mintA", models.TokenSourceLaunchLab, LaunchLabHint{})
	if m.Holders == nil || *m.Holders != 42 || len(counter.calls) != 0 {
		t.Fatalf("a served count must win and skip the chain, got %v calls=%v", m.Holders, counter.calls)
	}
	failing := &fakeHolderCounter{err: errors.New("rpc down")}
	svc = NewMetricsService(MetricsServiceDeps{LaunchLab: holdersLaunchLab{&models.TokenMetrics{}}, Holders: failing, DefaultSource: models.TokenSourceLaunchLab})
	m, _ = svc.fetchMetrics(context.Background(), "mintB", models.TokenSourceLaunchLab, LaunchLabHint{})
	if m.Holders != nil {
		t.Fatalf("a failed count must leave holders nil, got %v", *m.Holders)
	}
}
