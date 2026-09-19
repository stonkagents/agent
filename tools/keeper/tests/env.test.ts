import { describe, expect, it } from 'vitest';
import { loadConfig } from '../src/env.js';
import { parseJobArgs } from '../src/cli.js';

const MINT = 'So11111111111111111111111111111111111111112';

describe('loadConfig', () => {
  it('applies devnet defaults when nothing is set', () => {
    const cfg = loadConfig({} as NodeJS.ProcessEnv, '/tmp/keeper');
    expect(cfg.CLUSTER).toBe('devnet');
    expect(cfg.rpcUrl).toBe('https://api.devnet.solana.com');
    expect(cfg.BUYBACK_SHARE_BPS).toBe(5_000);
    expect(cfg.BUYBACK_MODE).toBe('burn');
    expect(cfg.DISTRIBUTION_MIN_USD).toBe(25);
    expect(cfg.JUPITER_API_URL).toBe('https://lite-api.jup.ag/swap/v1');
  });

  it('switches the default RPC with the cluster', () => {
    expect(loadConfig({ CLUSTER: 'mainnet' } as NodeJS.ProcessEnv).rpcUrl).toBe('https://api.mainnet-beta.solana.com');
  });

  it('falls back to the transfer-fee authority for the distribution wallet', () => {
    const cfg = loadConfig({ TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH: '/keys/tax.json' } as NodeJS.ProcessEnv);
    expect(cfg.distributionKeypairPath).toBe('/keys/tax.json');
    const explicit = loadConfig({
      TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH: '/keys/tax.json',
      DISTRIBUTION_WALLET_KEYPAIR_PATH: '/keys/dist.json',
    } as NodeJS.ProcessEnv);
    expect(explicit.distributionKeypairPath).toBe('/keys/dist.json');
  });

  it('parses comma-separated mint lists', () => {
    const cfg = loadConfig({ QUOTE_MINTS: ` ${MINT}, ${MINT} `, HOLDER_EXCLUDE: '' } as NodeJS.ProcessEnv);
    expect(cfg.QUOTE_MINTS).toEqual([MINT, MINT]);
    expect(cfg.HOLDER_EXCLUDE).toEqual([]);
  });

  it('rejects a platform id that is not base58', () => {
    expect(() => loadConfig({ LAUNCHPAD_PLATFORM_ID: 'not-a-key' } as NodeJS.ProcessEnv)).toThrow(/base58/);
  });

  it('rejects an out-of-range buyback share and an unknown mode', () => {
    expect(() => loadConfig({ BUYBACK_SHARE_BPS: '20000' } as NodeJS.ProcessEnv)).toThrow(/BUYBACK_SHARE_BPS/);
    expect(() => loadConfig({ BUYBACK_MODE: 'sell' } as NodeJS.ProcessEnv)).toThrow(/BUYBACK_MODE/);
  });

  it('rejects a non-numeric dust threshold', () => {
    expect(() => loadConfig({ DISTRIBUTION_DUST_UNITS: 'lots' } as NodeJS.ProcessEnv)).toThrow(/DISTRIBUTION_DUST_UNITS/);
  });

  it('resolves the state dir against the working directory', () => {
    const cfg = loadConfig({ STATE_DIR: './state' } as NodeJS.ProcessEnv, '/srv/keeper');
    expect(cfg.stateDir.replace(/\\/g, '/')).toMatch(/\/srv\/keeper\/state$/);
  });
});

describe('parseJobArgs', () => {
  it('defaults to a dry run', () => {
    expect(parseJobArgs([])).toMatchObject({ execute: false, vaultOnly: false });
    expect(parseJobArgs(['--dry-run']).execute).toBe(false);
  });

  it('reads --execute and the scoping flags', () => {
    const args = parseJobArgs(['--execute', '--mint', MINT, '--quote-mint', MINT, '--vault-only']);
    expect(args).toMatchObject({ execute: true, mint: MINT, quoteMint: MINT, vaultOnly: true });
  });

  it('refuses contradictory flags', () => {
    expect(() => parseJobArgs(['--execute', '--dry-run'])).toThrow(/mutually exclusive/);
  });

  it('refuses an unknown flag rather than ignoring it', () => {
    expect(() => parseJobArgs(['--yolo'])).toThrow();
  });
});
