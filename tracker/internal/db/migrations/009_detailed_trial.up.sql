-- 009_detailed_trial: Trial counter for detailed (gpt-5.4) mode for new free-tier users.
-- New signups get 3 trial calls + 30-day expiry; existing users get 0 (no retroactive grant).

ALTER TABLE credit_balances
  ADD COLUMN IF NOT EXISTS detailed_trial_remaining INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS detailed_trial_expires_at TIMESTAMPTZ;
