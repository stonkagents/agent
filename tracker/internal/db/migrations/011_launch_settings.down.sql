-- 011_launch_settings: Remove launchpad configuration tables.
DROP INDEX IF EXISTS idx_launch_quotes_enabled_sort;
DROP TABLE IF EXISTS launch_quotes;
DROP TABLE IF EXISTS launch_settings;
