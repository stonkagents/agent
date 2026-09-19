-- 012_token_launches: drop LaunchLab launch records.
DROP INDEX IF EXISTS idx_token_launches_created_at;
DROP INDEX IF EXISTS idx_token_launches_peer_id;
DROP INDEX IF EXISTS idx_token_launches_creator_wallet;
DROP TABLE IF EXISTS token_launches;
