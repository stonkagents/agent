// On-disk idempotency. state/<mint>.json is the distribution ledger for one token;
// state/buyback-watermark.json records how much revenue a buyback has already spent against.
// Writes are atomic (temp file + rename) so a killed process never leaves a half-written ledger.

import fs from 'node:fs';
import path from 'node:path';
import { Decimal } from './amounts.js';
import type { RevenueSummary } from './tracker.js';

export const LEDGER_VERSION = 1 as const;

export interface RecipientRecord {
  owner: string;
  account: string;
  amountRaw: string;
}

export type BatchStatus = 'pending' | 'confirmed' | 'failed';

export interface BatchRecord {
  index: number;
  status: BatchStatus;
  signature?: string;
  recipients: RecipientRecord[];
  totalRaw: string;
  sentAt?: string;
  confirmedAt?: string;
  error?: string;
}

export interface DistributionRun {
  id: string;
  seq: number;
  createdAt: string;
  quoteMint: string;
  quoteDecimals: number;
  distributableRaw: string;
  totalEligibleRaw: string;
  amountPerUnit: string;
  status: 'open' | 'complete';
  batches: BatchRecord[];
}

export interface MintLedger {
  version: typeof LEDGER_VERSION;
  cluster: string;
  mint: string;
  /** Rounding remainder from previous runs, added to the next distributable amount. */
  carryRaw: string;
  withdrawals: { signature: string; amountRaw: string; at: string }[];
  swaps: { signature: string; inRaw: string; outRaw: string; at: string }[];
  runs: DistributionRun[];
}

export function emptyLedger(mint: string, cluster: string): MintLedger {
  return { version: LEDGER_VERSION, cluster, mint, carryRaw: '0', withdrawals: [], swaps: [], runs: [] };
}

export function ledgerPath(stateDir: string, mint: string): string {
  return path.join(stateDir, `${mint}.json`);
}

export function readJsonFile<T>(file: string): T | undefined {
  if (!fs.existsSync(file)) return undefined;
  const text = fs.readFileSync(file, 'utf8').trim();
  if (!text) return undefined;
  return JSON.parse(text) as T;
}

/** Atomic write: same-directory temp file then rename, so readers see whole documents only. */
export function writeJsonFile(file: string, value: unknown): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const tmp = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(value, null, 2) + '\n', 'utf8');
  fs.renameSync(tmp, file);
}

export function loadLedger(stateDir: string, mint: string, cluster: string): MintLedger {
  const existing = readJsonFile<MintLedger>(ledgerPath(stateDir, mint));
  if (!existing) return emptyLedger(mint, cluster);
  if (existing.version !== LEDGER_VERSION) {
    throw new Error(`Ledger ${ledgerPath(stateDir, mint)} has version ${existing.version}, expected ${LEDGER_VERSION}`);
  }
  if (existing.cluster !== cluster) {
    throw new Error(
      `Ledger ${ledgerPath(stateDir, mint)} was written on ${existing.cluster} but CLUSTER is ${cluster}; ` +
        'use a separate STATE_DIR per cluster',
    );
  }
  return existing;
}

export function saveLedger(stateDir: string, ledger: MintLedger): void {
  writeJsonFile(ledgerPath(stateDir, ledger.mint), ledger);
}

/** The run a rerun must finish before starting a new one, if any. */
export function openRun(ledger: MintLedger): DistributionRun | undefined {
  return ledger.runs.find((r) => r.status === 'open');
}

export function nextRunSeq(ledger: MintLedger): number {
  return ledger.runs.reduce((max, r) => Math.max(max, r.seq), 0) + 1;
}

export interface NewRunInput {
  quoteMint: string;
  quoteDecimals: number;
  distributableRaw: bigint;
  totalEligibleRaw: bigint;
  amountPerUnit: string;
  batches: { index: number; recipients: { owner: string; account: string; amountRaw: bigint }[]; totalRaw: bigint }[];
  now?: Date;
}

/** Appends a new open run. Refuses while another run is open: that run must be resumed first. */
export function startRun(ledger: MintLedger, input: NewRunInput): DistributionRun {
  const existing = openRun(ledger);
  if (existing) throw new Error(`Run ${existing.id} is still open; resume it before starting another`);
  const seq = nextRunSeq(ledger);
  const run: DistributionRun = {
    id: `${ledger.mint}:${seq}`,
    seq,
    createdAt: (input.now ?? new Date()).toISOString(),
    quoteMint: input.quoteMint,
    quoteDecimals: input.quoteDecimals,
    distributableRaw: input.distributableRaw.toString(),
    totalEligibleRaw: input.totalEligibleRaw.toString(),
    amountPerUnit: input.amountPerUnit,
    status: 'open',
    batches: input.batches.map((b) => ({
      index: b.index,
      status: 'pending' as BatchStatus,
      recipients: b.recipients.map((r) => ({ owner: r.owner, account: r.account, amountRaw: r.amountRaw.toString() })),
      totalRaw: b.totalRaw.toString(),
    })),
  };
  ledger.runs.push(run);
  return run;
}

/** Batches that still need work. A confirmed batch is never rebuilt, which is the no-double-pay rule. */
export function unfinishedBatches(run: DistributionRun): BatchRecord[] {
  return run.batches.filter((b) => b.status !== 'confirmed');
}

export function markSent(run: DistributionRun, index: number, signature: string, now = new Date()): void {
  const batch = requireBatch(run, index);
  if (batch.status === 'confirmed') throw new Error(`Batch ${run.id}#${index} is already confirmed; refusing to resend`);
  batch.status = 'pending';
  batch.signature = signature;
  batch.sentAt = now.toISOString();
  delete batch.error;
}

export function markConfirmed(run: DistributionRun, index: number, signature: string, now = new Date()): void {
  const batch = requireBatch(run, index);
  batch.status = 'confirmed';
  batch.signature = signature;
  batch.confirmedAt = now.toISOString();
  delete batch.error;
  if (run.batches.every((b) => b.status === 'confirmed')) run.status = 'complete';
}

export function markFailed(run: DistributionRun, index: number, error: string): void {
  const batch = requireBatch(run, index);
  if (batch.status === 'confirmed') return;
  batch.status = 'failed';
  batch.error = error;
}

function requireBatch(run: DistributionRun, index: number): BatchRecord {
  const batch = run.batches.find((b) => b.index === index);
  if (!batch) throw new Error(`Run ${run.id} has no batch ${index}`);
  return batch;
}

export function totalConfirmedRaw(run: DistributionRun): bigint {
  return run.batches.filter((b) => b.status === 'confirmed').reduce((sum, b) => sum + BigInt(b.totalRaw), 0n);
}

// ------------------------------------------------------------------ watermark ----

export interface BuybackWatermark {
  version: typeof LEDGER_VERSION;
  cluster: string;
  lastRunAt?: string;
  /** Cumulative tracker figures already counted toward a buyback. */
  consumed: { launchFeeRaw: string; platformFeeClaimUsd: string };
}

export function watermarkPath(stateDir: string): string {
  return path.join(stateDir, 'buyback-watermark.json');
}

export function emptyWatermark(cluster: string): BuybackWatermark {
  return { version: LEDGER_VERSION, cluster, consumed: { launchFeeRaw: '0', platformFeeClaimUsd: '0' } };
}

export function loadWatermark(stateDir: string, cluster: string): BuybackWatermark {
  const existing = readJsonFile<BuybackWatermark>(watermarkPath(stateDir));
  if (!existing) return emptyWatermark(cluster);
  if (existing.cluster !== cluster) {
    throw new Error(`Watermark was written on ${existing.cluster} but CLUSTER is ${cluster}; use a separate STATE_DIR`);
  }
  return existing;
}

export function saveWatermark(stateDir: string, wm: BuybackWatermark): void {
  writeJsonFile(watermarkPath(stateDir), wm);
}

export interface BuybackBudget {
  /** New launch-fee lamports since the watermark. */
  newLaunchFeeRaw: bigint;
  newLaunchFeeUsd: Decimal;
  /** New claimed platform fees in USD since the watermark. */
  newPlatformFeeUsd: Decimal;
  grossUsd: Decimal;
  budgetUsd: Decimal;
  next: BuybackWatermark;
}

/**
 * New revenue since the last buyback, as a delta of the tracker's cumulative totals.
 * Deltas are clamped at zero so a ledger that shrank (a restored backup) can never mint budget.
 * Launch fees are lamports and are priced with solUsd here rather than trusting the tracker's
 * pricing hook, which may not have filled amount_usd yet.
 */
export function computeBuybackBudget(args: {
  summary: RevenueSummary;
  watermark: BuybackWatermark;
  shareBps: number;
  solUsd: number;
  now?: Date;
}): BuybackBudget {
  const { summary, watermark, shareBps, solUsd } = args;
  if (!Number.isInteger(shareBps) || shareBps < 0 || shareBps > 10_000) {
    throw new Error(`shareBps must be an integer in [0, 10000], got ${shareBps}`);
  }
  const launchTotalRaw = toBigIntFloor(summary.totals?.launch_fee?.amount_raw ?? 0);
  const claimTotalUsd = new Decimal(summary.totals?.platform_fee_claim?.amount_usd ?? 0);

  const consumedLaunch = BigInt(watermark.consumed.launchFeeRaw || '0');
  const consumedClaimUsd = new Decimal(watermark.consumed.platformFeeClaimUsd || '0');

  const newLaunchFeeRaw = launchTotalRaw > consumedLaunch ? launchTotalRaw - consumedLaunch : 0n;
  const newPlatformFeeUsd = Decimal.max(claimTotalUsd.minus(consumedClaimUsd), new Decimal(0));
  const newLaunchFeeUsd = new Decimal(newLaunchFeeRaw.toString()).div(1e9).mul(new Decimal(solUsd));

  const grossUsd = newLaunchFeeUsd.plus(newPlatformFeeUsd);
  const budgetUsd = grossUsd.mul(shareBps).div(10_000);

  return {
    newLaunchFeeRaw,
    newLaunchFeeUsd,
    newPlatformFeeUsd,
    grossUsd,
    budgetUsd,
    next: {
      version: LEDGER_VERSION,
      cluster: watermark.cluster,
      lastRunAt: (args.now ?? new Date()).toISOString(),
      consumed: { launchFeeRaw: launchTotalRaw.toString(), platformFeeClaimUsd: claimTotalUsd.toFixed() },
    },
  };
}

/**
 * Moves the watermark forward by the fraction of the budget actually spent. A buyback that could
 * only spend half its budget (the wallet was short) leaves the other half of the revenue eligible
 * for the next run instead of writing it off.
 */
export function advanceWatermark(args: {
  watermark: BuybackWatermark;
  budget: BuybackBudget;
  spentUsd: Decimal | number;
  now?: Date;
}): BuybackWatermark {
  const { watermark, budget } = args;
  const spent = new Decimal(args.spentUsd.toString());
  const fraction = budget.budgetUsd.lte(0)
    ? new Decimal(0)
    : Decimal.min(Decimal.max(spent.div(budget.budgetUsd), new Decimal(0)), new Decimal(1));

  const consumedLaunch = BigInt(watermark.consumed.launchFeeRaw || '0');
  const consumedClaimUsd = new Decimal(watermark.consumed.platformFeeClaimUsd || '0');
  const launchStep = BigInt(
    new Decimal(budget.newLaunchFeeRaw.toString()).mul(fraction).toDecimalPlaces(0, Decimal.ROUND_DOWN).toFixed(0),
  );
  const claimStep = budget.newPlatformFeeUsd.mul(fraction);

  return {
    version: LEDGER_VERSION,
    cluster: watermark.cluster,
    lastRunAt: (args.now ?? new Date()).toISOString(),
    consumed: {
      launchFeeRaw: (consumedLaunch + launchStep).toString(),
      platformFeeClaimUsd: consumedClaimUsd.plus(claimStep).toFixed(),
    },
  };
}

function toBigIntFloor(value: number): bigint {
  return BigInt(new Decimal(value).toDecimalPlaces(0, Decimal.ROUND_DOWN).toFixed(0));
}
