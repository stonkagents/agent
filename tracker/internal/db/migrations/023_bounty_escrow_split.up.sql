-- 023_bounty_escrow_split: how much of a bounty escrow left the paid bucket (the rest came from
-- free credits), so an expiry refund returns each part to the bucket it left. Posts escrowed
-- before this migration keep 0 and refund as free, which is what they did before.
ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_escrow_paid INT NOT NULL DEFAULT 0;
