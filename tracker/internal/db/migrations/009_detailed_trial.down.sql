ALTER TABLE credit_balances
  DROP COLUMN IF EXISTS detailed_trial_remaining,
  DROP COLUMN IF EXISTS detailed_trial_expires_at;
