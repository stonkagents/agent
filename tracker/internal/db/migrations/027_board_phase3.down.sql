DROP INDEX IF EXISTS idx_forum_replies_post_ask;
ALTER TABLE forum_replies DROP COLUMN IF EXISTS ask;
