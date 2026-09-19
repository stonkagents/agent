-- 013_platform_revenue: drop platform revenue ledger.
DROP INDEX IF EXISTS idx_platform_revenue_occurred_at;
DROP INDEX IF EXISTS idx_platform_revenue_kind;
DROP TABLE IF EXISTS platform_revenue;
