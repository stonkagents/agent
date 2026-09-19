// Safety checks that stand between a dry run and a real transaction.
// Every one of them fails closed: a missing key or a cluster mismatch aborts before signing.

import { Connection, Keypair, PublicKey } from '@solana/web3.js';
import { PlatformConfig } from '@raydium-io/raydium-sdk-v2';
import type { KeeperConfig } from './env.js';
import { launchpadProgramId, loadKeypairFile } from './chain.js';
import { log } from './log.js';

export type PlatformConfigInfo = ReturnType<typeof PlatformConfig.decode>;

export interface PlatformContext {
  platformId: PublicKey;
  config: PlatformConfigInfo;
  programId: PublicKey;
}

export function bytesToStr(bytes: number[]): string {
  return new TextDecoder().decode(Uint8Array.from(bytes)).replace(/\0+$/, '');
}

/**
 * Reads our platform config and proves the configured RPC really is the cluster the config lives on:
 * the account must exist AND be owned by the LaunchLab program id for CLUSTER. Pointing a mainnet
 * platform id at a devnet RPC (or the reverse) stops here rather than at a confusing on-chain error.
 */
export async function loadPlatform(cfg: KeeperConfig, connection: Connection): Promise<PlatformContext> {
  if (!cfg.LAUNCHPAD_PLATFORM_ID) {
    throw new Error('LAUNCHPAD_PLATFORM_ID is not set; every job needs our platform config id');
  }
  const programId = launchpadProgramId(cfg);
  const platformId = new PublicKey(cfg.LAUNCHPAD_PLATFORM_ID);
  const info = await connection.getAccountInfo(platformId);
  if (!info) {
    throw new Error(
      `Platform config ${platformId.toBase58()} does not exist on ${cfg.CLUSTER} (${cfg.rpcUrl}). ` +
        'CLUSTER / RPC_URL and LAUNCHPAD_PLATFORM_ID disagree.',
    );
  }
  if (!info.owner.equals(programId)) {
    throw new Error(
      `Platform config ${platformId.toBase58()} is owned by ${info.owner.toBase58()}, not the ${cfg.CLUSTER} ` +
        `LaunchLab program ${programId.toBase58()}. Refusing to act against the wrong cluster.`,
    );
  }
  const config = PlatformConfig.decode(info.data);
  log.info('platform config loaded', {
    platformId,
    name: bytesToStr(config.name),
    feeWallet: config.platformClaimFeeWallet,
    transferFeeAuthority: config.transferFeeExtensionAuth,
    feeRate: config.feeRate.toString(),
    creatorFeeRate: config.creatorFeeRate.toString(),
  });
  return { platformId, config, programId };
}

export interface KeypairRequest {
  /** Env key name, used verbatim in the error so the operator knows what to set. */
  envKey: string;
  filePath: string;
}

/**
 * Loads every requested keypair, reporting all problems at once. Called only on the --execute
 * path: a dry run must work with no keys on the box at all.
 */
export function requireKeypairs(requests: KeypairRequest[]): Map<string, Keypair> {
  const out = new Map<string, Keypair>();
  const problems: string[] = [];
  for (const req of requests) {
    if (!req.filePath.trim()) {
      problems.push(`${req.envKey} is not set`);
      continue;
    }
    try {
      out.set(req.envKey, loadKeypairFile(req.filePath, req.envKey));
    } catch (e) {
      problems.push((e as Error).message);
    }
  }
  if (problems.length) {
    throw new Error(`Refusing --execute: ${problems.join('; ')}`);
  }
  return out;
}

/** Fails when a loaded key is not the authority the on-chain config expects. */
export function assertAuthority(label: string, actual: PublicKey, expected: PublicKey): void {
  if (!actual.equals(expected)) {
    throw new Error(
      `${label} is ${actual.toBase58()} but the platform config expects ${expected.toBase58()}. ` +
        'The transaction would fail on chain; fix the keypair path or rotate the authority first.',
    );
  }
}

/** Fails when --execute is requested without a token for the tracker's internal ledger endpoint. */
export function assertLedgerToken(cfg: KeeperConfig): void {
  if (!cfg.INTERNAL_API_TOKEN) {
    throw new Error('Refusing --execute: INTERNAL_API_TOKEN is not set, so no ledger row could be written');
  }
}
