// Package: tracker/internal/solana/tx
// Feature: StonkAgents devnet drip
// Purpose: Byte-exactness against @solana/web3.js + @solana/spl-token (testdata/drip_legacy_tx.json),
//          ATA derivation, compact-u16 and signer checks.

package tx

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
)

// fixture is printed once by a Node script (see testdata/drip_legacy_tx.json "_source").
type fixture struct {
	Payer        string   `json:"payer"`
	Recipient    string   `json:"recipient"`
	Mint         string   `json:"mint"`
	Blockhash    string   `json:"blockhash"`
	PayerAta     string   `json:"payerAta"`
	RecipientAta string   `json:"recipientAta"`
	Lamports     uint64   `json:"lamports"`
	Amount       string   `json:"amount"`
	AccountKeys  []string `json:"accountKeys"`
	Header       struct {
		NumRequiredSignatures       uint8 `json:"numRequiredSignatures"`
		NumReadonlySignedAccounts   uint8 `json:"numReadonlySignedAccounts"`
		NumReadonlyUnsignedAccounts uint8 `json:"numReadonlyUnsignedAccounts"`
	} `json:"header"`
	MessageHex   string `json:"messageHex"`
	SignedTxHex  string `json:"signedTxHex"`
	SignatureHex string `json:"signatureHex"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/drip_legacy_tx.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f
}

// fixtureKey is Keypair.fromSeed(new Uint8Array(32).fill(1)) on the JS side.
func fixtureKey() ed25519.PrivateKey {
	seed := bytes.Repeat([]byte{1}, 32)
	return ed25519.NewKeyFromSeed(seed)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return b
}

// buildFixtureTx builds the drip transaction (SOL transfer + ATA CreateIdempotent + token transfer)
// exactly as the Node script did.
func buildFixtureTx(t *testing.T, f fixture) (*Message, ed25519.PrivateKey) {
	t.Helper()
	key := fixtureKey()
	var payer Pubkey
	copy(payer[:], key.Public().(ed25519.PublicKey))
	if payer.String() != f.Payer {
		t.Fatalf("payer derived from seed = %s, fixture %s", payer, f.Payer)
	}
	recipient := MustPubkey(f.Recipient)
	mint := MustPubkey(f.Mint)
	payerAta, err := FindAssociatedTokenAddress(payer, mint, TokenProgramID)
	if err != nil {
		t.Fatal(err)
	}
	recipientAta, err := FindAssociatedTokenAddress(recipient, mint, TokenProgramID)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := strconv.ParseUint(f.Amount, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	bh, err := BlockhashFromBase58(f.Blockhash)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := CompileLegacyMessage(payer, bh, []Instruction{
		SystemTransfer(payer, recipient, f.Lamports),
		CreateAssociatedTokenAccountIdempotent(payer, recipientAta, recipient, mint, TokenProgramID),
		TokenTransfer(payerAta, recipientAta, payer, amount, TokenProgramID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg, key
}

func TestFindAssociatedTokenAddress_MatchesSplToken(t *testing.T) {
	f := loadFixture(t)
	mint := MustPubkey(f.Mint)
	for _, tc := range []struct{ owner, want string }{{f.Payer, f.PayerAta}, {f.Recipient, f.RecipientAta}} {
		got, err := FindAssociatedTokenAddress(MustPubkey(tc.owner), mint, TokenProgramID)
		if err != nil {
			t.Fatalf("ata(%s): %v", tc.owner, err)
		}
		if got.String() != tc.want {
			t.Errorf("ata(%s) = %s, want %s", tc.owner, got, tc.want)
		}
	}
}

func TestCompileLegacyMessage_ByteExactWithWeb3js(t *testing.T) {
	f := loadFixture(t)
	msg, _ := buildFixtureTx(t, f)

	if msg.Header.NumRequiredSignatures != f.Header.NumRequiredSignatures ||
		msg.Header.NumReadonlySignedAccounts != f.Header.NumReadonlySignedAccounts ||
		msg.Header.NumReadonlyUnsignedAccounts != f.Header.NumReadonlyUnsignedAccounts {
		t.Errorf("header = %+v, want %+v", msg.Header, f.Header)
	}
	if len(msg.AccountKeys) != len(f.AccountKeys) {
		t.Fatalf("account keys = %d, want %d", len(msg.AccountKeys), len(f.AccountKeys))
	}
	for i, k := range msg.AccountKeys {
		if k.String() != f.AccountKeys[i] {
			t.Errorf("accountKeys[%d] = %s, want %s", i, k, f.AccountKeys[i])
		}
	}
	if got, want := msg.Serialize(), mustHex(t, f.MessageHex); !bytes.Equal(got, want) {
		t.Errorf("message bytes differ\n got %x\nwant %x", got, want)
	}
}

func TestSign_ByteExactWithWeb3js(t *testing.T) {
	f := loadFixture(t)
	msg, key := buildFixtureTx(t, f)
	signed, err := Sign(msg, key)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := signed.Signatures[0][:], mustHex(t, f.SignatureHex); !bytes.Equal(got, want) {
		t.Errorf("signature differs\n got %x\nwant %x", got, want)
	}
	if got, want := signed.Serialize(), mustHex(t, f.SignedTxHex); !bytes.Equal(got, want) {
		t.Errorf("signed tx bytes differ\n got %x\nwant %x", got, want)
	}
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), msg.Serialize(), signed.Signatures[0][:]) {
		t.Error("signature does not verify")
	}
	if signed.Signature() == "" || signed.Base64() == "" {
		t.Error("Signature()/Base64() must be set")
	}
}

func TestSign_MissingSigner(t *testing.T) {
	f := loadFixture(t)
	msg, _ := buildFixtureTx(t, f)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	if _, err := Sign(msg, other); !errors.Is(err, ErrMissingSigner) {
		t.Errorf("err = %v, want ErrMissingSigner", err)
	}
}

func TestCompileLegacyMessage_Ordering(t *testing.T) {
	payer := MustPubkey("AKnL4NNf3DGWZJS6cPknBuEGnVsV4A4m5tgebLHaRSZ9")
	a := MustPubkey("9hSR6S7WPtxmTojgo6GG3k4yDPecgJY292j7xrsUGWBu")
	b := MustPubkey("GK45ZT1FJNp8NutUNr2gVsJu8TuG4bB5iysrwDsxd5od")
	prog := SystemProgramID
	// a is readonly in the first instruction and writable in the second: flags merge and
	// the account stays at its first-appearance slot inside the writable non-signer group.
	msg, err := CompileLegacyMessage(payer, [32]byte{}, []Instruction{
		{ProgramID: prog, Accounts: []AccountMeta{{Pubkey: a}, {Pubkey: b, IsSigner: true}}},
		{ProgramID: prog, Accounts: []AccountMeta{{Pubkey: a, IsWritable: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Pubkey{payer, b, a, prog}
	if len(msg.AccountKeys) != len(want) {
		t.Fatalf("keys = %v", msg.AccountKeys)
	}
	for i := range want {
		if msg.AccountKeys[i] != want[i] {
			t.Errorf("keys[%d] = %s, want %s", i, msg.AccountKeys[i], want[i])
		}
	}
	if msg.Header != (MessageHeader{NumRequiredSignatures: 2, NumReadonlySignedAccounts: 1, NumReadonlyUnsignedAccounts: 1}) {
		t.Errorf("header = %+v", msg.Header)
	}
	if _, err := CompileLegacyMessage(payer, [32]byte{}, nil); !errors.Is(err, ErrNoInstructions) {
		t.Errorf("empty: %v", err)
	}
}

func TestEncodeCompactU16(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []byte
	}{
		{0, []byte{0x00}}, {1, []byte{0x01}}, {127, []byte{0x7f}}, {128, []byte{0x80, 0x01}},
		{255, []byte{0xff, 0x01}}, {16383, []byte{0xff, 0x7f}}, {16384, []byte{0x80, 0x80, 0x01}},
	} {
		if got := encodeCompactU16(tc.n); !bytes.Equal(got, tc.want) {
			t.Errorf("encodeCompactU16(%d) = %x, want %x", tc.n, got, tc.want)
		}
	}
}

func TestPubkeyFromBase58_Rejects(t *testing.T) {
	if _, err := PubkeyFromBase58("not-base58-0OIl"); err == nil {
		t.Error("invalid base58 accepted")
	}
	if _, err := PubkeyFromBase58("abc"); err == nil {
		t.Error("short key accepted")
	}
}
