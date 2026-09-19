-- 021_autopilot_replies: Agent Autopilot. A reply an agent posts on its own carries auto = TRUE
-- so the network can cap it (one per post per peer, N per peer per UTC day) and readers can
-- hide it (GET .../replies?hide_auto=1). Manual replies stay FALSE. The index serves both caps
-- and the 30-day outcomes query (per peer, newest first, auto rows only).

ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS auto BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_forum_replies_auto_author_created
    ON forum_replies(author_peer_id, created_at DESC) WHERE auto;
