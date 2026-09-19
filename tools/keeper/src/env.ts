// Environment contract for the keeper. Every key is declared here with a Devnet default,
// validated by zod, and documented in .env.example. Nothing reads process.env elsewhere.

import 'dotenv/config';
import path from 'node:path';
import { z } from 'zod';
import { PublicKey } from '@solana/web3.js';

const base58 = z
  .string()
  .trim()
  .refine((v) => {
    try {
      return new PublicKey(v).toBase58().length > 0;
    } catch {
      return false;
    }
  }, 'must be a base58 32-byte public key');

const optionalBase58 = z.union([base58, z.literal('')]).transform((v) => (v === '' ? undefined : v));

const intFrom = (min: number, max: number) =>
  z.coerce.number().int().min(min).max(max);

const decimalString = z
  .string()
  .trim()
  .refine((v) => /^\d+(\.\d+)?$/.test(v), 'must be a non-negative decimal number');

const csv = z
  .string()
  .default('')
  .transform((v) =>
    v
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s.length > 0),
  );

export const KeeperEnvSchema = z.object({
  CLUSTER: z.enum(['devnet', 'mainnet']).default('devnet'),
  RPC_URL: z.string().url().optional(),
  LAUNCHPAD_PROGRAM_ID: optionalBase58.optional(),

  LAUNCHPAD_PLATFORM_ID: optionalBase58.optional(),
  PLATFORM_TOKEN_MINT: optionalBase58.optional(),
  QUOTE_MINTS: csv,

  TRACKER_URL: z.string().url().default('http://localhost:8080'),
  INTERNAL_API_TOKEN: z.string().default(''),
  TRACKER_TIMEOUT_MS: intFrom(1_000, 120_000).default(15_000),
  TRACKER_PAGE_SIZE: intFrom(1, 100).default(100),

  FEE_WALLET_KEYPAIR_PATH: z.string().default(''),
  TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH: z.string().default(''),
  DISTRIBUTION_WALLET_KEYPAIR_PATH: z.string().default(''),
  BUYBACK_WALLET_KEYPAIR_PATH: z.string().default(''),

  BUYBACK_SHARE_BPS: intFrom(0, 10_000).default(5_000),
  BUYBACK_MODE: z.enum(['burn', 'lock']).default('burn'),
  BUYBACK_MIN_USD: z.coerce.number().nonnegative().default(25),

  DISTRIBUTION_MIN_USD: z.coerce.number().nonnegative().default(25),
  DISTRIBUTION_DUST_UNITS: decimalString.default('1'),
  DISTRIBUTION_BATCH_SIZE: intFrom(1, 20).default(10),
  WITHDRAW_BATCH_SIZE: intFrom(1, 30).default(20),
  HOLDER_EXCLUDE: csv,
  CLAIM_MIN_USD: z.coerce.number().nonnegative().default(5),

  JUPITER_API_URL: z.string().url().default('https://lite-api.jup.ag/swap/v1'),
  JUPITER_PRICE_URL: z.string().url().default('https://lite-api.jup.ag/price/v3'),
  JUPITER_SLIPPAGE_BPS: intFrom(1, 5_000).default(100),

  COMPUTE_UNIT_LIMIT: intFrom(10_000, 1_400_000).default(400_000),
  COMPUTE_UNIT_PRICE_MICROLAMPORTS: intFrom(0, 100_000_000).default(50_000),

  STATE_DIR: z.string().default('./state'),
  LOG_LEVEL: z.enum(['debug', 'info', 'warn', 'error']).default('info'),
});

export type RawKeeperEnv = z.infer<typeof KeeperEnvSchema>;

export interface KeeperConfig extends RawKeeperEnv {
  /** Absolute RPC endpoint, defaulted per cluster when RPC_URL is unset. */
  rpcUrl: string;
  /** Absolute directory for idempotency ledgers, the watermark and the failed-ledger queue. */
  stateDir: string;
  /** DISTRIBUTION_WALLET_KEYPAIR_PATH, falling back to the transfer-fee authority. */
  distributionKeypairPath: string;
}

const DEFAULT_RPC: Record<'devnet' | 'mainnet', string> = {
  devnet: 'https://api.devnet.solana.com',
  mainnet: 'https://api.mainnet-beta.solana.com',
};

/**
 * Parses process.env (or an injected record, which the tests use) into a KeeperConfig.
 * Throws a single readable error listing every invalid key.
 */
export function loadConfig(source: NodeJS.ProcessEnv = process.env, cwd = process.cwd()): KeeperConfig {
  const picked: Record<string, unknown> = {};
  for (const key of Object.keys(KeeperEnvSchema.shape)) {
    const value = source[key];
    if (value !== undefined && value !== '') picked[key] = value;
    // Empty strings fall through to the zod default, except for the CSV / optional keys
    // whose schema accepts '' explicitly.
    else if (value === '' && (key === 'QUOTE_MINTS' || key === 'HOLDER_EXCLUDE')) picked[key] = '';
  }
  const parsed = KeeperEnvSchema.safeParse(picked);
  if (!parsed.success) {
    const detail = parsed.error.issues.map((i) => `${i.path.join('.') || '(root)'}: ${i.message}`).join('; ');
    throw new Error(`Invalid keeper environment — ${detail}`);
  }
  const env = parsed.data;
  return {
    ...env,
    rpcUrl: env.RPC_URL ?? DEFAULT_RPC[env.CLUSTER],
    stateDir: path.resolve(cwd, env.STATE_DIR),
    distributionKeypairPath: env.DISTRIBUTION_WALLET_KEYPAIR_PATH || env.TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH,
  };
}
