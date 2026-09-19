-- 012_token_launches: Raydium LaunchLab launches recorded by the portal (token-first, agent later).
-- A row is created right after the launch transaction confirms (status='confirmed', peer_id NULL)
-- and bound to a peer when the creator installs the daemon and links the same wallet (status='bound').
CREATE TABLE IF NOT EXISTS token_launches (
    mint              TEXT PRIMARY KEY,
    pool_id           TEXT,
    creator_wallet    TEXT NOT NULL,
    quote_mint        TEXT NOT NULL,
    name              TEXT NOT NULL DEFAULT '',
    symbol            TEXT NOT NULL DEFAULT '',
    image_url         TEXT,
    metadata_uri      TEXT,
    launch_signature  TEXT NOT NULL UNIQUE,
    fee_lamports      BIGINT NOT NULL CHECK (fee_lamports >= 0),
    transfer_fee_bps  INT NOT NULL DEFAULT 100 CHECK (transfer_fee_bps >= 0 AND transfer_fee_bps <= 10000),
    platform_id       TEXT,
    peer_id           VARCHAR(255) REFERENCES peers(peer_id),
    status            TEXT NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'bound')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    bound_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_token_launches_creator_wallet ON token_launches(creator_wallet);
CREATE INDEX IF NOT EXISTS idx_token_launches_peer_id ON token_launches(peer_id);
CREATE INDEX IF NOT EXISTS idx_token_launches_created_at ON token_launches(created_at DESC);
