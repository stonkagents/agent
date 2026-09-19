import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { Decimal } from '../src/amounts.js';
import {
  advanceWatermark, computeBuybackBudget, emptyWatermark, loadWatermark, saveWatermark,
} from '../src/state.js';
import type { RevenueSummary } from '../src/tracker.js';

const SOL_USD = 200;

function summary(launchFeeRaw: number, platformFeeUsd: number): RevenueSummary {
  return {
    totals: {
      launch_fee: { kind: 'launch_fee', count: 1, amount_raw: launchFeeRaw, amount_usd: 0 },
      platform_fee_claim: { kind: 'platform_fee_claim', count: 1, amount_raw: 0, amount_usd: platformFeeUsd },
    },
    counts: {},
    total_entries: 2,
    generated_at: '2026-09-12T00:00:00Z',
  };
}

let dir: string;
beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'keeper-wm-'));
});
afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe('computeBuybackBudget', () => {
  it('takes the configured share of everything on a first run', () => {
    // 1 SOL of launch fees at $200 plus $100 of claimed platform fees = $300 gross.
    const budget = computeBuybackBudget({
      summary: summary(1_000_000_000, 100),
      watermark: emptyWatermark('devnet'),
      shareBps: 5_000,
      solUsd: SOL_USD,
    });
    expect(budget.newLaunchFeeRaw).toBe(1_000_000_000n);
    expect(budget.newLaunchFeeUsd.toFixed()).toBe('200');
    expect(budget.newPlatformFeeUsd.toFixed()).toBe('100');
    expect(budget.grossUsd.toFixed()).toBe('300');
    expect(budget.budgetUsd.toFixed()).toBe('150');
  });

  it('only counts revenue newer than the watermark', () => {
    const watermark = { ...emptyWatermark('devnet'), consumed: { launchFeeRaw: '1000000000', platformFeeClaimUsd: '100' } };
    const budget = computeBuybackBudget({ summary: summary(1_500_000_000, 140), watermark, shareBps: 5_000, solUsd: SOL_USD });
    expect(budget.newLaunchFeeRaw).toBe(500_000_000n);
    expect(budget.newPlatformFeeUsd.toFixed()).toBe('40');
    expect(budget.budgetUsd.toFixed()).toBe('70'); // (0.5 SOL * 200 + 40) / 2
  });

  it('yields nothing when the ledger has not grown', () => {
    const watermark = { ...emptyWatermark('devnet'), consumed: { launchFeeRaw: '1000000000', platformFeeClaimUsd: '100' } };
    const budget = computeBuybackBudget({ summary: summary(1_000_000_000, 100), watermark, shareBps: 5_000, solUsd: SOL_USD });
    expect(budget.budgetUsd.toFixed()).toBe('0');
  });

  it('clamps at zero when the tracker total shrank, so a restored backup cannot mint budget', () => {
    const watermark = { ...emptyWatermark('devnet'), consumed: { launchFeeRaw: '9000000000', platformFeeClaimUsd: '900' } };
    const budget = computeBuybackBudget({ summary: summary(1_000_000_000, 100), watermark, shareBps: 5_000, solUsd: SOL_USD });
    expect(budget.newLaunchFeeRaw).toBe(0n);
    expect(budget.newPlatformFeeUsd.toFixed()).toBe('0');
    expect(budget.budgetUsd.toFixed()).toBe('0');
  });

  it('honours a zero and a full share', () => {
    const zero = computeBuybackBudget({ summary: summary(1_000_000_000, 0), watermark: emptyWatermark('devnet'), shareBps: 0, solUsd: SOL_USD });
    expect(zero.budgetUsd.toFixed()).toBe('0');
    const all = computeBuybackBudget({ summary: summary(1_000_000_000, 0), watermark: emptyWatermark('devnet'), shareBps: 10_000, solUsd: SOL_USD });
    expect(all.budgetUsd.toFixed()).toBe('200');
  });

  it('rejects an out-of-range share', () => {
    expect(() =>
      computeBuybackBudget({ summary: summary(0, 0), watermark: emptyWatermark('devnet'), shareBps: 10_001, solUsd: SOL_USD }),
    ).toThrow(/shareBps/);
  });

  it('tolerates a summary with no rows yet', () => {
    const budget = computeBuybackBudget({
      summary: { totals: {}, counts: {}, total_entries: 0, generated_at: '' },
      watermark: emptyWatermark('devnet'),
      shareBps: 5_000,
      solUsd: SOL_USD,
    });
    expect(budget.budgetUsd.toFixed()).toBe('0');
  });
});

describe('advanceWatermark', () => {
  const watermark = emptyWatermark('devnet');
  const budget = computeBuybackBudget({ summary: summary(1_000_000_000, 100), watermark, shareBps: 5_000, solUsd: SOL_USD });

  it('consumes everything when the whole budget was spent', () => {
    const next = advanceWatermark({ watermark, budget, spentUsd: budget.budgetUsd });
    expect(next.consumed.launchFeeRaw).toBe('1000000000');
    expect(new Decimal(next.consumed.platformFeeClaimUsd).toFixed()).toBe('100');
  });

  it('consumes proportionally when the wallet was short', () => {
    const next = advanceWatermark({ watermark, budget, spentUsd: new Decimal(75) }); // half of $150
    expect(next.consumed.launchFeeRaw).toBe('500000000');
    expect(new Decimal(next.consumed.platformFeeClaimUsd).toFixed()).toBe('50');
  });

  it('does not move when nothing was spent', () => {
    const next = advanceWatermark({ watermark, budget, spentUsd: 0 });
    expect(next.consumed.launchFeeRaw).toBe('0');
    expect(new Decimal(next.consumed.platformFeeClaimUsd).toFixed()).toBe('0');
  });

  it('never consumes more than the budget even if the spend overshoots', () => {
    const next = advanceWatermark({ watermark, budget, spentUsd: new Decimal(10_000) });
    expect(next.consumed.launchFeeRaw).toBe('1000000000');
  });

  it('adds to what a previous run already consumed', () => {
    const prior = { ...emptyWatermark('devnet'), consumed: { launchFeeRaw: '250000000', platformFeeClaimUsd: '25' } };
    const priorBudget = computeBuybackBudget({ summary: summary(1_250_000_000, 125), watermark: prior, shareBps: 5_000, solUsd: SOL_USD });
    const next = advanceWatermark({ watermark: prior, budget: priorBudget, spentUsd: priorBudget.budgetUsd });
    expect(next.consumed.launchFeeRaw).toBe('1250000000');
    expect(new Decimal(next.consumed.platformFeeClaimUsd).toFixed()).toBe('125');
  });
});

describe('watermark persistence', () => {
  it('round-trips and refuses a cluster swap', () => {
    saveWatermark(dir, { ...emptyWatermark('mainnet'), consumed: { launchFeeRaw: '7', platformFeeClaimUsd: '1.5' } });
    expect(loadWatermark(dir, 'mainnet').consumed.launchFeeRaw).toBe('7');
    expect(() => loadWatermark(dir, 'devnet')).toThrow(/separate STATE_DIR/);
  });

  it('starts empty when no watermark file exists', () => {
    expect(loadWatermark(dir, 'devnet').consumed).toEqual({ launchFeeRaw: '0', platformFeeClaimUsd: '0' });
  });
});
