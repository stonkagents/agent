-- 014_launch_quotes_cluster: quote tokens are per Solana cluster. Raydium LaunchLab GlobalConfig
-- PDAs exist independently on mainnet and devnet, so a quote row must say which cluster its
-- launchlab_config_id lives on. Every existing row (migration 011) is a verified mainnet row.
-- The tracker reads only the rows for its configured cluster (LAUNCHPAD_CLUSTER, derived from
-- LAUNCHPAD_PROGRAM_ID when unset).

ALTER TABLE launch_quotes ADD COLUMN IF NOT EXISTS cluster TEXT NOT NULL DEFAULT 'mainnet';
ALTER TABLE launch_quotes DROP CONSTRAINT IF EXISTS launch_quotes_cluster_check;
ALTER TABLE launch_quotes ADD CONSTRAINT launch_quotes_cluster_check CHECK (cluster IN ('mainnet', 'devnet'));

-- Uniqueness moves from quote_mint to (cluster, quote_mint): the same mint (e.g. wrapped SOL)
-- appears once per cluster with a different GlobalConfig.
ALTER TABLE launch_quotes DROP CONSTRAINT IF EXISTS launch_quotes_pkey;
ALTER TABLE launch_quotes ADD PRIMARY KEY (cluster, quote_mint);

DROP INDEX IF EXISTS idx_launch_quotes_enabled_sort;
CREATE INDEX IF NOT EXISTS idx_launch_quotes_enabled_sort ON launch_quotes(cluster, sort_order) WHERE enabled;

-- Devnet: only Raydium's own SOL GlobalConfig exists (curveType 0, index 0, mintB wrapped SOL,
-- minFundRaisingB 1 raw, tradeFeeRate 2500). Raydium alone can create GlobalConfigs, so there is
-- no $STONK (or xStock) quote on devnet; the service falls back to this row as the default.
INSERT INTO launch_quotes (cluster, quote_mint, symbol, name, decimals, token_program, category, launchlab_config_id, min_fund_raising_raw, enabled, sort_order) VALUES
  ('devnet', 'So11111111111111111111111111111111111111112', 'SOL', 'Wrapped SOL (devnet)', 9, 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA', 'solana', '7ZR4zD7PYfY2XxoG1Gxcy2EgEeGYrpxrwzPuwdUBssEt', 1, TRUE, 0)
ON CONFLICT (cluster, quote_mint) DO NOTHING;
