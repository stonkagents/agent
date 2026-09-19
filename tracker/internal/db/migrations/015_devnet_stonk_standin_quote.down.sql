-- 015_devnet_stonk_standin_quote: remove the devnet $STONK stand-in; SOL is the devnet default again.
DELETE FROM launch_quotes WHERE cluster = 'devnet' AND quote_mint = 'USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT';
UPDATE launch_quotes SET sort_order = 0, updated_at = NOW()
WHERE cluster = 'devnet' AND quote_mint = 'So11111111111111111111111111111111111111112' AND sort_order = 1;
