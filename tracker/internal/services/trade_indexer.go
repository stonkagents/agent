// Package: tracker/internal/services
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: Background poller that turns the transactions of every launch's bonding-curve pool
//          into launch_trades rows (buy/sell, amounts, price) and the burns of the network
//          token into launch_burns rows, advancing a per-address signature cursor so each
//          tick only asks the RPC for what is new.
//
// Cost per tick, per pool with N new successful trades: 1 getSignaturesForAddress (+1 per
// extra page of 1000) + N getTransaction; failed signatures are skipped without a fetch and
// a pool's vault addresses are read once (getMultipleAccounts, batched across pools) and
// cached for the process lifetime. A quiet pool costs exactly one call per tick.

package services

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"golang.org/x/sync/singleflight"
)

// Indexer defaults (INDEXER_INTERVAL overrides the interval).
const (
	DefaultIndexerInterval     = 30 * time.Second
	DefaultIndexerMaxPools     = 50
	DefaultIndexerConcurrency  = 4 // getTransaction calls in flight per pool
	DefaultIndexerPoolWorkers  = 2 // pools indexed at once
	DefaultIndexerSignatureCap = 1000
	DefaultIndexerMaxPages     = 3 // signature pages per address per tick (3000 signatures)
	DefaultIndexerMaxErrors    = 3 // consecutive fetch errors that stop an address for this tick
	DefaultIndexerTickTimeout  = 25 * time.Second
	indexerChunkSize           = 20
	indexerRateLimitBackoff    = 2 * time.Second
	indexerRateLimitMaxBackoff = 8 * time.Second
	burnCursorPrefix           = "burns:"
)

// IndexedSignature is one entry of getSignaturesForAddress (newest first).
type IndexedSignature struct {
	Signature string
	Slot      int64
	BlockTime *time.Time
	// Failed is true when the transaction errored on chain (meta.err set); never fetched.
	Failed bool
}

// SignatureQuery bounds a getSignaturesForAddress call: newest first, strictly after Until
// (exclusive) and before Before (exclusive) when set.
type SignatureQuery struct {
	Until  string
	Before string
	Limit  int
}

// PoolVaults are the token accounts a LaunchLab pool swaps through.
type PoolVaults struct {
	PoolID string
	MintA  string // base (the launched token)
	MintB  string // quote
	VaultA string
	VaultB string
}

// PoolSwap is a swap derived from one transaction's vault balance deltas.
type PoolSwap struct {
	Signature   string
	Slot        int64
	BlockTime   time.Time
	Side        string // models.TradeSideBuy | models.TradeSideSell
	Trader      string // fee payer (first signer)
	BaseAmount  float64
	QuoteAmount float64
}

// TokenBurn is a burn / burnChecked of mint found in one transaction, amount in whole tokens.
type TokenBurn struct {
	Signature string
	Slot      int64
	BlockTime time.Time
	Amount    float64
	Burner    string
}

// TradeIndexSource is the RPC surface the indexer reads from (implemented by solana.TradeSource).
type TradeIndexSource interface {
	// SignaturesForAddress lists confirmed signatures touching address, newest first.
	SignaturesForAddress(ctx context.Context, address string, q SignatureQuery) ([]IndexedSignature, error)
	// PoolSwap fetches the transaction and derives the swap against vaults.
	// (nil, nil) when the transaction is not a swap, failed, or is unknown to the node.
	PoolSwap(ctx context.Context, signature string, vaults PoolVaults) (*PoolSwap, error)
	// TokenBurn fetches the transaction and sums its burn instructions for mint. (nil, nil) when none.
	TokenBurn(ctx context.Context, signature, mint string) (*TokenBurn, error)
}

// PoolVaultSource resolves pool ids to their vault addresses.
type PoolVaultSource interface {
	PoolVaults(ctx context.Context, poolIDs []string) (map[string]PoolVaults, error)
}

// AccountPoolVaultSource reads and decodes LaunchLab pool accounts through an AccountReader.
type AccountPoolVaultSource struct {
	rpc AccountReader
}

// NewAccountPoolVaultSource creates a PoolVaultSource over rpc.
func NewAccountPoolVaultSource(rpc AccountReader) *AccountPoolVaultSource {
	return &AccountPoolVaultSource{rpc: rpc}
}

// PoolVaults implements PoolVaultSource: at most 100 pools per RPC call; pools that do not
// exist or do not decode are omitted.
func (s *AccountPoolVaultSource) PoolVaults(ctx context.Context, poolIDs []string) (map[string]PoolVaults, error) {
	out := make(map[string]PoolVaults, len(poolIDs))
	for start := 0; start < len(poolIDs); start += 100 {
		end := start + 100
		if end > len(poolIDs) {
			end = len(poolIDs)
		}
		batch := poolIDs[start:end]
		accounts, err := s.rpc.GetMultipleAccounts(ctx, batch)
		if err != nil {
			return nil, err
		}
		for i, acc := range accounts {
			if acc == nil || i >= len(batch) {
				continue
			}
			pool, err := DecodeLaunchLabPool(acc.Data)
			if err != nil {
				continue
			}
			out[batch[i]] = PoolVaults{PoolID: batch[i], MintA: pool.MintA, MintB: pool.MintB, VaultA: pool.VaultA, VaultB: pool.VaultB}
		}
	}
	return out, nil
}

// TradeIndexerConfig tunes the indexer. Zero values take the defaults above.
type TradeIndexerConfig struct {
	Interval     time.Duration
	MaxPools     int
	Concurrency  int
	PoolWorkers  int
	SignatureCap int
	MaxPages     int
	MaxErrors    int
	TickTimeout  time.Duration
	// AgentMint is the network token whose burns are indexed; empty = burns off.
	AgentMint string
	Logger    *slog.Logger
}

// TradeIndexerDeps are the indexer's collaborators.
type TradeIndexerDeps struct {
	Launches repository.LaunchRepository
	Trades   repository.LaunchTradeRepository
	Burns    repository.LaunchBurnRepository  // optional; required for AgentMint
	Quotes   repository.LaunchQuoteRepository // optional; fills quote_symbol
	Source   TradeIndexSource
	Vaults   PoolVaultSource
}

// TickStats summarises one indexer tick.
type TickStats struct {
	Pools      int
	Signatures int
	Trades     int
	Burns      int
	Errors     int
	Duration   time.Duration
}

// TradeIndexer polls every recorded launch's pool for new swaps (and the network token for
// burns) on a fixed interval. Concurrent indexing of one address is coalesced (singleflight).
type TradeIndexer struct {
	d      TradeIndexerDeps
	cfg    TradeIndexerConfig
	logger *slog.Logger
	sf     singleflight.Group

	mu      sync.Mutex
	vaults  map[string]PoolVaults // pool id → vaults, never changes once known
	symbols map[string]string     // quote mint → symbol
}

// NewTradeIndexer creates an indexer; call Run to start it.
func NewTradeIndexer(d TradeIndexerDeps, cfg TradeIndexerConfig) *TradeIndexer {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultIndexerInterval
	}
	if cfg.MaxPools <= 0 {
		cfg.MaxPools = DefaultIndexerMaxPools
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultIndexerConcurrency
	}
	if cfg.PoolWorkers <= 0 {
		cfg.PoolWorkers = DefaultIndexerPoolWorkers
	}
	if cfg.SignatureCap <= 0 {
		cfg.SignatureCap = DefaultIndexerSignatureCap
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = DefaultIndexerMaxPages
	}
	if cfg.MaxErrors <= 0 {
		cfg.MaxErrors = DefaultIndexerMaxErrors
	}
	if cfg.TickTimeout <= 0 {
		cfg.TickTimeout = DefaultIndexerTickTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &TradeIndexer{
		d: d, cfg: cfg, logger: cfg.Logger,
		vaults:  make(map[string]PoolVaults),
		symbols: make(map[string]string),
	}
}

// Run blocks until ctx is done: one tick immediately, then every Interval.
func (x *TradeIndexer) Run(ctx context.Context) {
	x.logTick(x.Tick(ctx))
	ticker := time.NewTicker(x.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			x.logTick(x.Tick(ctx))
		}
	}
}

// Tick indexes the newest MaxPools launches with a pool, then the network token's burns.
func (x *TradeIndexer) Tick(parent context.Context) TickStats {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, x.cfg.TickTimeout)
	defer cancel()

	var st TickStats
	launches, _, err := x.d.Launches.List(ctx, repository.ListLaunchesOptions{Limit: x.cfg.MaxPools})
	if err != nil {
		x.logger.Warn("[trade-indexer] listing launches failed", "error", err)
		st.Errors++
		st.Duration = time.Since(start)
		return st
	}
	var pooled []*models.TokenLaunch
	for _, l := range launches {
		if l.PoolID != "" {
			pooled = append(pooled, l)
		}
	}
	if err := x.ensureVaults(ctx, pooled); err != nil {
		x.logger.Warn("[trade-indexer] pool vault lookup failed", "error", err)
		st.Errors++
	}

	// Signatures indexed as swaps this tick: the burn pass skips them (a swap of the network
	// token shows up under its mint too, and fetching it twice buys nothing).
	seen := &sync.Map{}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	sem := make(chan struct{}, x.cfg.PoolWorkers)
	for _, l := range pooled {
		if _, ok := x.vaultsFor(l.PoolID); !ok {
			continue
		}
		st.Pools++
		wg.Add(1)
		go func(l *models.TokenLaunch) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			ps := x.indexPool(ctx, l, seen)
			mu.Lock()
			st.Signatures += ps.Signatures
			st.Trades += ps.Trades
			st.Errors += ps.Errors
			mu.Unlock()
		}(l)
	}
	wg.Wait()

	if x.cfg.AgentMint != "" && x.d.Burns != nil && ctx.Err() == nil {
		bs := x.indexBurns(ctx, x.cfg.AgentMint, seen)
		st.Signatures += bs.Signatures
		st.Burns += bs.Burns
		st.Errors += bs.Errors
	}
	st.Duration = time.Since(start)
	return st
}

func (x *TradeIndexer) logTick(st TickStats) {
	x.logger.Info("[trade-indexer] tick complete", "pools", st.Pools, "signatures", st.Signatures,
		"trades", st.Trades, "burns", st.Burns, "errors", st.Errors, "duration", st.Duration.Round(time.Millisecond))
}

// ensureVaults resolves vault addresses for pools not seen before (one batched RPC call).
func (x *TradeIndexer) ensureVaults(ctx context.Context, launches []*models.TokenLaunch) error {
	var missing []string
	x.mu.Lock()
	for _, l := range launches {
		if _, ok := x.vaults[l.PoolID]; !ok {
			missing = append(missing, l.PoolID)
		}
	}
	x.mu.Unlock()
	if len(missing) == 0 || x.d.Vaults == nil {
		return nil
	}
	found, err := x.d.Vaults.PoolVaults(ctx, missing)
	if err != nil {
		return err
	}
	x.mu.Lock()
	for id, v := range found {
		x.vaults[id] = v
	}
	x.mu.Unlock()
	return nil
}

func (x *TradeIndexer) vaultsFor(poolID string) (PoolVaults, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	v, ok := x.vaults[poolID]
	return v, ok
}

// quoteSymbol resolves (and caches) the symbol of a quote mint; "" when unknown.
func (x *TradeIndexer) quoteSymbol(ctx context.Context, quoteMint string) string {
	if quoteMint == "" || x.d.Quotes == nil {
		return ""
	}
	x.mu.Lock()
	sym, ok := x.symbols[quoteMint]
	x.mu.Unlock()
	if ok {
		return sym
	}
	if q, err := x.d.Quotes.GetByMint(ctx, quoteMint); err == nil && q != nil {
		sym = q.Symbol
		x.mu.Lock()
		x.symbols[quoteMint] = sym
		x.mu.Unlock()
	}
	return sym
}

// fetchNewSignatures returns the signatures after the cursor, oldest first, paging with
// `before` up to MaxPages. When the pages run out before reaching the cursor, older
// signatures are skipped (logged) rather than blocking the address forever.
func (x *TradeIndexer) fetchNewSignatures(ctx context.Context, address, until string) ([]IndexedSignature, error) {
	var all []IndexedSignature
	before := ""
	for page := 0; page < x.cfg.MaxPages; page++ {
		sigs, err := x.d.Source.SignaturesForAddress(ctx, address, SignatureQuery{Until: until, Before: before, Limit: x.cfg.SignatureCap})
		if err != nil {
			return nil, err
		}
		all = append(all, sigs...)
		if len(sigs) < x.cfg.SignatureCap {
			break
		}
		before = sigs[len(sigs)-1].Signature
		if page == x.cfg.MaxPages-1 {
			x.logger.Warn("[trade-indexer] signature backlog exceeds page budget; older signatures skipped",
				"address", address, "fetched", len(all))
		}
	}
	// Oldest first so the cursor can advance chunk by chunk.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}

// indexPool processes one pool under singleflight: new signatures → swaps → rows → cursor.
func (x *TradeIndexer) indexPool(ctx context.Context, l *models.TokenLaunch, seen *sync.Map) TickStats {
	res, _, _ := x.sf.Do(l.PoolID, func() (interface{}, error) {
		return x.indexAddress(ctx, l.PoolID, l.PoolID, func(chunk []IndexedSignature) ([]interface{}, error) {
			vaults, _ := x.vaultsFor(l.PoolID)
			symbol := x.quoteSymbol(ctx, l.QuoteMint)
			swaps, err := x.fetchChunk(ctx, chunk, func(sig IndexedSignature) (interface{}, error) {
				s, err := x.d.Source.PoolSwap(ctx, sig.Signature, vaults)
				if err != nil || s == nil {
					return nil, err
				}
				seen.Store(sig.Signature, struct{}{})
				return s, nil
			})
			if err != nil {
				return nil, err
			}
			trades := make([]*models.LaunchTrade, 0, len(swaps))
			for _, v := range swaps {
				s := v.(*PoolSwap)
				price := 0.0
				if s.BaseAmount > 0 {
					price = s.QuoteAmount / s.BaseAmount
				}
				trades = append(trades, &models.LaunchTrade{
					Mint: l.Mint, PoolID: l.PoolID, Signature: s.Signature, Slot: s.Slot, BlockTime: s.BlockTime.UTC(),
					Side: s.Side, Trader: s.Trader, BaseAmount: s.BaseAmount, QuoteAmount: s.QuoteAmount,
					PriceQuote: price, QuoteSymbol: symbol,
				})
			}
			n, err := x.d.Trades.InsertTrades(ctx, trades)
			if err != nil {
				return nil, err
			}
			return make([]interface{}, n), nil
		})
	})
	st, _ := res.(TickStats)
	return st
}

// indexBurns processes the network token mint: new signatures → burns → rows → cursor.
func (x *TradeIndexer) indexBurns(ctx context.Context, mint string, seen *sync.Map) TickStats {
	key := burnCursorPrefix + mint
	res, _, _ := x.sf.Do(key, func() (interface{}, error) {
		st, err := x.indexAddress(ctx, mint, key, func(chunk []IndexedSignature) ([]interface{}, error) {
			burns, err := x.fetchChunk(ctx, chunk, func(sig IndexedSignature) (interface{}, error) {
				if _, swap := seen.Load(sig.Signature); swap {
					return nil, nil
				}
				b, err := x.d.Source.TokenBurn(ctx, sig.Signature, mint)
				if err != nil || b == nil {
					return nil, err
				}
				return b, nil
			})
			if err != nil {
				return nil, err
			}
			rows := make([]*models.LaunchBurn, 0, len(burns))
			for _, v := range burns {
				b := v.(*TokenBurn)
				rows = append(rows, &models.LaunchBurn{
					Mint: mint, Signature: b.Signature, Slot: b.Slot, BlockTime: b.BlockTime.UTC(), Amount: b.Amount, Burner: b.Burner,
				})
			}
			n, err := x.d.Burns.InsertBurns(ctx, rows)
			if err != nil {
				return nil, err
			}
			return make([]interface{}, n), nil
		})
		st.Burns, st.Trades = st.Trades, 0
		return st, err
	})
	st, _ := res.(TickStats)
	return st
}

// indexAddress walks the new signatures of address in chunks; store turns a chunk into
// stored rows (its length is the count). The cursor advances after each stored chunk, so a
// chunk that fails is retried in full next tick and nothing between the cursor and the
// newest signature is ever skipped. Successful trades are counted in TickStats.Trades.
func (x *TradeIndexer) indexAddress(ctx context.Context, address, cursorKey string, store func([]IndexedSignature) ([]interface{}, error)) (TickStats, error) {
	var st TickStats
	until := ""
	if c, err := x.d.Trades.GetCursor(ctx, cursorKey); err == nil {
		until = c.LastSignature
	} else if !errors.Is(err, models.ErrNotFound) {
		st.Errors++
		return st, err
	}
	sigs, err := x.fetchNewSignatures(ctx, address, until)
	if err != nil {
		x.logger.Warn("[trade-indexer] signature fetch failed", "address", address, "error", err)
		st.Errors++
		return st, err
	}
	st.Signatures = len(sigs)
	for start := 0; start < len(sigs); start += indexerChunkSize {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		end := start + indexerChunkSize
		if end > len(sigs) {
			end = len(sigs)
		}
		chunk := sigs[start:end]
		stored, err := store(chunk)
		if err != nil {
			x.logger.Warn("[trade-indexer] chunk failed; cursor kept", "address", address, "from", chunk[0].Signature, "error", err)
			st.Errors++
			return st, err
		}
		st.Trades += len(stored)
		last := chunk[len(chunk)-1]
		if err := x.d.Trades.UpsertCursor(ctx, &models.LaunchIndexCursor{Key: cursorKey, LastSignature: last.Signature, LastBlockTime: last.BlockTime}); err != nil {
			st.Errors++
			return st, err
		}
	}
	return st, nil
}

// fetchChunk fetches a chunk of signatures with bounded concurrency, skipping failed ones and
// backing off on rate limits. Any fetch error after backoff fails the whole chunk; results are
// returned in signature order.
func (x *TradeIndexer) fetchChunk(ctx context.Context, chunk []IndexedSignature, fetch func(IndexedSignature) (interface{}, error)) ([]interface{}, error) {
	results := make([]interface{}, len(chunk))
	errs := make([]error, len(chunk))
	sem := make(chan struct{}, x.cfg.Concurrency)
	var wg sync.WaitGroup
	for i, sig := range chunk {
		if sig.Failed {
			continue
		}
		wg.Add(1)
		go func(i int, sig IndexedSignature) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			results[i], errs[i] = x.fetchWithBackoff(ctx, sig, fetch)
		}(i, sig)
	}
	wg.Wait()
	out := make([]interface{}, 0, len(chunk))
	errCount := 0
	var firstErr error
	for i := range chunk {
		if errs[i] != nil {
			errCount++
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		if results[i] != nil {
			out = append(out, results[i])
		}
	}
	if errCount > 0 {
		return nil, firstErr
	}
	return out, nil
}

// fetchWithBackoff retries a rate-limited fetch with growing pauses, up to MaxErrors attempts.
func (x *TradeIndexer) fetchWithBackoff(ctx context.Context, sig IndexedSignature, fetch func(IndexedSignature) (interface{}, error)) (interface{}, error) {
	backoff := indexerRateLimitBackoff
	var err error
	for attempt := 0; attempt < x.cfg.MaxErrors; attempt++ {
		var v interface{}
		v, err = fetch(sig)
		if err == nil {
			return v, nil
		}
		if !isRateLimited(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > indexerRateLimitMaxBackoff {
			backoff = indexerRateLimitMaxBackoff
		}
	}
	return nil, err
}

func isRateLimited(err error) bool {
	return errors.Is(err, ErrRPCRateLimited) || strings.Contains(err.Error(), "429")
}
