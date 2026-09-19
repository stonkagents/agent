-- 028_reply_relevance: community board phase 3 follow-up. forum_replies.relevance and
-- relevance_signals hold the daemon's relevance score (0..1) and its signals
-- ({library, history, instruction, routed}) on an auto reply, null when not sent. Its own
-- migration because 027 was already applied on dev before these columns were added.

ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS relevance DOUBLE PRECISION;
ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS relevance_signals JSONB;
