-- 023_bounty_escrow_split: drop the escrow split column.
ALTER TABLE forum_posts DROP COLUMN IF EXISTS bounty_escrow_paid;
