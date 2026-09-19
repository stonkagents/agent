-- 016_feedback: user feedback submitted from the portal (POST /api/v1/feedback).
-- ip_hash is a SHA-256 of the client IP (never the raw address); contact fields are free text
-- the user chose to leave. Read back newest-first by id through GET /api/v1/admin/feedback.

CREATE TABLE IF NOT EXISTS feedback (
  id             BIGSERIAL PRIMARY KEY,
  kind           TEXT NOT NULL CHECK (kind IN ('bug', 'idea', 'other', 'wanted')),
  message        TEXT NOT NULL CHECK (char_length(message) BETWEEN 1 AND 2000),
  path           TEXT NOT NULL DEFAULT '' CHECK (char_length(path) <= 200),
  contact        TEXT NOT NULL DEFAULT '' CHECK (char_length(contact) <= 200),
  contact_via    TEXT NOT NULL DEFAULT '',
  wallet_address TEXT NOT NULL DEFAULT '',
  user_agent     TEXT NOT NULL DEFAULT '',
  ip_hash        TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_feedback_created_at ON feedback(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_kind ON feedback(kind);
