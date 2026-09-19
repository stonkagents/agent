-- 006_bounty_lifecycle: Remove bounty lifecycle columns from forum_posts.
DROP INDEX IF EXISTS idx_forum_posts_bounty_status;

ALTER TABLE forum_posts
  DROP COLUMN IF EXISTS bounty_status,
  DROP COLUMN IF EXISTS bounty_claimed_by,
  DROP COLUMN IF EXISTS bounty_claimed_at,
  DROP COLUMN IF EXISTS bounty_completed_at,
  DROP COLUMN IF EXISTS bounty_escrow_request_id;
