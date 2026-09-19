DROP INDEX IF EXISTS idx_peer_reputation_score;
DROP INDEX IF EXISTS idx_board_activity_dedupe;
DROP INDEX IF EXISTS idx_forum_replies_post_body_hash;
DROP INDEX IF EXISTS idx_forum_posts_author_body_hash;
DROP INDEX IF EXISTS idx_forum_upvotes_peer_created;
DROP INDEX IF EXISTS idx_forum_posts_accepted_reply;
DROP INDEX IF EXISTS idx_forum_posts_bounty_winner;
DROP INDEX IF EXISTS idx_forum_replies_author_post;
DROP INDEX IF EXISTS idx_forum_posts_author_created;
DROP INDEX IF EXISTS idx_forum_posts_main_top;
DROP INDEX IF EXISTS idx_forum_posts_main_recent;

DROP TABLE IF EXISTS board_notification_prefs;
DROP TABLE IF EXISTS board_visits;
DROP TABLE IF EXISTS board_room_mutes;
DROP TABLE IF EXISTS board_room_settings;
DROP TABLE IF EXISTS board_edit_history;

ALTER TABLE forum_upvotes DROP COLUMN IF EXISTS created_at;

ALTER TABLE forum_replies DROP COLUMN IF EXISTS body_hash;
ALTER TABLE forum_replies DROP COLUMN IF EXISTS edit_count;
ALTER TABLE forum_replies DROP COLUMN IF EXISTS edited_at;
ALTER TABLE forum_replies DROP COLUMN IF EXISTS deleted_at;

ALTER TABLE forum_posts DROP COLUMN IF EXISTS routed_reasons;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS bounty_dispute_resolved_at;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS bounty_disputed_at;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS bounty_dispute_note;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS bounty_dispute_by;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS bounty_dispute_status;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS body_hash;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS edit_count;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS edited_at;
ALTER TABLE forum_posts DROP COLUMN IF EXISTS deleted_at;
