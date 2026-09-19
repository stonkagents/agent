package repository

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/pkg/chatthread"
)

func isPersonalChatTokenAddress(tokenAddress string) bool {
	t := strings.TrimSpace(tokenAddress)
	return t == "" || t == chatthread.PersonalContextTokenAddress
}

// PostgresAgentChatHistoryRepository stores token chat history keyed by user_id + token_address.
type PostgresAgentChatHistoryRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresAgentChatHistoryRepository(pool *pgxpool.Pool) *PostgresAgentChatHistoryRepository {
	return &PostgresAgentChatHistoryRepository{pool: pool}
}

func (r *PostgresAgentChatHistoryRepository) AppendMessages(ctx context.Context, userID, tokenAddress string, messages []*AgentChatHistoryMessage) error {
	if len(messages) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	storeToken := strings.TrimSpace(tokenAddress)
	var maxSeq int
	if isPersonalChatTokenAddress(storeToken) {
		storeToken = chatthread.PersonalContextTokenAddress
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(seq), -1) FROM agent_chat_history_messages WHERE user_id = $1 AND (token_address = '' OR token_address = $2)`,
			userID, chatthread.PersonalContextTokenAddress,
		).Scan(&maxSeq); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), -1) FROM agent_chat_history_messages WHERE user_id = $1 AND token_address = $2`, userID, storeToken).Scan(&maxSeq); err != nil {
			return err
		}
	}
	next := maxSeq + 1
	for _, m := range messages {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_chat_history_messages (user_id, token_address, seq, role, content, agent_id, credits_deducted, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,NOW())`,
			userID, storeToken, next, m.Role, m.Content, m.AgentID, m.CreditsDeducted); err != nil {
			return err
		}
		next++
	}
	return tx.Commit(ctx)
}

func (r *PostgresAgentChatHistoryRepository) ListMessages(ctx context.Context, userID, tokenAddress string) ([]*AgentChatHistoryMessage, error) {
	tokenAddress = strings.TrimSpace(tokenAddress)
	var rows pgx.Rows
	var err error
	if isPersonalChatTokenAddress(tokenAddress) {
		rows, err = r.pool.Query(ctx,
			`SELECT seq, role, content, COALESCE(agent_id,''), credits_deducted, created_at FROM agent_chat_history_messages WHERE user_id = $1 AND (token_address = '' OR token_address = $2) ORDER BY created_at ASC, id ASC`,
			userID, chatthread.PersonalContextTokenAddress,
		)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT seq, role, content, COALESCE(agent_id,''), credits_deducted, created_at FROM agent_chat_history_messages WHERE user_id = $1 AND token_address = $2 ORDER BY seq ASC`, userID, tokenAddress)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*AgentChatHistoryMessage, 0)
	for rows.Next() {
		var m AgentChatHistoryMessage
		if err := rows.Scan(&m.Seq, &m.Role, &m.Content, &m.AgentID, &m.CreditsDeducted, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// PersonalSessionBounds implements AgentChatHistoryRepository for personal threads (” legacy or 0x sentinel).
func (r *PostgresAgentChatHistoryRepository) PersonalSessionBounds(ctx context.Context, userID string) (bool, time.Time, time.Time, error) {
	var cnt int64
	var firstAt, lastAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*)::bigint, COALESCE(MIN(created_at), 'epoch'::timestamptz), COALESCE(MAX(created_at), 'epoch'::timestamptz) FROM agent_chat_history_messages WHERE user_id = $1 AND (token_address = '' OR token_address = $2)`,
		userID, chatthread.PersonalContextTokenAddress,
	).Scan(&cnt, &firstAt, &lastAt)
	if err != nil {
		return false, time.Time{}, time.Time{}, err
	}
	if cnt == 0 {
		return false, time.Time{}, time.Time{}, nil
	}
	return true, firstAt, lastAt, nil
}
