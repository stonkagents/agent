// Package repository: token usage logging for margin validation. Captures real
// OpenAI input/output token counts per call so we can recalibrate model_cost
// from production data via SQL.
package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LLMUsageRepository writes token-usage rows for completed chat calls.
type LLMUsageRepository struct {
	pool *pgxpool.Pool
}

// NewLLMUsageRepository creates a new usage logger.
func NewLLMUsageRepository(pool *pgxpool.Pool) *LLMUsageRepository {
	return &LLMUsageRepository{pool: pool}
}

// LogEntry is a single completed LLM call.
type LLMUsageEntry struct {
	AccountID        string
	Model            string
	InputTokens      int
	OutputTokens     int
	EstimatedCostUSD float64
	CreditsCharged   int
	RequestID        string
}

// Insert writes a usage row. Errors are non-fatal — the caller logs and continues.
func (r *LLMUsageRepository) Insert(ctx context.Context, e LLMUsageEntry) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO llm_usage_log
		   (account_id, model, input_tokens, output_tokens, estimated_cost_usd, credits_charged, request_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.AccountID, e.Model, e.InputTokens, e.OutputTokens, e.EstimatedCostUSD, e.CreditsCharged, e.RequestID,
	)
	return err
}
