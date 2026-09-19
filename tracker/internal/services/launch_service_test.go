// Package: tracker/internal/services
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: LaunchService tests — record, idempotency, verification failures, claim + reward

package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mr-tron/base58"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// testKey returns a deterministic base58 32-byte pubkey seeded by b.
func testKey(b byte) string {
	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = b + byte(i)
	}
	return base58.Encode(buf)
}

// testSig returns a deterministic base58 64-byte signature seeded by b.
func testSig(b byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = b ^ byte(i)
	}
	return base58.Encode(buf)
}

var (
	tMint     = testKey(1)
	tPool     = testKey(2)
	tCreator  = testKey(3)
	tTreasury = testKey(4)
	tPlatform = testKey(5)
	tOther    = testKey(6)
	tSig      = testSig(7)
)

type launchTestDeps struct {
	launches *repository.MemoryLaunchRepository
	revenue  *repository.MemoryRevenueRepository
	tokens   *repository.MemoryTokenRepository
	accounts *repository.MemoryAccountRepository
	wallets  *repository.MemoryWalletRepository
	credits  *repository.MemoryCreditRepository
	verifier *StubLaunchVerifier
	clock    *clock.MockClock
}

func newLaunchTestDeps() launchTestDeps {
	clk := clock.NewMockClock(time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC))
	return launchTestDeps{
		launches: repository.NewMemoryLaunchRepository(),
		revenue:  repository.NewMemoryRevenueRepository(),
		tokens:   repository.NewMemoryTokenRepository(),
		accounts: repository.NewMemoryAccountRepository(),
		wallets:  repository.NewMemoryWalletRepository(),
		credits:  repository.NewMemoryCreditRepositoryWithClock(clk),
		verifier: &StubLaunchVerifier{},
		clock:    clk,
	}
}

func newLaunchService(d launchTestDeps, minFee int64) *LaunchService {
	return NewLaunchService(LaunchServiceDeps{
		Launches: d.launches, Revenue: d.revenue, Tokens: d.tokens, Accounts: d.accounts,
		Wallets: d.wallets, Credits: d.credits, Verifier: d.verifier, Clock: d.clock,
		TreasuryAddress: tTreasury, PlatformID: tPlatform, MinFeeLamports: minFee,
	})
}

func recordInput() RecordLaunchInput {
	return RecordLaunchInput{
		Mint: tMint, PoolID: tPool, CreatorWallet: tCreator, QuoteMint: DefaultLaunchQuoteMint,
		Name: "Stonk Agent", Symbol: "STNK", ImageURL: "https://cdn.example/img.png",
		MetadataURI: "ipfs://meta", LaunchSignature: tSig, FeeLamports: 20_000_000, TransferFeeBps: 100,
	}
}

// linkPeerWallet registers an account + balance + linked wallet for peerID.
func linkPeerWallet(t *testing.T, d launchTestDeps, peerID, wallet string) string {
	t.Helper()
	ctx := context.Background()
	acc, _, err := d.accounts.GetOrCreateForPeer(ctx, peerID)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := d.credits.CreateBalance(ctx, &models.CreditBalance{AccountID: acc.ID, UpdatedAt: d.clock.Now()}); err != nil {
		t.Fatalf("create balance: %v", err)
	}
	if err := d.wallets.UpsertWalletForDisplay(ctx, acc.ID, wallet, "solana"); err != nil {
		t.Fatalf("link wallet: %v", err)
	}
	return acc.ID
}

func TestLaunchService_Record_HappyPath(t *testing.T) {
	d := newLaunchTestDeps()
	svc := newLaunchService(d, 10_000_000)

	launch, created, err := svc.Record(context.Background(), recordInput())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first record")
	}
	if launch.Status != models.LaunchStatusConfirmed || launch.PeerID != "" {
		t.Errorf("status/peer = %q/%q, want confirmed/empty", launch.Status, launch.PeerID)
	}
	if launch.FeeLamports != 20_000_000 {
		t.Errorf("fee_lamports = %d, want 20000000 (max of floor and quoted, reported by stub)", launch.FeeLamports)
	}
	if launch.PlatformID != tPlatform {
		t.Errorf("platform_id = %q, want %q", launch.PlatformID, tPlatform)
	}
	if !launch.CreatedAt.Equal(d.clock.Now()) {
		t.Errorf("created_at = %v, want clock time", launch.CreatedAt)
	}

	totals, _ := d.revenue.TotalsByKind(context.Background())
	if len(totals) != 1 || totals[0].Kind != models.RevenueKindLaunchFee || totals[0].AmountRaw != 20_000_000 || totals[0].Count != 1 {
		t.Fatalf("revenue totals = %+v, want one launch_fee of 20000000", totals)
	}
	if stored, err := d.launches.GetBySignature(context.Background(), tSig); err != nil || stored.Mint != tMint {
		t.Fatalf("launch not stored by signature: %v", err)
	}
}

func TestLaunchService_Record_IdempotentOnSignature(t *testing.T) {
	d := newLaunchTestDeps()
	svc := newLaunchService(d, 0)

	first, created, err := svc.Record(context.Background(), recordInput())
	if err != nil || !created {
		t.Fatalf("first Record: created=%v err=%v", created, err)
	}
	d.clock.Advance(time.Hour)
	second, created, err := svc.Record(context.Background(), recordInput())
	if err != nil {
		t.Fatalf("second Record: %v", err)
	}
	if created {
		t.Error("expected created=false on re-record")
	}
	if second.Mint != first.Mint || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("re-record returned a different record: %+v vs %+v", second, first)
	}
	if d.verifier.Calls != 1 {
		t.Errorf("verifier calls = %d, want 1 (re-record must not hit RPC)", d.verifier.Calls)
	}
	totals, _ := d.revenue.TotalsByKind(context.Background())
	if len(totals) != 1 || totals[0].Count != 1 {
		t.Errorf("revenue rows = %+v, want exactly one launch_fee", totals)
	}

	// Same signature, different mint → conflict.
	in := recordInput()
	in.Mint = tOther
	if _, _, err := svc.Record(context.Background(), in); !errors.Is(err, ErrLaunchSignatureConflict) {
		t.Errorf("signature reuse err = %v, want ErrLaunchSignatureConflict", err)
	}
	// Same mint, different signature → conflict.
	in = recordInput()
	in.LaunchSignature = testSig(9)
	if _, _, err := svc.Record(context.Background(), in); !errors.Is(err, ErrLaunchMintConflict) {
		t.Errorf("mint reuse err = %v, want ErrLaunchMintConflict", err)
	}
}

func TestLaunchService_Record_RejectsFeeMissing(t *testing.T) {
	d := newLaunchTestDeps()
	d.verifier.FeeMissing = true
	svc := newLaunchService(d, 10_000_000)

	_, _, err := svc.Record(context.Background(), recordInput())
	if !errors.Is(err, ErrLaunchFeeMissing) {
		t.Fatalf("err = %v, want ErrLaunchFeeMissing", err)
	}
	if _, err := d.launches.GetByMint(context.Background(), tMint); !errors.Is(err, models.ErrNotFound) {
		t.Error("launch must not be stored when the fee transfer is missing")
	}
	totals, _ := d.revenue.TotalsByKind(context.Background())
	if len(totals) != 0 {
		t.Errorf("revenue must stay empty, got %+v", totals)
	}

	// Below the quoted amount is also rejected (accept >= only).
	d2 := newLaunchTestDeps()
	d2.verifier.FeeLamports = 19_999_999
	if _, _, err := newLaunchService(d2, 0).Record(context.Background(), recordInput()); !errors.Is(err, ErrLaunchFeeMissing) {
		t.Errorf("underpaid err = %v, want ErrLaunchFeeMissing", err)
	}
	// Overpaying is fine and the on-chain amount is what gets stored.
	d3 := newLaunchTestDeps()
	d3.verifier.FeeLamports = 25_000_000
	l, _, err := newLaunchService(d3, 0).Record(context.Background(), recordInput())
	if err != nil || l.FeeLamports != 25_000_000 {
		t.Errorf("overpaid: err=%v fee=%d, want nil/25000000", err, l.FeeLamports)
	}
}

func TestLaunchService_Record_OtherVerificationFailures(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*StubLaunchVerifier)
		want error
	}{
		{"tx not found", func(v *StubLaunchVerifier) { v.NotFound = true }, ErrLaunchTxNotFound},
		{"tx failed", func(v *StubLaunchVerifier) { v.Failed = true }, ErrLaunchTxFailed},
		{"creator not signer", func(v *StubLaunchVerifier) { v.CreatorNotSigner = true }, ErrLaunchCreatorNotSigner},
		{"no launchlab ix", func(v *StubLaunchVerifier) { v.NoLaunchLabInstruction = true }, ErrLaunchProgramMissing},
		{"mint absent", func(v *StubLaunchVerifier) { v.OmitMint = true }, ErrLaunchProgramMissing},
		{"rpc down", func(v *StubLaunchVerifier) { v.Err = errors.New("boom") }, ErrLaunchVerifyUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newLaunchTestDeps()
			tc.mut(d.verifier)
			_, _, err := newLaunchService(d, 0).Record(context.Background(), recordInput())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("treasury not configured", func(t *testing.T) {
		d := newLaunchTestDeps()
		svc := NewLaunchService(LaunchServiceDeps{Launches: d.launches, Verifier: d.verifier, Clock: d.clock})
		if _, _, err := svc.Record(context.Background(), recordInput()); !errors.Is(err, ErrLaunchNotConfigured) {
			t.Fatalf("err = %v, want ErrLaunchNotConfigured", err)
		}
	})
}

// Launches are quoted in $STONK only. The rule is checked before the on-chain verification
// (no RPC spent on a rejected quote) and follows the catalog when one is wired: the devnet
// stand-in (category "stonk") passes, an enabled SOL row does not; without a catalog only
// the mainnet $STONK mint passes.
func TestLaunchService_Record_RejectsNonSTONKQuote(t *testing.T) {
	t.Run("no catalog: SOL rejected before verification", func(t *testing.T) {
		d := newLaunchTestDeps()
		in := recordInput()
		in.QuoteMint = models.SOLMint
		_, _, err := newLaunchService(d, 0).Record(context.Background(), in)
		if !errors.Is(err, ErrLaunchQuoteNotAllowed) {
			t.Fatalf("err = %v, want ErrLaunchQuoteNotAllowed", err)
		}
		if d.verifier.Calls != 0 {
			t.Errorf("verifier calls = %d, want 0", d.verifier.Calls)
		}
	})

	t.Run("catalog: enabled SOL row is still not launchable", func(t *testing.T) {
		d := newLaunchTestDeps()
		quotes := repository.NewMemoryLaunchQuoteRepository()
		quotes.Put(&models.LaunchQuote{QuoteMint: models.SOLMint, Symbol: "SOL", Category: "solana", Enabled: true})
		svc := NewLaunchService(LaunchServiceDeps{Launches: d.launches, Revenue: d.revenue, Verifier: d.verifier, Clock: d.clock,
			Quotes: quotes, TreasuryAddress: tTreasury})
		in := recordInput()
		in.QuoteMint = models.SOLMint
		if _, _, err := svc.Record(context.Background(), in); !errors.Is(err, ErrLaunchQuoteNotAllowed) {
			t.Fatalf("err = %v, want ErrLaunchQuoteNotAllowed", err)
		}
		// Mainnet $STONK is not in this (devnet-like) catalog either: not allowed.
		if _, _, err := svc.Record(context.Background(), recordInput()); !errors.Is(err, ErrLaunchQuoteNotAllowed) {
			t.Fatalf("uncatalogued $STONK err = %v, want ErrLaunchQuoteNotAllowed", err)
		}
	})

	t.Run("catalog: devnet stand-in passes by category", func(t *testing.T) {
		d := newLaunchTestDeps()
		quotes := repository.NewMemoryLaunchQuoteRepositoryForCluster(models.LaunchClusterDevnet)
		quotes.Put(&models.LaunchQuote{Cluster: models.LaunchClusterDevnet, QuoteMint: testDevnetSTONKMint, Symbol: "STONK",
			Category: models.LaunchQuoteCategorySTONK, Enabled: true})
		svc := NewLaunchService(LaunchServiceDeps{Launches: d.launches, Revenue: d.revenue, Verifier: d.verifier, Clock: d.clock,
			Quotes: quotes, TreasuryAddress: tTreasury})
		in := recordInput()
		in.QuoteMint = testDevnetSTONKMint
		launch, created, err := svc.Record(context.Background(), in)
		if err != nil || !created || launch.QuoteMint != testDevnetSTONKMint {
			t.Fatalf("Record(stand-in) = %+v, %v, %v", launch, created, err)
		}
	})
}

func TestLaunchService_Claim_HappyPath_GrantsRewardOnce(t *testing.T) {
	d := newLaunchTestDeps()
	svc := newLaunchService(d, 0)
	ctx := context.Background()
	if _, _, err := svc.Record(ctx, recordInput()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	accID := linkPeerWallet(t, d, "peer-1", tCreator)

	res, err := svc.Claim(ctx, "peer-1", tMint)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.CreditsGranted != LaunchRewardCredits || res.AlreadyBound {
		t.Errorf("granted=%d already=%v, want 250/false", res.CreditsGranted, res.AlreadyBound)
	}
	if res.Launch.Status != models.LaunchStatusBound || res.Launch.PeerID != "peer-1" || res.Launch.BoundAt == nil {
		t.Errorf("launch after claim = %+v, want bound to peer-1", res.Launch)
	}
	bal, _ := d.credits.GetBalance(ctx, accID)
	if bal.FreeBalance != LaunchRewardCredits {
		t.Errorf("free balance = %d, want %d", bal.FreeBalance, LaunchRewardCredits)
	}
	tx, err := d.credits.GetTransactionByRequestID(ctx, accID, "launch:"+tMint)
	if err != nil || tx.Reason != LaunchRewardReason || tx.Amount != LaunchRewardCredits {
		t.Errorf("reward tx = %+v err=%v, want reason %q amount 250", tx, err, LaunchRewardReason)
	}
	tok, err := d.tokens.GetByPeerID(ctx, "peer-1")
	if err != nil || tok.TokenContractAddress != tMint || tok.TokenTicker != "STNK" || tok.TokenName != "Stonk Agent" {
		t.Errorf("peer_tokens mirror = %+v err=%v", tok, err)
	}

	// Second claim by the same peer: idempotent, no second grant.
	res2, err := svc.Claim(ctx, "peer-1", tMint)
	if err != nil || !res2.AlreadyBound || res2.CreditsGranted != 0 {
		t.Fatalf("re-claim: res=%+v err=%v, want already_bound with 0 credits", res2, err)
	}
	bal, _ = d.credits.GetBalance(ctx, accID)
	if bal.FreeBalance != LaunchRewardCredits {
		t.Errorf("free balance after re-claim = %d, want %d (no double grant)", bal.FreeBalance, LaunchRewardCredits)
	}

	// Another peer that also controls... cannot: the launch is bound already.
	linkPeerWallet(t, d, "peer-2", tOther)
	if _, err := svc.Claim(ctx, "peer-2", tMint); !errors.Is(err, ErrLaunchAlreadyBound) {
		t.Errorf("claim by other peer err = %v, want ErrLaunchAlreadyBound", err)
	}

	// Pending list no longer includes the bound launch.
	pending, _ := svc.Pending(ctx, tCreator, 10)
	if len(pending) != 0 {
		t.Errorf("pending after claim = %d, want 0", len(pending))
	}
}

func TestLaunchService_Claim_RejectsWalletMismatch(t *testing.T) {
	d := newLaunchTestDeps()
	svc := newLaunchService(d, 0)
	ctx := context.Background()
	if _, _, err := svc.Record(ctx, recordInput()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	accID := linkPeerWallet(t, d, "peer-x", tOther)

	_, err := svc.Claim(ctx, "peer-x", tMint)
	if !errors.Is(err, ErrLaunchWalletMismatch) {
		t.Fatalf("err = %v, want ErrLaunchWalletMismatch", err)
	}
	l, _ := d.launches.GetByMint(ctx, tMint)
	if l.IsBound() {
		t.Error("launch must stay unbound after a rejected claim")
	}
	bal, _ := d.credits.GetBalance(ctx, accID)
	if bal.FreeBalance != 0 {
		t.Errorf("no credits may be granted on mismatch, got %d", bal.FreeBalance)
	}

	// Peer with no linked wallet at all.
	if _, err := svc.Claim(ctx, "peer-nowallet", tMint); !errors.Is(err, ErrLaunchWalletNotLinked) {
		t.Errorf("no wallet err = %v, want ErrLaunchWalletNotLinked", err)
	}
	// Unknown mint.
	if _, err := svc.Claim(ctx, "peer-x", tOther); !errors.Is(err, ErrLaunchNotFound) {
		t.Errorf("unknown mint err = %v, want ErrLaunchNotFound", err)
	}

	// Pending still lists it for the real creator.
	pending, _ := svc.Pending(ctx, tCreator, 10)
	if len(pending) != 1 || pending[0].Mint != tMint {
		t.Errorf("pending = %+v, want the unbound launch", pending)
	}
}

func TestLaunchService_List_FiltersByCreatorNewestFirst(t *testing.T) {
	d := newLaunchTestDeps()
	svc := newLaunchService(d, 0)
	ctx := context.Background()

	if _, _, err := svc.Record(ctx, recordInput()); err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	d.clock.Advance(time.Minute)
	in := recordInput()
	in.Mint, in.LaunchSignature, in.PoolID = tOther, testSig(11), testKey(12)
	if _, _, err := svc.Record(ctx, in); err != nil {
		t.Fatalf("Record 2: %v", err)
	}
	d.clock.Advance(time.Minute)
	in = recordInput()
	in.Mint, in.LaunchSignature, in.CreatorWallet = testKey(20), testSig(21), testKey(22)
	if _, _, err := svc.Record(ctx, in); err != nil {
		t.Fatalf("Record 3: %v", err)
	}

	all, total, err := svc.List(ctx, "", 10, 0)
	if err != nil || total != 3 || len(all) != 3 || all[0].Mint != testKey(20) || all[2].Mint != tMint {
		t.Fatalf("List all = %d/%d err=%v (first=%s)", len(all), total, err, all[0].Mint)
	}
	mine, total, _ := svc.List(ctx, tCreator, 1, 0)
	if total != 2 || len(mine) != 1 || mine[0].Mint != tOther {
		t.Errorf("List creator page1 = %+v total=%d, want tOther/2", mine, total)
	}
	page2, _, _ := svc.List(ctx, tCreator, 1, 1)
	if len(page2) != 1 || page2[0].Mint != tMint {
		t.Errorf("List creator page2 = %+v, want tMint", page2)
	}
	if got, err := svc.Get(ctx, tOther); err != nil || got.PoolID != testKey(12) {
		t.Errorf("Get = %+v err=%v", got, err)
	}
}

func TestLaunchService_Record_OnePerWallet(t *testing.T) {
	d := newLaunchTestDeps()
	svc := NewLaunchService(LaunchServiceDeps{
		Launches: d.launches, Revenue: d.revenue, Tokens: d.tokens, Accounts: d.accounts,
		Wallets: d.wallets, Credits: d.credits, Verifier: d.verifier, Clock: d.clock,
		TreasuryAddress: tTreasury, PlatformID: tPlatform, OnePerWallet: true,
	})
	if !svc.OnePerWallet() {
		t.Fatal("OnePerWallet() = false")
	}
	first, created, err := svc.Record(context.Background(), recordInput())
	if err != nil || !created {
		t.Fatalf("first Record = %+v, %v, %v", first, created, err)
	}
	calls := d.verifier.Calls

	second := recordInput()
	second.Mint, second.PoolID, second.LaunchSignature = tOther, testKey(8), testSig(9)
	_, _, err = svc.Record(context.Background(), second)
	var exists *LaunchExistsError
	if !errors.As(err, &exists) || exists.Mint != tMint || !errors.Is(err, ErrLaunchWalletHasLaunch) {
		t.Fatalf("second Record err = %v, want *LaunchExistsError{Mint: %s}", err, tMint)
	}
	if d.verifier.Calls != calls {
		t.Errorf("verifier called for the rejected launch")
	}
	if _, err := d.launches.GetByMint(context.Background(), tOther); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("rejected launch was stored: %v", err)
	}
	// Same signature again: idempotent replay, not a conflict.
	if l, created, err := svc.Record(context.Background(), recordInput()); err != nil || created || l.Mint != tMint {
		t.Errorf("replay = %+v, %v, %v; want the stored launch with created=false", l, created, err)
	}
	// Another creator is unaffected.
	third := second
	third.CreatorWallet, third.Mint, third.PoolID, third.LaunchSignature = testKey(10), testKey(11), testKey(12), testSig(13)
	if _, created, err := svc.Record(context.Background(), third); err != nil || !created {
		t.Errorf("other creator Record = %v, %v; want created", created, err)
	}
	// ByWallet lists claimed and unclaimed launches alike.
	items, err := svc.ByWallet(context.Background(), tCreator, 10)
	if err != nil || len(items) != 1 || items[0].Mint != tMint {
		t.Errorf("ByWallet = %+v, %v", items, err)
	}

	// With the rule off the same second launch is accepted.
	off := newLaunchService(d, 0)
	if _, created, err := off.Record(context.Background(), second); err != nil || !created {
		t.Errorf("OnePerWallet=false Record = %v, %v; want created", created, err)
	}
	if items, _ := svc.ByWallet(context.Background(), tCreator, 10); len(items) != 2 {
		t.Errorf("ByWallet after second launch = %d, want 2", len(items))
	}
}
