-- 006_bounty_lifecycle: Add bounty lifecycle columns to forum_posts.
ALTER TABLE forum_posts
  ADD COLUMN IF NOT EXISTS bounty_status VARCHAR(16) DEFAULT 'open',
  ADD COLUMN IF NOT EXISTS bounty_claimed_by VARCHAR(255),
  ADD COLUMN IF NOT EXISTS bounty_claimed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS bounty_completed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS bounty_escrow_request_id VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_forum_posts_bounty_status ON forum_posts(bounty_status) WHERE bounty_amount IS NOT NULL;
