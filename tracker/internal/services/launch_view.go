// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Enrich stored launches into the shape the portal gallery renders — quote token
//          details and live market metrics — so a card needs one request, not three.

package services

import (
	"context"
	"errors"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// LaunchMetricsProvider supplies market metrics for launched mints.
// Implemented by *MetricsService; nil disables the metrics block on launch views.
type LaunchMetricsProvider interface {
	// BatchGetCachedMetrics reads already-cached metrics only (no upstream calls),
	// which is what a paginated list can afford.
	BatchGetCachedMetrics(ctx context.Context, contractAddrs []string) map[string]*models.TokenMetrics
	// GetMetricsByMint fetches (and caches) metrics for one mint that may have no peer bound.
	GetMetricsByMint(ctx context.Context, mint string, hint LaunchLabHint) (*models.TokenMetrics, error)
}

// LaunchQuoteView is the quote token a launch raises in, inlined on every launch.
type LaunchQuoteView struct {
	Mint         string `json:"mint"`
	Symbol       string `json:"symbol"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Decimals     int    `json:"decimals"`
	TokenProgram string `json:"tokenProgram,omitempty"`
}

// LaunchMetricsView is the live market state of a launch. Every field is nil when unknown,
// so a card can render from a partial answer instead of waiting for a complete one.
type LaunchMetricsView struct {
	MarketCapUsd     *float64 `json:"marketCapUsd"`
	CurveProgressPct *float64 `json:"curveProgressPct"`
	Holders          *int     `json:"holders"`
	PriceUsd         *float64 `json:"priceUsd"`
	QuoteRaised      *float64 `json:"quoteRaised"`
	QuoteTarget      *float64 `json:"quoteTarget"`
	Graduated        *bool    `json:"graduated"`
	Source           string   `json:"source,omitempty"`
	// PriceQuote is the live curve price in quote units (what the trade feed prices in).
	PriceQuote *float64 `json:"priceQuote"`
	// Trade-derived fields (launch_trades, see LaunchTradeService). Null until the indexer
	// has seen a trade of the launch; PriceChange24hPct is null until a trade is 24h old.
	PriceChange24hPct *float64 `json:"priceChange24hPct"`
	Volume24hQuote    *float64 `json:"volume24hQuote"`
	Volume24hUsd      *float64 `json:"volume24hUsd"`
	Trades24h         *int     `json:"trades24h"`
}

// LaunchView is one launch as the portal consumes it.
//
// The embedded TokenLaunch keeps the original snake_case fields (pool_id, image_url, …) so
// anything written against the first version of these endpoints keeps working. The fields
// below are the camelCase duplicates the portal reads plus the quote/metrics enrichment;
// each "…Camel" field mirrors exactly the embedded field of the same name.
type LaunchView struct {
	*models.TokenLaunch

	PoolIDCamel          string     `json:"poolId,omitempty"`
	CreatorWalletCamel   string     `json:"creatorWallet"`
	QuoteMintCamel       string     `json:"quoteMint"`
	ImageURLCamel        string     `json:"imageUrl,omitempty"`
	ImageThumbURLCamel   *string    `json:"imageThumbUrl"` // ≤ 128 px copy of imageUrl; null when the launch has none
	MetadataURICamel     string     `json:"metadataUri,omitempty"`
	LaunchSignatureCamel string     `json:"launchSignature"`
	FeeLamportsCamel     int64      `json:"feeLamports"`
	TransferFeeBpsCamel  int        `json:"transferFeeBps"`
	PlatformIDCamel      string     `json:"platformId,omitempty"`
	PeerIDCamel          string     `json:"peerId,omitempty"`
	CreatedAtCamel       time.Time  `json:"createdAt"`
	BoundAtCamel         *time.Time `json:"boundAt,omitempty"`

	// AgentBound is true once an agent (daemon peer) has claimed this launch.
	AgentBound bool `json:"agentBound"`
	// PeerDisplayName is the bound agent's owner-set name (peer_id -> peers.display_name);
	// omitted while the launch is unclaimed or the owner has not named the agent.
	// Both spellings carry the same value, matching the snake/camel pairs above.
	PeerDisplayName      string `json:"peer_display_name,omitempty"`
	PeerDisplayNameCamel string `json:"peerDisplayName,omitempty"`
	// PeerReputationTier is the bound agent's community board tier (new, active, trusted, top);
	// omitted while the launch is unclaimed or the reputation service is not wired.
	PeerReputationTier      string `json:"peer_reputation_tier,omitempty"`
	PeerReputationTierCamel string `json:"peerReputationTier,omitempty"`
	// Quote is the quote token details; nil when the mint is not in the quote catalog.
	Quote *LaunchQuoteView `json:"quote,omitempty"`
	// Metrics is the live market state; nil when nothing is known yet.
	Metrics *LaunchMetricsView `json:"metrics,omitempty"`
}

// newLaunchView copies a stored launch into the enriched shape (without quote or metrics).
func newLaunchView(l *models.TokenLaunch) *LaunchView {
	if l == nil {
		return nil
	}
	return &LaunchView{
		TokenLaunch:          l,
		PoolIDCamel:          l.PoolID,
		CreatorWalletCamel:   l.CreatorWallet,
		QuoteMintCamel:       l.QuoteMint,
		ImageURLCamel:        l.ImageURL,
		ImageThumbURLCamel:   optionalString(l.ImageThumbURL),
		MetadataURICamel:     l.MetadataURI,
		LaunchSignatureCamel: l.LaunchSignature,
		FeeLamportsCamel:     l.FeeLamports,
		TransferFeeBpsCamel:  l.TransferFeeBps,
		PlatformIDCamel:      l.PlatformID,
		PeerIDCamel:          l.PeerID,
		CreatedAtCamel:       l.CreatedAt,
		BoundAtCamel:         l.BoundAt,
		AgentBound:           l.IsBound(),
	}
}

// GetView returns one launch enriched with its quote token and live metrics.
// Metrics and quote failures degrade to nil rather than failing the request.
func (s *LaunchService) GetView(ctx context.Context, mint string) (*LaunchView, error) {
	l, err := s.Get(ctx, mint)
	if err != nil {
		return nil, err
	}
	view := newLaunchView(l)
	view.Quote = s.quoteView(ctx, l.QuoteMint)
	if s.metrics != nil {
		m, merr := s.metrics.GetMetricsByMint(ctx, l.Mint, LaunchLabHint{PoolID: l.PoolID, QuoteMint: l.QuoteMint})
		if merr != nil {
			s.logger.Warn("[LaunchService.GetView] metrics unavailable", "mint", l.Mint, "error", merr)
		} else {
			view.Metrics = toLaunchMetricsView(m)
		}
	}
	s.attachTradeStats(ctx, []*LaunchView{view})
	s.attachPeerNames(ctx, []*LaunchView{view})
	return view, nil
}

// ListViews returns a page of launches enriched with quote tokens and cached metrics.
// The list path reads metrics from cache only: a gallery page must not fan out to the
// upstream launchpad once per card.
func (s *LaunchService) ListViews(ctx context.Context, creator string, limit, offset int) ([]*LaunchView, int, error) {
	items, total, err := s.List(ctx, creator, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	views := s.enrich(ctx, items)
	// First page only: fill cache misses live (bounded budget) so fresh cards are not blank.
	if offset == 0 && len(items) <= listLiveFillMaxRows {
		s.fillMissingMetricsLive(ctx, views)
	}
	s.attachTradeStats(ctx, views)
	s.attachPeerNames(ctx, views)
	return views, total, nil
}

// PendingViews returns unbound launches for a wallet in the enriched shape.
func (s *LaunchService) PendingViews(ctx context.Context, wallet string, limit int) ([]*LaunchView, error) {
	items, err := s.Pending(ctx, wallet, limit)
	if err != nil {
		return nil, err
	}
	views := s.enrich(ctx, items)
	s.attachTradeStats(ctx, views)
	s.attachPeerNames(ctx, views)
	return views, nil
}

// ByWalletViews returns every launch of a creator wallet (bound or not) with quote and metrics.
func (s *LaunchService) ByWalletViews(ctx context.Context, wallet string, limit int) ([]*LaunchView, error) {
	items, err := s.ByWallet(ctx, wallet, limit)
	if err != nil {
		return nil, err
	}
	views := s.enrich(ctx, items)
	s.attachTradeStats(ctx, views)
	s.attachPeerNames(ctx, views)
	return views, nil
}

// attachTradeStats fills the 24h trade block on each view from indexed trades (one batched
// read for the page). A view with no metrics but with trades gets a metrics block for them.
func (s *LaunchService) attachTradeStats(ctx context.Context, views []*LaunchView) {
	if s.trades == nil || len(views) == 0 {
		return
	}
	inputs := make([]TradeStatsInput, 0, len(views))
	for _, v := range views {
		in := TradeStatsInput{Mint: v.Mint, QuoteMint: v.QuoteMint}
		if v.Metrics != nil {
			in.PriceQuote, in.PriceUsd = v.Metrics.PriceQuote, v.Metrics.PriceUsd
		}
		inputs = append(inputs, in)
	}
	stats := s.trades.Stats24h(ctx, inputs)
	for _, v := range views {
		st, ok := stats[v.Mint]
		if !ok || st == nil {
			continue
		}
		if v.Metrics == nil {
			v.Metrics = &LaunchMetricsView{}
		}
		v.Metrics.PriceChange24hPct = st.PriceChange24hPct
		v.Metrics.Volume24hQuote = st.Volume24hQuote
		v.Metrics.Volume24hUsd = st.Volume24hUsd
		v.Metrics.Trades24h = st.Trades24h
	}
}

// attachPeerNames fills the bound agent's display name on each view from one batched
// peer lookup for the page. Unclaimed launches, unnamed peers and lookup failures leave
// the field empty (omitted from JSON).
func (s *LaunchService) attachPeerNames(ctx context.Context, views []*LaunchView) {
	if s.peers == nil || len(views) == 0 {
		return
	}
	ids := make([]string, 0, len(views))
	seen := make(map[string]struct{}, len(views))
	for _, v := range views {
		if v.PeerID == "" {
			continue
		}
		if _, dup := seen[v.PeerID]; dup {
			continue
		}
		seen[v.PeerID] = struct{}{}
		ids = append(ids, v.PeerID)
	}
	if len(ids) == 0 {
		return
	}
	names, err := s.peers.DisplayNamesByIDs(ctx, ids)
	if err != nil {
		s.logger.Warn("[LaunchService] peer display name lookup degraded", "peers", len(ids), "error", err)
		return
	}
	var tiers map[string]string
	if s.reputation != nil {
		tiers = s.reputation.TiersByIDs(ctx, ids)
	}
	for _, v := range views {
		if name := names[v.PeerID]; name != "" {
			v.PeerDisplayName, v.PeerDisplayNameCamel = name, name
		}
		if tier := tiers[v.PeerID]; tier != "" {
			v.PeerReputationTier, v.PeerReputationTierCamel = tier, tier
		}
	}
}

// enrich attaches quote details and cached metrics to a page of launches.
func (s *LaunchService) enrich(ctx context.Context, items []*models.TokenLaunch) []*LaunchView {
	views := make([]*LaunchView, 0, len(items))
	quotes := s.quoteIndex(ctx, items)

	var cached map[string]*models.TokenMetrics
	if s.metrics != nil && len(items) > 0 {
		mints := make([]string, 0, len(items))
		for _, l := range items {
			mints = append(mints, l.Mint)
		}
		cached = s.metrics.BatchGetCachedMetrics(ctx, mints)
	}

	for _, l := range items {
		v := newLaunchView(l)
		v.Quote = quotes[l.QuoteMint]
		if m, ok := cached[l.Mint]; ok {
			v.Metrics = toLaunchMetricsView(m)
		}
		views = append(views, v)
	}
	return views
}

// quoteIndex builds a mint → quote lookup for a page of launches. The enabled catalog is
// read once; a launch quoted in a token that has since been disabled costs one extra read
// per distinct mint, not per launch.
func (s *LaunchService) quoteIndex(ctx context.Context, items []*models.TokenLaunch) map[string]*LaunchQuoteView {
	out := make(map[string]*LaunchQuoteView, len(items))
	if s.quotes == nil || len(items) == 0 {
		return out
	}
	if enabled, err := s.quotes.ListEnabled(ctx); err == nil {
		for _, q := range enabled {
			out[q.QuoteMint] = toQuoteView(q)
		}
	} else {
		s.logger.Warn("[LaunchService] quote catalog unavailable", "error", err)
	}
	seen := make(map[string]struct{}, len(items))
	for _, l := range items {
		if l.QuoteMint == "" {
			continue
		}
		if _, ok := out[l.QuoteMint]; ok {
			continue
		}
		if _, ok := seen[l.QuoteMint]; ok {
			continue
		}
		seen[l.QuoteMint] = struct{}{}
		if q, err := s.quotes.GetByMint(ctx, l.QuoteMint); err == nil {
			out[l.QuoteMint] = toQuoteView(q)
		}
	}
	return out
}

// quoteView looks up one quote token; nil when unknown or unavailable.
func (s *LaunchService) quoteView(ctx context.Context, quoteMint string) *LaunchQuoteView {
	if s.quotes == nil || quoteMint == "" {
		return nil
	}
	q, err := s.quotes.GetByMint(ctx, quoteMint)
	if err != nil {
		if !errors.Is(err, models.ErrNotFound) {
			s.logger.Warn("[LaunchService] quote lookup failed", "quote_mint", quoteMint, "error", err)
		}
		return nil
	}
	return toQuoteView(q)
}

func toQuoteView(q *models.LaunchQuote) *LaunchQuoteView {
	if q == nil {
		return nil
	}
	return &LaunchQuoteView{
		Mint: q.QuoteMint, Symbol: q.Symbol, Name: q.Name,
		Category: q.Category, Decimals: q.Decimals, TokenProgram: q.TokenProgram,
	}
}

// toLaunchMetricsView maps raw token metrics to the launch card shape.
// Returns nil for an all-empty metrics object so the card can tell "no data" from "zero".
func toLaunchMetricsView(m *models.TokenMetrics) *LaunchMetricsView {
	if m == nil {
		return nil
	}
	out := &LaunchMetricsView{
		MarketCapUsd: m.MarketCapUsd,
		Holders:      m.Holders,
		PriceUsd:     m.PriceUsd,
		QuoteRaised:  m.QuoteRaised,
		QuoteTarget:  m.QuoteTarget,
		Graduated:    m.Complete,
		Source:       m.Source,
		PriceQuote:   m.PriceQuote,
	}
	out.CurveProgressPct = curveProgressPct(m)
	if out.MarketCapUsd == nil && out.CurveProgressPct == nil && out.Holders == nil &&
		out.PriceUsd == nil && out.QuoteRaised == nil && out.QuoteTarget == nil && out.Graduated == nil &&
		out.PriceQuote == nil {
		return nil
	}
	return out
}

// curveProgressPct prefers the launchpad's own bonding curve percentage and falls back to
// raised/target when only the raw quote amounts are known.
func curveProgressPct(m *models.TokenMetrics) *float64 {
	if m.BondingCurvePercent != nil {
		pct := float64(*m.BondingCurvePercent)
		return &pct
	}
	if m.QuoteRaised != nil && m.QuoteTarget != nil && *m.QuoteTarget > 0 {
		pct := *m.QuoteRaised / *m.QuoteTarget * 100
		if pct > 100 {
			pct = 100
		}
		return &pct
	}
	return nil
}

// Live fill for the first gallery page: cache misses are fetched in parallel within one
// overall budget; whatever has not answered by then stays without metrics (as before).
const (
	listLiveFillMaxRows     = 25
	listLiveFillConcurrency = 4
)

// listLiveFillBudget is a variable so tests can shorten it.
var listLiveFillBudget = 4 * time.Second

// fillMissingMetricsLive fetches metrics for views that have none, best effort within
// listLiveFillBudget. Fetched values are cached by the metrics service, so the next list
// (and the refresher) benefit too.
func (s *LaunchService) fillMissingMetricsLive(ctx context.Context, views []*LaunchView) {
	if s.metrics == nil {
		return
	}
	var missing []*LaunchView
	for _, v := range views {
		if v.Metrics == nil {
			missing = append(missing, v)
		}
	}
	if len(missing) == 0 {
		return
	}
	budgetCtx, cancel := context.WithTimeout(ctx, listLiveFillBudget)
	defer cancel()

	type result struct {
		idx int
		m   *models.TokenMetrics
	}
	results := make(chan result, len(missing))
	sem := make(chan struct{}, listLiveFillConcurrency)
	for i, v := range missing {
		go func(idx int, l *models.TokenLaunch) {
			select {
			case sem <- struct{}{}:
			case <-budgetCtx.Done():
				results <- result{idx: idx}
				return
			}
			defer func() { <-sem }()
			m, err := s.metrics.GetMetricsByMint(budgetCtx, l.Mint, LaunchLabHint{PoolID: l.PoolID, QuoteMint: l.QuoteMint})
			if err != nil || budgetCtx.Err() != nil {
				m = nil
			}
			results <- result{idx: idx, m: m}
		}(i, v.TokenLaunch)
	}
	// Collect until every fetch reported or the budget ran out; late answers are dropped.
	for pending := len(missing); pending > 0; pending-- {
		select {
		case r := <-results:
			if r.m != nil {
				missing[r.idx].Metrics = toLaunchMetricsView(r.m)
			}
		case <-budgetCtx.Done():
			return
		}
	}
}

// optionalString maps an empty stored string to JSON null and anything else to itself.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
