-- 018_launch_trades: trade indexer for LaunchLab launches + burn ledger for the network token.
--
-- launch_trades: one row per swap against a launch's bonding-curve pool, derived from the
-- pool vaults' token balance deltas of the confirming transaction. Amounts are whole tokens
-- (raw / 10^decimals); price_quote is quote per base. The API's 24h change / volume, the
-- trade feed and the candles all read from here.
CREATE TABLE IF NOT EXISTS launch_trades (
  id            BIGSERIAL PRIMARY KEY,
  mint          TEXT NOT NULL,
  pool_id       TEXT NOT NULL,
  signature     TEXT NOT NULL UNIQUE,
  slot          BIGINT NOT NULL,
  block_time    TIMESTAMPTZ NOT NULL,
  side          TEXT NOT NULL CHECK (side IN ('buy', 'sell')),
  trader        TEXT NOT NULL,
  base_amount   DOUBLE PRECISION NOT NULL CHECK (base_amount >= 0),
  quote_amount  DOUBLE PRECISION NOT NULL CHECK (quote_amount >= 0),
  price_quote   DOUBLE PRECISION NOT NULL CHECK (price_quote >= 0),
  quote_symbol  TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_launch_trades_mint_block_time ON launch_trades(mint, block_time DESC);
CREATE INDEX IF NOT EXISTS idx_launch_trades_pool_slot ON launch_trades(pool_id, slot DESC);

-- launch_index_cursor: per indexed address (a pool id, or "burns:<mint>" for the burn
-- ledger) the newest signature already stored, so each tick asks the RPC only for what
-- came after it (getSignaturesForAddress until=<last_signature>).
CREATE TABLE IF NOT EXISTS launch_index_cursor (
  pool_id          TEXT PRIMARY KEY,
  last_signature   TEXT NOT NULL,
  last_block_time  TIMESTAMPTZ,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- launch_burns: burn / burnChecked instructions on the network token mint (AGENT_TOKEN_MINT),
-- amount in whole tokens. Serves GET /api/v1/agent-token/burnplan.
CREATE TABLE IF NOT EXISTS launch_burns (
  id          BIGSERIAL PRIMARY KEY,
  mint        TEXT NOT NULL,
  signature   TEXT NOT NULL UNIQUE,
  slot        BIGINT NOT NULL,
  block_time  TIMESTAMPTZ NOT NULL,
  amount      DOUBLE PRECISION NOT NULL CHECK (amount >= 0),
  burner      TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_launch_burns_mint_block_time ON launch_burns(mint, block_time DESC);
