import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  emptyLedger, ledgerPath, loadLedger, markConfirmed, markFailed, markSent, openRun,
  saveLedger, startRun, totalConfirmedRaw, unfinishedBatches,
} from '../src/state.js';

const MINT = 'So11111111111111111111111111111111111111112';

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'keeper-state-'));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function seedRun(cluster = 'devnet') {
  const ledger = emptyLedger(MINT, cluster);
  const run = startRun(ledger, {
    quoteMint: 'quote',
    quoteDecimals: 6,
    distributableRaw: 300n,
    totalEligibleRaw: 1_000n,
    amountPerUnit: '0.3',
    batches: [
      { index: 0, recipients: [{ owner: 'a', account: 'a-ata', amountRaw: 200n }], totalRaw: 200n },
      { index: 1, recipients: [{ owner: 'b', account: 'b-ata', amountRaw: 100n }], totalRaw: 100n },
    ],
  });
  return { ledger, run };
}

describe('ledger persistence', () => {
  it('round-trips through disk atomically', () => {
    const { ledger } = seedRun();
    saveLedger(dir, ledger);
    expect(fs.existsSync(ledgerPath(dir, MINT))).toBe(true);
    expect(fs.readdirSync(dir).filter((f) => f.endsWith('.tmp'))).toEqual([]);
    const reloaded = loadLedger(dir, MINT, 'devnet');
    expect(reloaded.runs[0]?.batches).toHaveLength(2);
    expect(reloaded.runs[0]?.batches[0]?.recipients[0]?.amountRaw).toBe('200');
  });

  it('returns an empty ledger when the file does not exist', () => {
    const ledger = loadLedger(dir, MINT, 'devnet');
    expect(ledger.runs).toEqual([]);
    expect(ledger.carryRaw).toBe('0');
  });

  it('refuses a ledger written on a different cluster', () => {
    saveLedger(dir, seedRun('mainnet').ledger);
    expect(() => loadLedger(dir, MINT, 'devnet')).toThrow(/separate STATE_DIR/);
  });

  it('refuses a ledger from a future schema version', () => {
    const ledger = emptyLedger(MINT, 'devnet');
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(ledgerPath(dir, MINT), JSON.stringify({ ...ledger, version: 99 }));
    expect(() => loadLedger(dir, MINT, 'devnet')).toThrow(/version 99/);
  });
});

describe('run idempotency', () => {
  it('will not open a second run while one is open', () => {
    const { ledger } = seedRun();
    expect(openRun(ledger)?.seq).toBe(1);
    expect(() =>
      startRun(ledger, {
        quoteMint: 'quote', quoteDecimals: 6, distributableRaw: 1n, totalEligibleRaw: 1n,
        amountPerUnit: '1', batches: [],
      }),
    ).toThrow(/still open/);
  });

  it('opens a new run once the previous one is complete', () => {
    const { ledger, run } = seedRun();
    markConfirmed(run, 0, 'sig-0');
    markConfirmed(run, 1, 'sig-1');
    expect(run.status).toBe('complete');
    expect(openRun(ledger)).toBeUndefined();
    const next = startRun(ledger, {
      quoteMint: 'quote', quoteDecimals: 6, distributableRaw: 5n, totalEligibleRaw: 5n,
      amountPerUnit: '1', batches: [],
    });
    expect(next.seq).toBe(2);
    expect(next.id).toBe(`${MINT}:2`);
  });

  it('never resends a confirmed batch', () => {
    const { run } = seedRun();
    markConfirmed(run, 0, 'sig-0');
    expect(() => markSent(run, 0, 'sig-0-again')).toThrow(/already confirmed/);
    expect(unfinishedBatches(run).map((b) => b.index)).toEqual([1]);
  });

  it('leaves a sent-but-unconfirmed batch resumable with its signature', () => {
    const { ledger, run } = seedRun();
    markSent(run, 0, 'sig-0');
    saveLedger(dir, ledger);
    const resumed = loadLedger(dir, MINT, 'devnet');
    const batch = resumed.runs[0]?.batches[0];
    expect(batch?.status).toBe('pending');
    expect(batch?.signature).toBe('sig-0');
    expect(unfinishedBatches(resumed.runs[0]!).map((b) => b.index)).toEqual([0, 1]);
  });

  it('a failed batch is retryable and a later confirm clears the error', () => {
    const { run } = seedRun();
    markFailed(run, 0, 'blockhash expired');
    expect(run.batches[0]?.status).toBe('failed');
    markSent(run, 0, 'sig-retry');
    markConfirmed(run, 0, 'sig-retry');
    expect(run.batches[0]?.error).toBeUndefined();
    expect(run.batches[0]?.status).toBe('confirmed');
  });

  it('marking a confirmed batch failed is a no-op', () => {
    const { run } = seedRun();
    markConfirmed(run, 0, 'sig-0');
    markFailed(run, 0, 'late error');
    expect(run.batches[0]?.status).toBe('confirmed');
  });

  it('totals only what actually confirmed', () => {
    const { run } = seedRun();
    expect(totalConfirmedRaw(run)).toBe(0n);
    markConfirmed(run, 0, 'sig-0');
    expect(totalConfirmedRaw(run)).toBe(200n);
    markConfirmed(run, 1, 'sig-1');
    expect(totalConfirmedRaw(run)).toBe(300n);
  });

  it('rejects an unknown batch index', () => {
    const { run } = seedRun();
    expect(() => markConfirmed(run, 7, 'sig')).toThrow(/no batch 7/);
  });
});
