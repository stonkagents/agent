// Package agentchat: SQLite persistence for agent chat sessions and message history.
//
// Token chat: the token owner (creator) has an LLM that talks on their behalf. Only holders (buyers) can chat with that LLM; the owner cannot chat with their own.
// UserID = the person chatting (holder wallet). ContextID = token contract (which owner's LLM). Empty context = main agent chat.

package agentchat

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/stonkagents/agent/pkg/chatthread"

	_ "modernc.org/sqlite"
)

// Session is one conversation. UserID = chatter (e.g. holder wallet). ContextID = token contract for "chat with this token's owner LLM", or empty for main agent chat.
type Session struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id,omitempty"`
	ContextID    string    `json:"context_id,omitempty"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Message is a single message in a session (user, assistant, or system).
type Message struct {
	ID              int64     `json:"id"`
	SessionID       string    `json:"session_id"`
	Seq             int       `json:"seq"`
	Role            string    `json:"role"`
	Content         string    `json:"content"`
	AgentID         string    `json:"agent_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	CreditsDeducted int       `json:"credits_deducted"` // e.g. 10 when tracker path used, 0 for gateway
}

// Repository persists agent chat sessions and messages.
type Repository struct {
	db *sql.DB
}

// NewRepository opens (or creates) the agent chat SQLite database.
func NewRepository(dbPath string) (*Repository, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("agentchat: open db: %w", err)
	}
	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Repository{db: db}, nil
}

func createSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS agent_chat_sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL DEFAULT '',
		context_id TEXT NOT NULL DEFAULT '',
		system_prompt TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS agent_chat_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		role TEXT NOT NULL CHECK(role IN ('system','user','assistant')),
		content TEXT NOT NULL,
		agent_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		credits_deducted INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY(session_id) REFERENCES agent_chat_sessions(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_agent_chat_messages_session ON agent_chat_messages(session_id);
	CREATE INDEX IF NOT EXISTS idx_agent_chat_sessions_user_context ON agent_chat_sessions(user_id, context_id);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("agentchat: create schema: %w", err)
	}
	return migrateAddUserContext(db)
}

// migrateAddUserContext adds user_id and context_id columns to existing tables (no-op if already present).
func migrateAddUserContext(db *sql.DB) error {
	for _, col := range []string{"user_id", "context_id"} {
		_, err := db.Exec(fmt.Sprintf("ALTER TABLE agent_chat_sessions ADD COLUMN %s TEXT NOT NULL DEFAULT ''", col))
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("agentchat: migrate add %s: %w", col, err)
		}
	}
	return nil
}

// GenerateSessionID returns a new random session ID (32 hex chars).
func GenerateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("agentchat: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateSession inserts a new session. userID = chatter (e.g. holder wallet); contextID = token contract for "chat with this token's owner LLM", or empty for main agent chat.
func (r *Repository) CreateSession(systemPrompt, userID, contextID string) (string, error) {
	id, err := GenerateSessionID()
	if err != nil {
		return "", err
	}
	userID = strings.TrimSpace(userID)
	contextID = strings.TrimSpace(contextID)
	now := time.Now().UTC()
	_, err = r.db.Exec(
		`INSERT INTO agent_chat_sessions (id, user_id, context_id, system_prompt, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, contextID, systemPrompt, now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return "", fmt.Errorf("agentchat: create session: %w", err)
	}
	return id, nil
}

// ListSessions returns sessions for the given user (and optional token context), ordered by updated_at descending.
// userID = the chatter (e.g. holder wallet). contextID = token contract to filter to "conversations with this token's owner LLM"; empty = all contexts for that user.
func (r *Repository) ListSessions(userID, contextID string) ([]*Session, error) {
	userID = strings.TrimSpace(userID)
	contextID = strings.TrimSpace(contextID)
	var rows *sql.Rows
	var err error
	if contextID == "" {
		rows, err = r.db.Query(
			`SELECT id, user_id, context_id, system_prompt, created_at, updated_at FROM agent_chat_sessions WHERE user_id = ? ORDER BY updated_at DESC`,
			userID,
		)
	} else {
		rows, err = r.db.Query(
			`SELECT id, user_id, context_id, system_prompt, created_at, updated_at FROM agent_chat_sessions WHERE user_id = ? AND context_id = ? ORDER BY updated_at DESC`,
			userID, contextID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("agentchat: list sessions: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		var s Session
		var createdStr, updatedStr string
		if err := rows.Scan(&s.ID, &s.UserID, &s.ContextID, &s.SystemPrompt, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("agentchat: scan session: %w", err)
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		out = append(out, &s)
	}
	return out, nil
}

// GetSession loads a session by ID. Returns nil if not found.
func (r *Repository) GetSession(id string) (*Session, error) {
	var s Session
	var createdStr, updatedStr string
	err := r.db.QueryRow(
		`SELECT id, user_id, context_id, system_prompt, created_at, updated_at FROM agent_chat_sessions WHERE id = ?`,
		id,
	).Scan(&s.ID, &s.UserID, &s.ContextID, &s.SystemPrompt, &createdStr, &updatedStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agentchat: get session: %w", err)
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return &s, nil
}

// ListMessages returns all messages for a session in order (ascending seq).
func (r *Repository) ListMessages(sessionID string) ([]*Message, error) {
	rows, err := r.db.Query(
		`SELECT id, session_id, seq, role, content, agent_id, created_at, credits_deducted
		 FROM agent_chat_messages WHERE session_id = ? ORDER BY seq ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("agentchat: list messages: %w", err)
	}
	defer rows.Close()

	var out []*Message
	for rows.Next() {
		var m Message
		var createdStr string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Content, &m.AgentID, &createdStr, &m.CreditsDeducted); err != nil {
			return nil, fmt.Errorf("agentchat: scan message: %w", err)
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		out = append(out, &m)
	}
	return out, nil
}

// NextSeq returns the next sequence number for a session (max(seq)+1, or 0 if no messages).
func (r *Repository) NextSeq(sessionID string) (int, error) {
	var max sql.NullInt64
	err := r.db.QueryRow(`SELECT MAX(seq) FROM agent_chat_messages WHERE session_id = ?`, sessionID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("agentchat: next seq: %w", err)
	}
	if max.Valid {
		return int(max.Int64) + 1, nil
	}
	return 0, nil
}

// AppendMessages appends one or more messages to a session and updates session updated_at.
func (r *Repository) AppendMessages(sessionID string, messages []*Message) error {
	if len(messages) == 0 {
		return nil
	}
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("agentchat: begin tx: %w", err)
	}
	defer tx.Rollback()

	seq, err := r.nextSeqTx(tx, sessionID)
	if err != nil {
		return err
	}
	for _, m := range messages {
		_, err = tx.Exec(
			`INSERT INTO agent_chat_messages (session_id, seq, role, content, agent_id, created_at, credits_deducted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sessionID, seq, m.Role, m.Content, m.AgentID, nowStr, m.CreditsDeducted,
		)
		if err != nil {
			return fmt.Errorf("agentchat: insert message: %w", err)
		}
		seq++
	}
	_, err = tx.Exec(`UPDATE agent_chat_sessions SET updated_at = ? WHERE id = ?`, nowStr, sessionID)
	if err != nil {
		return fmt.Errorf("agentchat: update session: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) nextSeqTx(tx *sql.Tx, sessionID string) (int, error) {
	var max sql.NullInt64
	err := tx.QueryRow(`SELECT MAX(seq) FROM agent_chat_messages WHERE session_id = ?`, sessionID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("agentchat: next seq: %w", err)
	}
	if max.Valid {
		return int(max.Int64) + 1, nil
	}
	return 0, nil
}

// Close closes the database.
func (r *Repository) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// userTokenThreadID returns a stable thread/session ID for a user+token_address pair.
// tokenAddress is the token contract address and can be empty for non-token chat.
func userTokenThreadID(userID, tokenAddress string) string {
	return chatthread.StableID(userID, tokenAddress)
}

// StablePersonalSessionID is the stable SQLite/tracker thread id for personal agent chat (no token context).
func StablePersonalSessionID(userID string) string {
	return chatthread.StableID(userID, "")
}

// EnsureUserTokenSession ensures a stable mapped session exists for a user+token_address pair.
// This is used by new chat history behavior where session identity is user↔token_address.
func (r *Repository) EnsureUserTokenSession(systemPrompt, userID, tokenAddress string) (string, error) {
	userID = strings.TrimSpace(userID)
	tokenAddress = strings.TrimSpace(tokenAddress)
	id := userTokenThreadID(userID, tokenAddress)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`INSERT OR IGNORE INTO agent_chat_sessions (id, user_id, context_id, system_prompt, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, tokenAddress, systemPrompt, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("agentchat: ensure mapped session: %w", err)
	}
	_, err = r.db.Exec(`UPDATE agent_chat_sessions SET updated_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return "", fmt.Errorf("agentchat: touch mapped session: %w", err)
	}
	return id, nil
}

// BackfillSessionUserIDs sets user_id on the deterministic session row for this user+context when it was stored empty
// (e.g. legacy clients). The canonical id is userTokenThreadID(userID, contextID); only that row is updated.
func (r *Repository) BackfillSessionUserIDs(userID, contextID string) error {
	userID = strings.TrimSpace(userID)
	contextID = strings.TrimSpace(contextID)
	if userID == "" {
		return nil
	}
	id := userTokenThreadID(userID, contextID)
	res, err := r.db.Exec(
		`UPDATE agent_chat_sessions SET user_id = ? WHERE id = ? AND COALESCE(TRIM(user_id), '') = ''`,
		userID, id,
	)
	if err != nil {
		return fmt.Errorf("agentchat: backfill session user_id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = r.db.Exec(`UPDATE agent_chat_sessions SET updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), id)
	}
	return nil
}
