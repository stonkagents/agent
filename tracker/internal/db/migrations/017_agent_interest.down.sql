-- 017_agent_interest: drop the roadmap interest table.
DROP INDEX IF EXISTS idx_agent_interest_capabilities;
DROP INDEX IF EXISTS idx_agent_interest_priority;
DROP INDEX IF EXISTS idx_agent_interest_created_at;
DROP TABLE IF EXISTS agent_interest;
