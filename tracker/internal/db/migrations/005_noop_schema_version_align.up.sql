-- No schema change. Aligns embedded migrations with DBs that already recorded version 4
-- (e.g. older builds or manual schema_migrations bumps) so golang-migrate can run.
SELECT 1;
