CREATE TABLE IF NOT EXISTS agent_chat_history_messages (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    token_address TEXT NOT NULL,
    seq INT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant')),
    content TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    credits_deducted INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, token_address, seq)
);

CREATE INDEX IF NOT EXISTS idx_agent_chat_history_user_token
    ON agent_chat_history_messages(user_id, token_address, seq);
