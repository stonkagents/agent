-- 010_llm_usage_log: Captures real OpenAI token usage per call for margin validation.
-- Lets us recalibrate model_cost via SQL once we have ~1 week of production data.

CREATE TABLE IF NOT EXISTS llm_usage_log (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id VARCHAR(255) NOT NULL,
  model VARCHAR(64) NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  estimated_cost_usd NUMERIC(10, 6) NOT NULL DEFAULT 0,
  credits_charged INTEGER NOT NULL DEFAULT 0,
  request_id VARCHAR(255),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_llm_usage_account_created ON llm_usage_log(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model_created ON llm_usage_log(model, created_at DESC);
