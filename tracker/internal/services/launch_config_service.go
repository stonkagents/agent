// Package: tracker/internal/services
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Single source of truth for launch parameters — USD-pegged launch fee priced in SOL,
//          quote token list, and raise sizing so every quote raises the USD value of 85 SOL.

package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Launch config errors.
var (
	ErrLaunchQuoteNotFound   = errors.New("launch quote not found or disabled")
	ErrLaunchFeeUnavailable  = errors.New("launch fee has not been priced yet")
	ErrQuotePriceUnavailable = errors.New("quote token price unavailable")
	// ErrLaunchQuoteNotAllowed is returned for a quote that exists in the catalog but is not
	// the cluster's $STONK quote: launches on this platform are quoted in $STONK only
	// (product decision 2026-09-14). Other rows stay in launch_quotes for metrics lookups
	// of existing pools.
	ErrLaunchQuoteNotAllowed = errors.New("only the $STONK quote is launchable on this platform")
)

// Launch constants.
const (
	// DefaultLaunchQuoteMint is $STONK — the only launchable quote on mainnet (the devnet
	// stand-in is recognised by category, see IsSTONKQuote).
	DefaultLaunchQuoteMint = "6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx"
	// DefaultLaunchRaiseSOL is the reference raise every quote is sized against (USD-equivalent).
	DefaultLaunchRaiseSOL = 85.0
	// LaunchFeeStaleAfter marks the persisted fee stale when the price is older than this.
	LaunchFeeStaleAfter = 15 * time.Minute
	// LaunchQuotePriceTTL is how long a quote token USD price is cached.
	LaunchQuotePriceTTL = 30 * time.Second
	// LaunchRaiseBasis explains the raise sizing rule to the client.
	LaunchRaiseBasis = "Sized so this launch is worth the same as the default 85 SOL raise"
	// DevnetQuoteUSDFallback is the USD value assumed for one whole unit of a devnet quote the
	// price feed cannot price (devnet mints are not listed on Jupiter). The devnet $STONK
	// stand-in is Raydium's devnet USDC, so 1 USD per unit keeps the raise sizing sane.
	DevnetQuoteUSDFallback = 1.0

	lamportsPerSOL = 1_000_000_000
)

// LaunchCurveParams are the fixed LaunchLab curve parameters the browser passes to the SDK.
// Amounts are decimal strings because they exceed 2^53.
type LaunchCurveParams struct {
	ConfigID          string
	CurveType         string
	MigrateType       string
	BaseDecimals      int
	Supply            string
	TotalSellA        string
	TotalLockedAmount string
	CliffPeriod       string
	UnlockPeriod      string
	CpmmCreatorFeeOn  int
}

// ResolveLaunchCluster returns the Solana cluster the launchpad runs on. An explicit value
// (LAUNCHPAD_CLUSTER) wins when valid; otherwise it is derived from the LaunchLab program id
// (the devnet program means devnet, anything else — including empty — means mainnet).
func ResolveLaunchCluster(explicit, programID string) string {
	if c := strings.ToLower(strings.TrimSpace(explicit)); models.IsValidLaunchCluster(c) {
		return c
	}
	if strings.TrimSpace(programID) == LaunchLabProgramDevnet {
		return models.LaunchClusterDevnet
	}
	return models.LaunchClusterMainnet
}

// LaunchConfigSettings are the static, env-provided launch parameters.
type LaunchConfigSettings struct {
	// Cluster is the Solana cluster the quotes and program live on ("mainnet" | "devnet").
	Cluster        string
	ProgramID      string
	PlatformID     string
	Treasury       string
	TransferFeeBps int
	FeeUSD         float64
	// QuoteMint pins the launchable quote; empty = the cluster's $STONK quote.
	QuoteMint string
	// RaiseUnits fixes the graduation raise in whole quote tokens; 0 = USD-sized.
	RaiseUnits     float64
	MinFeeLamports int64
	MaxFeeLamports int64
}

// LaunchConfigServiceDeps holds dependencies for LaunchConfigService.
type LaunchConfigServiceDeps struct {
	Settings repository.LaunchSettingsRepository
	Quotes   repository.LaunchQuoteRepository
	Prices   PriceFeed
	Clock    clock.Clock
	Config   LaunchConfigSettings
}

// LaunchFeeView is the fee as served to clients.
type LaunchFeeView struct {
	USD      float64
	Lamports int64
	SolUSD   float64
	PricedAt time.Time
	Stale    bool
}

// LaunchRaise is the sized raise for the selected quote.
type LaunchRaise struct {
	Raw        *big.Int
	Units      float64
	MinimumRaw *big.Int
	Basis      string
}

// LaunchConfigResult is the full launch configuration for one selected quote.
type LaunchConfigResult struct {
	Cluster        string
	ProgramID      string
	PlatformID     string
	Treasury       string
	TransferFeeBps int
	Fee            LaunchFeeView
	// DefaultQuoteMint is the mint of the cluster's $STONK quote — the quote a launch raises in.
	DefaultQuoteMint string
	// Quotes is the launchable catalog: exactly the $STONK quote of the cluster.
	Quotes []*models.LaunchQuote
	Quote  *models.LaunchQuote
	Raise  LaunchRaise
	Curve  LaunchCurveParams
}

type cachedPrice struct {
	usdRaw float64
	at     time.Time
}

// LaunchConfigService prices the launch fee and assembles launch configuration.
type LaunchConfigService struct {
	settings repository.LaunchSettingsRepository
	quotes   repository.LaunchQuoteRepository
	prices   PriceFeed
	clock    clock.Clock
	cfg      LaunchConfigSettings

	mu         sync.Mutex
	priceCache map[string]cachedPrice
}

// NewLaunchConfigService creates a new LaunchConfigService.
func NewLaunchConfigService(deps LaunchConfigServiceDeps) *LaunchConfigService {
	return &LaunchConfigService{
		settings:   deps.Settings,
		quotes:     deps.Quotes,
		prices:     deps.Prices,
		clock:      deps.Clock,
		cfg:        deps.Config,
		priceCache: make(map[string]cachedPrice),
	}
}

// ComputeFeeLamports converts a USD fee to lamports at the given SOL/USD price, rounding up and
// clamping to [minLamports, maxLamports].
func ComputeFeeLamports(feeUSD, solUSD float64, minLamports, maxLamports int64) int64 {
	if solUSD <= 0 || feeUSD <= 0 {
		return maxLamports
	}
	lamports := int64(math.Ceil(feeUSD / solUSD * lamportsPerSOL))
	if lamports < minLamports {
		return minLamports
	}
	if lamports > maxLamports {
		return maxLamports
	}
	return lamports
}

// ComputeRaiseRaw returns round(85 * solUSD / quoteUSD * 10^decimals) as a big integer.
// quoteUSD must be the USD value of 10^decimals RAW units of the quote token.
func ComputeRaiseRaw(solUSD, quoteUSD float64, decimals int) *big.Int {
	if solUSD <= 0 || quoteUSD <= 0 || decimals < 0 {
		return big.NewInt(0)
	}
	const prec = 128
	v := new(big.Float).SetPrec(prec).SetFloat64(DefaultLaunchRaiseSOL)
	v.Mul(v, new(big.Float).SetPrec(prec).SetFloat64(solUSD))
	v.Quo(v, new(big.Float).SetPrec(prec).SetFloat64(quoteUSD))
	scale := new(big.Float).SetPrec(prec).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	v.Mul(v, scale)
	v.Add(v, new(big.Float).SetPrec(prec).SetFloat64(0.5)) // round half up (v > 0)
	out, _ := v.Int(nil)
	return out
}

// FixedRaiseRaw returns round(units * 10^decimals) for a raise pinned in whole quote tokens.
func FixedRaiseRaw(units float64, decimals int) *big.Int {
	if units <= 0 || decimals < 0 {
		return big.NewInt(0)
	}
	const prec = 128
	v := new(big.Float).SetPrec(prec).SetFloat64(units)
	v.Mul(v, new(big.Float).SetPrec(prec).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
	v.Add(v, new(big.Float).SetPrec(prec).SetFloat64(0.5))
	out, _ := v.Int(nil)
	return out
}

// formatUnits prints a whole-token amount with thousands separators (25000 -> "25,000").
func formatUnits(units float64) string {
	s := strconv.FormatFloat(units, 'f', -1, 64)
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String() + frac
}

// RefreshFee fetches SOL/USD, recomputes the launch fee in lamports and persists it.
// On failure the previously persisted fee is left untouched (it will read as stale after 15 minutes).
func (s *LaunchConfigService) RefreshFee(ctx context.Context) (*models.LaunchFee, error) {
	prices, err := s.prices.GetPrices(ctx, []string{SOLMint})
	if err != nil {
		return nil, fmt.Errorf("refresh launch fee: %w", err)
	}
	sol, ok := prices[SOLMint]
	if !ok || sol.USDPrice <= 0 {
		return nil, fmt.Errorf("refresh launch fee: %w for SOL", ErrQuotePriceUnavailable)
	}
	now := s.clock.Now()
	fee := &models.LaunchFee{
		FeeUSD:      s.cfg.FeeUSD,
		FeeLamports: ComputeFeeLamports(s.cfg.FeeUSD, sol.USDPrice, s.cfg.MinFeeLamports, s.cfg.MaxFeeLamports),
		SolUSD:      sol.USDPrice,
		PricedAt:    now,
		Source:      s.prices.Name(),
	}
	if err := s.settings.UpsertFee(ctx, fee); err != nil {
		return nil, fmt.Errorf("refresh launch fee: persist: %w", err)
	}
	return fee, nil
}

// Cluster returns the configured cluster (mainnet when unset).
func (s *LaunchConfigService) Cluster() string {
	if s.cfg.Cluster == "" {
		return models.LaunchClusterMainnet
	}
	return s.cfg.Cluster
}

// resolveQuote returns the launchable quote for quoteMint within the configured cluster.
// Launches are quoted in $STONK only: an empty mint means the cluster's $STONK quote — the
// real mint on mainnet, or the category "stonk" stand-in seeded on devnet by migration 015 —
// and any other enabled catalog row answers ErrLaunchQuoteNotAllowed. Among several $STONK
// candidates the lowest sort_order wins (ListEnabled is ordered by sort_order ASC).
// The quote repository is cluster-scoped, so a mint seeded only for another cluster is not found.
func (s *LaunchConfigService) resolveQuote(ctx context.Context, quoteMint string, stonk *models.LaunchQuote) (*models.LaunchQuote, error) {
	if quoteMint == "" {
		if stonk == nil {
			return nil, ErrLaunchQuoteNotFound
		}
		return stonk, nil
	}
	quote, err := s.quotes.GetByMint(ctx, quoteMint)
	if errors.Is(err, models.ErrNotFound) {
		return nil, ErrLaunchQuoteNotFound
	}
	if err != nil {
		return nil, err
	}
	if !quote.Enabled {
		return nil, ErrLaunchQuoteNotFound
	}
	if !IsSTONKQuote(quote) {
		return nil, ErrLaunchQuoteNotAllowed
	}
	return quote, nil
}

// IsSTONKQuote reports whether q is the $STONK quote of its cluster: the mainnet mint, or a
// row in category "stonk" (the devnet stand-in).
func IsSTONKQuote(q *models.LaunchQuote) bool {
	if q == nil {
		return false
	}
	if pinned := launchQuoteMint.Load(); pinned != nil && *pinned != "" {
		return q.QuoteMint == *pinned
	}
	return q.QuoteMint == DefaultLaunchQuoteMint || q.Category == models.LaunchQuoteCategorySTONK
}

// launchQuoteMint is the LAUNCH_QUOTE_MINT pin, consulted by IsSTONKQuote so that the
// config endpoint and the record endpoint agree on the one launchable quote.
var launchQuoteMint atomic.Pointer[string]

// PinLaunchQuote makes mint the single launchable quote (empty = back to the $STONK rule).
func PinLaunchQuote(mint string) {
	m := strings.TrimSpace(mint)
	launchQuoteMint.Store(&m)
}

// FindSTONKQuote returns the enabled $STONK quote of the repository's cluster (nil when the
// catalog has none). It is the single launchable quote for GET /api/launch/config and the
// only quoteMint POST /api/launch/record accepts.
func FindSTONKQuote(ctx context.Context, quotes repository.LaunchQuoteRepository) (*models.LaunchQuote, error) {
	enabled, err := quotes.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	for _, q := range enabled {
		if IsSTONKQuote(q) {
			return q, nil
		}
	}
	return nil, nil
}

// LaunchableQuotes is the served catalog: exactly the $STONK quote, or empty when the cluster
// has none (a misconfigured catalog; GET /api/launch/config then answers 404).
func LaunchableQuotes(stonk *models.LaunchQuote) []*models.LaunchQuote {
	if stonk == nil {
		return []*models.LaunchQuote{}
	}
	return []*models.LaunchQuote{stonk}
}

// GetConfig assembles the launch configuration for quoteMint (empty = the cluster's $STONK quote).
func (s *LaunchConfigService) GetConfig(ctx context.Context, quoteMint string) (*LaunchConfigResult, error) {
	stonk, err := FindSTONKQuote(ctx, s.quotes)
	if err != nil {
		return nil, err
	}
	quote, err := s.resolveQuote(ctx, quoteMint, stonk)
	if err != nil {
		return nil, err
	}

	fee, err := s.settings.GetFee(ctx)
	if errors.Is(err, models.ErrNotFound) {
		return nil, ErrLaunchFeeUnavailable
	}
	if err != nil {
		return nil, err
	}

	quoteUSD := fee.SolUSD
	if quote.QuoteMint != SOLMint {
		quoteUSD, err = s.quotePriceUSD(ctx, quote.QuoteMint)
		if err != nil {
			if s.Cluster() != models.LaunchClusterDevnet {
				return nil, err
			}
			// Devnet mints have no market price; assume DevnetQuoteUSDFallback per whole unit
			// so the config endpoint keeps serving (see the devnet $STONK stand-in, migration 015).
			quoteUSD = DevnetQuoteUSDFallback
		}
	}

	minRaw, ok := new(big.Int).SetString(quote.MinFundRaisingRaw, 10)
	if !ok || minRaw.Sign() < 0 {
		minRaw = big.NewInt(1)
	}
	raw := ComputeRaiseRaw(fee.SolUSD, quoteUSD, quote.Decimals)
	basis := LaunchRaiseBasis
	if s.cfg.RaiseUnits > 0 {
		raw = FixedRaiseRaw(s.cfg.RaiseUnits, quote.Decimals)
		basis = fmt.Sprintf("Fixed raise of %s %s", formatUnits(s.cfg.RaiseUnits), quote.Symbol)
	}
	if raw.Cmp(minRaw) < 0 {
		raw = new(big.Int).Set(minRaw)
	}
	units, _ := new(big.Float).Quo(
		new(big.Float).SetInt(raw),
		new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(quote.Decimals)), nil)),
	).Float64()

	now := s.clock.Now()
	return &LaunchConfigResult{
		Cluster:        s.Cluster(),
		ProgramID:      s.cfg.ProgramID,
		PlatformID:     s.cfg.PlatformID,
		Treasury:       s.cfg.Treasury,
		TransferFeeBps: s.cfg.TransferFeeBps,
		Fee: LaunchFeeView{
			USD:      fee.FeeUSD,
			Lamports: fee.FeeLamports,
			SolUSD:   fee.SolUSD,
			PricedAt: fee.PricedAt,
			Stale:    now.Sub(fee.PricedAt) > LaunchFeeStaleAfter,
		},
		DefaultQuoteMint: stonk.QuoteMint,
		Quotes:           LaunchableQuotes(stonk),
		Quote:            quote,
		Raise:            LaunchRaise{Raw: raw, Units: units, MinimumRaw: minRaw, Basis: basis},
		Curve:            DefaultLaunchCurve(quote.LaunchLabConfigID),
	}, nil
}

// DefaultLaunchCurve returns the fixed ConstantCurve parameters bound to a quote's GlobalConfig.
func DefaultLaunchCurve(configID string) LaunchCurveParams {
	return LaunchCurveParams{
		ConfigID:          configID,
		CurveType:         "ConstantCurve",
		MigrateType:       "cpmm",
		BaseDecimals:      6,
		Supply:            "1000000000000000",
		TotalSellA:        "793100000000000",
		TotalLockedAmount: "0",
		CliffPeriod:       "0",
		UnlockPeriod:      "0",
		CpmmCreatorFeeOn:  0,
	}
}

// quotePriceUSD returns the cached USD price (per 10^decimals raw units) of a quote mint,
// refreshing from the feed when older than LaunchQuotePriceTTL. A feed failure falls back to
// the last cached value if one exists.
func (s *LaunchConfigService) quotePriceUSD(ctx context.Context, mint string) (float64, error) {
	now := s.clock.Now()
	s.mu.Lock()
	cached, ok := s.priceCache[mint]
	s.mu.Unlock()
	if ok && now.Sub(cached.at) <= LaunchQuotePriceTTL {
		return cached.usdRaw, nil
	}

	prices, err := s.prices.GetPrices(ctx, []string{mint})
	if err == nil {
		if p, found := prices[mint]; found && p.USDPriceRaw > 0 {
			s.mu.Lock()
			s.priceCache[mint] = cachedPrice{usdRaw: p.USDPriceRaw, at: now}
			s.mu.Unlock()
			return p.USDPriceRaw, nil
		}
		err = ErrQuotePriceUnavailable
	}
	if ok {
		return cached.usdRaw, nil // stale cache beats no answer
	}
	return 0, fmt.Errorf("%w: %s: %v", ErrQuotePriceUnavailable, mint, err)
}
