// Solana + Raydium plumbing: connection, program ids, keypair loading, SDK init, explorer links.

import fs from 'node:fs';
import path from 'node:path';
import { Connection, Keypair, PublicKey, ComputeBudgetProgram, TransactionInstruction } from '@solana/web3.js';
import { DEV_API_URLS, DEVNET_PROGRAM_ID, LAUNCHPAD_PROGRAM, Raydium, TxVersion } from '@raydium-io/raydium-sdk-v2';
import type { KeeperConfig } from './env.js';

export const TX_VERSION = TxVersion.V0;

export function launchpadProgramId(cfg: KeeperConfig): PublicKey {
  if (cfg.LAUNCHPAD_PROGRAM_ID) return new PublicKey(cfg.LAUNCHPAD_PROGRAM_ID);
  return cfg.CLUSTER === 'mainnet' ? LAUNCHPAD_PROGRAM : DEVNET_PROGRAM_ID.LAUNCHPAD_PROGRAM;
}

export function makeConnection(cfg: KeeperConfig): Connection {
  return new Connection(cfg.rpcUrl, { commitment: 'confirmed' });
}

/**
 * Loads a solana-keygen JSON keypair from disk. Paths only: a secret key never travels
 * through the environment, so it cannot land in a process listing or a container inspect.
 */
export function loadKeypairFile(filePath: string, label: string): Keypair {
  const resolved = path.resolve(filePath);
  if (!filePath.trim()) throw new Error(`${label} is not set`);
  if (!fs.existsSync(resolved)) throw new Error(`${label} points at ${resolved}, which does not exist`);
  let parsed: unknown;
  try {
    parsed = JSON.parse(fs.readFileSync(resolved, 'utf8'));
  } catch (e) {
    throw new Error(`${label} at ${resolved} is not valid JSON: ${(e as Error).message}`);
  }
  if (!Array.isArray(parsed) || parsed.length !== 64) {
    throw new Error(`${label} at ${resolved} must be a 64-byte solana-keygen JSON array`);
  }
  return Keypair.fromSecretKey(Uint8Array.from(parsed as number[]));
}

/** Raydium SDK bound to `owner`. The owner signs and, for claims, receives into its ATA. */
export async function initSdk(cfg: KeeperConfig, connection: Connection, owner: Keypair | PublicKey): Promise<Raydium> {
  return Raydium.load({
    owner,
    connection,
    cluster: cfg.CLUSTER,
    disableFeatureCheck: true,
    disableLoadToken: true,
    blockhashCommitment: 'finalized',
    ...(cfg.CLUSTER === 'devnet'
      ? {
          urlConfigs: {
            ...DEV_API_URLS,
            BASE_HOST: 'https://api-v3-devnet.raydium.io',
            OWNER_BASE_HOST: 'https://owner-v1-devnet.raydium.io',
            SWAP_HOST: 'https://transaction-v1-devnet.raydium.io',
            CPMM_LOCK: 'https://dynamic-ipfs-devnet.raydium.io/lock/cpmm/position',
          },
        }
      : {}),
  });
}

export interface ComputeBudget {
  units: number;
  microLamports: number;
}

export function computeBudget(cfg: KeeperConfig): ComputeBudget {
  return { units: cfg.COMPUTE_UNIT_LIMIT, microLamports: cfg.COMPUTE_UNIT_PRICE_MICROLAMPORTS };
}

/** Explicit compute-budget instructions for the transactions we build ourselves. */
export function computeBudgetIxs(cfg: KeeperConfig): TransactionInstruction[] {
  const ixs: TransactionInstruction[] = [ComputeBudgetProgram.setComputeUnitLimit({ units: cfg.COMPUTE_UNIT_LIMIT })];
  if (cfg.COMPUTE_UNIT_PRICE_MICROLAMPORTS > 0) {
    ixs.push(ComputeBudgetProgram.setComputeUnitPrice({ microLamports: cfg.COMPUTE_UNIT_PRICE_MICROLAMPORTS }));
  }
  return ixs;
}

export function explorerTx(cfg: KeeperConfig, signature: string): string {
  return `https://solscan.io/tx/${signature}${cfg.CLUSTER === 'devnet' ? '?cluster=devnet' : ''}`;
}

export function explorerAccount(cfg: KeeperConfig, address: PublicKey | string): string {
  const b58 = typeof address === 'string' ? address : address.toBase58();
  return `https://solscan.io/account/${b58}${cfg.CLUSTER === 'devnet' ? '?cluster=devnet' : ''}`;
}
