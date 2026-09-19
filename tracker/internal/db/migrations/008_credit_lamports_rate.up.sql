-- 008_credit_lamports_rate: Move credit→lamport conversion rate to platform_settings
-- so it can be re-anchored as SOL price drifts without a redeploy.
-- 20000 lamports = 0.00002 SOL = ~$0.00172/credit at SOL $86 (April 2026 baseline).

INSERT INTO platform_settings (key, value) VALUES
  ('credit_lamports_rate', '20000')
ON CONFLICT (key) DO NOTHING;

-- Update detailed model cost from old 50 to new 75 (flat-rate floor for ~80% margin).
UPDATE platform_settings SET value = '75', updated_at = NOW()
WHERE key = 'model_cost:gpt-5.4';
