// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: In-memory implementation of WalletRepository for testing

package repository

import (
	"context"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// MemoryWalletRepository is a thread-safe in-memory WalletRepository.
type MemoryWalletRepository struct {
	mu           sync.RWMutex
	wallets      map[string]*models.AccountWallet      // key: "accountID:chain"
	grantHistory map[string]*models.WalletGrantHistory // key: "walletAddress:chain"
}

// NewMemoryWalletRepository creates a new in-memory wallet repository.
func NewMemoryWalletRepository() *MemoryWalletRepository {
	return &MemoryWalletRepository{
		wallets:      make(map[string]*models.AccountWallet),
		grantHistory: make(map[string]*models.WalletGrantHistory),
	}
}

func walletKey(accountID, chain string) string {
	return accountID + ":" + chain
}

func grantKey(walletAddress, chain string) string {
	return walletAddress + ":" + chain
}

func (r *MemoryWalletRepository) LinkWallet(_ context.Context, wallet *models.AccountWallet) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check account:chain uniqueness (one wallet per chain per account)
	key := walletKey(wallet.AccountID, wallet.Chain)
	if _, exists := r.wallets[key]; exists {
		return models.ErrAlreadyExists
	}

	// Check wallet_address:chain uniqueness (one account per wallet — PK in SQL)
	for _, w := range r.wallets {
		if w.WalletAddress == wallet.WalletAddress && w.Chain == wallet.Chain {
			return models.ErrAlreadyExists
		}
	}

	stored := *wallet
	r.wallets[key] = &stored
	return nil
}

func (r *MemoryWalletRepository) GetByAccountID(_ context.Context, accountID, chain string) (*models.AccountWallet, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	w, ok := r.wallets[walletKey(accountID, chain)]
	if !ok {
		return nil, models.ErrNotFound
	}
	copy := *w
	return &copy, nil
}

func (r *MemoryWalletRepository) HasGrantHistory(_ context.Context, walletAddress, chain string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.grantHistory[grantKey(walletAddress, chain)]
	return ok, nil
}

func (r *MemoryWalletRepository) UpsertWalletForDisplay(_ context.Context, accountID, walletAddress, chain string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := walletKey(accountID, chain)
	existing, exists := r.wallets[key]
	if exists && existing.WalletAddress == walletAddress {
		return nil
	}
	if exists {
		delete(r.wallets, key)
	}
	for k, w := range r.wallets {
		if w.WalletAddress == walletAddress && w.Chain == chain {
			delete(r.wallets, k)
			break
		}
	}
	stored := &models.AccountWallet{
		AccountID:     accountID,
		WalletAddress: walletAddress,
		Chain:         chain,
		LinkedAt:      time.Now().UTC(),
	}
	r.wallets[key] = stored
	return nil
}

func (r *MemoryWalletRepository) InsertGrantHistory(_ context.Context, grant *models.WalletGrantHistory) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored := *grant
	r.grantHistory[grantKey(grant.WalletAddress, grant.Chain)] = &stored
	return nil
}
