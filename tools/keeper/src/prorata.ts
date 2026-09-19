// Pro-rata distribution maths. Pure, deterministic, BigInt only: same holder set in, same
// payout list out, byte for byte. Every rounding remainder is accounted for, never dropped.

import { Decimal } from './amounts.js';

export interface Holder {
  /** Token account owner, the address we pay. */
  owner: string;
  /** The holder's token account for the taxed mint (used for the snapshot audit trail). */
  account: string;
  balanceRaw: bigint;
}

export interface Payout {
  owner: string;
  account: string;
  amountRaw: bigint;
}

export interface EligibilityOptions {
  /** Balances strictly below this are dust and are neither paid nor counted in the denominator. */
  dustRaw: bigint;
  /** Owners or token accounts excluded entirely (pool vault, treasury, program-owned accounts). */
  exclude: Iterable<string>;
}

/**
 * Deterministic order: descending balance, then ascending owner. Two runs over the same snapshot
 * therefore build byte-identical batches, which is what makes the on-disk ledger a safe resume point.
 */
export function sortHolders(holders: Holder[]): Holder[] {
  return [...holders].sort((a, b) => {
    if (a.balanceRaw !== b.balanceRaw) return a.balanceRaw > b.balanceRaw ? -1 : 1;
    if (a.owner !== b.owner) return a.owner < b.owner ? -1 : 1;
    return a.account < b.account ? -1 : a.account > b.account ? 1 : 0;
  });
}

export interface EligibilityResult {
  eligible: Holder[];
  excluded: Holder[];
  dust: Holder[];
  totalEligibleRaw: bigint;
}

/** Splits a snapshot into payable holders, explicit exclusions and dust. */
export function selectEligible(holders: Holder[], opts: EligibilityOptions): EligibilityResult {
  const excludeSet = new Set(opts.exclude);
  const eligible: Holder[] = [];
  const excluded: Holder[] = [];
  const dust: Holder[] = [];
  for (const h of sortHolders(holders)) {
    if (h.balanceRaw <= 0n) {
      dust.push(h);
      continue;
    }
    if (excludeSet.has(h.owner) || excludeSet.has(h.account)) {
      excluded.push(h);
      continue;
    }
    if (h.balanceRaw < opts.dustRaw) {
      dust.push(h);
      continue;
    }
    eligible.push(h);
  }
  const totalEligibleRaw = eligible.reduce((sum, h) => sum + h.balanceRaw, 0n);
  return { eligible, excluded, dust, totalEligibleRaw };
}

export interface ProRataResult {
  payouts: Payout[];
  /** Sum of payouts actually issued. */
  paidRaw: bigint;
  /** distributableRaw - paidRaw. Carried into the next run, never silently kept. */
  remainderRaw: bigint;
  totalEligibleRaw: bigint;
  /** Quote raw units per one raw unit of the taxed token, as a decimal string for the ledger meta. */
  amountPerUnit: string;
  /** Holders whose pro-rata share floored to zero; they keep their weight for the next run. */
  zeroShare: Holder[];
}

/**
 * payout_i = floor(distributable * balance_i / totalEligible).
 * Floor, never round: the sum of payouts can never exceed what we hold. The shortfall is the
 * remainder and is carried, so nothing is created and nothing disappears.
 */
export function computeProRata(distributableRaw: bigint, eligible: Holder[]): ProRataResult {
  if (distributableRaw < 0n) throw new Error('distributableRaw must be >= 0');
  const ordered = sortHolders(eligible);
  const totalEligibleRaw = ordered.reduce((sum, h) => sum + h.balanceRaw, 0n);
  if (totalEligibleRaw === 0n || distributableRaw === 0n) {
    return {
      payouts: [],
      paidRaw: 0n,
      remainderRaw: distributableRaw,
      totalEligibleRaw,
      amountPerUnit: '0',
      zeroShare: ordered,
    };
  }
  const payouts: Payout[] = [];
  const zeroShare: Holder[] = [];
  let paidRaw = 0n;
  for (const h of ordered) {
    const amountRaw = (distributableRaw * h.balanceRaw) / totalEligibleRaw;
    if (amountRaw <= 0n) {
      zeroShare.push(h);
      continue;
    }
    payouts.push({ owner: h.owner, account: h.account, amountRaw });
    paidRaw += amountRaw;
  }
  const amountPerUnit = new Decimal(distributableRaw.toString())
    .div(new Decimal(totalEligibleRaw.toString()))
    .toSignificantDigits(24)
    .toFixed();
  return { payouts, paidRaw, remainderRaw: distributableRaw - paidRaw, totalEligibleRaw, amountPerUnit, zeroShare };
}

export interface PayoutBatch {
  index: number;
  recipients: Payout[];
  totalRaw: bigint;
}

/** Splits payouts into transaction-sized batches, preserving the deterministic order. */
export function buildBatches(payouts: Payout[], batchSize: number): PayoutBatch[] {
  if (!Number.isInteger(batchSize) || batchSize < 1) throw new Error(`batchSize must be a positive integer, got ${batchSize}`);
  const batches: PayoutBatch[] = [];
  for (let i = 0; i < payouts.length; i += batchSize) {
    const recipients = payouts.slice(i, i + batchSize);
    batches.push({
      index: batches.length,
      recipients,
      totalRaw: recipients.reduce((sum, r) => sum + r.amountRaw, 0n),
    });
  }
  return batches;
}
