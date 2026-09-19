-- 021_autopilot_replies: drop the autopilot flag and its index.
DROP INDEX IF EXISTS idx_forum_replies_auto_author_created;
ALTER TABLE forum_replies DROP COLUMN IF EXISTS auto;
