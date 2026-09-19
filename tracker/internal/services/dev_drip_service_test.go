// Package: tracker/internal/services
// Feature: StonkAgents devnet drip
// Purpose: DevDripService tests — both legs, each leg skipped, cooldowns, empty drip wallet,
//          chain failures — against a fake chain and the in-memory repository.

package services

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/solana/tx"
)

const (
	dripTestRecipient = "9hSR6S7WPtxmTojgo6GG3k4yDPecgJY292j7xrsUGWBu"
	dripTestBlockhash = "CktRuQ2mttgRGkXJtyksdKHjUdc2C4TgDzyB98oEzy8"
)

// fakeChain answers balances per address and records what was sent.
type fakeChain struct {
	mu        sync.Mutex
	sol       map[string]int64
	tokens    map[string]uint64 // ata -> amount (absent = account missing)
	sent      []string          // base64 transactions
	status    *DevDripSignatureStatus
	statusErr error
	sendErr   error
	balErr    error
	polls     int
}

func newFakeChain() *fakeChain {
	return &fakeChain{sol: map[string]int64{}, tokens: map[string]uint64{}, status: &DevDripSignatureStatus{Confirmed: true}}
}

func (f *fakeChain) GetBalance(_ context.Context, addr string) (int64, error) {
	if f.balErr != nil {
		return 0, f.balErr
	}
	return f.sol[addr], nil
}
func (f *fakeChain) GetTokenAccountBalance(_ context.Context, addr string) (uint64, bool, error) {
	amt, ok := f.tokens[addr]
	return amt, ok, nil
}
func (f *fakeChain) GetLatestBlockhash(context.Context) (string, error) {
	return dripTestBlockhash, nil
}
func (f *fakeChain) SendTransaction(_ context.Context, b64 string) (string, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, b64)
	return "", nil // service falls back to the signature it computed
}
func (f *fakeChain) GetSignatureStatus(context.Context, string) (*DevDripSignatureStatus, error) {
	f.polls++
	return f.status, f.statusErr
}

type dripEnv struct {
	svc   *DevDripService
	chain *fakeChain
	repo  *repository.MemoryDevDripRepository
	clk   *clock.MockClock
	key   ed25519.PrivateKey
	ata   string // drip wallet's ATA
	rAta  string // recipient's ATA
}

func newDripEnv(t *testing.T) *dripEnv {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	e := &dripEnv{chain: newFakeChain(), repo: repository.NewMemoryDevDripRepository(), key: key,
		clk: clock.NewMockClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))}
	svc, err := NewDevDripService(DevDripDeps{Repo: e.repo, Chain: e.chain, Key: key, Clock: e.clk,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: DevDripConfig{ConfirmPoll: -1, ConfirmTimeout: 50 * time.Millisecond, ExplorerClusterSuffix: "?cluster=devnet"}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	mint := tx.MustPubkey(DefaultDevDripMint)
	var wallet tx.Pubkey
	copy(wallet[:], key.Public().(ed25519.PublicKey))
	ata, _ := tx.FindAssociatedTokenAddress(wallet, mint, tx.TokenProgramID)
	rAta, _ := tx.FindAssociatedTokenAddress(tx.MustPubkey(dripTestRecipient), mint, tx.TokenProgramID)
	e.ata, e.rAta = ata.String(), rAta.String()
	// A funded drip wallet: 1 SOL and 1000 STONK.
	e.chain.sol[svc.Wallet()] = 1_000_000_000
	e.chain.tokens[e.ata] = 1_000_000_000
	return e
}

// sentInstructions decodes the last sent transaction and returns its instruction count.
func (e *dripEnv) sentInstructions(t *testing.T) int {
	t.Helper()
	if len(e.chain.sent) == 0 {
		t.Fatal("nothing sent")
	}
	raw, err := base64.StdEncoding.DecodeString(e.chain.sent[len(e.chain.sent)-1])
	if err != nil {
		t.Fatal(err)
	}
	// 1 sig count byte + 64 sig + 3 header + 1 key count + 32*n keys + 32 blockhash + ix count
	n := int(raw[65+3])
	return int(raw[65+3+1+32*n+32])
}

func TestDevDrip_SendsBothLegs(t *testing.T) {
	e := newDripEnv(t)
	res, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.SentSol || !res.SentStonk || res.SolLamports != 50_000_000 || res.StonkRaw != 25_000_000 {
		t.Errorf("result = %+v", res)
	}
	if res.Signature == "" || res.Explorer != "https://solscan.io/tx/"+res.Signature+"?cluster=devnet" {
		t.Errorf("signature/explorer = %q / %q", res.Signature, res.Explorer)
	}
	if got := e.sentInstructions(t); got != 3 {
		t.Errorf("instructions = %d, want 3 (transfer, create ata, token transfer)", got)
	}
	rec, err := e.repo.Get(context.Background(), dripTestRecipient)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Signature != res.Signature || rec.IPHash != HashIP("10.0.0.1") || rec.AmountSol != 50_000_000 || rec.AmountStonk != 25_000_000 || !rec.DrippedAt.Equal(e.clk.Now()) {
		t.Errorf("record = %+v", rec)
	}
}

func TestDevDrip_SkipsSolWhenRecipientHasEnough(t *testing.T) {
	e := newDripEnv(t)
	e.chain.sol[dripTestRecipient] = 100_000_000 // exactly 0.1 SOL
	res, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if res.SentSol || !res.SentStonk || res.SolLamports != 0 {
		t.Errorf("result = %+v", res)
	}
	if got := e.sentInstructions(t); got != 2 {
		t.Errorf("instructions = %d, want 2", got)
	}
}

func TestDevDrip_SkipsStonkWhenRecipientHasEnough(t *testing.T) {
	e := newDripEnv(t)
	e.chain.tokens[e.rAta] = 25_000_000
	res, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.SentSol || res.SentStonk || res.StonkRaw != 0 {
		t.Errorf("result = %+v", res)
	}
	if got := e.sentInstructions(t); got != 1 {
		t.Errorf("instructions = %d, want 1", got)
	}
}

func TestDevDrip_NothingToSend(t *testing.T) {
	e := newDripEnv(t)
	e.chain.sol[dripTestRecipient] = 500_000_000
	e.chain.tokens[e.rAta] = 30_000_000
	res, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if res.SentSol || res.SentStonk || res.Signature != "" || len(e.chain.sent) != 0 {
		t.Errorf("result = %+v, sent = %d", res, len(e.chain.sent))
	}
	if e.repo.Len() != 0 {
		t.Error("nothing sent must not start a cooldown")
	}
}

func TestDevDrip_WalletCooldown(t *testing.T) {
	e := newDripEnv(t)
	if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(23 * time.Hour)
	_, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.2")
	var lim *DripLimitedError
	if !errors.As(err, &lim) || lim.Scope != "wallet" {
		t.Fatalf("err = %v, want wallet DripLimitedError", err)
	}
	if want := e.clk.Now().Add(time.Hour); !lim.NextAt.Equal(want) {
		t.Errorf("NextAt = %s, want %s", lim.NextAt, want)
	}
	e.clk.Advance(time.Hour)
	if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.2"); err != nil {
		t.Errorf("after 24h: %v", err)
	}
	if len(e.chain.sent) != 2 {
		t.Errorf("sent = %d, want 2", len(e.chain.sent))
	}
}

func TestDevDrip_IPLimit(t *testing.T) {
	e := newDripEnv(t)
	wallets := []string{
		dripTestRecipient,
		"So11111111111111111111111111111111111111112",
		"GK45ZT1FJNp8NutUNr2gVsJu8TuG4bB5iysrwDsxd5od",
		"2eCui1mP4ouj4BHEASpqiPNMv8v797ay7URe5MMaJBLv",
	}
	first := e.clk.Now()
	for i, w := range wallets[:3] {
		if _, err := e.svc.Drip(context.Background(), w, "10.0.0.9"); err != nil {
			t.Fatalf("drip %d: %v", i, err)
		}
		e.clk.Advance(10 * time.Minute)
	}
	_, err := e.svc.Drip(context.Background(), wallets[3], "10.0.0.9")
	var lim *DripLimitedError
	if !errors.As(err, &lim) || lim.Scope != "ip" {
		t.Fatalf("err = %v, want ip DripLimitedError", err)
	}
	if want := first.Add(time.Hour); !lim.NextAt.Equal(want) {
		t.Errorf("NextAt = %s, want %s", lim.NextAt, want)
	}
	// Another IP is unaffected.
	if _, err := e.svc.Drip(context.Background(), wallets[3], "10.0.0.10"); err != nil {
		t.Errorf("other ip: %v", err)
	}
}

func TestDevDrip_EmptyDripWallet(t *testing.T) {
	t.Run("sol", func(t *testing.T) {
		e := newDripEnv(t)
		e.chain.sol[e.svc.Wallet()] = 50_000_000 // the drip amount but no fee/rent headroom
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripEmpty) {
			t.Errorf("err = %v, want ErrDripEmpty", err)
		}
	})
	t.Run("stonk", func(t *testing.T) {
		e := newDripEnv(t)
		e.chain.tokens[e.ata] = 24_999_999
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripEmpty) {
			t.Errorf("err = %v, want ErrDripEmpty", err)
		}
	})
	t.Run("no ata", func(t *testing.T) {
		e := newDripEnv(t)
		delete(e.chain.tokens, e.ata)
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripEmpty) {
			t.Errorf("err = %v, want ErrDripEmpty", err)
		}
	})
}

func TestDevDrip_InvalidAndSelf(t *testing.T) {
	e := newDripEnv(t)
	if _, err := e.svc.Drip(context.Background(), "nope", "10.0.0.1"); !errors.Is(err, ErrDripInvalidWallet) {
		t.Errorf("err = %v", err)
	}
	if _, err := e.svc.Drip(context.Background(), e.svc.Wallet(), "10.0.0.1"); !errors.Is(err, ErrDripSelf) {
		t.Errorf("err = %v", err)
	}
}

func TestDevDrip_ChainFailures(t *testing.T) {
	t.Run("balance", func(t *testing.T) {
		e := newDripEnv(t)
		e.chain.balErr = errors.New("rpc down")
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripChain) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("send", func(t *testing.T) {
		e := newDripEnv(t)
		e.chain.sendErr = errors.New("preflight failed")
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripChain) {
			t.Errorf("err = %v", err)
		}
		if e.repo.Len() != 0 {
			t.Error("a failed send must not start a cooldown")
		}
	})
	t.Run("failed on chain", func(t *testing.T) {
		e := newDripEnv(t)
		e.chain.status = &DevDripSignatureStatus{Failed: true}
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripFailed) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unconfirmed", func(t *testing.T) {
		e := newDripEnv(t)
		e.chain.status = nil
		if _, err := e.svc.Drip(context.Background(), dripTestRecipient, "10.0.0.1"); !errors.Is(err, ErrDripUnconfirmed) {
			t.Errorf("err = %v", err)
		}
		if e.chain.polls < 2 {
			t.Errorf("polls = %d, want repeated polling until the deadline", e.chain.polls)
		}
	})
}

func TestDevDrip_Conversions(t *testing.T) {
	if SolFloat(50_000_000) != 0.05 || TokenFloat(25_000_000, 6) != 25 {
		t.Error("conversions")
	}
}
