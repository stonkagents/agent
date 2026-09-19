-- 015_devnet_stonk_standin_quote: a $STONK stand-in quote on devnet.
-- There is no $STONK mint (or GlobalConfig) on devnet. Raydium's devnet USDC mint
-- (USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT, 6 decimals, SPL Token program) has a LaunchLab
-- GlobalConfig (4wHbNkobu7iARU9MbCEqDSAq6JuQreGupG2Jsf2R3DFP: curveType 0, tradeFeeRate 2500,
-- probed on chain 2026-09-13), so it stands in for $STONK on the dev tracker. Category 'stonk'
-- is what the service prefers as the default quote (see LaunchConfigService.resolveQuote).
-- min_fund_raising_raw follows migration 014's convention: the GlobalConfig's on-chain
-- minFundRaisingB, which is 1 raw unit for Raydium's own devnet configs.
-- Mainnet rows are untouched; the devnet SOL row stays enabled but moves behind STONK in sort_order.

INSERT INTO launch_quotes (cluster, quote_mint, symbol, name, decimals, token_program, category, launchlab_config_id, min_fund_raising_raw, enabled, sort_order) VALUES
  ('devnet', 'USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT', 'STONK', '$STONK (devnet stand-in)', 6, 'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA', 'stonk', '4wHbNkobu7iARU9MbCEqDSAq6JuQreGupG2Jsf2R3DFP', 1, TRUE, 0)
ON CONFLICT (cluster, quote_mint) DO NOTHING;

UPDATE launch_quotes SET sort_order = 1, updated_at = NOW()
WHERE cluster = 'devnet' AND quote_mint = 'So11111111111111111111111111111111111111112' AND sort_order = 0;
