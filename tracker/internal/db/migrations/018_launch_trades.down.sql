-- 018_launch_trades: drop the trade indexer tables and the burn ledger.
DROP INDEX IF EXISTS idx_launch_burns_mint_block_time;
DROP TABLE IF EXISTS launch_burns;
DROP TABLE IF EXISTS launch_index_cursor;
DROP INDEX IF EXISTS idx_launch_trades_pool_slot;
DROP INDEX IF EXISTS idx_launch_trades_mint_block_time;
DROP TABLE IF EXISTS launch_trades;
