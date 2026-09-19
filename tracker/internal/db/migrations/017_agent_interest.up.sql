-- 017_agent_interest: roadmap interest captured from the portal (POST /api/v1/interest).
-- Distinct from feedback: each row says which capabilities a person wants their agent to have,
-- how much it matters, and (optionally) why. Mined by product development through
-- GET /api/v1/admin/interest and GET /api/v1/admin/interest/summary.
-- ip_hash is a SHA-256 of the client IP (never the raw address).

CREATE TABLE IF NOT EXISTS agent_interest (
  id             BIGSERIAL PRIMARY KEY,
  capabilities   TEXT[] NOT NULL CHECK (
                   cardinality(capabilities) BETWEEN 1 AND 8
                   AND capabilities <@ ARRAY['trade', 'knowledge', 'learn', 'community', 'alerts', 'token', 'automate', 'other']::text[]
                 ),
  description    TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 600),
  priority       TEXT NOT NULL CHECK (priority IN ('nice', 'important', 'pay')),
  contact        TEXT NOT NULL DEFAULT '' CHECK (char_length(contact) <= 200),
  contact_via    TEXT NOT NULL DEFAULT '',
  wallet_address TEXT NOT NULL DEFAULT '',
  user_agent     TEXT NOT NULL DEFAULT '',
  ip_hash        TEXT NOT NULL DEFAULT '',
  path           TEXT NOT NULL DEFAULT '' CHECK (char_length(path) <= 200),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_interest_created_at ON agent_interest(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_interest_priority ON agent_interest(priority);
CREATE INDEX IF NOT EXISTS idx_agent_interest_capabilities ON agent_interest USING GIN (capabilities);
