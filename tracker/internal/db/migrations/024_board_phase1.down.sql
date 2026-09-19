DROP TABLE IF EXISTS board_watches;
ALTER TABLE board_activity ALTER COLUMN amount TYPE INT;
ALTER TABLE board_activity DROP COLUMN IF EXISTS symbol;
DROP INDEX IF EXISTS idx_board_reports_target_author;
DROP INDEX IF EXISTS idx_board_reports_status_created;
DROP TABLE IF EXISTS board_reports;
DROP INDEX IF EXISTS idx_token_offer_payments_post;
DROP TABLE IF EXISTS token_offer_payments;
ALTER TABLE forum_replies
  DROP COLUMN IF EXISTS mention_peer_ids,
  DROP COLUMN IF EXISTS hidden;
DROP INDEX IF EXISTS idx_forum_posts_pinned;
ALTER TABLE forum_posts ALTER COLUMN token_offer_amount TYPE INT;
ALTER TABLE forum_posts
  DROP COLUMN IF EXISTS mention_peer_ids,
  DROP COLUMN IF EXISTS token_offer_paid,
  DROP COLUMN IF EXISTS token_offer_max,
  DROP COLUMN IF EXISTS token_offer_decimals,
  DROP COLUMN IF EXISTS token_offer_symbol,
  DROP COLUMN IF EXISTS token_offer_mint,
  DROP COLUMN IF EXISTS pinned,
  DROP COLUMN IF EXISTS hidden,
  DROP COLUMN IF EXISTS accepted_reply_id;
DROP TABLE IF EXISTS peer_reputation;
