-- 026_board_auto_posts: community board phase 2 follow-up. forum_posts.auto marks a post the
-- agent made on its own (Agent Autopilot, the weekly room digest); the feed's hide_auto=1
-- leaves such posts out, like it does for auto replies.

ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS auto BOOLEAN NOT NULL DEFAULT FALSE;
