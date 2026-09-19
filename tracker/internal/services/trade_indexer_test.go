// Package: tracker/internal/services
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: Indexer ticks against a fake RPC source and the memory repositories — cursor
//          idempotency, skipped failures, chunk failure keeps the cursor, burn indexing.

package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

const (
	ixMint  = "Mint1111111111111111111111111111111111111111"
	ixPool  = "Poo11111111111111111111111111111111111111111"
	ixQuote = "Quote111111111111111111111111111111111111111"
	ixAgent = "Agent11111111111111111111111111111111111111"
)

// fakeTradeSource serves a fixed newest-first signature list per address and swaps/burns by signature.
type fakeTradeSource struct {
	mu        sync.Mutex
	sigs      map[string][]IndexedSignature // address → newest first
	swaps     map[string]*PoolSwap
	burns     map[string]*TokenBurn
	failSig   map[string]error
	sigCalls  int
	txCalls   int
	lastQuery SignatureQuery
}

func newFakeSource() *fakeTradeSource {
	return &fakeTradeSource{sigs: map[string][]IndexedSignature{}, swaps: map[string]*PoolSwap{}, burns: map[string]*TokenBurn{}, failSig: map[string]error{}}
}

func (f *fakeTradeSource) SignaturesForAddress(_ context.Context, address string, q SignatureQuery) ([]IndexedSignature, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigCalls++
	f.lastQuery = q
	var out []IndexedSignature
	for _, s := range f.sigs[address] {
		if s.Signature == q.Until {
			break
		}
		out = append(out, s)
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (f *fakeTradeSource) PoolSwap(_ context.Context, sig string, _ PoolVaults) (*PoolSwap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txCalls++
	if err := f.failSig[sig]; err != nil {
		return nil, err
	}
	return f.swaps[sig], nil
}

func (f *fakeTradeSource) TokenBurn(_ context.Context, sig, _ string) (*TokenBurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txCalls++
	if err := f.failSig[sig]; err != nil {
		return nil, err
	}
	return f.burns[sig], nil
}

type staticVaults map[string]PoolVaults

func (v staticVaults) PoolVaults(_ context.Context, ids []string) (map[string]PoolVaults, error) {
	out := map[string]PoolVaults{}
	for _, id := range ids {
		if pv, ok := v[id]; ok {
			out[id] = pv
		}
	}
	return out, nil
}

type indexerEnv struct {
	launches *repository.MemoryLaunchRepository
	trades   *repository.MemoryLaunchTradeRepository
	burns    *repository.MemoryLaunchBurnRepository
	src      *fakeTradeSource
	idx      *TradeIndexer
}

func newIndexerEnv(t *testing.T, agentMint string) *indexerEnv {
	t.Helper()
	env := &indexerEnv{
		launches: repository.NewMemoryLaunchRepository(),
		trades:   repository.NewMemoryLaunchTradeRepository(),
		burns:    repository.NewMemoryLaunchBurnRepository(),
		src:      newFakeSource(),
	}
	_ = env.launches.Create(context.Background(), &models.TokenLaunch{
		Mint: ixMint, PoolID: ixPool, CreatorWallet: "c", QuoteMint: ixQuote, LaunchSignature: "launchsig", CreatedAt: time.Now(),
	})
	quotes := repository.NewMemoryLaunchQuoteRepository()
	quotes.Put(&models.LaunchQuote{QuoteMint: ixQuote, Symbol: "USDC", Decimals: 6, Enabled: true})
	env.idx = NewTradeIndexer(TradeIndexerDeps{
		Launches: env.launches, Trades: env.trades, Burns: env.burns, Quotes: quotes, Source: env.src,
		Vaults: staticVaults{ixPool: {PoolID: ixPool, MintA: ixMint, MintB: ixQuote, VaultA: "vA", VaultB: "vB"}},
	}, TradeIndexerConfig{AgentMint: agentMint, MaxErrors: 1})
	return env
}

// addSwaps appends n newest-first signatures (sigN..sig1) with buys 1 quote each.
func (e *indexerEnv) addSwaps(address string, from, to int, base time.Time) {
	var list []IndexedSignature
	for i := to; i >= from; i-- {
		sig := fmt.Sprintf("sig%d", i)
		bt := base.Add(time.Duration(i) * time.Minute)
		list = append(list, IndexedSignature{Signature: sig, Slot: int64(i), BlockTime: &bt})
		e.src.swaps[sig] = &PoolSwap{Signature: sig, Slot: int64(i), BlockTime: bt, Side: models.TradeSideBuy, Trader: "t", BaseAmount: 100, QuoteAmount: 1}
	}
	e.src.sigs[address] = append(list, e.src.sigs[address]...)
}

func TestTradeIndexer_TickStoresTradesAndAdvancesCursor(t *testing.T) {
	env := newIndexerEnv(t, "")
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	env.addSwaps(ixPool, 1, 45, base)
	// One failed signature in the list: skipped without a fetch.
	bt := base.Add(30 * time.Minute)
	env.src.sigs[ixPool] = append([]IndexedSignature{{Signature: "failed", Slot: 46, BlockTime: &bt, Failed: true}}, env.src.sigs[ixPool]...)

	st := env.idx.Tick(context.Background())
	if st.Pools != 1 || st.Signatures != 46 || st.Trades != 45 || st.Errors != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if env.trades.Count() != 45 {
		t.Errorf("stored = %d, want 45", env.trades.Count())
	}
	if env.src.txCalls != 45 {
		t.Errorf("getTransaction calls = %d, want 45 (failed signature not fetched)", env.src.txCalls)
	}
	c, err := env.trades.GetCursor(context.Background(), ixPool)
	if err != nil || c.LastSignature != "failed" {
		t.Fatalf("cursor = %+v err = %v (want newest signature 'failed')", c, err)
	}
	rows, _ := env.trades.ListTrades(context.Background(), ixMint, 1, nil)
	if len(rows) != 1 || rows[0].Signature != "sig45" || rows[0].PriceQuote != 0.01 || rows[0].QuoteSymbol != "USDC" || rows[0].Mint != ixMint {
		t.Errorf("newest = %+v", rows[0])
	}

	// Second tick: nothing new → one signature call with until=cursor, no transaction fetches.
	env.src.txCalls, env.src.sigCalls = 0, 0
	st = env.idx.Tick(context.Background())
	if st.Trades != 0 || st.Signatures != 0 || env.src.txCalls != 0 || env.src.sigCalls != 1 || env.src.lastQuery.Until != "failed" {
		t.Errorf("quiet tick: stats=%+v txCalls=%d sigCalls=%d until=%q", st, env.src.txCalls, env.src.sigCalls, env.src.lastQuery.Until)
	}
}

func TestTradeIndexer_ReplayIsIdempotent(t *testing.T) {
	env := newIndexerEnv(t, "")
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	env.addSwaps(ixPool, 1, 5, base)
	env.idx.Tick(context.Background())
	// Cursor lost (e.g. reset): the same signatures come back; rows must not duplicate.
	_ = env.trades.UpsertCursor(context.Background(), &models.LaunchIndexCursor{Key: ixPool, LastSignature: "unknown"})
	st := env.idx.Tick(context.Background())
	if st.Trades != 0 || env.trades.Count() != 5 {
		t.Errorf("replay stored %d new (total %d), want 0 new / 5 total", st.Trades, env.trades.Count())
	}
}

func TestTradeIndexer_ChunkFailureKeepsCursor(t *testing.T) {
	env := newIndexerEnv(t, "")
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	env.addSwaps(ixPool, 1, 30, base) // two chunks of 20 + 10 (oldest first)
	env.src.failSig["sig25"] = errors.New("boom")

	st := env.idx.Tick(context.Background())
	if st.Errors == 0 {
		t.Fatal("expected an error")
	}
	// First chunk (sig1..sig20) stored and cursor advanced to sig20; second chunk not committed.
	c, err := env.trades.GetCursor(context.Background(), ixPool)
	if err != nil || c.LastSignature != "sig20" {
		t.Fatalf("cursor = %+v err = %v, want sig20", c, err)
	}
	if env.trades.Count() != 20 {
		t.Errorf("stored = %d, want 20", env.trades.Count())
	}
	// Next tick with the error gone finishes the job from sig21.
	delete(env.src.failSig, "sig25")
	st = env.idx.Tick(context.Background())
	if st.Trades != 10 || env.trades.Count() != 30 {
		t.Errorf("resume: stats=%+v total=%d", st, env.trades.Count())
	}
}

func TestTradeIndexer_IndexesBurnsOfAgentMint(t *testing.T) {
	env := newIndexerEnv(t, ixAgent)
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	env.addSwaps(ixPool, 1, 2, base)
	b1, b2 := base.Add(time.Hour), base.Add(2*time.Hour)
	env.src.sigs[ixAgent] = []IndexedSignature{
		{Signature: "burn2", Slot: 200, BlockTime: &b2},
		{Signature: "sig2", Slot: 2, BlockTime: &base}, // a swap also appears under the mint: skipped, not refetched
		{Signature: "burn1", Slot: 100, BlockTime: &b1},
		{Signature: "transfer", Slot: 50, BlockTime: &b1}, // not a burn
	}
	env.src.burns["burn1"] = &TokenBurn{Signature: "burn1", Slot: 100, BlockTime: b1, Amount: 250000, Burner: "keeper"}
	env.src.burns["burn2"] = &TokenBurn{Signature: "burn2", Slot: 200, BlockTime: b2, Amount: 250000, Burner: "keeper"}

	st := env.idx.Tick(context.Background())
	if st.Burns != 2 || st.Trades != 2 || st.Errors != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if env.src.txCalls != 2+3 {
		t.Errorf("tx fetches = %d, want 5 (2 swaps + burn2 + burn1 + transfer; sig2 deduplicated)", env.src.txCalls)
	}
	sum, _ := env.burns.Summary(context.Background(), ixAgent)
	if sum.Burned != 500000 || sum.Burns != 2 {
		t.Errorf("summary = %+v", sum)
	}
	c, err := env.trades.GetCursor(context.Background(), "burns:"+ixAgent)
	if err != nil || c.LastSignature != "burn2" {
		t.Errorf("burn cursor = %+v err = %v", c, err)
	}
	// Quiet tick: no burn fetches.
	env.src.txCalls = 0
	env.idx.Tick(context.Background())
	if env.src.txCalls != 0 {
		t.Errorf("quiet tick fetched %d transactions", env.src.txCalls)
	}
}
