// Token-2022 transfer-tax plumbing: read the withheld fees, take a holder snapshot,
// and decide which accounts are real holders. Everything here is raw BigInt.

import { Connection, PublicKey } from '@solana/web3.js';
import {
  TOKEN_2022_PROGRAM_ID,
  getTransferFeeAmount,
  getTransferFeeConfig,
  unpackAccount,
  unpackMint,
  type Account as TokenAccount,
} from '@solana/spl-token';
import type { Holder } from './prorata.js';
import { chunkArray } from './prices.js';

/** Offset of the `mint` field in a token account: it is the first 32 bytes, extensions or not. */
const ACCOUNT_MINT_OFFSET = 0;

export interface TaxedMintInfo {
  mint: PublicKey;
  program: PublicKey;
  decimals: number;
  supplyRaw: bigint;
  transferFeeBps?: number;
  maximumFeeRaw?: bigint;
  /** Fees already swept to the mint account, withdrawable with withdrawWithheldTokensFromMint. */
  withheldOnMintRaw: bigint;
  withdrawWithheldAuthority?: PublicKey;
  transferFeeConfigAuthority?: PublicKey;
}

export async function fetchTaxedMint(connection: Connection, mint: PublicKey): Promise<TaxedMintInfo> {
  const info = await connection.getAccountInfo(mint);
  if (!info) throw new Error(`Mint ${mint.toBase58()} not found on this cluster`);
  const parsed = unpackMint(mint, info, info.owner);
  const out: TaxedMintInfo = {
    mint,
    program: info.owner,
    decimals: parsed.decimals,
    supplyRaw: parsed.supply,
    withheldOnMintRaw: 0n,
  };
  if (info.owner.equals(TOKEN_2022_PROGRAM_ID)) {
    const fee = getTransferFeeConfig(parsed);
    if (fee) {
      out.transferFeeBps = fee.newerTransferFee.transferFeeBasisPoints;
      out.maximumFeeRaw = fee.newerTransferFee.maximumFee;
      out.withheldOnMintRaw = fee.withheldAmount;
      out.withdrawWithheldAuthority = fee.withdrawWithheldAuthority ?? undefined;
      out.transferFeeConfigAuthority = fee.transferFeeConfigAuthority ?? undefined;
    }
  }
  return out;
}

export interface ScannedAccount {
  address: PublicKey;
  owner: PublicKey;
  amountRaw: bigint;
  withheldRaw: bigint;
  frozen: boolean;
  /** An owner off the ed25519 curve is a PDA: a program, not a person. */
  programOwned: boolean;
}

/**
 * Every token account of `mint`, with its balance and its withheld transfer fee.
 * One getProgramAccounts call filtered on the mint; Token-2022 accounts vary in length because of
 * extensions, so no dataSize filter is applied and anything that fails to decode is skipped.
 */
export async function scanTokenAccounts(
  connection: Connection,
  mint: PublicKey,
  programId: PublicKey = TOKEN_2022_PROGRAM_ID,
): Promise<ScannedAccount[]> {
  const raw = await connection.getProgramAccounts(programId, {
    commitment: 'confirmed',
    filters: [{ memcmp: { offset: ACCOUNT_MINT_OFFSET, bytes: mint.toBase58() } }],
  });
  const out: ScannedAccount[] = [];
  for (const { pubkey, account } of raw) {
    let parsed: TokenAccount;
    try {
      parsed = unpackAccount(pubkey, { ...account, owner: programId }, programId);
    } catch {
      continue; // Not a token account (a mint or an extension-only account matched the memcmp).
    }
    if (!parsed.mint.equals(mint)) continue;
    out.push({
      address: pubkey,
      owner: parsed.owner,
      amountRaw: parsed.amount,
      withheldRaw: getTransferFeeAmount(parsed)?.withheldAmount ?? 0n,
      frozen: parsed.isFrozen,
      programOwned: !PublicKey.isOnCurve(parsed.owner.toBytes()),
    });
  }
  out.sort((a, b) => (a.address.toBase58() < b.address.toBase58() ? -1 : 1));
  return out;
}

export interface SnapshotOptions {
  /** Pool vault, treasury and any operator-configured address, as base58 owners or accounts. */
  exclude: Iterable<string>;
  /** The taxed mint itself, never a recipient. */
  mint: PublicKey;
  /** Keep accounts owned by a PDA. Off by default: program vaults are not holders. */
  includeProgramOwned?: boolean;
}

export interface SnapshotResult {
  holders: Holder[];
  /** Accounts left out, with the reason, so the plan table can show them. */
  skipped: { account: string; owner: string; amountRaw: bigint; reason: string }[];
}

/**
 * Turns scanned accounts into payable holders. Balances are summed per owner: one wallet with
 * three token accounts is one recipient, paid once, weighted by its whole position.
 */
export function buildHolderSnapshot(accounts: ScannedAccount[], opts: SnapshotOptions): SnapshotResult {
  const excludeSet = new Set(opts.exclude);
  excludeSet.add(opts.mint.toBase58());
  const byOwner = new Map<string, Holder>();
  const skipped: SnapshotResult['skipped'] = [];

  for (const acc of accounts) {
    const address = acc.address.toBase58();
    const owner = acc.owner.toBase58();
    if (acc.amountRaw <= 0n) continue;
    if (excludeSet.has(address) || excludeSet.has(owner)) {
      skipped.push({ account: address, owner, amountRaw: acc.amountRaw, reason: 'excluded' });
      continue;
    }
    if (acc.programOwned && !opts.includeProgramOwned) {
      skipped.push({ account: address, owner, amountRaw: acc.amountRaw, reason: 'program-owned' });
      continue;
    }
    const existing = byOwner.get(owner);
    if (existing) {
      existing.balanceRaw += acc.amountRaw;
      // Keep the lexicographically smallest account as the audit reference for a multi-account owner.
      if (address < existing.account) existing.account = address;
    } else {
      byOwner.set(owner, { owner, account: address, balanceRaw: acc.amountRaw });
    }
  }
  return { holders: Array.from(byOwner.values()), skipped };
}

/** Accounts carrying withheld tax, largest first, so a partial sweep takes the biggest first. */
export function withheldSources(accounts: ScannedAccount[]): ScannedAccount[] {
  return accounts.filter((a) => a.withheldRaw > 0n).sort((a, b) => (a.withheldRaw > b.withheldRaw ? -1 : 1));
}

export interface WithdrawBatch {
  index: number;
  sources: PublicKey[];
  withheldRaw: bigint;
}

/** Splits withheld-fee sources into transaction-sized batches. */
export function buildWithdrawBatches(sources: ScannedAccount[], batchSize: number): WithdrawBatch[] {
  return chunkArray(sources, batchSize).map((chunk, index) => ({
    index,
    sources: chunk.map((c) => c.address),
    withheldRaw: chunk.reduce((sum, c) => sum + c.withheldRaw, 0n),
  }));
}

export function sumWithheld(accounts: ScannedAccount[]): bigint {
  return accounts.reduce((sum, a) => sum + a.withheldRaw, 0n);
}
