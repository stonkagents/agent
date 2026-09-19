-- 007_platform_settings: Key-value store for configurable platform settings (model costs, etc.)
CREATE TABLE IF NOT EXISTS platform_settings (
  key VARCHAR(128) PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Seed model pricing (credits per completion)
INSERT INTO platform_settings (key, value) VALUES
  ('model_cost:gpt-5.4-mini', '10'),
  ('model_cost:gpt-5.4', '50'),
  ('default_model', 'gpt-5.4-mini'),
  ('allowed_models', 'gpt-5.4-mini,gpt-5.4')
ON CONFLICT (key) DO NOTHING;
