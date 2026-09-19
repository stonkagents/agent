import { describe, expect, it } from 'vitest';
import { buildBatches, computeProRata, selectEligible, sortHolders, type Holder } from '../src/prorata.js';

function holder(owner: string, balanceRaw: bigint, account = `${owner}-ata`): Holder {
  return { owner, account, balanceRaw };
}

describe('selectEligible', () => {
  it('drops dust, explicit exclusions and empty accounts', () => {
    const res = selectEligible(
      [holder('alice', 1_000n), holder('bob', 5n), holder('vault', 9_000n), holder('empty', 0n)],
      { dustRaw: 100n, exclude: ['vault'] },
    );
    expect(res.eligible.map((h) => h.owner)).toEqual(['alice']);
    expect(res.dust.map((h) => h.owner).sort()).toEqual(['bob', 'empty']);
    expect(res.excluded.map((h) => h.owner)).toEqual(['vault']);
    expect(res.totalEligibleRaw).toBe(1_000n);
  });

  it('excludes by token account address as well as by owner', () => {
    const res = selectEligible([holder('carol', 500n, 'pool-vault')], { dustRaw: 0n, exclude: ['pool-vault'] });
    expect(res.eligible).toHaveLength(0);
    expect(res.excluded).toHaveLength(1);
  });

  it('keeps a balance exactly on the dust threshold', () => {
    const res = selectEligible([holder('dave', 100n)], { dustRaw: 100n, exclude: [] });
    expect(res.eligible.map((h) => h.owner)).toEqual(['dave']);
  });
});

describe('sortHolders', () => {
  it('orders by descending balance then ascending owner, deterministically', () => {
    const input = [holder('zoe', 10n), holder('adam', 10n), holder('mia', 50n)];
    expect(sortHolders(input).map((h) => h.owner)).toEqual(['mia', 'adam', 'zoe']);
    expect(sortHolders([...input].reverse()).map((h) => h.owner)).toEqual(['mia', 'adam', 'zoe']);
  });
});

describe('computeProRata', () => {
  it('splits exactly when the division is clean', () => {
    const res = computeProRata(300n, [holder('a', 1n), holder('b', 1n), holder('c', 1n)]);
    expect(res.payouts.map((p) => p.amountRaw)).toEqual([100n, 100n, 100n]);
    expect(res.paidRaw).toBe(300n);
    expect(res.remainderRaw).toBe(0n);
  });

  it('floors every share so payouts never exceed what is held', () => {
    const res = computeProRata(100n, [holder('a', 1n), holder('b', 1n), holder('c', 1n)]);
    expect(res.paidRaw).toBe(99n);
    expect(res.remainderRaw).toBe(1n);
    expect(res.paidRaw + res.remainderRaw).toBe(100n);
  });

  it('weights by balance', () => {
    const res = computeProRata(1_000n, [holder('whale', 900n), holder('shrimp', 100n)]);
    expect(res.payouts).toEqual([
      { owner: 'whale', account: 'whale-ata', amountRaw: 900n },
      { owner: 'shrimp', account: 'shrimp-ata', amountRaw: 100n },
    ]);
    expect(res.remainderRaw).toBe(0n);
  });

  it('reports holders whose share floors to zero instead of sending empty transfers', () => {
    const res = computeProRata(10n, [holder('whale', 1_000_000n), holder('mote', 1n)]);
    expect(res.payouts.map((p) => p.owner)).toEqual(['whale']);
    expect(res.zeroShare.map((h) => h.owner)).toEqual(['mote']);
    expect(res.paidRaw).toBe(9n);
  });

  it('carries the whole amount when there are no eligible holders', () => {
    const res = computeProRata(500n, []);
    expect(res.payouts).toEqual([]);
    expect(res.remainderRaw).toBe(500n);
    expect(res.amountPerUnit).toBe('0');
  });

  it('returns nothing to pay for a zero distributable', () => {
    const res = computeProRata(0n, [holder('a', 10n)]);
    expect(res.payouts).toEqual([]);
    expect(res.remainderRaw).toBe(0n);
  });

  it('reports amountPerUnit as distributable / total eligible', () => {
    const res = computeProRata(500n, [holder('a', 750n), holder('b', 250n)]);
    expect(res.totalEligibleRaw).toBe(1_000n);
    expect(res.amountPerUnit).toBe('0.5');
  });

  it('is deterministic across input orderings', () => {
    const holders = [holder('a', 7n), holder('b', 11n), holder('c', 13n)];
    const first = computeProRata(1_000n, holders);
    const second = computeProRata(1_000n, [...holders].reverse());
    expect(second.payouts).toEqual(first.payouts);
  });

  it('handles amounts far beyond 2^53 without loss', () => {
    const huge = 10n ** 24n;
    const res = computeProRata(huge, [holder('a', 3n), holder('b', 1n)]);
    expect(res.paidRaw + res.remainderRaw).toBe(huge);
    expect(res.payouts[0]?.amountRaw).toBe((huge * 3n) / 4n);
  });

  it('rejects a negative distributable', () => {
    expect(() => computeProRata(-1n, [])).toThrow(/must be >= 0/);
  });
});

describe('buildBatches', () => {
  it('chunks payouts in order and totals each batch', () => {
    const res = computeProRata(1_000n, [holder('a', 4n), holder('b', 3n), holder('c', 2n), holder('d', 1n)]);
    const batches = buildBatches(res.payouts, 3);
    expect(batches).toHaveLength(2);
    expect(batches[0]?.recipients.map((r) => r.owner)).toEqual(['a', 'b', 'c']);
    expect(batches[1]?.recipients.map((r) => r.owner)).toEqual(['d']);
    expect(batches[0]!.totalRaw + batches[1]!.totalRaw).toBe(res.paidRaw);
    expect(batches.map((b) => b.index)).toEqual([0, 1]);
  });

  it('returns no batches for no payouts', () => {
    expect(buildBatches([], 5)).toEqual([]);
  });

  it('rejects a non-positive batch size', () => {
    expect(() => buildBatches([], 0)).toThrow(/positive integer/);
  });
});
