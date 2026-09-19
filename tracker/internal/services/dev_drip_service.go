// Package: tracker/internal/services
// Feature: StonkAgents devnet drip
// Purpose: Sends a small amount of test SOL and test $STONK to a wallet connecting to the dev
//          portal — once per wallet per 24 h, at most three per IP per hour — by building,
//          signing and submitting a legacy Solana transaction from the drip wallet.
//          Devnet only: the bootstrap refuses to wire this on any other cluster.

package services

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/solana/tx"
)

// Drip defaults (overridable through DevDripConfig).
const (
	DefaultDevDripSol            = 0.05
	DefaultDevDripStonk          = 25.0
	DefaultDevDripMint           = "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT"
	DefaultDevDripMintDecimals   = 6
	DefaultDevDripWalletCooldown = 24 * time.Hour
	DefaultDevDripIPLimit        = 3
	DefaultDevDripIPWindow       = time.Hour
	DefaultDevDripConfirmTimeout = 30 * time.Second
	DefaultDevDripConfirmPoll    = time.Second
	// DevDripRecipientSolSkip is the recipient balance at or above which no SOL is sent (0.1 SOL).
	DevDripRecipientSolSkip uint64 = 100_000_000
	// devDripFeeReserve keeps the drip wallet able to pay the transaction fee (5000 lamports/sig)
	// plus a small margin.
	devDripFeeReserve uint64 = 10_000
	// devDripAtaRentLamports is the rent-exempt minimum for a 165-byte SPL token account.
	devDripAtaRentLamports uint64 = 2_039_280
	lamportsPerSol                = 1_000_000_000
)

// Drip errors. DripLimitedError carries when the caller may try again.
var (
	ErrDripInvalidWallet = errors.New("drip: wallet must be a base58 32-byte public key")
	ErrDripSelf          = errors.New("drip: cannot drip to the drip wallet")
	ErrDripEmpty         = errors.New("drip: drip wallet does not hold enough SOL or $STONK")
	ErrDripChain         = errors.New("drip: chain request failed")
	ErrDripFailed        = errors.New("drip: transaction failed on chain")
	ErrDripUnconfirmed   = errors.New("drip: transaction not confirmed in time")
)

// DripLimitedError is returned when the wallet or the IP is inside its cooldown.
type DripLimitedError struct {
	// Scope is "wallet" or "ip".
	Scope  string
	NextAt time.Time
}

func (e *DripLimitedError) Error() string {
	return fmt.Sprintf("drip: %s limited until %s", e.Scope, e.NextAt.UTC().Format(time.RFC3339))
}

// DevDripChain is the slice of the Solana RPC client the drip needs (*solana.Client).
type DevDripChain interface {
	GetBalance(ctx context.Context, walletAddress string) (int64, error)
	GetTokenAccountBalance(ctx context.Context, address string) (amount uint64, exists bool, err error)
	GetLatestBlockhash(ctx context.Context) (string, error)
	SendTransaction(ctx context.Context, base64Tx string) (string, error)
	GetSignatureStatus(ctx context.Context, signature string) (*DevDripSignatureStatus, error)
}

// DevDripSignatureStatus is the confirmation state of a submitted transaction.
type DevDripSignatureStatus struct {
	Confirmed bool
	Failed    bool
}

// DevDripConfig holds the drip amounts and limits.
type DevDripConfig struct {
	// SolLamports is the SOL amount sent (lamports).
	SolLamports uint64
	// StonkRaw is the token amount sent (base units, i.e. whole units * 10^Decimals).
	StonkRaw uint64
	Mint     tx.Pubkey
	Decimals uint8
	// TokenProgram owns Mint (classic SPL Token for the devnet stand-in).
	TokenProgram tx.Pubkey
	// RecipientSolSkip: recipients holding at least this many lamports get no SOL.
	RecipientSolSkip uint64
	WalletCooldown   time.Duration
	IPLimit          int
	IPWindow         time.Duration
	ConfirmTimeout   time.Duration
	// ConfirmPoll is the status poll interval (0 = default; negative = no wait, tests only).
	ConfirmPoll time.Duration
	// ExplorerBaseURL + ExplorerClusterSuffix build the explorer link (e.g. https://solscan.io, ?cluster=devnet).
	ExplorerBaseURL       string
	ExplorerClusterSuffix string
}

// DevDripDeps are the collaborators of DevDripService.
type DevDripDeps struct {
	Repo  repository.DevDripRepository
	Chain DevDripChain
	// Key is the drip wallet's ed25519 private key (64 bytes). Never logged.
	Key    ed25519.PrivateKey
	Clock  clock.Clock
	Logger *slog.Logger
	Config DevDripConfig
}

// DevDripResult describes what was sent.
type DevDripResult struct {
	// Signature is empty when nothing needed sending.
	Signature   string
	SolLamports uint64
	StonkRaw    uint64
	SentSol     bool
	SentStonk   bool
	Explorer    string
}

// DevDripService implements the drip.
type DevDripService struct {
	repo   repository.DevDripRepository
	chain  DevDripChain
	key    ed25519.PrivateKey
	wallet tx.Pubkey
	clock  clock.Clock
	logger *slog.Logger
	cfg    DevDripConfig
}

// NewDevDripService creates a DevDripService. Key must be a 64-byte ed25519 private key.
func NewDevDripService(d DevDripDeps) (*DevDripService, error) {
	if len(d.Key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("drip: key must be %d bytes", ed25519.PrivateKeySize)
	}
	if d.Repo == nil || d.Chain == nil {
		return nil, errors.New("drip: repo and chain are required")
	}
	cfg := d.Config
	if cfg.SolLamports == 0 {
		cfg.SolLamports = uint64(DefaultDevDripSol * lamportsPerSol)
	}
	if cfg.Decimals == 0 {
		cfg.Decimals = DefaultDevDripMintDecimals
	}
	if cfg.StonkRaw == 0 {
		cfg.StonkRaw = uint64(DefaultDevDripStonk * math.Pow10(int(cfg.Decimals)))
	}
	if cfg.Mint == (tx.Pubkey{}) {
		cfg.Mint = tx.MustPubkey(DefaultDevDripMint)
	}
	if cfg.TokenProgram == (tx.Pubkey{}) {
		cfg.TokenProgram = tx.TokenProgramID
	}
	if cfg.RecipientSolSkip == 0 {
		cfg.RecipientSolSkip = DevDripRecipientSolSkip
	}
	if cfg.WalletCooldown <= 0 {
		cfg.WalletCooldown = DefaultDevDripWalletCooldown
	}
	if cfg.IPLimit <= 0 {
		cfg.IPLimit = DefaultDevDripIPLimit
	}
	if cfg.IPWindow <= 0 {
		cfg.IPWindow = DefaultDevDripIPWindow
	}
	if cfg.ConfirmTimeout <= 0 {
		cfg.ConfirmTimeout = DefaultDevDripConfirmTimeout
	}
	if cfg.ConfirmPoll == 0 {
		cfg.ConfirmPoll = DefaultDevDripConfirmPoll
	}
	if cfg.ExplorerBaseURL == "" {
		cfg.ExplorerBaseURL = "https://solscan.io"
	}
	s := &DevDripService{repo: d.Repo, chain: d.Chain, key: d.Key, clock: d.Clock, logger: d.Logger, cfg: cfg}
	copy(s.wallet[:], d.Key.Public().(ed25519.PublicKey))
	if s.clock == nil {
		s.clock = clock.RealClock{}
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	return s, nil
}

// Wallet returns the drip wallet's public address.
func (s *DevDripService) Wallet() string { return s.wallet.String() }

// Config returns the effective configuration.
func (s *DevDripService) Config() DevDripConfig { return s.cfg }

// HashIP returns the SHA-256 hex digest stored for an IP ("" for "").
func HashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}

// ExplorerURL builds the explorer link for a signature.
func (s *DevDripService) ExplorerURL(signature string) string {
	return strings.TrimRight(s.cfg.ExplorerBaseURL, "/") + "/tx/" + signature + s.cfg.ExplorerClusterSuffix
}

// Drip sends the configured amounts to wallet, skipping the SOL leg when the recipient already
// holds RecipientSolSkip and the token leg when it already holds StonkRaw. ip is the raw client
// IP (hashed before storage). A result with an empty Signature means nothing needed sending
// and no cooldown was recorded.
func (s *DevDripService) Drip(ctx context.Context, wallet, ip string) (*DevDripResult, error) {
	recipient, err := tx.PubkeyFromBase58(strings.TrimSpace(wallet))
	if err != nil {
		return nil, ErrDripInvalidWallet
	}
	if recipient == s.wallet {
		return nil, ErrDripSelf
	}
	now := s.clock.Now().UTC()
	ipHash := HashIP(ip)

	if prev, err := s.repo.Get(ctx, recipient.String()); err == nil {
		if next := prev.DrippedAt.Add(s.cfg.WalletCooldown); now.Before(next) {
			return nil, &DripLimitedError{Scope: "wallet", NextAt: next}
		}
	} else if !errors.Is(err, models.ErrNotFound) {
		return nil, fmt.Errorf("drip: read wallet record: %w", err)
	}
	if ipHash != "" {
		times, err := s.repo.ListByIPSince(ctx, ipHash, now.Add(-s.cfg.IPWindow))
		if err != nil {
			return nil, fmt.Errorf("drip: read ip records: %w", err)
		}
		if len(times) >= s.cfg.IPLimit {
			return nil, &DripLimitedError{Scope: "ip", NextAt: times[0].Add(s.cfg.IPWindow)}
		}
	}

	// What does the recipient still need?
	recipientSol, err := s.chain.GetBalance(ctx, recipient.String())
	if err != nil {
		return nil, fmt.Errorf("%w: recipient balance: %v", ErrDripChain, err)
	}
	recipientAta, err := tx.FindAssociatedTokenAddress(recipient, s.cfg.Mint, s.cfg.TokenProgram)
	if err != nil {
		return nil, fmt.Errorf("drip: recipient ata: %w", err)
	}
	recipientStonk, recipientAtaExists, err := s.chain.GetTokenAccountBalance(ctx, recipientAta.String())
	if err != nil {
		return nil, fmt.Errorf("%w: recipient token balance: %v", ErrDripChain, err)
	}
	sendSol := recipientSol < 0 || uint64(recipientSol) < s.cfg.RecipientSolSkip
	sendStonk := recipientStonk < s.cfg.StonkRaw
	result := &DevDripResult{}
	if !sendSol && !sendStonk {
		return result, nil
	}

	// Can the drip wallet pay for it?
	dripSol, err := s.chain.GetBalance(ctx, s.wallet.String())
	if err != nil {
		return nil, fmt.Errorf("%w: drip wallet balance: %v", ErrDripChain, err)
	}
	need := devDripFeeReserve
	if sendSol {
		need += s.cfg.SolLamports
	}
	if sendStonk && !recipientAtaExists {
		need += devDripAtaRentLamports
	}
	if dripSol < 0 || uint64(dripSol) < need {
		s.logger.Error("[DevDrip] drip wallet is out of SOL", "wallet", s.wallet.String(), "lamports", dripSol, "need", need)
		return nil, ErrDripEmpty
	}
	dripAta, err := tx.FindAssociatedTokenAddress(s.wallet, s.cfg.Mint, s.cfg.TokenProgram)
	if err != nil {
		return nil, fmt.Errorf("drip: drip ata: %w", err)
	}
	if sendStonk {
		held, exists, err := s.chain.GetTokenAccountBalance(ctx, dripAta.String())
		if err != nil {
			return nil, fmt.Errorf("%w: drip wallet token balance: %v", ErrDripChain, err)
		}
		if !exists || held < s.cfg.StonkRaw {
			s.logger.Error("[DevDrip] drip wallet is out of $STONK", "wallet", s.wallet.String(), "ata", dripAta.String(), "held", held, "need", s.cfg.StonkRaw)
			return nil, ErrDripEmpty
		}
	}

	// Build, sign, send, confirm.
	var ixs []tx.Instruction
	if sendSol {
		ixs = append(ixs, tx.SystemTransfer(s.wallet, recipient, s.cfg.SolLamports))
		result.SentSol, result.SolLamports = true, s.cfg.SolLamports
	}
	if sendStonk {
		ixs = append(ixs,
			tx.CreateAssociatedTokenAccountIdempotent(s.wallet, recipientAta, recipient, s.cfg.Mint, s.cfg.TokenProgram),
			tx.TokenTransfer(dripAta, recipientAta, s.wallet, s.cfg.StonkRaw, s.cfg.TokenProgram),
		)
		result.SentStonk, result.StonkRaw = true, s.cfg.StonkRaw
	}
	blockhashStr, err := s.chain.GetLatestBlockhash(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: blockhash: %v", ErrDripChain, err)
	}
	blockhash, err := tx.BlockhashFromBase58(blockhashStr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDripChain, err)
	}
	msg, err := tx.CompileLegacyMessage(s.wallet, blockhash, ixs)
	if err != nil {
		return nil, fmt.Errorf("drip: compile: %w", err)
	}
	signed, err := tx.Sign(msg, s.key)
	if err != nil {
		return nil, fmt.Errorf("drip: sign: %w", err)
	}
	sig, err := s.chain.SendTransaction(ctx, signed.Base64())
	if err != nil {
		return nil, fmt.Errorf("%w: send: %v", ErrDripChain, err)
	}
	if sig == "" {
		sig = signed.Signature()
	}
	if err := s.confirm(ctx, sig); err != nil {
		return nil, err
	}
	result.Signature = sig
	result.Explorer = s.ExplorerURL(sig)

	if err := s.repo.Upsert(ctx, &models.DevDrip{
		Wallet: recipient.String(), IPHash: ipHash, Signature: sig,
		AmountSol: result.SolLamports, AmountStonk: result.StonkRaw, DrippedAt: now,
	}); err != nil {
		// The transfer happened; a lost record only weakens the cooldown.
		s.logger.Error("[DevDrip] record drip failed", "wallet", recipient.String(), "signature", sig, "error", err)
	}
	s.logger.Info("[DevDrip] sent", "wallet", recipient.String(), "signature", sig,
		"sol_lamports", result.SolLamports, "stonk_raw", result.StonkRaw)
	return result, nil
}

// confirm polls getSignatureStatuses until the transaction is confirmed, fails, or the
// timeout elapses.
func (s *DevDripService) confirm(ctx context.Context, sig string) error {
	deadline := time.Now().Add(s.cfg.ConfirmTimeout)
	for {
		st, err := s.chain.GetSignatureStatus(ctx, sig)
		if err != nil {
			return fmt.Errorf("%w: status: %v", ErrDripChain, err)
		}
		if st != nil {
			if st.Failed {
				return ErrDripFailed
			}
			if st.Confirmed {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return ErrDripUnconfirmed
		}
		if s.cfg.ConfirmPoll < 0 {
			// Tests: no real waiting, still bounded by the deadline above.
			continue
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrDripChain, ctx.Err())
		case <-time.After(s.cfg.ConfirmPoll):
		}
	}
}

// SolFloat converts lamports to SOL for responses.
func SolFloat(lamports uint64) float64 { return float64(lamports) / lamportsPerSol }

// TokenFloat converts raw units to whole tokens for responses.
func TokenFloat(raw uint64, decimals uint8) float64 { return float64(raw) / math.Pow10(int(decimals)) }
