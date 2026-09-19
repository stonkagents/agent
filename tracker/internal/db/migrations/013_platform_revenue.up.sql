-- 013_platform_revenue: ledger of platform revenue and outflows (launch fees, LaunchLab platform
-- fee claims, buybacks, burns, NFT yield, holder distributions). amount_raw is in the quote
-- mint's base units (lamports for SOL); amount_usd is filled by the pricing hook when available.
CREATE TABLE IF NOT EXISTS platform_revenue (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN (
                    'launch_fee', 'platform_fee_claim', 'buyback', 'burn', 'nft_yield', 'holder_distribution')),
    quote_mint  TEXT,
    amount_raw  NUMERIC NOT NULL,
    amount_usd  NUMERIC,
    signature   TEXT UNIQUE,
    mint        TEXT,
    occurred_at TIMESTAMPTZ NOT NULL,
    meta        JSONB
);

CREATE INDEX IF NOT EXISTS idx_platform_revenue_kind ON platform_revenue(kind);
CREATE INDEX IF NOT EXISTS idx_platform_revenue_occurred_at ON platform_revenue(occurred_at DESC);
