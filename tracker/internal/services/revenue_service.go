// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Aggregate the platform revenue ledger for GET /api/revenue

package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// RevenueWindowDays is the length of the daily series returned by Summary.
const RevenueWindowDays = 30

// RevenueRecentLimit is how many ledger entries the public summary lists.
const RevenueRecentLimit = 20

// DefaultExplorerBaseURL builds the explorer links in the public revenue feed.
// Overridable with SOLANA_EXPLORER_BASE_URL (read in bootstrap).
const DefaultExplorerBaseURL = "https://solscan.io"

// ErrRevenueInvalidKind is returned when a ledger write names an unknown kind.
var ErrRevenueInvalidKind = errors.New("unknown revenue kind")

// RevenueConfig carries the public wallet addresses and explorer base shown on the revenue page.
// Every field is optional; empty fields are omitted from the response.
type RevenueConfig struct {
	TreasuryAddress      string
	BuybackWallet        string
	TransferFeeAuthority string
	// ExplorerBaseURL defaults to DefaultExplorerBaseURL when empty.
	ExplorerBaseURL string
	// ExplorerCluster is appended as ?cluster=<value> (e.g. "devnet"). Empty = mainnet.
	ExplorerCluster string
}

// RevenueWallets are the public addresses behind the platform's money flows.
type RevenueWallets struct {
	Treasury             string `json:"treasury,omitempty"`
	Buyback              string `json:"buyback,omitempty"`
	TransferFeeAuthority string `json:"transfer_fee_authority,omitempty"`
}

// RevenueEntry is one ledger row as served to the public revenue page, with explorer links
// already built so the browser does not have to know the cluster or explorer host.
type RevenueEntry struct {
	ID          int64                  `json:"id"`
	Kind        string                 `json:"kind"`
	QuoteMint   string                 `json:"quote_mint,omitempty"`
	AmountRaw   float64                `json:"amount_raw"`
	AmountUSD   *float64               `json:"amount_usd"`
	Signature   string                 `json:"signature,omitempty"`
	Mint        string                 `json:"mint,omitempty"`
	OccurredAt  time.Time              `json:"occurred_at"`
	ExplorerURL string                 `json:"explorer_url,omitempty"`
	TokenURL    string                 `json:"token_explorer_url,omitempty"`
	Meta        map[string]interface{} `json:"meta,omitempty"`
}

// RevenueDailyPoint is one day of the revenue series (UTC days, zero-filled).
type RevenueDailyPoint struct {
	Date              string  `json:"date"`
	LaunchCount       int     `json:"launch_count"`
	LaunchFeeLamports int64   `json:"launch_fee_lamports"`
	LaunchFeeUSD      float64 `json:"launch_fee_usd"`
	PlatformFeeUSD    float64 `json:"platform_fee_usd"`
	BuybackUSD        float64 `json:"buyback_usd"`
}

// RevenueSummary is the response body of GET /api/revenue.
type RevenueSummary struct {
	Totals       map[string]*models.RevenueKindTotal `json:"totals"` // every kind present (zero when empty)
	Counts       map[string]int                      `json:"counts"`
	TotalEntries int                                 `json:"total_entries"`
	WindowDays   int                                 `json:"window_days"`
	Daily        []RevenueDailyPoint                 `json:"daily"`
	// Entries is the newest RevenueRecentLimit ledger rows, newest first.
	Entries []RevenueEntry `json:"entries"`
	// Wallets are the public treasury/buyback addresses (empty fields omitted).
	Wallets     RevenueWallets `json:"wallets"`
	GeneratedAt time.Time      `json:"generated_at"`
}

// RevenueService aggregates the platform revenue ledger.
type RevenueService struct {
	revenue repository.RevenueRepository
	clock   clock.Clock
	cfg     RevenueConfig
}

// NewRevenueService creates a RevenueService with no wallet/explorer configuration.
func NewRevenueService(revenue repository.RevenueRepository, clk clock.Clock) *RevenueService {
	return NewRevenueServiceWithConfig(revenue, clk, RevenueConfig{})
}

// NewRevenueServiceWithConfig creates a RevenueService that also publishes the platform
// wallet addresses and builds explorer links for ledger entries.
func NewRevenueServiceWithConfig(revenue repository.RevenueRepository, clk clock.Clock, cfg RevenueConfig) *RevenueService {
	if clk == nil {
		clk = clock.RealClock{}
	}
	if cfg.ExplorerBaseURL == "" {
		cfg.ExplorerBaseURL = DefaultExplorerBaseURL
	}
	cfg.ExplorerBaseURL = strings.TrimRight(cfg.ExplorerBaseURL, "/")
	return &RevenueService{revenue: revenue, clock: clk, cfg: cfg}
}

// Record appends one ledger entry. Idempotent on signature: an entry whose signature is
// already in the ledger returns created=false without writing.
func (s *RevenueService) Record(ctx context.Context, entry *models.PlatformRevenue) (bool, error) {
	if entry == nil {
		return false, ErrRevenueInvalidKind
	}
	if !models.IsValidRevenueKind(entry.Kind) {
		return false, fmt.Errorf("%w: %q", ErrRevenueInvalidKind, entry.Kind)
	}
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = s.clock.Now().UTC()
	}
	entry.OccurredAt = entry.OccurredAt.UTC()
	if err := s.revenue.Insert(ctx, entry); err != nil {
		if errors.Is(err, models.ErrAlreadyExists) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// explorerTx returns the explorer URL for a transaction signature ("" when there is none).
func (s *RevenueService) explorerTx(signature string) string {
	if signature == "" {
		return ""
	}
	return s.cfg.ExplorerBaseURL + "/tx/" + signature + s.explorerQuery()
}

// explorerToken returns the explorer URL for a mint ("" when there is none).
func (s *RevenueService) explorerToken(mint string) string {
	if mint == "" {
		return ""
	}
	return s.cfg.ExplorerBaseURL + "/token/" + mint + s.explorerQuery()
}

func (s *RevenueService) explorerQuery() string {
	if s.cfg.ExplorerCluster == "" {
		return ""
	}
	return "?cluster=" + url.QueryEscape(s.cfg.ExplorerCluster)
}

// recentEntries returns the newest ledger rows with explorer links attached.
// A repository failure degrades to an empty list rather than failing the whole summary.
func (s *RevenueService) recentEntries(ctx context.Context) []RevenueEntry {
	rows, err := s.revenue.Recent(ctx, RevenueRecentLimit)
	if err != nil {
		return []RevenueEntry{}
	}
	out := make([]RevenueEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, RevenueEntry{
			ID: r.ID, Kind: r.Kind, QuoteMint: r.QuoteMint, AmountRaw: r.AmountRaw,
			AmountUSD: r.AmountUSD, Signature: r.Signature, Mint: r.Mint,
			OccurredAt:  r.OccurredAt.UTC(),
			ExplorerURL: s.explorerTx(r.Signature),
			TokenURL:    s.explorerToken(r.Mint),
			Meta:        r.Meta,
		})
	}
	return out
}

// Summary returns totals by kind plus a zero-filled daily series for the last RevenueWindowDays days.
func (s *RevenueService) Summary(ctx context.Context) (*RevenueSummary, error) {
	now := s.clock.Now().UTC()
	totals, err := s.revenue.TotalsByKind(ctx)
	if err != nil {
		return nil, err
	}
	out := &RevenueSummary{
		Totals:     make(map[string]*models.RevenueKindTotal, len(models.RevenueKinds)),
		Counts:     make(map[string]int, len(models.RevenueKinds)),
		WindowDays: RevenueWindowDays, GeneratedAt: now,
		Entries: s.recentEntries(ctx),
		Wallets: RevenueWallets{
			Treasury:             s.cfg.TreasuryAddress,
			Buyback:              s.cfg.BuybackWallet,
			TransferFeeAuthority: s.cfg.TransferFeeAuthority,
		},
	}
	for _, k := range models.RevenueKinds {
		out.Totals[k] = &models.RevenueKindTotal{Kind: k}
		out.Counts[k] = 0
	}
	for _, t := range totals {
		out.Totals[t.Kind] = t
		out.Counts[t.Kind] = t.Count
		out.TotalEntries += t.Count
	}

	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(RevenueWindowDays - 1))
	rows, err := s.revenue.DailyByKind(ctx, startDay)
	if err != nil {
		return nil, err
	}
	byDate := make(map[string]*RevenueDailyPoint, RevenueWindowDays)
	out.Daily = make([]RevenueDailyPoint, RevenueWindowDays)
	for i := 0; i < RevenueWindowDays; i++ {
		date := startDay.AddDate(0, 0, i).Format("2006-01-02")
		out.Daily[i] = RevenueDailyPoint{Date: date}
		byDate[date] = &out.Daily[i]
	}
	for _, r := range rows {
		p, ok := byDate[r.Date]
		if !ok {
			continue
		}
		switch r.Kind {
		case models.RevenueKindLaunchFee:
			p.LaunchCount += r.Count
			p.LaunchFeeLamports += int64(r.AmountRaw)
			p.LaunchFeeUSD += r.AmountUSD
		case models.RevenueKindPlatformFeeClaim, models.RevenueKindNFTYield:
			p.PlatformFeeUSD += r.AmountUSD
		case models.RevenueKindBuyback:
			p.BuybackUSD += r.AmountUSD
		}
	}
	return out, nil
}
