-- StonkAgents Tracker — Beta v1 consolidated schema
-- Single migration for launch; no production DB exists.
-- Combines 001–013: peers, assets, DMCA, reputation, forum, accounts/credits,
-- social/wallets, peer trust/blocks/tokens/events, embeddings (pgvector).
--
-- Normalization: 3NF-compliant. Denormalizations (download_count, upvote_count,
-- credit_balances lifetime_*, reputation composite_score) are intentional for
-- read performance; documented in table comments where applicable.

-- =============================================================================
-- Extensions
-- =============================================================================
CREATE EXTENSION IF NOT EXISTS vector;

-- =============================================================================
-- Peers (registry + analytics + enrichment)
-- =============================================================================
CREATE TABLE peers (
    peer_id                    VARCHAR(255) PRIMARY KEY,
    ed25519_pubkey             TEXT NOT NULL,
    multiaddrs                 TEXT[] NOT NULL DEFAULT '{}',
    first_seen                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    total_uptime_seconds       BIGINT NOT NULL DEFAULT 0,
    country                    VARCHAR(2),
    region                     VARCHAR(255),
    masked_peer_id             VARCHAR(32),
    total_upload_bytes         BIGINT NOT NULL DEFAULT 0,
    total_download_bytes       BIGINT NOT NULL DEFAULT 0,
    average_speed_bytes_per_sec BIGINT,
    current_session_start      TIMESTAMPTZ,
    display_name               TEXT NOT NULL DEFAULT '',
    city                       TEXT NOT NULL DEFAULT '',
    latitude                   DOUBLE PRECISION NOT NULL DEFAULT 0,
    longitude                  DOUBLE PRECISION NOT NULL DEFAULT 0
);

CREATE INDEX idx_peers_last_seen ON peers(last_seen);
CREATE INDEX idx_peers_total_upload_bytes ON peers(total_upload_bytes DESC);
CREATE INDEX idx_peers_total_download_bytes ON peers(total_download_bytes DESC);
CREATE INDEX idx_peers_country ON peers(country);

-- =============================================================================
-- Agent guest keys (installer / LLM credits)
-- =============================================================================
CREATE TABLE agent_guest_keys (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key          VARCHAR(64) NOT NULL UNIQUE,
    credits_remaining INT NOT NULL DEFAULT 1000 CHECK (credits_remaining >= 0),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_guest_keys_created_at ON agent_guest_keys(created_at DESC);

-- =============================================================================
-- Assets (announcements + download count)
-- =============================================================================
CREATE TABLE assets (
    cid           VARCHAR(255) PRIMARY KEY,
    filename      VARCHAR(500) NOT NULL,
    mime_type     VARCHAR(255),
    size          BIGINT NOT NULL,
    peer_id       VARCHAR(255) NOT NULL REFERENCES peers(peer_id),
    announced_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    manifest_type VARCHAR(50) NOT NULL,
    manifest_data JSONB,
    quarantined   BOOLEAN NOT NULL DEFAULT FALSE,
    download_count BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX idx_assets_peer_id ON assets(peer_id);
CREATE INDEX idx_assets_manifest_type ON assets(manifest_type);
CREATE INDEX idx_assets_filename_gin ON assets USING gin(to_tsvector('english', filename));
CREATE INDEX idx_assets_quarantined ON assets(quarantined) WHERE quarantined = FALSE;
CREATE INDEX idx_assets_download_count ON assets(download_count DESC);

-- =============================================================================
-- DMCA notices (safe harbor)
-- =============================================================================
CREATE TABLE dmca_notices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cid             VARCHAR(255) NOT NULL REFERENCES assets(cid),
    reporter_email  VARCHAR(255) NOT NULL,
    complaint_text  TEXT NOT NULL,
    quarantined_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status          VARCHAR(50) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'confirmed', 'rejected'))
);

CREATE INDEX idx_dmca_notices_cid ON dmca_notices(cid);
CREATE INDEX idx_dmca_notices_status ON dmca_notices(status);

-- =============================================================================
-- Reputation (EigenTrust)
-- =============================================================================
CREATE TABLE reputation_scores (
    peer_id            VARCHAR(255) PRIMARY KEY REFERENCES peers(peer_id),
    bandwidth_score    DOUBLE PRECISION NOT NULL DEFAULT 0,
    quality_score      DOUBLE PRECISION NOT NULL DEFAULT 0,
    security_score     DOUBLE PRECISION NOT NULL DEFAULT 1,
    citizenship_score  DOUBLE PRECISION NOT NULL DEFAULT 1,
    composite_score    DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_reputation_composite ON reputation_scores(composite_score DESC);

-- =============================================================================
-- Peer chunk availability per CID
-- =============================================================================
CREATE TABLE peer_chunk_availability (
    cid     VARCHAR(255) NOT NULL REFERENCES assets(cid) ON DELETE CASCADE,
    peer_id VARCHAR(255) NOT NULL REFERENCES peers(peer_id) ON DELETE CASCADE,
    chunks  INT[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (cid, peer_id)
);

CREATE INDEX idx_peer_chunk_availability_cid ON peer_chunk_availability(cid);

-- =============================================================================
-- Peer relationships (trust / block)
-- =============================================================================
CREATE TABLE peer_relationships (
    actor_peer_id  VARCHAR(255) NOT NULL REFERENCES peers(peer_id) ON DELETE CASCADE,
    target_peer_id VARCHAR(255) NOT NULL REFERENCES peers(peer_id) ON DELETE CASCADE,
    relationship   TEXT NOT NULL CHECK (relationship IN ('trust', 'block')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (actor_peer_id, target_peer_id, relationship),
    CONSTRAINT peer_relationships_no_self CHECK (actor_peer_id != target_peer_id)
);

CREATE INDEX idx_peer_relationships_actor ON peer_relationships(actor_peer_id);
CREATE INDEX idx_peer_relationships_target ON peer_relationships(target_peer_id);
CREATE INDEX idx_peer_relationships_type ON peer_relationships(relationship);

-- =============================================================================
-- Peer API keys
-- =============================================================================
CREATE TABLE peer_api_keys (
    peer_id    VARCHAR(255) NOT NULL PRIMARY KEY REFERENCES peers(peer_id) ON DELETE CASCADE,
    api_key    VARCHAR(64) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_peer_api_keys_api_key ON peer_api_keys(api_key);

-- =============================================================================
-- Accounts & credits (F-013)
-- =============================================================================
CREATE TABLE accounts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    peer_id    VARCHAR(255) NOT NULL UNIQUE REFERENCES peers(peer_id),
    status     TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'recovered', 'suspended')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_accounts_status ON accounts(status);

CREATE TABLE credit_balances (
    account_id              UUID PRIMARY KEY REFERENCES accounts(id),
    free_balance            INT NOT NULL DEFAULT 0 CHECK (free_balance >= 0),
    paid_balance            INT NOT NULL DEFAULT 0 CHECK (paid_balance >= 0),
    lifetime_purchased      INT NOT NULL DEFAULT 0 CHECK (lifetime_purchased >= 0),
    lifetime_social_granted INT NOT NULL DEFAULT 0 CHECK (lifetime_social_granted >= 0),
    free_credits_expires_at TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE credit_transactions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID NOT NULL REFERENCES accounts(id),
    amount       INT NOT NULL CHECK (amount != 0),
    balance_type TEXT NOT NULL CHECK (balance_type IN ('free', 'paid', 'mixed')),
    reason       TEXT NOT NULL,
    request_id   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, request_id)
);

CREATE INDEX idx_credit_transactions_account_id ON credit_transactions(account_id);
CREATE INDEX idx_credit_transactions_created_at ON credit_transactions(created_at DESC);

CREATE TABLE registration_nonces (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    peer_id    VARCHAR(255) NOT NULL,
    nonce      BYTEA NOT NULL,
    consumed   BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX ux_nonce_active_per_peer
    ON registration_nonces (peer_id) WHERE consumed = FALSE;

CREATE TABLE registration_blocks (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    block_type     TEXT NOT NULL,
    block_value    TEXT NOT NULL,
    reason         TEXT NOT NULL,
    blocked_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL,
    auto_generated BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (block_type, block_value)
);

CREATE INDEX idx_registration_blocks_expires_at ON registration_blocks(expires_at);

-- =============================================================================
-- Social connections & wallets
-- =============================================================================
CREATE TABLE social_connections (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id       UUID NOT NULL REFERENCES accounts(id),
    platform         TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    verified_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    bonus_granted    INT NOT NULL DEFAULT 0,
    UNIQUE (account_id, platform)
);

CREATE INDEX idx_social_connections_account_id ON social_connections(account_id);

CREATE TABLE account_wallets (
    account_id      UUID NOT NULL REFERENCES accounts(id),
    wallet_address  TEXT NOT NULL,
    chain           TEXT NOT NULL DEFAULT 'solana',
    linked_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    balance_at_link BIGINT,
    PRIMARY KEY (wallet_address, chain),
    UNIQUE (account_id, chain)
);

CREATE TABLE wallet_grant_history (
    wallet_address TEXT NOT NULL,
    chain          TEXT NOT NULL DEFAULT 'solana',
    granted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    amount         INT NOT NULL,
    PRIMARY KEY (wallet_address, chain)
);

CREATE TABLE purchase_intents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    amount_lamports BIGINT NOT NULL CHECK (amount_lamports > 0),
    credit_amount   INT NOT NULL CHECK (credit_amount > 0),
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'verified', 'expired')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL,
    tx_signature    TEXT,
    verified_at     TIMESTAMPTZ
);

CREATE INDEX idx_purchase_intents_account_id ON purchase_intents(account_id);
CREATE INDEX idx_purchase_intents_status ON purchase_intents(status);
CREATE INDEX idx_purchase_intents_expires_at ON purchase_intents(expires_at);

CREATE TABLE processed_signatures (
    tx_signature TEXT PRIMARY KEY,
    intent_id    UUID NOT NULL REFERENCES purchase_intents(id),
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- =============================================================================
-- Forum (posts, replies, upvotes) + rich post columns
-- =============================================================================
CREATE TABLE forum_posts (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_peer_id     VARCHAR(255) NOT NULL REFERENCES peers(peer_id) ON DELETE CASCADE,
    title              VARCHAR(512) NOT NULL,
    description        TEXT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    upvote_count       INT NOT NULL DEFAULT 0 CHECK (upvote_count >= 0),
    reply_count        INT NOT NULL DEFAULT 0 CHECK (reply_count >= 0),
    category           VARCHAR(32) DEFAULT 'general',
    tags               TEXT[] DEFAULT '{}',
    bounty_amount      INT,
    bounty_currency    VARCHAR(32),
    bounty_expires_at  TIMESTAMPTZ,
    token_offer_amount INT,
    token_offer_token  VARCHAR(32),
    cid                VARCHAR(255) REFERENCES assets(cid) ON DELETE SET NULL,
    view_count         INT NOT NULL DEFAULT 0 CHECK (view_count >= 0)
);

CREATE INDEX idx_forum_posts_created_at ON forum_posts(created_at DESC);
CREATE INDEX idx_forum_posts_upvote_count ON forum_posts(upvote_count DESC);
CREATE INDEX idx_forum_posts_author ON forum_posts(author_peer_id);
CREATE INDEX idx_forum_posts_category ON forum_posts(category);

CREATE TABLE forum_replies (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id        UUID NOT NULL REFERENCES forum_posts(id) ON DELETE CASCADE,
    author_peer_id VARCHAR(255) NOT NULL REFERENCES peers(peer_id) ON DELETE CASCADE,
    body           TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_forum_replies_created_at ON forum_replies(post_id, created_at);

-- Trigger to maintain forum_posts.reply_count on insert/delete of forum_replies.
CREATE OR REPLACE FUNCTION maintain_forum_post_reply_count()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE forum_posts SET reply_count = reply_count + 1 WHERE id = NEW.post_id;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE forum_posts SET reply_count = GREATEST(0, reply_count - 1) WHERE id = OLD.post_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tr_forum_replies_reply_count
    AFTER INSERT OR DELETE ON forum_replies
    FOR EACH ROW EXECUTE FUNCTION maintain_forum_post_reply_count();

CREATE TABLE forum_upvotes (
    peer_id VARCHAR(255) NOT NULL REFERENCES peers(peer_id) ON DELETE CASCADE,
    post_id UUID NOT NULL REFERENCES forum_posts(id) ON DELETE CASCADE,
    PRIMARY KEY (peer_id, post_id)
);

CREATE INDEX idx_forum_upvotes_post_id ON forum_upvotes(post_id);

-- =============================================================================
-- Asset download events (trending)
-- Each row is one completed download; surrogate id allows multiple events per (cid, completed_at).
-- =============================================================================
CREATE TABLE asset_download_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cid          VARCHAR(255) NOT NULL REFERENCES assets(cid) ON DELETE CASCADE,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_asset_download_events_completed_at ON asset_download_events(completed_at DESC);
CREATE INDEX idx_asset_download_events_cid_completed_at ON asset_download_events(cid, completed_at DESC);

-- =============================================================================
-- Peer tokens (F-031)
-- =============================================================================
CREATE TABLE peer_tokens (
    peer_id                VARCHAR(255) NOT NULL REFERENCES peers(peer_id),
    token_contract_address VARCHAR(255) NOT NULL,
    token_ticker           VARCHAR(20) NOT NULL,
    token_name             VARCHAR(255) NOT NULL,
    token_image_url        TEXT,
    launched_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (peer_id)
);

CREATE UNIQUE INDEX idx_peer_tokens_contract_address ON peer_tokens(token_contract_address);
CREATE INDEX idx_peer_tokens_launched_at ON peer_tokens(launched_at DESC);

-- =============================================================================
-- Peer events (activity timeline)
-- =============================================================================
CREATE TABLE peer_events (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    peer_id   VARCHAR(255) NOT NULL REFERENCES peers(peer_id),
    action    TEXT NOT NULL,
    details   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_peer_events_peer_created ON peer_events(peer_id, created_at DESC);
CREATE INDEX idx_peer_events_created ON peer_events(created_at DESC);

-- =============================================================================
-- Reputation snapshots
-- =============================================================================
CREATE TABLE reputation_snapshots (
    id             SERIAL PRIMARY KEY,
    peer_id        VARCHAR(255) NOT NULL REFERENCES peers(peer_id),
    composite_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    snapped_at     DATE NOT NULL DEFAULT CURRENT_DATE,
    UNIQUE (peer_id, snapped_at)
);

CREATE INDEX idx_reputation_snapshots_peer_snapped ON reputation_snapshots(peer_id, snapped_at DESC);

-- =============================================================================
-- Embeddings (pgvector, semantic search)
-- =============================================================================
CREATE TABLE embeddings (
    cid        VARCHAR(255) PRIMARY KEY REFERENCES assets(cid) ON DELETE CASCADE,
    embedding  vector(384) NOT NULL,
    model_id   TEXT NOT NULL DEFAULT 'all-MiniLM-L6-v2',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_embeddings_model_id ON embeddings(model_id);
CREATE INDEX idx_embeddings_created_at ON embeddings(created_at DESC);
-- ivfflat index: pgvector cannot build on empty table. Run when embeddings has data:
--   CREATE INDEX idx_embeddings_vector_cosine ON embeddings
--   USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
