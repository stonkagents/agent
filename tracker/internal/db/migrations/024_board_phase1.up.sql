-- 024_board_phase1: community board phase 1 (reputation and reach).
-- 1. peer_reputation: the board reputation per peer (score, tier and the counters behind
--    them), recomputed on the events that move it and nightly in full.
-- 2. Accepted answer: forum_posts.accepted_reply_id.
-- 3. Token offers that settle: the offer's launch mint, symbol, decimals, per-reply amount,
--    max accepts and paid count on forum_posts (token_offer_amount becomes BIGINT because it is
--    in raw token units), plus token_offer_payments (one verified transfer per reply).
-- 4. Report, hide, pin: board_reports, forum_posts.hidden / pinned, forum_replies.hidden.
-- 5. Watch and mentions: board_watches, mention_peer_ids on posts and replies.
-- 6. board_activity carries the token symbol for token_offer_paid rows and a BIGINT amount
--    (raw token units).

CREATE TABLE IF NOT EXISTS peer_reputation (
    peer_id           TEXT PRIMARY KEY,
    score             INT NOT NULL DEFAULT 0,
    tier              TEXT NOT NULL DEFAULT 'new',
    bounties_won      INT NOT NULL DEFAULT 0,
    credits_won       INT NOT NULL DEFAULT 0,
    answers_accepted  INT NOT NULL DEFAULT 0,
    upvotes_received  INT NOT NULL DEFAULT 0,
    first_replies_1h  INT NOT NULL DEFAULT 0,
    reports_upheld    INT NOT NULL DEFAULT 0,
    computed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE forum_posts
  ADD COLUMN IF NOT EXISTS accepted_reply_id TEXT,
  ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS token_offer_mint TEXT,
  ADD COLUMN IF NOT EXISTS token_offer_symbol TEXT,
  ADD COLUMN IF NOT EXISTS token_offer_decimals INT,
  ADD COLUMN IF NOT EXISTS token_offer_max INT,
  ADD COLUMN IF NOT EXISTS token_offer_paid INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS mention_peer_ids TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE forum_posts ALTER COLUMN token_offer_amount TYPE BIGINT;

CREATE INDEX IF NOT EXISTS idx_forum_posts_pinned ON forum_posts(pinned) WHERE pinned;

ALTER TABLE forum_replies
  ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS mention_peer_ids TEXT[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS token_offer_payments (
    signature     TEXT PRIMARY KEY,
    post_id       TEXT NOT NULL,
    reply_id      TEXT NOT NULL UNIQUE,
    from_wallet   TEXT NOT NULL,
    to_wallet     TEXT NOT NULL,
    amount_raw    BIGINT NOT NULL CHECK (amount_raw > 0),
    verified_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_token_offer_payments_post ON token_offer_payments(post_id);

CREATE TABLE IF NOT EXISTS board_reports (
    id                TEXT PRIMARY KEY,
    target_type       TEXT NOT NULL CHECK (target_type IN ('post', 'reply')),
    target_id         TEXT NOT NULL,
    target_author_peer_id TEXT NOT NULL DEFAULT '',
    reporter_peer_id  TEXT NOT NULL,
    reason            TEXT NOT NULL CHECK (reason IN ('spam', 'abuse', 'scam', 'other')),
    note              TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'upheld', 'dismissed')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at       TIMESTAMPTZ,
    UNIQUE (target_type, target_id, reporter_peer_id)
);

CREATE INDEX IF NOT EXISTS idx_board_reports_status_created ON board_reports(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_board_reports_target_author ON board_reports(target_author_peer_id) WHERE status = 'upheld';

ALTER TABLE board_activity
  ADD COLUMN IF NOT EXISTS symbol TEXT NOT NULL DEFAULT '';
ALTER TABLE board_activity ALTER COLUMN amount TYPE BIGINT;

CREATE TABLE IF NOT EXISTS board_watches (
    post_id     TEXT NOT NULL,
    peer_id     TEXT NOT NULL,
    watching    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (post_id, peer_id)
);
