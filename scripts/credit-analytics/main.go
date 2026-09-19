// One-off analytics query: credit usage, OpenAI cost, and effective margin
// from llm_usage_log. Reads DATABASE_URL from env.
//
//	go run ./scripts/credit-analytics                  # all-time + last 24h + last 7d
//	go run ./scripts/credit-analytics 12D3KooW...      # filter by peer_id
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// usdPerCredit is the Starter-tier base rate (1 credit = 0.00002 SOL ≈ $0.00172 at SOL $86).
// Used to compute revenue-per-call and effective margin.
const usdPerCredit = 0.00172

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL not set")
		os.Exit(2)
	}

	peerFilter := ""
	if len(os.Args) >= 2 {
		peerFilter = os.Args[1]
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	accountFilter := ""
	args := []interface{}{}
	if peerFilter != "" {
		var accountID string
		err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE peer_id = $1`, peerFilter).Scan(&accountID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "peer_id %q not found: %v\n", peerFilter, err)
			os.Exit(1)
		}
		accountFilter = " AND account_id = $1"
		args = append(args, accountID)
		fmt.Printf("Filtered by peer_id %s (account %s)\n\n", peerFilter, accountID)
	}

	// === Total volume by model ===
	fmt.Println("─── Per-model totals (all time) ────────────────────────────────")
	rows, err := pool.Query(ctx, `
		SELECT model,
		       COUNT(*) AS calls,
		       COALESCE(SUM(input_tokens),  0) AS in_tokens,
		       COALESCE(SUM(output_tokens), 0) AS out_tokens,
		       COALESCE(SUM(estimated_cost_usd), 0) AS cost_usd,
		       COALESCE(SUM(credits_charged),    0) AS credits_charged
		  FROM llm_usage_log
		 WHERE 1=1`+accountFilter+`
		 GROUP BY model
		 ORDER BY calls DESC`, args...)
	must(err)
	printModelStats(rows)

	// === Last 24h ===
	fmt.Println("\n─── Last 24h ────────────────────────────────────────────────────")
	rows, err = pool.Query(ctx, `
		SELECT model,
		       COUNT(*) AS calls,
		       COALESCE(SUM(input_tokens),  0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(estimated_cost_usd), 0),
		       COALESCE(SUM(credits_charged),    0)
		  FROM llm_usage_log
		 WHERE created_at > NOW() - INTERVAL '24 hours'`+accountFilter+`
		 GROUP BY model
		 ORDER BY calls DESC`, args...)
	must(err)
	printModelStats(rows)

	// === Last 7d ===
	fmt.Println("\n─── Last 7 days ─────────────────────────────────────────────────")
	rows, err = pool.Query(ctx, `
		SELECT model,
		       COUNT(*) AS calls,
		       COALESCE(SUM(input_tokens),  0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(estimated_cost_usd), 0),
		       COALESCE(SUM(credits_charged),    0)
		  FROM llm_usage_log
		 WHERE created_at > NOW() - INTERVAL '7 days'`+accountFilter+`
		 GROUP BY model
		 ORDER BY calls DESC`, args...)
	must(err)
	printModelStats(rows)

	// === Per-call distribution for detailed mode (margin sanity) ===
	fmt.Println("\n─── Detailed-mode per-call distribution (last 7d) ──────────────")
	row := pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(MIN(input_tokens),  0),
		       COALESCE(AVG(input_tokens),  0)::int,
		       COALESCE(MAX(input_tokens),  0),
		       COALESCE(MIN(output_tokens), 0),
		       COALESCE(AVG(output_tokens), 0)::int,
		       COALESCE(MAX(output_tokens), 0),
		       COALESCE(MIN(estimated_cost_usd), 0)::numeric(10,5),
		       COALESCE(AVG(estimated_cost_usd), 0)::numeric(10,5),
		       COALESCE(MAX(estimated_cost_usd), 0)::numeric(10,5)
		  FROM llm_usage_log
		 WHERE model NOT LIKE '%-mini%'
		   AND created_at > NOW() - INTERVAL '7 days'`+accountFilter, args...)
	var (
		count                     int
		inMin, inAvg, inMax       int
		outMin, outAvg, outMax    int
		costMin, costAvg, costMax float64
	)
	if err := row.Scan(&count, &inMin, &inAvg, &inMax, &outMin, &outAvg, &outMax, &costMin, &costAvg, &costMax); err == nil {
		if count == 0 {
			fmt.Println("  (no detailed-mode calls in window)")
		} else {
			fmt.Printf("  calls            : %d\n", count)
			fmt.Printf("  input tokens     : min=%d  avg=%d  max=%d\n", inMin, inAvg, inMax)
			fmt.Printf("  output tokens    : min=%d  avg=%d  max=%d\n", outMin, outAvg, outMax)
			fmt.Printf("  OpenAI cost USD  : min=$%.5f avg=$%.5f max=$%.5f\n", costMin, costAvg, costMax)
		}
	}

	// === Per-model margin ===
	fmt.Println("\n─── Effective margin per model (all time) ──────────────────────")
	rows, err = pool.Query(ctx, `
		SELECT model,
		       COUNT(*) AS calls,
		       COALESCE(SUM(estimated_cost_usd), 0) AS cost_usd,
		       COALESCE(SUM(credits_charged),    0) AS credits_charged
		  FROM llm_usage_log
		 WHERE 1=1`+accountFilter+`
		 GROUP BY model
		 ORDER BY calls DESC`, args...)
	must(err)
	defer rows.Close()
	for rows.Next() {
		var model string
		var calls int
		var costUSD float64
		var credits int
		_ = rows.Scan(&model, &calls, &costUSD, &credits)
		revenue := float64(credits) * usdPerCredit
		profit := revenue - costUSD
		margin := 0.0
		if revenue > 0 {
			margin = (profit / revenue) * 100
		}
		fmt.Printf("  %-20s  calls=%-5d  cost=$%-8.4f  revenue=$%-8.4f  profit=$%-8.4f  margin=%5.1f%%\n",
			model, calls, costUSD, revenue, profit, margin)
	}
}

func printModelStats(rows interface {
	Next() bool
	Scan(...interface{}) error
	Close()
	Err() error
}) {
	defer rows.Close()
	any := false
	for rows.Next() {
		any = true
		var model string
		var calls, inTokens, outTokens, credits int
		var costUSD float64
		if err := rows.Scan(&model, &calls, &inTokens, &outTokens, &costUSD, &credits); err != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", err)
			continue
		}
		revenue := float64(credits) * usdPerCredit
		fmt.Printf("  %-20s  calls=%-5d  in=%-7d  out=%-7d  cost=$%-7.4f  credits=%-6d  revenue=$%-7.4f\n",
			model, calls, inTokens, outTokens, costUSD, credits, revenue)
	}
	if !any {
		fmt.Println("  (no data)")
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		os.Exit(1)
	}
}
