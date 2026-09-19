-- 025_board_phase2: community board phase 2 (rooms and matchmaking).
-- 1. Token rooms: a post may belong to the room of a token launched on this platform
--    (forum_posts.room_mint, the token_launches row is the room record). room_pinned marks the
--    launch announcement pinned within its room; one per room (partial unique index) so the
--    claim flow creates it idempotently.
-- 2. Request routing: routed_to stores the peers a Request or Bounty post was routed to.
-- 3. peer_autopilot: the autopilot categories each daemon reports on its heartbeat, read by
--    the request routing score.
-- 4. room_holder_snapshots: holder counts recorded when a room digest is read, so the next
--    digest can report the delta (new_holders).

ALTER TABLE forum_posts
  ADD COLUMN IF NOT EXISTS room_mint TEXT,
  ADD COLUMN IF NOT EXISTS room_pinned BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS routed_to TEXT[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_forum_posts_room_created ON forum_posts(room_mint, created_at DESC) WHERE room_mint IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_forum_posts_room_announcement ON forum_posts(room_mint) WHERE room_pinned;

CREATE TABLE IF NOT EXISTS peer_autopilot (
    peer_id     TEXT PRIMARY KEY,
    categories  TEXT[] NOT NULL DEFAULT '{}',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS room_holder_snapshots (
    mint        TEXT NOT NULL,
    taken_on    DATE NOT NULL,
    holders     INT NOT NULL,
    PRIMARY KEY (mint, taken_on)
);

CREATE INDEX IF NOT EXISTS idx_board_activity_peer_unread ON board_activity(peer_id, created_at DESC) WHERE read_at IS NULL;
