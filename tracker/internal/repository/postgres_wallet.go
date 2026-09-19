// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-06 (Wallet Linking)
// Purpose: PostgreSQL implementation of WalletRepository

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// PostgresWalletRepository implements WalletRepository using PostgreSQL.
type PostgresWalletRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresWalletRepository creates a new Postgres wallet repository.
func NewPostgresWalletRepository(pool *pgxpool.Pool) *PostgresWalletRepository {
	return &PostgresWalletRepository{pool: pool}
}

func (r *PostgresWalletRepository) LinkWallet(ctx context.Context, wallet *models.AccountWallet) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO account_wallets (account_id, wallet_address, chain, linked_at, balance_at_link)
		 VALUES ($1, $2, $3, $4, $5)`,
		wallet.AccountID, wallet.WalletAddress, wallet.Chain, wallet.LinkedAt, wallet.BalanceAtLink,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return models.ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (r *PostgresWalletRepository) GetByAccountID(ctx context.Context, accountID, chain string) (*models.AccountWallet, error) {
	var w models.AccountWallet
	err := r.pool.QueryRow(ctx,
		`SELECT account_id, wallet_address, chain, linked_at, balance_at_link
		 FROM account_wallets WHERE account_id = $1 AND chain = $2`,
		accountID, chain,
	).Scan(&w.AccountID, &w.WalletAddress, &w.Chain, &w.LinkedAt, &w.BalanceAtLink)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return &w, nil
}

func (r *PostgresWalletRepository) HasGrantHistory(ctx context.Context, walletAddress, chain string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM wallet_grant_history WHERE wallet_address = $1 AND chain = $2)`,
		walletAddress, chain,
	).Scan(&exists)
	return exists, err
}

func (r *PostgresWalletRepository) InsertGrantHistory(ctx context.Context, grant *models.WalletGrantHistory) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO wallet_grant_history (wallet_address, chain, granted_at, amount)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (wallet_address, chain) DO NOTHING`,
		grant.WalletAddress, grant.Chain, grant.GrantedAt, grant.Amount,
	)
	return err
}

// UpsertWalletForDisplay links a wallet to an account for display purposes (no signature verification).
// Used by PATCH /peers/me when consolidating peers.wallet_address into account_wallets.
// If the wallet is already linked to another account (PK on wallet_address, chain), we re-link it
// to the current account so the same wallet can be used after re-registering or switching peers.
func (r *PostgresWalletRepository) UpsertWalletForDisplay(ctx context.Context, accountID, walletAddress, chain string) error {
	now := time.Now().UTC()
	// Claim wallet if it exists for another account (avoids PK violation on (wallet_address, chain)).
	cmd, err := r.pool.Exec(ctx,
		`UPDATE account_wallets SET account_id = $1, linked_at = $2 WHERE wallet_address = $3 AND chain = $4`,
		accountID, now, walletAddress, chain,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() > 0 {
		return nil
	}
	// Wallet not in table; insert (or replace this account's wallet for this chain).
	_, err = r.pool.Exec(ctx,
		`INSERT INTO account_wallets (account_id, wallet_address, chain, linked_at, balance_at_link)
		 VALUES ($1, $2, $3, $4, NULL)
		 ON CONFLICT (account_id, chain) DO UPDATE SET
		   wallet_address = EXCLUDED.wallet_address,
		   linked_at = EXCLUDED.linked_at`,
		accountID, walletAddress, chain, now,
	)
	return err
}
