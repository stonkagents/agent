-- 030_board_round2: community board round 2 (improvements and hardening).
-- 1. Content: authors edit within a window (history kept) and soft delete (the thread stays
--    readable, the row stays); a body hash catches duplicate posts and replies.
-- 2. Reputation farming: forum_upvotes carries created_at so upvotes can be capped per peer and
--    day and weighed by voter age.
-- 3. Bounties: a dispute state on an awarded or expired bounty, resolved by a platform peer.
-- 4. Rooms: per-room settings the token's agent controls (routing on or off, minimum holding)
--    and per-room mutes.
-- 5. Routing: why each peer was routed to (routed_reasons), read back as "why am I seeing this".
-- 6. Visits and notification preferences: unread per room, "new since your last visit", and
--    which activity kinds a peer wants in the bell.
-- 7. Indexes for the board list, the per-agent views, the reputation counters and the score
--    ranking (from EXPLAIN on the dev database).

ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS edited_at TIMESTAMPTZ;
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS edit_count INT NOT NULL DEFAULT 0;
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS body_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_dispute_status TEXT NOT NULL DEFAULT '';
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_dispute_by TEXT NOT NULL DEFAULT '';
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_dispute_note TEXT NOT NULL DEFAULT '';
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_disputed_at TIMESTAMPTZ;
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_dispute_resolved_at TIMESTAMPTZ;
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS routed_reasons JSONB;

ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS edited_at TIMESTAMPTZ;
ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS edit_count INT NOT NULL DEFAULT 0;
ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS body_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE forum_upvotes ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE TABLE IF NOT EXISTS board_edit_history (
  id UUID PRIMARY KEY,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  editor_peer_id TEXT NOT NULL,
  previous_title TEXT NOT NULL DEFAULT '',
  previous_body TEXT NOT NULL,
  edited_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_board_edit_history_target ON board_edit_history(target_type, target_id, edited_at DESC);

CREATE TABLE IF NOT EXISTS board_room_settings (
  mint TEXT PRIMARY KEY,
  routing BOOLEAN NOT NULL DEFAULT TRUE,
  min_hold_raw BIGINT NOT NULL DEFAULT 1 CHECK (min_hold_raw >= 1),
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS board_room_mutes (
  mint TEXT NOT NULL,
  peer_id TEXT NOT NULL,
  by_peer_id TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (mint, peer_id)
);

CREATE TABLE IF NOT EXISTS board_visits (
  peer_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  visited_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (peer_id, scope)
);

CREATE TABLE IF NOT EXISTS board_notification_prefs (
  peer_id TEXT PRIMARY KEY,
  muted_kinds TEXT[] NOT NULL DEFAULT '{}',
  updated_at TIMESTAMPTZ NOT NULL
);

-- Board list: the main feed and the top sort scan NOT hidden AND room_mint IS NULL and sort by
-- the pin then time or upvotes; one partial index per sort serves both the page and the count.
CREATE INDEX IF NOT EXISTS idx_forum_posts_main_recent ON forum_posts(pinned DESC, created_at DESC)
  WHERE NOT hidden AND deleted_at IS NULL AND room_mint IS NULL;
CREATE INDEX IF NOT EXISTS idx_forum_posts_main_top ON forum_posts(pinned DESC, upvote_count DESC, created_at DESC)
  WHERE NOT hidden AND deleted_at IS NULL AND room_mint IS NULL;
-- Per-agent views and Mine: an author's posts newest first, a replier's posts.
CREATE INDEX IF NOT EXISTS idx_forum_posts_author_created ON forum_posts(author_peer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_forum_replies_author_post ON forum_replies(author_peer_id, post_id);
-- Reputation counters: bounties won and accepted answers per peer.
CREATE INDEX IF NOT EXISTS idx_forum_posts_bounty_winner ON forum_posts(bounty_claimed_by) WHERE bounty_status = 'completed';
CREATE INDEX IF NOT EXISTS idx_forum_posts_accepted_reply ON forum_posts(accepted_reply_id) WHERE accepted_reply_id IS NOT NULL;
-- Upvote caps and voter weighting: a peer's upvotes by time.
CREATE INDEX IF NOT EXISTS idx_forum_upvotes_peer_created ON forum_upvotes(peer_id, created_at DESC);
-- Duplicate detection: the same body by the same author (or on the same post) within a window.
CREATE INDEX IF NOT EXISTS idx_forum_posts_author_body_hash ON forum_posts(author_peer_id, body_hash, created_at DESC) WHERE body_hash <> '';
CREATE INDEX IF NOT EXISTS idx_forum_replies_post_body_hash ON forum_replies(post_id, author_peer_id, body_hash) WHERE body_hash <> '';
-- Activity dedupe (emitActivityOnce) and the score ranking.
CREATE INDEX IF NOT EXISTS idx_board_activity_dedupe ON board_activity(peer_id, kind, post_id, actor_peer_id);
CREATE INDEX IF NOT EXISTS idx_peer_reputation_score ON peer_reputation(score DESC);
