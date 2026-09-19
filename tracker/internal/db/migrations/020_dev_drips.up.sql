-- 020_dev_drips: test-SOL / test-$STONK drips sent to wallets that connect to the dev portal
-- (POST /api/dev/drip, devnet only). One row per wallet holding its latest drip; ip is a
-- SHA-256 hex of the client IP (never the raw address) used for the per-IP hourly cap.
-- Amounts are raw chain units: lamports and token base units.

CREATE TABLE IF NOT EXISTS dev_drips (
  wallet        TEXT PRIMARY KEY,
  ip            TEXT NOT NULL DEFAULT '',
  signature     TEXT NOT NULL DEFAULT '',
  amount_sol    BIGINT NOT NULL DEFAULT 0 CHECK (amount_sol >= 0),
  amount_stonk  BIGINT NOT NULL DEFAULT 0 CHECK (amount_stonk >= 0),
  dripped_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dev_drips_ip_dripped_at ON dev_drips(ip, dripped_at DESC);
