-- 022_board_phase0: drop the activity feed, the search column and the bounty expiry columns.
DROP TABLE IF EXISTS board_activity;

DROP INDEX IF EXISTS idx_forum_posts_search_tsv;

ALTER TABLE forum_posts
  DROP COLUMN IF EXISTS search_tsv,
  DROP COLUMN IF EXISTS bounty_refunded_at,
  DROP COLUMN IF EXISTS bounty_extended;
