// One-off admin script to refund credits to a user's paid balance, identified by peer_id.
// Reads DATABASE_URL from env. Show current balance, prompt-free UPDATE, show new balance.
//
//	go run ./scripts/refund-credits 12D3KooW...peerid 225
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: refund-credits <peer_id> <amount>")
		os.Exit(2)
	}
	peerID := os.Args[1]
	amount, err := strconv.Atoi(os.Args[2])
	if err != nil || amount <= 0 {
		fmt.Fprintln(os.Stderr, "amount must be a positive integer")
		os.Exit(2)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL not set")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Look up account
	var accountID string
	err = pool.QueryRow(ctx, `SELECT id FROM accounts WHERE peer_id = $1`, peerID).Scan(&accountID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "account lookup failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Account ID: %s\n", accountID)

	// Current balance
	var freeBefore, paidBefore, lifetimePurchasedBefore int
	err = pool.QueryRow(ctx,
		`SELECT free_balance, paid_balance, lifetime_purchased FROM credit_balances WHERE account_id = $1`,
		accountID,
	).Scan(&freeBefore, &paidBefore, &lifetimePurchasedBefore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "balance lookup failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("BEFORE  free=%d paid=%d lifetime_purchased=%d\n", freeBefore, paidBefore, lifetimePurchasedBefore)

	// Update
	cmd, err := pool.Exec(ctx,
		`UPDATE credit_balances
		    SET paid_balance = paid_balance + $1,
		        lifetime_purchased = lifetime_purchased + $1,
		        updated_at = NOW()
		  WHERE account_id = $2`,
		amount, accountID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		os.Exit(1)
	}
	if cmd.RowsAffected() != 1 {
		fmt.Fprintf(os.Stderr, "expected 1 row affected, got %d\n", cmd.RowsAffected())
		os.Exit(1)
	}

	// Verify
	var freeAfter, paidAfter, lifetimePurchasedAfter int
	err = pool.QueryRow(ctx,
		`SELECT free_balance, paid_balance, lifetime_purchased FROM credit_balances WHERE account_id = $1`,
		accountID,
	).Scan(&freeAfter, &paidAfter, &lifetimePurchasedAfter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("AFTER   free=%d paid=%d lifetime_purchased=%d\n", freeAfter, paidAfter, lifetimePurchasedAfter)
	fmt.Printf("Δ       paid=+%d lifetime_purchased=+%d\n", paidAfter-paidBefore, lifetimePurchasedAfter-lifetimePurchasedBefore)
}
