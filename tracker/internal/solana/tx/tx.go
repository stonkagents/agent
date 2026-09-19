// Package: tracker/internal/solana/tx
// Feature: StonkAgents devnet drip
// Purpose: Legacy (non-versioned) Solana transaction building and ed25519 signing with the
//          standard library only. Account ordering follows the runtime's CompiledKeys rules
//          (payer first, then writable signers, readonly signers, writable non-signers,
//          readonly non-signers — each group in first-appearance order), which is what
//          @solana/web3.js compileToLegacyMessage and the Rust SDK produce, so a message built
//          here is byte-identical to one built by the reference libraries (see tx_test.go).

package tx

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/mr-tron/base58"
)

// Pubkey is a 32-byte ed25519 public key / account address.
type Pubkey [32]byte

// PubkeyFromBase58 decodes a base58 address into a Pubkey.
func PubkeyFromBase58(s string) (Pubkey, error) {
	var out Pubkey
	raw, err := base58.Decode(s)
	if err != nil {
		return out, fmt.Errorf("tx: pubkey %q: %w", s, err)
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("tx: pubkey %q must be 32 bytes, got %d", s, len(raw))
	}
	copy(out[:], raw)
	return out, nil
}

// MustPubkey decodes a base58 address or panics (package-level program ids only).
func MustPubkey(s string) Pubkey {
	p, err := PubkeyFromBase58(s)
	if err != nil {
		panic(err)
	}
	return p
}

// String returns the base58 form.
func (p Pubkey) String() string { return base58.Encode(p[:]) }

// AccountMeta is an account referenced by an instruction.
type AccountMeta struct {
	Pubkey     Pubkey
	IsSigner   bool
	IsWritable bool
}

// Instruction is a program invocation before compilation.
type Instruction struct {
	ProgramID Pubkey
	Accounts  []AccountMeta
	Data      []byte
}

// CompiledInstruction references accounts by index into Message.AccountKeys.
type CompiledInstruction struct {
	ProgramIDIndex uint8
	AccountIndexes []uint8
	Data           []byte
}

// MessageHeader is the three-byte legacy message header.
type MessageHeader struct {
	NumRequiredSignatures       uint8
	NumReadonlySignedAccounts   uint8
	NumReadonlyUnsignedAccounts uint8
}

// Message is a compiled legacy transaction message.
type Message struct {
	Header          MessageHeader
	AccountKeys     []Pubkey
	RecentBlockhash [32]byte
	Instructions    []CompiledInstruction
}

// Transaction is a signed legacy transaction.
type Transaction struct {
	Signatures [][64]byte
	Message    *Message
}

var (
	// ErrNoInstructions is returned when compiling an empty instruction list.
	ErrNoInstructions = errors.New("tx: no instructions")
	// ErrTooManyAccounts is returned when a message references more than 256 accounts.
	ErrTooManyAccounts = errors.New("tx: max static account keys length exceeded")
	// ErrMissingSigner is returned when Sign lacks the key for a required signer.
	ErrMissingSigner = errors.New("tx: missing signer")
)

// keyMeta mirrors CompiledKeys' per-address flags.
type keyMeta struct {
	isSigner   bool
	isWritable bool
}

// CompileLegacyMessage compiles instructions into a legacy message paid for by payer.
func CompileLegacyMessage(payer Pubkey, recentBlockhash [32]byte, instructions []Instruction) (*Message, error) {
	if len(instructions) == 0 {
		return nil, ErrNoInstructions
	}
	order := []Pubkey{}
	metas := map[Pubkey]*keyMeta{}
	getOrInsert := func(k Pubkey) *keyMeta {
		if m, ok := metas[k]; ok {
			return m
		}
		m := &keyMeta{}
		metas[k] = m
		order = append(order, k)
		return m
	}
	pm := getOrInsert(payer)
	pm.isSigner, pm.isWritable = true, true
	for _, ix := range instructions {
		getOrInsert(ix.ProgramID)
		for _, a := range ix.Accounts {
			m := getOrInsert(a.Pubkey)
			m.isSigner = m.isSigner || a.IsSigner
			m.isWritable = m.isWritable || a.IsWritable
		}
	}
	if len(order) > 256 {
		return nil, ErrTooManyAccounts
	}
	var writableSigners, readonlySigners, writableNonSigners, readonlyNonSigners []Pubkey
	for _, k := range order {
		m := metas[k]
		switch {
		case m.isSigner && m.isWritable:
			writableSigners = append(writableSigners, k)
		case m.isSigner:
			readonlySigners = append(readonlySigners, k)
		case m.isWritable:
			writableNonSigners = append(writableNonSigners, k)
		default:
			readonlyNonSigners = append(readonlyNonSigners, k)
		}
	}
	keys := make([]Pubkey, 0, len(order))
	keys = append(keys, writableSigners...)
	keys = append(keys, readonlySigners...)
	keys = append(keys, writableNonSigners...)
	keys = append(keys, readonlyNonSigners...)
	index := make(map[Pubkey]uint8, len(keys))
	for i, k := range keys {
		index[k] = uint8(i)
	}
	msg := &Message{
		Header: MessageHeader{
			NumRequiredSignatures:       uint8(len(writableSigners) + len(readonlySigners)),
			NumReadonlySignedAccounts:   uint8(len(readonlySigners)),
			NumReadonlyUnsignedAccounts: uint8(len(readonlyNonSigners)),
		},
		AccountKeys:     keys,
		RecentBlockhash: recentBlockhash,
	}
	for _, ix := range instructions {
		ci := CompiledInstruction{ProgramIDIndex: index[ix.ProgramID], Data: ix.Data}
		for _, a := range ix.Accounts {
			ci.AccountIndexes = append(ci.AccountIndexes, index[a.Pubkey])
		}
		msg.Instructions = append(msg.Instructions, ci)
	}
	return msg, nil
}

// encodeCompactU16 encodes n as Solana's compact-u16 (LEB128-style, 1..3 bytes).
func encodeCompactU16(n int) []byte {
	out := make([]byte, 0, 3)
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// Serialize returns the wire bytes of the message (what gets signed).
func (m *Message) Serialize() []byte {
	out := []byte{m.Header.NumRequiredSignatures, m.Header.NumReadonlySignedAccounts, m.Header.NumReadonlyUnsignedAccounts}
	out = append(out, encodeCompactU16(len(m.AccountKeys))...)
	for _, k := range m.AccountKeys {
		out = append(out, k[:]...)
	}
	out = append(out, m.RecentBlockhash[:]...)
	out = append(out, encodeCompactU16(len(m.Instructions))...)
	for _, ix := range m.Instructions {
		out = append(out, ix.ProgramIDIndex)
		out = append(out, encodeCompactU16(len(ix.AccountIndexes))...)
		out = append(out, ix.AccountIndexes...)
		out = append(out, encodeCompactU16(len(ix.Data))...)
		out = append(out, ix.Data...)
	}
	return out
}

// Sign signs msg with every key that matches a required signer. Every required signer
// must be covered by one of keys.
func Sign(msg *Message, keys ...ed25519.PrivateKey) (*Transaction, error) {
	n := int(msg.Header.NumRequiredSignatures)
	if n > len(msg.AccountKeys) {
		return nil, fmt.Errorf("tx: header requires %d signatures for %d accounts", n, len(msg.AccountKeys))
	}
	byPub := make(map[Pubkey]ed25519.PrivateKey, len(keys))
	for _, k := range keys {
		if len(k) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("tx: private key must be %d bytes", ed25519.PrivateKeySize)
		}
		var pub Pubkey
		copy(pub[:], k.Public().(ed25519.PublicKey))
		byPub[pub] = k
	}
	body := msg.Serialize()
	t := &Transaction{Message: msg, Signatures: make([][64]byte, n)}
	for i := 0; i < n; i++ {
		k, ok := byPub[msg.AccountKeys[i]]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrMissingSigner, msg.AccountKeys[i])
		}
		copy(t.Signatures[i][:], ed25519.Sign(k, body))
	}
	return t, nil
}

// Serialize returns the wire bytes of the signed transaction.
func (t *Transaction) Serialize() []byte {
	out := encodeCompactU16(len(t.Signatures))
	for _, s := range t.Signatures {
		out = append(out, s[:]...)
	}
	return append(out, t.Message.Serialize()...)
}

// Base64 returns the serialized transaction base64-encoded, as sendTransaction expects.
func (t *Transaction) Base64() string { return base64.StdEncoding.EncodeToString(t.Serialize()) }

// Signature returns the base58 first signature, which is the transaction id.
func (t *Transaction) Signature() string {
	if len(t.Signatures) == 0 {
		return ""
	}
	return base58.Encode(t.Signatures[0][:])
}

// BlockhashFromBase58 decodes a base58 recent blockhash.
func BlockhashFromBase58(s string) ([32]byte, error) {
	p, err := PubkeyFromBase58(s)
	if err != nil {
		return [32]byte{}, fmt.Errorf("tx: blockhash: %w", err)
	}
	return [32]byte(p), nil
}
