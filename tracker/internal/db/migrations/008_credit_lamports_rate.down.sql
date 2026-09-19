-- Roll back model cost change; leave credit_lamports_rate in place (harmless if unused).
UPDATE platform_settings SET value = '50', updated_at = NOW()
WHERE key = 'model_cost:gpt-5.4';

DELETE FROM platform_settings WHERE key = 'credit_lamports_rate';
