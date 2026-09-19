// Package: tracker/internal/solana/pda
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Program-derived address (PDA) derivation without external Solana SDKs.
//
// Solana PDAs are sha256(seeds..., bump, programID, "ProgramDerivedAddress") for the
// first bump (255 down to 0) whose hash is NOT a valid ed25519 curve point. The on-curve
// check is done here with math/big (Euler's criterion on the twisted Edwards equation),
// matching curve25519-dalek's CompressedEdwardsY::decompress().is_some().

package pda

import (
	"crypto/sha256"
	"errors"
	"math/big"

	"github.com/mr-tron/base58"
)

const (
	// MaxSeedLength is the maximum byte length of a single PDA seed.
	MaxSeedLength = 32
	// MaxSeeds is the maximum number of seeds (excluding the bump).
	MaxSeeds = 16

	pdaMarker = "ProgramDerivedAddress"
)

var (
	// ErrInvalidSeeds is returned when the seed list violates Solana's limits.
	ErrInvalidSeeds = errors.New("solana: invalid PDA seeds")
	// ErrNoViableBump is returned when no bump in 255..0 yields an off-curve address.
	ErrNoViableBump = errors.New("solana: unable to find a viable program address bump seed")

	// ed25519 field prime p = 2^255 - 19.
	edP = func() *big.Int {
		p := new(big.Int).Lsh(big.NewInt(1), 255)
		return p.Sub(p, big.NewInt(19))
	}()
	// Twisted Edwards constant d = -121665/121666 mod p.
	edD = func() *big.Int {
		d, _ := new(big.Int).SetString("37095705934669439343138083508754565189542113879843219016388785533085940283555", 10)
		return d
	}()
	// (p - 1) / 2, exponent for Euler's criterion.
	edHalf = func() *big.Int {
		h := new(big.Int).Sub(edP, big.NewInt(1))
		return h.Rsh(h, 1)
	}()
	one = big.NewInt(1)
)

// IsOnCurve reports whether the 32-byte compressed point decodes to a valid ed25519
// point (i.e. it could be a real keypair's public key). PDAs are, by construction,
// NOT on the curve.
func IsOnCurve(pub [32]byte) bool {
	// y is the little-endian integer with the top (sign) bit cleared.
	var le [32]byte
	for i := 0; i < 32; i++ {
		le[i] = pub[31-i]
	}
	le[0] &= 0x7f
	y := new(big.Int).SetBytes(le[:])
	y.Mod(y, edP)

	yy := new(big.Int).Mul(y, y)
	yy.Mod(yy, edP)

	// u = y^2 - 1 ; v = d*y^2 + 1 ; point is valid iff u/v is a square (or u == 0).
	u := new(big.Int).Sub(yy, one)
	u.Mod(u, edP)
	if u.Sign() == 0 {
		return true
	}
	v := new(big.Int).Mul(edD, yy)
	v.Add(v, one)
	v.Mod(v, edP)

	vInv := new(big.Int).ModInverse(v, edP)
	if vInv == nil {
		return false
	}
	ratio := new(big.Int).Mul(u, vInv)
	ratio.Mod(ratio, edP)
	legendre := new(big.Int).Exp(ratio, edHalf, edP)
	return legendre.Cmp(one) == 0
}

// CreateProgramAddress computes sha256(seeds..., programID, "ProgramDerivedAddress")
// and returns it if it is off-curve. The caller supplies the bump as the last seed.
func CreateProgramAddress(seeds [][]byte, programID [32]byte) ([32]byte, error) {
	var out [32]byte
	if len(seeds) > MaxSeeds+1 {
		return out, ErrInvalidSeeds
	}
	h := sha256.New()
	for _, s := range seeds {
		if len(s) > MaxSeedLength {
			return out, ErrInvalidSeeds
		}
		h.Write(s)
	}
	h.Write(programID[:])
	h.Write([]byte(pdaMarker))
	copy(out[:], h.Sum(nil))
	if IsOnCurve(out) {
		return out, errors.New("solana: derived address is on curve")
	}
	return out, nil
}

// FindProgramAddress returns the first off-curve address for seeds, trying bumps
// from 255 down to 0, along with the bump that produced it.
func FindProgramAddress(seeds [][]byte, programID [32]byte) ([32]byte, uint8, error) {
	if len(seeds) > MaxSeeds {
		return [32]byte{}, 0, ErrInvalidSeeds
	}
	withBump := make([][]byte, len(seeds)+1)
	copy(withBump, seeds)
	for bump := 255; bump >= 0; bump-- {
		withBump[len(seeds)] = []byte{byte(bump)}
		addr, err := CreateProgramAddress(withBump, programID)
		if err == nil {
			return addr, uint8(bump), nil
		}
		if errors.Is(err, ErrInvalidSeeds) {
			return [32]byte{}, 0, err
		}
	}
	return [32]byte{}, 0, ErrNoViableBump
}

// DecodePubkey decodes a base58 Solana address into 32 bytes.
func DecodePubkey(addr string) ([32]byte, error) {
	var out [32]byte
	raw, err := base58.Decode(addr)
	if err != nil {
		return out, err
	}
	if len(raw) != 32 {
		return out, errors.New("solana: pubkey must be 32 bytes")
	}
	copy(out[:], raw)
	return out, nil
}

// EncodePubkey encodes 32 bytes as a base58 Solana address.
func EncodePubkey(key [32]byte) string {
	return base58.Encode(key[:])
}
