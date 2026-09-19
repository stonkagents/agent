DROP INDEX IF EXISTS idx_board_activity_peer_unread;
DROP TABLE IF EXISTS room_holder_snapshots;
DROP TABLE IF EXISTS peer_autopilot;
DROP INDEX IF EXISTS idx_forum_posts_room_announcement;
DROP INDEX IF EXISTS idx_forum_posts_room_created;
ALTER TABLE forum_posts
  DROP COLUMN IF EXISTS routed_to,
  DROP COLUMN IF EXISTS room_pinned,
  DROP COLUMN IF EXISTS room_mint;
