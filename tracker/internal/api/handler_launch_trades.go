// Package: tracker/internal/api
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: Public detail-page reads over indexed trades — the newest-first trade feed and
//          OHLC candles of a launch — plus the network token's burn plan and payouts.
//          Answers are cached for a few seconds per URL so a busy detail page costs one
//          query per interval, not one per viewer.

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	tradeReadCacheTTL = 5 * time.Second
	tradeReadTimeout  = 5 * time.Second
	tradeReadCacheCap = 2000
)

// tradeView is one row of GET /api/launch/{mint}/trades.
type tradeView struct {
	Signature   string    `json:"signature"`
	BlockTime   time.Time `json:"blockTime"`
	Side        string    `json:"side"`
	Trader      string    `json:"trader"`
	BaseAmount  float64   `json:"baseAmount"`
	QuoteAmount float64   `json:"quoteAmount"`
	PriceQuote  float64   `json:"priceQuote"`
}

// tradesResponse is the body of GET /api/launch/{mint}/trades.
type tradesResponse struct {
	Data       []tradeView `json:"data"`
	NextCursor string      `json:"next_cursor"`
}

// responseCache is a tiny per-URL cache of encoded JSON answers.
type responseCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	clock   clock.Clock
	entries map[string]cachedResponse
}

type cachedResponse struct {
	status  int
	body    interface{}
	expires time.Time
}

func newResponseCache(ttl time.Duration, clk clock.Clock) *responseCache {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &responseCache{ttl: ttl, clock: clk, entries: make(map[string]cachedResponse)}
}

func (c *responseCache) get(key string) (cachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.clock.Now().After(e.expires) {
		return cachedResponse{}, false
	}
	return e, true
}

func (c *responseCache) put(key string, status int, body interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if len(c.entries) >= tradeReadCacheCap {
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= tradeReadCacheCap {
			c.entries = make(map[string]cachedResponse)
		}
	}
	c.entries[key] = cachedResponse{status: status, body: body, expires: now.Add(c.ttl)}
}

// LaunchTradesHandler serves GET /api/launch/{mint}/trades and /candles.
type LaunchTradesHandler struct {
	svc    *services.LaunchTradeService
	cache  *responseCache
	logger *slog.Logger
}

// NewLaunchTradesHandler creates a LaunchTradesHandler; clk may be nil.
func NewLaunchTradesHandler(svc *services.LaunchTradeService, clk clock.Clock) *LaunchTradesHandler {
	return &LaunchTradesHandler{svc: svc, cache: newResponseCache(tradeReadCacheTTL, clk), logger: slog.Default()}
}

// HandleTrades handles GET /api/launch/{mint}/trades?limit=&cursor= (public, newest first).
func (h *LaunchTradesHandler) HandleTrades(w http.ResponseWriter, r *http.Request) {
	mint := mux.Vars(r)["mint"]
	if !isPubkey(mint) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", services.ErrLaunchNotFound.Error())
		return
	}
	limit := services.DefaultTradePageLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if _, err := services.DecodeTradeCursor(cursor); err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "cursor must come from a previous page")
		return
	}
	key := "trades:" + mint + ":" + strconv.Itoa(limit) + ":" + cursor
	if e, ok := h.cache.get(key); ok {
		SendJSON(w, e.status, e.body)
		return
	}
	ctx, cancel := contextWithTimeout(r, tradeReadTimeout)
	defer cancel()
	rows, next, err := h.svc.Trades(ctx, mint, limit, cursor)
	if err != nil {
		h.logger.Error("[LaunchTradesHandler.HandleTrades] failed", "mint", mint, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Trade read failed")
		return
	}
	body := tradesResponse{Data: make([]tradeView, 0, len(rows)), NextCursor: next}
	for _, t := range rows {
		body.Data = append(body.Data, toTradeView(t))
	}
	h.cache.put(key, http.StatusOK, body)
	SendJSON(w, http.StatusOK, body)
}

func toTradeView(t *models.LaunchTrade) tradeView {
	return tradeView{
		Signature: t.Signature, BlockTime: t.BlockTime.UTC(), Side: t.Side, Trader: t.Trader,
		BaseAmount: t.BaseAmount, QuoteAmount: t.QuoteAmount, PriceQuote: t.PriceQuote,
	}
}

// HandleCandles handles GET /api/launch/{mint}/candles?interval=1m|5m|15m|1h|1d&limit= (public).
func (h *LaunchTradesHandler) HandleCandles(w http.ResponseWriter, r *http.Request) {
	mint := mux.Vars(r)["mint"]
	if !isPubkey(mint) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", services.ErrLaunchNotFound.Error())
		return
	}
	interval := strings.TrimSpace(r.URL.Query().Get("interval"))
	if _, err := services.ParseCandleInterval(interval); err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	limit := services.DefaultCandleLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil || parsed <= 0 {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	key := "candles:" + mint + ":" + interval + ":" + strconv.Itoa(limit)
	if e, ok := h.cache.get(key); ok {
		SendJSON(w, e.status, e.body)
		return
	}
	ctx, cancel := contextWithTimeout(r, tradeReadTimeout)
	defer cancel()
	candles, err := h.svc.Candles(ctx, mint, interval, limit)
	if err != nil {
		if errors.Is(err, services.ErrInvalidCandleInterval) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
		h.logger.Error("[LaunchTradesHandler.HandleCandles] failed", "mint", mint, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Candle read failed")
		return
	}
	if candles == nil {
		candles = []services.Candle{}
	}
	body := DataEnvelope{Data: candles}
	h.cache.put(key, http.StatusOK, body)
	SendJSON(w, http.StatusOK, body)
}

// AgentTokenHandler serves GET /api/v1/agent-token/burnplan and /payouts.
type AgentTokenHandler struct {
	svc    *services.AgentTokenService
	cache  *responseCache
	logger *slog.Logger
}

// NewAgentTokenHandler creates an AgentTokenHandler; clk may be nil.
func NewAgentTokenHandler(svc *services.AgentTokenService, clk clock.Clock) *AgentTokenHandler {
	return &AgentTokenHandler{svc: svc, cache: newResponseCache(tradeReadCacheTTL, clk), logger: slog.Default()}
}

// HandleBurnPlan handles GET /api/v1/agent-token/burnplan (public, bare object).
func (h *AgentTokenHandler) HandleBurnPlan(w http.ResponseWriter, r *http.Request) {
	if e, ok := h.cache.get("burnplan"); ok {
		SendJSON(w, e.status, e.body)
		return
	}
	ctx, cancel := contextWithTimeout(r, tradeReadTimeout)
	defer cancel()
	plan, err := h.svc.BurnPlan(ctx)
	if err != nil {
		h.logger.Error("[AgentTokenHandler.HandleBurnPlan] failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Burn plan read failed")
		return
	}
	h.cache.put("burnplan", http.StatusOK, plan)
	SendJSON(w, http.StatusOK, plan)
}

// HandlePayouts handles GET /api/v1/agent-token/payouts (public, bare object).
func (h *AgentTokenHandler) HandlePayouts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, tradeReadTimeout)
	defer cancel()
	p, err := h.svc.Payouts(ctx)
	if err != nil {
		h.logger.Error("[AgentTokenHandler.HandlePayouts] failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Payouts read failed")
		return
	}
	SendJSON(w, http.StatusOK, p)
}

// contextWithTimeout bounds a request context for a read that hits the database.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
