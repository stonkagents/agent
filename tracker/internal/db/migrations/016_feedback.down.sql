-- 016_feedback: drop the portal feedback table.
DROP INDEX IF EXISTS idx_feedback_kind;
DROP INDEX IF EXISTS idx_feedback_created_at;
DROP TABLE IF EXISTS feedback;
