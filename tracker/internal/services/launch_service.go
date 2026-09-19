// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: Record browser launches after on-chain verification, bind them to daemon peers,
//          grant the launch reward once, and mirror the launch into peer_tokens.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Launch reward constants. The 250-credit reward promised by the portal wizard is granted
// at claim time (wallet bound to a peer), never at record time, so it cannot be farmed
// by launching from throwaway wallets.
const (
	LaunchRewardCredits   = 250
	LaunchRewardReason    = "token_launch"
	LaunchRewardExpiry    = 30 * 24 * time.Hour
	DefaultTransferFeeBps = 100
	launchWalletChain     = "solana"
)

// Launch flow errors. Handlers map these to HTTP codes.
var (
	ErrLaunchNotConfigured     = errors.New("launch recording is not configured (treasury address missing)")
	ErrLaunchVerifyUnavailable = errors.New("launch verification unavailable")
	ErrLaunchTxNotFound        = errors.New("launch transaction not found")
	ErrLaunchTxFailed          = errors.New("launch transaction failed on chain")
	ErrLaunchCreatorNotSigner  = errors.New("creator wallet did not sign the launch transaction")
	ErrLaunchProgramMissing    = errors.New("launch transaction has no LaunchLab instruction for this mint")
	ErrLaunchPoolMismatch      = errors.New("pool id is not part of the LaunchLab instruction")
	ErrLaunchQuoteMismatch     = errors.New("quote mint is not part of the LaunchLab instruction")
	ErrLaunchPlatformMismatch  = errors.New("launch was not created under our platform config")
	ErrLaunchFeeMissing        = errors.New("launch fee transfer to treasury missing or below the quoted amount")
	ErrLaunchSignatureConflict = errors.New("launch signature already recorded for a different mint")
	ErrLaunchMintConflict      = errors.New("mint already recorded under a different launch signature")
	ErrLaunchNotFound          = errors.New("launch not found")
	ErrLaunchAlreadyBound      = errors.New("launch already claimed by another peer")
	ErrLaunchWalletNotLinked   = errors.New("no wallet linked to this peer")
	ErrLaunchWalletMismatch    = errors.New("linked wallet does not match the launch creator")
	// ErrLaunchWalletHasLaunch is the sentinel behind *LaunchExistsError (one agent per wallet).
	ErrLaunchWalletHasLaunch = errors.New("this wallet already has a recorded launch")
)

// LaunchExistsError is returned by Record when LAUNCH_ONE_PER_WALLET is on and the creator wallet
// already has a launch on record; Mint identifies that launch. errors.Is(err, ErrLaunchWalletHasLaunch) holds.
type LaunchExistsError struct {
	Mint string
}

func (e *LaunchExistsError) Error() string {
	return ErrLaunchWalletHasLaunch.Error() + ": " + e.Mint
}

// Is makes errors.Is(err, ErrLaunchWalletHasLaunch) true.
func (e *LaunchExistsError) Is(target error) bool { return target == ErrLaunchWalletHasLaunch }

// LaunchTradeStatsProvider supplies the 24h trade block per mint (implemented by *LaunchTradeService).
type LaunchTradeStatsProvider interface {
	Stats24h(ctx context.Context, inputs []TradeStatsInput) map[string]*LaunchTradeStats
}

// QuotePriceOracle converts a raw quote-mint amount to USD. Optional pricing hook.
// TODO(pricing): wire a SOL/USD (and $STONK, xStock) price source and backfill amount_usd.
type QuotePriceOracle interface {
	QuoteToUSD(ctx context.Context, quoteMint string, amountRaw float64) (usd float64, ok bool)
}

// RecordLaunchInput is the validated body of POST /api/launch/record.
type RecordLaunchInput struct {
	Mint            string
	PoolID          string
	CreatorWallet   string
	QuoteMint       string
	Name            string
	Symbol          string
	ImageURL        string
	ImageThumbURL   string // optional ≤ 128 px copy of ImageURL
	MetadataURI     string
	LaunchSignature string
	FeeLamports     int64 // fee the browser says it paid; on-chain amount must be >= this
	TransferFeeBps  int
}

// LaunchServiceDeps holds dependencies for LaunchService.
type LaunchServiceDeps struct {
	Launches repository.LaunchRepository
	Revenue  repository.RevenueRepository
	Tokens   repository.TokenRepository
	Accounts repository.AccountRepository
	Wallets  repository.WalletRepository
	Credits  repository.CreditRepository
	Verifier LaunchVerifier
	Prices   QuotePriceOracle // optional
	// Quotes is the quote token catalog used to inline quote details on launch views. Optional.
	Quotes repository.LaunchQuoteRepository
	// Metrics supplies market data for launch views (implemented by *MetricsService). Optional.
	// Refresher gets an immediate async metrics refresh after a launch is recorded. Optional.
	Refresher LaunchMetricsTrigger
	Metrics   LaunchMetricsProvider
	// Trades fills the 24h trade block (price change, volume, count) on launch views. Optional.
	Trades LaunchTradeStatsProvider
	// Peers resolves the bound agent's owner-set display name on launch views. Optional.
	Peers  repository.PeerRepository
	Clock  clock.Clock
	Logger *slog.Logger
	// TreasuryAddress receives the launch fee (SolanaTreasuryAddress, or LAUNCH_TREASURY_ADDRESS override).
	TreasuryAddress string
	// PlatformID is our LaunchLab PlatformConfig; when set, launches must reference it.
	PlatformID string
	// MinFeeLamports is the floor for the creator->treasury transfer (0 = only the body's feeLamports applies).
	MinFeeLamports int64
	// OnePerWallet rejects a record when the creator wallet already has a launch (LAUNCH_ONE_PER_WALLET).
	OnePerWallet bool
}

// LaunchService implements the launch record / claim flow.
type LaunchService struct {
	launches repository.LaunchRepository
	revenue  repository.RevenueRepository
	tokens   repository.TokenRepository
	accounts repository.AccountRepository
	wallets  repository.WalletRepository
	credits  repository.CreditRepository
	verifier LaunchVerifier
	prices   QuotePriceOracle
	quotes   repository.LaunchQuoteRepository
	metrics  LaunchMetricsProvider
	refresh  LaunchMetricsTrigger
	trades   LaunchTradeStatsProvider
	peers    repository.PeerRepository
	// reputation adds the bound agent's board tier to launch views (nil = omitted).
	reputation *ReputationService
	// announcer pins the launch announcement in the token room at claim time (nil = none).
	announcer LaunchAnnouncer
	clock     clock.Clock
	logger    *slog.Logger
	treasury  string
	platform  string
	minFee    int64
	onePer    bool
}

// NewLaunchService creates a LaunchService.
func NewLaunchService(d LaunchServiceDeps) *LaunchService {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := d.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &LaunchService{
		launches: d.Launches, revenue: d.Revenue, tokens: d.Tokens, accounts: d.Accounts,
		wallets: d.Wallets, credits: d.Credits, verifier: d.Verifier, prices: d.Prices,
		quotes: d.Quotes, metrics: d.Metrics, refresh: d.Refresher, trades: d.Trades, peers: d.Peers,
		clock: clk, logger: logger, treasury: d.TreasuryAddress, platform: d.PlatformID, minFee: d.MinFeeLamports,
		onePer: d.OnePerWallet,
	}
}

// OnePerWallet reports whether a creator wallet may record at most one launch.
func (s *LaunchService) OnePerWallet() bool { return s.onePer }

// Record verifies the launch transaction on chain and stores the launch + launch_fee revenue.
// Idempotent on signature: re-posting the same launch returns the stored record with created=false.
func (s *LaunchService) Record(ctx context.Context, in RecordLaunchInput) (*models.TokenLaunch, bool, error) {
	if existing, err := s.launches.GetBySignature(ctx, in.LaunchSignature); err == nil {
		if existing.Mint != in.Mint {
			return nil, false, ErrLaunchSignatureConflict
		}
		return existing, false, nil
	} else if !errors.Is(err, models.ErrNotFound) {
		return nil, false, err
	}
	if _, err := s.launches.GetByMint(ctx, in.Mint); err == nil {
		return nil, false, ErrLaunchMintConflict
	} else if !errors.Is(err, models.ErrNotFound) {
		return nil, false, err
	}
	// One agent per wallet: checked before the on-chain verification so a rejected
	// second launch costs no RPC call. Re-posting the same signature returned above.
	if s.onePer {
		existing, _, err := s.launches.List(ctx, repository.ListLaunchesOptions{CreatorWallet: in.CreatorWallet, Limit: 1})
		if err != nil {
			return nil, false, err
		}
		if len(existing) > 0 {
			return nil, false, &LaunchExistsError{Mint: existing[0].Mint}
		}
	}
	// $STONK only: the on-chain verifier confirms the quote is part of the LaunchLab
	// instruction, but the platform rule — no other quote is launchable — is checked here
	// so a wrong quote answers QUOTE_NOT_ALLOWED before any RPC call.
	allowed, err := s.quoteAllowed(ctx, in.QuoteMint)
	if err != nil {
		return nil, false, err
	}
	if !allowed {
		return nil, false, ErrLaunchQuoteNotAllowed
	}
	if s.treasury == "" || s.verifier == nil {
		return nil, false, ErrLaunchNotConfigured
	}

	minFee := s.minFee
	if in.FeeLamports > minFee {
		minFee = in.FeeLamports
	}
	res, err := s.verifier.VerifyLaunch(ctx, LaunchVerifyParams{
		Signature: in.LaunchSignature, CreatorWallet: in.CreatorWallet, TreasuryAddress: s.treasury,
		MinFeeLamports: minFee, Mint: in.Mint, PoolID: in.PoolID, QuoteMint: in.QuoteMint, PlatformID: s.platform,
	})
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrLaunchVerifyUnavailable, err)
	}
	if err := s.checkLaunch(in, res, minFee); err != nil {
		return nil, false, err
	}

	now := s.clock.Now().UTC()
	bps := in.TransferFeeBps
	if bps <= 0 {
		bps = DefaultTransferFeeBps
	}
	launch := &models.TokenLaunch{
		Mint: in.Mint, PoolID: in.PoolID, CreatorWallet: in.CreatorWallet, QuoteMint: in.QuoteMint,
		Name: in.Name, Symbol: in.Symbol, ImageURL: in.ImageURL, ImageThumbURL: in.ImageThumbURL, MetadataURI: in.MetadataURI,
		LaunchSignature: in.LaunchSignature, FeeLamports: res.TreasuryLamports, TransferFeeBps: bps,
		PlatformID: s.platform, Status: models.LaunchStatusConfirmed, CreatedAt: now,
	}
	if err := s.launches.Create(ctx, launch); err != nil {
		if errors.Is(err, models.ErrAlreadyExists) {
			// Concurrent record of the same launch — return whichever won.
			if existing, gerr := s.launches.GetBySignature(ctx, in.LaunchSignature); gerr == nil {
				return existing, false, nil
			}
			return nil, false, ErrLaunchMintConflict
		}
		return nil, false, err
	}
	s.recordLaunchFee(ctx, launch, res.BlockTime)
	// Warm the metrics cache right away so the new gallery card has numbers within seconds.
	if s.refresh != nil {
		s.refresh.TriggerRefresh(launch.Mint, LaunchLabHint{PoolID: launch.PoolID, QuoteMint: launch.QuoteMint})
	}
	return launch, true, nil
}

// quoteAllowed reports whether quoteMint is the cluster's $STONK quote. With a quote catalog
// wired it must be an enabled row that IsSTONKQuote (mainnet mint or the devnet stand-in);
// without one only the mainnet $STONK mint passes.
func (s *LaunchService) quoteAllowed(ctx context.Context, quoteMint string) (bool, error) {
	if s.quotes == nil {
		return quoteMint == DefaultLaunchQuoteMint, nil
	}
	q, err := s.quotes.GetByMint(ctx, quoteMint)
	if errors.Is(err, models.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return q.Enabled && IsSTONKQuote(q), nil
}

// checkLaunch applies the recording rules to the observed transaction facts.
func (s *LaunchService) checkLaunch(in RecordLaunchInput, res *LaunchVerifyResult, minFee int64) error {
	switch {
	case !res.Found:
		return ErrLaunchTxNotFound
	case !res.Succeeded:
		return ErrLaunchTxFailed
	case !contains(res.Signers, in.CreatorWallet):
		return ErrLaunchCreatorNotSigner
	case len(res.LaunchLabAccounts) == 0 || !contains(res.LaunchLabAccounts, in.Mint):
		return ErrLaunchProgramMissing
	case in.PoolID != "" && !contains(res.LaunchLabAccounts, in.PoolID):
		return ErrLaunchPoolMismatch
	case !contains(res.LaunchLabAccounts, in.QuoteMint):
		return ErrLaunchQuoteMismatch
	case s.platform != "" && !contains(res.LaunchLabAccounts, s.platform):
		return ErrLaunchPlatformMismatch
	case res.TreasuryLamports <= 0 || res.TreasuryLamports < minFee:
		return ErrLaunchFeeMissing
	}
	return nil
}

// recordLaunchFee appends the launch_fee ledger row. Failure is logged, not fatal: the launch
// row is the source of truth and the ledger can be backfilled from it.
func (s *LaunchService) recordLaunchFee(ctx context.Context, l *models.TokenLaunch, blockTime *time.Time) {
	if s.revenue == nil {
		return
	}
	occurred := l.CreatedAt
	if blockTime != nil {
		occurred = blockTime.UTC()
	}
	entry := &models.PlatformRevenue{
		Kind: models.RevenueKindLaunchFee, QuoteMint: models.SOLMint, AmountRaw: float64(l.FeeLamports),
		Signature: l.LaunchSignature, Mint: l.Mint, OccurredAt: occurred,
		Meta: map[string]interface{}{"creator_wallet": l.CreatorWallet, "launch_quote_mint": l.QuoteMint},
	}
	if s.prices != nil {
		if usd, ok := s.prices.QuoteToUSD(ctx, models.SOLMint, entry.AmountRaw); ok {
			entry.AmountUSD = &usd
		}
	}
	if err := s.revenue.Insert(ctx, entry); err != nil && !errors.Is(err, models.ErrAlreadyExists) {
		s.logger.Error("[LaunchService.Record] revenue insert failed", "mint", l.Mint, "error", err)
	}
}

// Get returns a launch by mint.
func (s *LaunchService) Get(ctx context.Context, mint string) (*models.TokenLaunch, error) {
	l, err := s.launches.GetByMint(ctx, mint)
	if errors.Is(err, models.ErrNotFound) {
		return nil, ErrLaunchNotFound
	}
	return l, err
}

// List returns launches (optionally filtered by creator) newest first.
func (s *LaunchService) List(ctx context.Context, creator string, limit, offset int) ([]*models.TokenLaunch, int, error) {
	return s.launches.List(ctx, repository.ListLaunchesOptions{CreatorWallet: creator, Limit: limit, Offset: offset})
}

// Pending returns unbound launches for a creator wallet (portal prompt after daemon install).
func (s *LaunchService) Pending(ctx context.Context, wallet string, limit int) ([]*models.TokenLaunch, error) {
	items, _, err := s.launches.List(ctx, repository.ListLaunchesOptions{CreatorWallet: wallet, UnboundOnly: true, Limit: limit})
	return items, err
}

// ByWallet returns every launch recorded for a creator wallet, claimed or not, newest first.
func (s *LaunchService) ByWallet(ctx context.Context, wallet string, limit int) ([]*models.TokenLaunch, error) {
	items, _, err := s.launches.List(ctx, repository.ListLaunchesOptions{CreatorWallet: wallet, Limit: limit})
	return items, err
}

// ClaimResult is returned by Claim.
type ClaimResult struct {
	Launch         *models.TokenLaunch
	CreditsGranted int
	AlreadyBound   bool // launch was already bound to this same peer (idempotent replay)
	// PeerDisplayName is the bound agent's display name after the claim ("" when the peer
	// repository is not wired or the peer row is missing).
	PeerDisplayName string
}

// Claim binds an unbound launch to the calling peer when the peer's linked wallet is the
// launch creator, mirrors it into peer_tokens, and grants the launch reward exactly once.
func (s *LaunchService) Claim(ctx context.Context, peerID, mint string) (*ClaimResult, error) {
	launch, err := s.launches.GetByMint(ctx, mint)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil, ErrLaunchNotFound
		}
		return nil, err
	}
	if launch.IsBound() {
		if launch.PeerID == peerID {
			s.announce(ctx, launch)
			return &ClaimResult{Launch: launch, AlreadyBound: true, PeerDisplayName: s.peerDisplayName(ctx, peerID)}, nil
		}
		return nil, ErrLaunchAlreadyBound
	}

	account, err := s.accounts.GetByPeerID(ctx, peerID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil, ErrLaunchWalletNotLinked
		}
		return nil, err
	}
	wallet, err := s.wallets.GetByAccountID(ctx, account.ID, launchWalletChain)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil, ErrLaunchWalletNotLinked
		}
		return nil, err
	}
	if wallet.WalletAddress != launch.CreatorWallet {
		return nil, ErrLaunchWalletMismatch
	}

	now := s.clock.Now().UTC()
	if err := s.launches.Bind(ctx, mint, peerID, now); err != nil {
		if errors.Is(err, models.ErrAlreadyExists) {
			return nil, ErrLaunchAlreadyBound
		}
		if errors.Is(err, models.ErrNotFound) {
			return nil, ErrLaunchNotFound
		}
		return nil, err
	}
	launch.PeerID = peerID
	launch.Status = models.LaunchStatusBound
	launch.BoundAt = &now

	// Mirror into peer_tokens so the gallery and /api/peers/{id}/token keep working.
	if s.tokens != nil {
		if err := s.tokens.Upsert(ctx, &models.PeerToken{
			PeerID: peerID, TokenContractAddress: launch.Mint, TokenTicker: launch.Symbol,
			TokenName: launch.Name, TokenImageURL: launch.ImageURL, LaunchedAt: launch.CreatedAt,
		}); err != nil {
			s.logger.Error("[LaunchService.Claim] peer_tokens upsert failed", "peer_id", peerID, "mint", mint, "error", err)
		}
	}

	granted, err := s.grantLaunchReward(ctx, account.ID, mint, now)
	if err != nil {
		s.logger.Error("[LaunchService.Claim] reward grant failed", "peer_id", peerID, "mint", mint, "error", err)
	}
	name := s.nameAgentFromToken(ctx, peerID, launch.Symbol)
	s.announce(ctx, launch)
	return &ClaimResult{Launch: launch, CreditsGranted: granted, PeerDisplayName: name}, nil
}

// nameAgentFromToken gives the bound agent its token's name ("<symbol>_agent") when the
// peer has no display name yet or still carries the daemon placeholder; an owner-set
// name is kept. Returns the peer's display name after the claim.
func (s *LaunchService) nameAgentFromToken(ctx context.Context, peerID, symbol string) string {
	if s.peers == nil {
		return ""
	}
	peer, err := s.peers.FindByID(ctx, peerID)
	if err != nil {
		if !errors.Is(err, models.ErrNotFound) {
			s.logger.Warn("[LaunchService.Claim] peer lookup failed", "peer_id", peerID, "error", err)
		}
		return ""
	}
	if peer.DisplayName != "" && !models.IsPlaceholderDisplayName(peer.DisplayName) {
		return peer.DisplayName
	}
	name := models.AgentDisplayNameForSymbol(symbol)
	if name == "" {
		return peer.DisplayName
	}
	if err := s.peers.UpdateDisplayName(ctx, peerID, name); err != nil {
		s.logger.Warn("[LaunchService.Claim] display name update failed", "peer_id", peerID, "error", err)
		return peer.DisplayName
	}
	return name
}

// peerDisplayName reads the bound agent's current display name ("" when unknown).
func (s *LaunchService) peerDisplayName(ctx context.Context, peerID string) string {
	if s.peers == nil {
		return ""
	}
	peer, err := s.peers.FindByID(ctx, peerID)
	if err != nil {
		return ""
	}
	return peer.DisplayName
}

// grantLaunchReward credits the launch reward once per mint (request_id "launch:<mint>").
func (s *LaunchService) grantLaunchReward(ctx context.Context, accountID, mint string, now time.Time) (int, error) {
	if s.credits == nil {
		return 0, nil
	}
	requestID := "launch:" + mint
	if _, err := s.credits.GetTransactionByRequestID(ctx, accountID, requestID); err == nil {
		return 0, nil
	}
	expires := now.Add(LaunchRewardExpiry)
	err := s.credits.CreditFree(ctx, accountID, LaunchRewardCredits, LaunchRewardReason, requestID, expires)
	if errors.Is(err, models.ErrNotFound) {
		// Account exists but has no balance row yet (registered before balances were created).
		cerr := s.credits.CreateBalance(ctx, &models.CreditBalance{AccountID: accountID, FreeCreditsExpiresAt: &expires, UpdatedAt: now})
		if cerr != nil && !errors.Is(cerr, models.ErrAlreadyExists) {
			return 0, cerr
		}
		err = s.credits.CreditFree(ctx, accountID, LaunchRewardCredits, LaunchRewardReason, requestID, expires)
	}
	if err != nil {
		return 0, err
	}
	return LaunchRewardCredits, nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// SetReputationService wires the board reputation so launch views carry peer_reputation_tier.
func (s *LaunchService) SetReputationService(r *ReputationService) { s.reputation = r }

// LaunchAnnouncer creates the room announcement of a bound launch (ForumService.AnnounceLaunch).
type LaunchAnnouncer interface {
	AnnounceLaunch(ctx context.Context, launch *models.TokenLaunch) (*models.ForumPost, error)
}

// SetAnnouncer wires the community board so Claim pins the launch announcement in the token
// room (phase 2). Idempotent per mint, so a replayed claim fills in a missed announcement.
func (s *LaunchService) SetAnnouncer(a LaunchAnnouncer) { s.announcer = a }

// announce creates the room announcement; failures are logged, the claim stands.
func (s *LaunchService) announce(ctx context.Context, launch *models.TokenLaunch) {
	if s.announcer == nil || launch == nil || launch.PeerID == "" {
		return
	}
	if _, err := s.announcer.AnnounceLaunch(ctx, launch); err != nil {
		s.logger.Warn("[LaunchService.Claim] room announcement failed", "mint", launch.Mint, "error", err)
	}
}
