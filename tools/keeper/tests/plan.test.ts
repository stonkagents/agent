import { describe, expect, it } from 'vitest';
import { renderPlan, renderTable, shortAddr } from '../src/plan.js';
import { applyBps, fmtUnits, fmtUsd, rawToUsd, unitsToRaw, usdToRaw, Decimal } from '../src/amounts.js';

describe('renderTable', () => {
  const columns = [
    { key: 'mint', label: 'MINT' },
    { key: 'amount', label: 'AMOUNT', align: 'right' as const },
  ];

  it('pads columns to the widest cell and aligns numbers right', () => {
    const out = renderTable(columns, [
      { mint: 'abc', amount: '1' },
      { mint: 'abcdefgh', amount: '1000' },
    ]);
    const lines = out.split('\n');
    expect(lines[0]).toBe('MINT      AMOUNT');
    expect(lines[1]).toBe('--------  ------');
    expect(lines[2]).toBe('abc            1');
    expect(lines[3]).toBe('abcdefgh    1000');
  });

  it('renders a header even with no rows', () => {
    expect(renderTable(columns, []).split('\n')).toHaveLength(2);
  });

  it('renders a missing cell as blank rather than undefined', () => {
    expect(renderTable(columns, [{ mint: 'abc' }])).not.toContain('undefined');
  });
});

describe('renderPlan', () => {
  const base = {
    title: 'claim-platform-fees',
    columns: [{ key: 'quote', label: 'QUOTE' }],
    rows: [{ quote: 'STONK' }],
  };

  it('says clearly that a dry run sends nothing', () => {
    const out = renderPlan({ ...base, mode: 'dry-run' });
    expect(out).toContain('DRY RUN — nothing is sent');
    expect(out).toContain('claim-platform-fees');
    expect(out).toContain('STONK');
  });

  it('says clearly that execute sends transactions', () => {
    expect(renderPlan({ ...base, mode: 'execute' })).toContain('EXECUTE — transactions will be sent');
  });

  it('shows the empty message instead of a table when there is nothing to do', () => {
    const out = renderPlan({ ...base, rows: [], mode: 'dry-run', emptyMessage: 'Nothing accrued.' });
    expect(out).toContain('Nothing accrued.');
    expect(out).not.toContain('QUOTE');
  });

  it('aligns fact labels and prefixes notes', () => {
    const out = renderPlan({
      ...base,
      mode: 'dry-run',
      facts: [['cluster', 'devnet'], ['fee wallet', 'Fee111']],
      notes: ['Below threshold.'],
    });
    expect(out).toContain('cluster    : devnet');
    expect(out).toContain('fee wallet : Fee111');
    expect(out).toContain('! Below threshold.');
  });
});

describe('shortAddr', () => {
  it('keeps short strings intact and elides long ones', () => {
    expect(shortAddr('abc')).toBe('abc');
    expect(shortAddr('So11111111111111111111111111111111111111112')).toBe('So1111..111112');
  });
});

describe('amount formatting', () => {
  it('renders raw units as whole tokens without float error', () => {
    expect(fmtUnits(1_234_567_890n, 9)).toBe('1.234567');
    expect(fmtUnits(0n, 6)).toBe('0');
    expect(fmtUnits(10n ** 24n, 9)).toBe('1000000000000000');
  });

  it('formats USD to two places and marks unknown prices', () => {
    expect(fmtUsd(new Decimal('12.345'))).toBe('$12.35');
    expect(fmtUsd(undefined)).toBe('-');
  });

  it('converts between raw units and USD without floats', () => {
    expect(rawToUsd(1_000_000_000n, 9, 200).toFixed()).toBe('200');
    expect(usdToRaw(new Decimal(100), 9, 200)).toBe(500_000_000n);
  });

  it('truncates rather than rounds up when converting to raw', () => {
    expect(unitsToRaw('1.9999999', 6)).toBe(1_999_999n);
  });

  it('applies basis points by flooring', () => {
    expect(applyBps(1_001n, 5_000)).toBe(500n);
    expect(applyBps(1_000n, 10_000)).toBe(1_000n);
    expect(() => applyBps(1n, 10_001)).toThrow(/bps/);
  });
});
