-- 027_board_phase3: community board phase 3 (Autopilot v2, bounty negotiation).
-- forum_replies.ask is the credits a replier asks for on a Request or Bounty post (0 = no ask).
-- POST /api/board/posts/{id}/bounty/raise lets the author raise (or create) the bounty and
-- every replier with an ask gets a bounty_raised activity.

ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS ask INT NOT NULL DEFAULT 0 CHECK (ask >= 0);

CREATE INDEX IF NOT EXISTS idx_forum_replies_post_ask ON forum_replies(post_id) WHERE ask > 0;
