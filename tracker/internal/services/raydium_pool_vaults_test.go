// Package: tracker/internal/services
// Feature: StonkAgents launchpad (trade indexer)
// Purpose: The pool decoder exposes the vault accounts the indexer derives swaps from.
//
// testdata/raydium_pool_account_devnet.json is getAccountInfo(D6iVThFiYSg7wu69PhBJ42QU2WqvpFPJkmZX2PRz6hd3)
// on devnet, captured 2026-09-13.

package services

import (
	"context"
	"testing"
)

func TestDecodeLaunchLabPool_VaultsFromDevnetFixture(t *testing.T) {
	acc := fixtureAccount(t, "raydium_pool_account_devnet.json")
	pool, err := DecodeLaunchLabPool(acc.Data)
	if err != nil {
		t.Fatal(err)
	}
	if pool.MintA != "2161TxLYzfZabXowZGHeE5oNMYC3HsQ2SKqRQfdc23ki" || pool.MintB != "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT" {
		t.Errorf("mints = %s / %s", pool.MintA, pool.MintB)
	}
	if pool.VaultA != "J7ii5isZa32q8H6UU5pY6Tyt4Rv8rKxchV9gpbBQDjsi" {
		t.Errorf("vaultA = %s", pool.VaultA)
	}
	if pool.VaultB != "GzgvLxRW2yUTz2XAHsjt6ecEheRVTCiwXf3zL4R9PqEZ" {
		t.Errorf("vaultB = %s", pool.VaultB)
	}
	if pool.Creator != "652nWgxA2vJpWFBtwZVfJt6aQyLhNyTd16ZPZQdxo274" {
		t.Errorf("creator = %s (vault fields shifted the layout)", pool.Creator)
	}
	if pool.MintDecimalsA != 6 || pool.MintDecimalsB != 6 || pool.Status != 0 {
		t.Errorf("decimals/status = %d/%d/%d", pool.MintDecimalsA, pool.MintDecimalsB, pool.Status)
	}
}

type fixtureAccountReader struct {
	accounts map[string]*RPCAccount
	calls    int
}

func (r *fixtureAccountReader) GetMultipleAccounts(_ context.Context, addrs []string) ([]*RPCAccount, error) {
	r.calls++
	out := make([]*RPCAccount, len(addrs))
	for i, a := range addrs {
		out[i] = r.accounts[a]
	}
	return out, nil
}

func TestAccountPoolVaultSource_ResolvesKnownPoolsOnly(t *testing.T) {
	const pool = "D6iVThFiYSg7wu69PhBJ42QU2WqvpFPJkmZX2PRz6hd3"
	rdr := &fixtureAccountReader{accounts: map[string]*RPCAccount{pool: fixtureAccount(t, "raydium_pool_account_devnet.json")}}
	src := NewAccountPoolVaultSource(rdr)
	got, err := src.PoolVaults(context.Background(), []string{pool, "MissingPoolXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"})
	if err != nil {
		t.Fatal(err)
	}
	if rdr.calls != 1 {
		t.Errorf("calls = %d, want 1 batched call", rdr.calls)
	}
	if len(got) != 1 || got[pool].VaultA == "" || got[pool].VaultB == "" || got[pool].MintA == "" {
		t.Errorf("vaults = %+v", got)
	}
}
