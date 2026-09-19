// Package: tracker/internal/services
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: Read side of the indexed trades — the 24h block on launch metrics (price change,
//          volume, trade count), the newest-first trade feed and OHLC candles for the
//          detail page.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Trade read defaults and caps.
const (
	TradeStatsWindow      = 24 * time.Hour
	DefaultTradePageLimit = 50
	MaxTradePageLimit     = 200
	DefaultCandleLimit    = 100
	MaxCandleLimit        = 500
	maxCandleRows         = 10000
	DefaultCandleInterval = "5m"
)

// ErrInvalidCandleInterval is returned for an interval outside 1m|5m|15m|1h|1d.
var ErrInvalidCandleInterval = errors.New("interval must be one of 1m, 5m, 15m, 1h, 1d")

// ErrInvalidTradeCursor is returned for a cursor that did not come from a previous page.
var ErrInvalidTradeCursor = errors.New("invalid cursor")

// candleIntervals maps the accepted interval names to durations.
var candleIntervals = map[string]time.Duration{
	"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour, "1d": 24 * time.Hour,
}

// QuoteUsdSource prices one whole quote unit in USD (implemented by *HTTPRaydiumClient,
// devnet fallback included). Optional: without it the USD volume is implied from the
// launch's own priceUsd / priceQuote when both are known.
type QuoteUsdSource interface {
	QuoteUsdPrice(ctx context.Context, quoteMint string) (float64, bool)
}

// LaunchTradeStats is the trade-derived block of a launch's metrics. Every field is nil
// when unknown (no trade of that age, no USD price for the quote, ...).
type LaunchTradeStats struct {
	PriceChange24hPct *float64
	Volume24hQuote    *float64
	Volume24hUsd      *float64
	Trades24h         *int
}

// TradeStatsInput is what Stats24h needs to know about a launch: its live curve price
// (PriceQuote, nil once frozen) and USD price so the change and USD volume line up with
// the numbers already on the card.
type TradeStatsInput struct {
	Mint       string
	QuoteMint  string
	PriceQuote *float64
	PriceUsd   *float64
}

// Candle is one OHLC bucket in quote units; T is the bucket start (unix seconds) and V the
// quote volume.
type Candle struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
}

// LaunchTradeService reads indexed trades for launch views and the detail endpoints.
type LaunchTradeService struct {
	trades   repository.LaunchTradeRepository
	quoteUsd QuoteUsdSource
	clock    clock.Clock
	logger   *slog.Logger
}

// NewLaunchTradeService creates a LaunchTradeService. quoteUsd and clk may be nil.
func NewLaunchTradeService(trades repository.LaunchTradeRepository, quoteUsd QuoteUsdSource, clk clock.Clock, logger *slog.Logger) *LaunchTradeService {
	if clk == nil {
		clk = clock.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LaunchTradeService{trades: trades, quoteUsd: quoteUsd, clock: clk, logger: logger}
}

// Stats24h returns the 24h trade block per mint. Mints with no indexed trade at all are
// omitted, so a card can tell "no data" from "zero". Never fails: a repository error logs
// and yields an empty map.
func (s *LaunchTradeService) Stats24h(ctx context.Context, inputs []TradeStatsInput) map[string]*LaunchTradeStats {
	out := make(map[string]*LaunchTradeStats, len(inputs))
	if s == nil || len(inputs) == 0 {
		return out
	}
	mints := make([]string, 0, len(inputs))
	for _, in := range inputs {
		mints = append(mints, in.Mint)
	}
	now := s.clock.Now().UTC()
	windows, err := s.trades.AggregateWindow(ctx, mints, now.Add(-TradeStatsWindow), now)
	if err != nil {
		s.logger.Warn("[LaunchTradeService.Stats24h] aggregate failed", "error", err)
		return out
	}
	usdCache := make(map[string]*float64)
	for _, in := range inputs {
		w, ok := windows[in.Mint]
		if !ok || w == nil {
			continue
		}
		st := &LaunchTradeStats{}
		vol := w.VolumeQuote
		n := w.Trades
		st.Volume24hQuote = &vol
		st.Trades24h = &n

		// Price now: the live curve price when the pool is still funding, else the last trade.
		priceNow := in.PriceQuote
		if priceNow == nil {
			priceNow = w.PriceAtTo
		}
		if priceNow != nil && w.PriceAtFrom != nil && *w.PriceAtFrom > 0 {
			pct := (*priceNow - *w.PriceAtFrom) / *w.PriceAtFrom * 100
			if !math.IsNaN(pct) && !math.IsInf(pct, 0) {
				st.PriceChange24hPct = &pct
			}
		}

		if usd := s.quoteUsdFor(ctx, in, usdCache); usd != nil {
			v := vol * *usd
			st.Volume24hUsd = &v
		}
		out[in.Mint] = st
	}
	return out
}

// quoteUsdFor resolves the USD price of one whole quote unit: the configured source first
// (cached per quote mint for this call), else the ratio implied by the launch's own prices.
func (s *LaunchTradeService) quoteUsdFor(ctx context.Context, in TradeStatsInput, cache map[string]*float64) *float64 {
	if s.quoteUsd != nil && in.QuoteMint != "" {
		if v, ok := cache[in.QuoteMint]; ok {
			return v
		}
		var res *float64
		if p, ok := s.quoteUsd.QuoteUsdPrice(ctx, in.QuoteMint); ok && p > 0 {
			res = &p
		}
		cache[in.QuoteMint] = res
		if res != nil {
			return res
		}
	}
	if in.PriceUsd != nil && in.PriceQuote != nil && *in.PriceQuote > 0 && *in.PriceUsd > 0 {
		v := *in.PriceUsd / *in.PriceQuote
		return &v
	}
	return nil
}

// Trades returns a mint's trades newest first plus the cursor for the next page ("" at the end).
func (s *LaunchTradeService) Trades(ctx context.Context, mint string, limit int, cursor string) ([]*models.LaunchTrade, string, error) {
	if limit <= 0 {
		limit = DefaultTradePageLimit
	}
	if limit > MaxTradePageLimit {
		limit = MaxTradePageLimit
	}
	after, err := DecodeTradeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.trades.ListTrades(ctx, mint, limit, after)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == limit {
		next = EncodeTradeCursor(rows[len(rows)-1])
	}
	return rows, next, nil
}

// EncodeTradeCursor renders the keyset position after t as "<unix>:<slot>:<signature>".
func EncodeTradeCursor(t *models.LaunchTrade) string {
	return fmt.Sprintf("%d:%d:%s", t.BlockTime.Unix(), t.Slot, t.Signature)
}

// DecodeTradeCursor parses a cursor from EncodeTradeCursor; "" means the newest page.
func DecodeTradeCursor(cursor string) (*repository.TradeCursor, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil, nil
	}
	parts := strings.SplitN(cursor, ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return nil, ErrInvalidTradeCursor
	}
	unix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, ErrInvalidTradeCursor
	}
	slot, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, ErrInvalidTradeCursor
	}
	return &repository.TradeCursor{BlockTime: time.Unix(unix, 0).UTC(), Slot: slot, Signature: parts[2]}, nil
}

// ParseCandleInterval validates an interval name ("" = DefaultCandleInterval).
func ParseCandleInterval(name string) (time.Duration, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultCandleInterval
	}
	d, ok := candleIntervals[name]
	if !ok {
		return 0, ErrInvalidCandleInterval
	}
	return d, nil
}

// Candles returns up to limit OHLC buckets of interval for mint, oldest first, ending at the
// current bucket. Buckets without a trade are omitted.
func (s *LaunchTradeService) Candles(ctx context.Context, mint, interval string, limit int) ([]Candle, error) {
	d, err := ParseCandleInterval(interval)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultCandleLimit
	}
	if limit > MaxCandleLimit {
		limit = MaxCandleLimit
	}
	now := s.clock.Now().UTC()
	since := bucketStart(now, d).Add(-time.Duration(limit-1) * d)
	rows, err := s.trades.ListTradesAsc(ctx, mint, since, maxCandleRows)
	if err != nil {
		return nil, err
	}
	return BucketCandles(rows, d, limit), nil
}

// bucketStart floors t to the start of its interval bucket (unix-aligned).
func bucketStart(t time.Time, d time.Duration) time.Time {
	secs := int64(d / time.Second)
	return time.Unix(t.Unix()/secs*secs, 0).UTC()
}

// BucketCandles folds trades (any order) into OHLC buckets of interval d and keeps the
// newest limit buckets, oldest first. Exported so the bucketing is testable on its own.
func BucketCandles(trades []*models.LaunchTrade, d time.Duration, limit int) []Candle {
	type acc struct {
		c     Candle
		first time.Time
		last  time.Time
		slotF int64
		slotL int64
	}
	byBucket := make(map[int64]*acc)
	for _, t := range trades {
		start := bucketStart(t.BlockTime, d).Unix()
		a, ok := byBucket[start]
		if !ok {
			a = &acc{c: Candle{T: start, O: t.PriceQuote, H: t.PriceQuote, L: t.PriceQuote, C: t.PriceQuote}, first: t.BlockTime, last: t.BlockTime, slotF: t.Slot, slotL: t.Slot}
			byBucket[start] = a
		}
		if t.BlockTime.Before(a.first) || (t.BlockTime.Equal(a.first) && t.Slot < a.slotF) {
			a.first, a.slotF, a.c.O = t.BlockTime, t.Slot, t.PriceQuote
		}
		if t.BlockTime.After(a.last) || (t.BlockTime.Equal(a.last) && t.Slot >= a.slotL) {
			a.last, a.slotL, a.c.C = t.BlockTime, t.Slot, t.PriceQuote
		}
		if t.PriceQuote > a.c.H {
			a.c.H = t.PriceQuote
		}
		if t.PriceQuote < a.c.L {
			a.c.L = t.PriceQuote
		}
		a.c.V += t.QuoteAmount
	}
	out := make([]Candle, 0, len(byBucket))
	for _, a := range byBucket {
		out = append(out, a.c)
	}
	sortCandles(out)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func sortCandles(c []Candle) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].T < c[j-1].T; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}
