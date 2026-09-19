-- 014_launch_quotes_cluster: back to one row per quote mint (mainnet only).
DELETE FROM launch_quotes WHERE cluster <> 'mainnet';
DROP INDEX IF EXISTS idx_launch_quotes_enabled_sort;
ALTER TABLE launch_quotes DROP CONSTRAINT IF EXISTS launch_quotes_pkey;
ALTER TABLE launch_quotes ADD PRIMARY KEY (quote_mint);
ALTER TABLE launch_quotes DROP CONSTRAINT IF EXISTS launch_quotes_cluster_check;
ALTER TABLE launch_quotes DROP COLUMN IF EXISTS cluster;
CREATE INDEX IF NOT EXISTS idx_launch_quotes_enabled_sort ON launch_quotes(sort_order) WHERE enabled;
