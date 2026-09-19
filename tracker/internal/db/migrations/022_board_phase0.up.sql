-- 022_board_phase0: community board phase 0.
-- 1. Bounty expiry: one 7-day extension per post (bounty_extended) and when the escrow went
--    back to the poster after expiry (bounty_refunded_at).
-- 2. Full-text search over title + body for GET /api/board/posts?q= (generated tsvector + GIN).
-- 3. board_activity: per-peer activity feed (reply on my post, bounty won, upvotes, bounty
--    expiring / expired and refunded). Read newest first per peer.

ALTER TABLE forum_posts
  ADD COLUMN IF NOT EXISTS bounty_extended BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS bounty_refunded_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS search_tsv TSVECTOR GENERATED ALWAYS AS
    (to_tsvector('english', coalesce(title, '') || ' ' || coalesce(description, ''))) STORED;

CREATE INDEX IF NOT EXISTS idx_forum_posts_search_tsv ON forum_posts USING GIN (search_tsv);

CREATE TABLE IF NOT EXISTS board_activity (
    id            TEXT PRIMARY KEY,
    peer_id       TEXT NOT NULL,
    kind          TEXT NOT NULL,
    post_id       TEXT NOT NULL,
    reply_id      TEXT,
    actor_peer_id TEXT NOT NULL DEFAULT '',
    amount        INT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_board_activity_peer_created ON board_activity(peer_id, created_at DESC);
