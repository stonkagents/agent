-- 011_launch_settings: StonkAgents launchpad (Raydium LaunchLab) configuration.
-- launch_settings: single-row table holding the live launch fee (priced from SOL/USD).
-- launch_quotes: quote tokens a launch can raise in, with their Raydium LaunchLab GlobalConfig PDAs.

CREATE TABLE IF NOT EXISTS launch_settings (
  id           SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  fee_usd      NUMERIC(18, 6) NOT NULL,
  fee_lamports BIGINT NOT NULL,
  sol_usd      NUMERIC(18, 8) NOT NULL,
  priced_at    TIMESTAMPTZ NOT NULL,
  source       TEXT NOT NULL DEFAULT '',
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS launch_quotes (
  quote_mint           TEXT PRIMARY KEY,
  symbol               TEXT NOT NULL,
  name                 TEXT NOT NULL,
  decimals             INT NOT NULL CHECK (decimals >= 0 AND decimals <= 18),
  token_program        TEXT NOT NULL,
  category             TEXT NOT NULL,
  launchlab_config_id  TEXT NOT NULL,
  min_fund_raising_raw NUMERIC(40, 0) NOT NULL DEFAULT 1,
  enabled              BOOLEAN NOT NULL DEFAULT TRUE,
  sort_order           INT NOT NULL DEFAULT 0,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_launch_quotes_enabled_sort ON launch_quotes(sort_order) WHERE enabled;

-- Verified mainnet rows. launchlab_config_id values are Raydium LaunchLab GlobalConfig PDAs live on mainnet.
INSERT INTO launch_quotes (quote_mint, symbol, name, decimals, token_program, category, launchlab_config_id, min_fund_raising_raw, enabled, sort_order) VALUES
  ('6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx', 'STONK', 'STONK',         9, 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA', 'custom',   '4Rb2joMnuDt9zRPuTYxUjQRGQ8oNBbKdXnCJSCojvBsW', 1,           TRUE, 0),
  ('So11111111111111111111111111111111111111112', 'SOL',   'Wrapped SOL',   9, 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA', 'solana',   '6s1xP3hpbAfFoNtUNF8mfHsjr2Bd97JxFJRWLbL6aHuX', 24000000000, TRUE, 1),
  ('Xsc9qvGR1efVDFGLrVsmkzv3qi45LTBjeUKSPmx9qEh', 'NVDAx', 'NVIDIA xStock', 8, 'TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb', 'xstock',   '2NuVPU5ViAyQsZamVfJ1vU6Cp77WSzret4LtNTMiWFQ4', 1,           TRUE, 2),
  ('XsoCS1TfEyfFhfvj8EtZ528L3CaKBDBRqRapnBbDF2W', 'SPYx',  'SP500 xStock',  8, 'TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb', 'xstock',   'B7ctMMdGvy46Am56myTtzfkNzt9kWZVTNGM2BWrJ9adg', 1,           TRUE, 3),
  ('XsueG8BtpquVJX9LVLLEGuViXUungE6WmK5YZ3p3bd1', 'CRCLx', 'Circle xStock', 8, 'TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb', 'xstock',   'CW9f7d56a4idicgJ1CgTegjTMifgD3cKUnW5pkRRjA9V', 1,           TRUE, 4),
  ('EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v', 'USDC',  'USD Coin',      6, 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA', 'currency', '8gj14w8vkNZjJTPak6e96Uf1sY438WKheH4s5dHyxiRw', 1,           TRUE, 5),
  ('Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB', 'USDT',  'Tether',        6, 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA', 'currency', '97yPgo2FSXbC1uzimwezv3n4MKkzhTvGkzQ45uiZ4XDu', 1,           TRUE, 6)
ON CONFLICT (quote_mint) DO NOTHING;
