-- StonkAgents Tracker — Beta v1 rollback. Drop in reverse dependency order.

DROP TABLE IF EXISTS embeddings;
DROP TABLE IF EXISTS reputation_snapshots;
DROP TABLE IF EXISTS peer_events;
DROP TABLE IF EXISTS peer_tokens;
DROP TABLE IF EXISTS asset_download_events;
DROP TABLE IF EXISTS forum_upvotes;
DROP TABLE IF EXISTS forum_replies;
DROP FUNCTION IF EXISTS maintain_forum_post_reply_count();
DROP TABLE IF EXISTS forum_posts;
DROP TABLE IF EXISTS processed_signatures;
DROP TABLE IF EXISTS purchase_intents;
DROP TABLE IF EXISTS wallet_grant_history;
DROP TABLE IF EXISTS account_wallets;
DROP TABLE IF EXISTS social_connections;
DROP TABLE IF EXISTS registration_blocks;
DROP TABLE IF EXISTS registration_nonces;
DROP TABLE IF EXISTS credit_transactions;
DROP TABLE IF EXISTS credit_balances;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS peer_api_keys;
DROP TABLE IF EXISTS peer_relationships;
DROP TABLE IF EXISTS peer_chunk_availability;
DROP TABLE IF EXISTS reputation_scores;
DROP TABLE IF EXISTS dmca_notices;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS agent_guest_keys;
DROP TABLE IF EXISTS peers;

DROP EXTENSION IF EXISTS vector;
