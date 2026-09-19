// Package: tracker/internal/solana/pda
// Feature: StonkAgents (Raydium LaunchLab metrics)
// Purpose: Tests for PDA derivation and the ed25519 on-curve check against live mainnet vectors

package pda

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

const (
	launchLabProgram = "LanMV9sAd7wArD4vJFi2qDdfnVhFxYSUg6eADduJ3uj"
	stonkMint        = "6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx"
	wsolMint         = "So11111111111111111111111111111111111111112"
)

// Real LaunchLab pools observed on mainnet (mintA, mintB → pool id) via
// https://launch-mint-v1.raydium.io/get/by/mints?ids=<mintA>.
var livePoolVectors = []struct{ mintA, mintB, pool string }{
	{"3U11GQJQTf94P5gAPdvHFWuFFprWCgZFk3KV6ovxa46V", stonkMint, "F5eCY1hG4ohq1JcQQxoJ2VVEgYYvsJzZAEtGQ642qvw3"},
	{"Ha4ibKjB1Bhjci8bvLHFGRET4hggHG76hU6pLe3Stonk", stonkMint, "HxtvdqcinxqLngbW4dFKFsizbd5ojXQkUjWL4mg8yWVK"},
	{"Dz9mQ9NzkBcCsuGPFJ3r1bS4wgqKMHBPiVuniW8Mbonk", wsolMint, "GWqWrb44KJ8rmKvytQVUDh9X2pAVUkT8zE5RqTnJ4Dw4"},
}

func mustDecode(t *testing.T, addr string) [32]byte {
	t.Helper()
	k, err := DecodePubkey(addr)
	if err != nil {
		t.Fatalf("DecodePubkey(%s): %v", addr, err)
	}
	return k
}

func TestFindProgramAddress_LaunchLabPoolVectors(t *testing.T) {
	program := mustDecode(t, launchLabProgram)
	for _, v := range livePoolVectors {
		mintA := mustDecode(t, v.mintA)
		mintB := mustDecode(t, v.mintB)
		addr, _, err := FindProgramAddress([][]byte{[]byte("pool"), mintA[:], mintB[:]}, program)
		if err != nil {
			t.Fatalf("FindProgramAddress(%s): %v", v.mintA, err)
		}
		if got := EncodePubkey(addr); got != v.pool {
			t.Errorf("pool PDA for %s = %s, want %s", v.mintA, got, v.pool)
		}
	}
}

func TestIsOnCurve_RealKeysOnCurve_PDAsOff(t *testing.T) {
	for i := 0; i < 20; i++ {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		var k [32]byte
		copy(k[:], pub)
		if !IsOnCurve(k) {
			t.Errorf("generated ed25519 key %d reported off-curve", i)
		}
	}
	// System program id (all zeros → y = 0) is a valid curve point.
	if !IsOnCurve(mustDecode(t, "11111111111111111111111111111111")) {
		t.Error("system program id should be on curve")
	}
	for _, v := range livePoolVectors {
		if IsOnCurve(mustDecode(t, v.pool)) {
			t.Errorf("PDA %s should be off curve", v.pool)
		}
	}
	// Real wallets / mints are on curve.
	for _, addr := range []string{stonkMint, wsolMint, "CE7gJhaTZZjf3ewcKYtVLgNyYJEZTHgdaf1kP3j7ZwT"} {
		if !IsOnCurve(mustDecode(t, addr)) {
			t.Errorf("%s should be on curve", addr)
		}
	}
}

func TestFindProgramAddress_RejectsBadSeeds(t *testing.T) {
	program := mustDecode(t, launchLabProgram)
	if _, _, err := FindProgramAddress([][]byte{make([]byte, 33)}, program); err != ErrInvalidSeeds {
		t.Errorf("33-byte seed: err = %v, want ErrInvalidSeeds", err)
	}
	tooMany := make([][]byte, MaxSeeds+1)
	for i := range tooMany {
		tooMany[i] = []byte{1}
	}
	if _, _, err := FindProgramAddress(tooMany, program); err != ErrInvalidSeeds {
		t.Errorf("17 seeds: err = %v, want ErrInvalidSeeds", err)
	}
}
