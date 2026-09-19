// Raw-unit arithmetic. Raw token amounts are BigInt end to end; Decimal is used only to render
// human units and USD. No float ever touches a raw amount.

import Decimal from 'decimal.js';

Decimal.set({ precision: 40, toExpNeg: -30, toExpPos: 40 });

export function pow10(decimals: number): bigint {
  if (!Number.isInteger(decimals) || decimals < 0 || decimals > 24) {
    throw new Error(`decimals must be an integer in [0, 24], got ${decimals}`);
  }
  return 10n ** BigInt(decimals);
}

/** Raw units -> whole-token Decimal. Exact: no float division. */
export function rawToUnits(raw: bigint | string | number, decimals: number): Decimal {
  return new Decimal(raw.toString()).div(new Decimal(pow10(decimals).toString()));
}

/** Whole-token amount -> raw units, truncating extra precision (never rounds a payout up). */
export function unitsToRaw(units: string | number | Decimal, decimals: number): bigint {
  const d = new Decimal(units.toString());
  if (d.isNegative()) throw new Error(`amount must be >= 0, got ${units}`);
  return BigInt(d.mul(new Decimal(pow10(decimals).toString())).toDecimalPlaces(0, Decimal.ROUND_DOWN).toFixed(0));
}

/** USD value of a raw amount at a USD-per-whole-token price. Display only. */
export function rawToUsd(raw: bigint, decimals: number, usdPerUnit: number): Decimal {
  return rawToUnits(raw, decimals).mul(new Decimal(usdPerUnit));
}

/** Raw amount worth `usd` at `usdPerUnit`, truncated down. Used to size a buyback budget. */
export function usdToRaw(usd: Decimal | number, decimals: number, usdPerUnit: number): bigint {
  if (!(usdPerUnit > 0)) throw new Error(`usdPerUnit must be positive, got ${usdPerUnit}`);
  return unitsToRaw(new Decimal(usd.toString()).div(new Decimal(usdPerUnit)), decimals);
}

/** Fixed-width token amount for plan tables: trims trailing zeroes, keeps at most 6 decimals. */
export function fmtUnits(raw: bigint, decimals: number): string {
  return rawToUnits(raw, decimals).toDecimalPlaces(6, Decimal.ROUND_DOWN).toFixed();
}

export function fmtUsd(usd: Decimal | number | undefined): string {
  if (usd === undefined) return '-';
  return '$' + new Decimal(usd.toString()).toDecimalPlaces(2, Decimal.ROUND_HALF_UP).toFixed(2);
}

/** bps of a bigint, floored. 5000 bps = 50%. */
export function applyBps(value: bigint, bps: number): bigint {
  if (!Number.isInteger(bps) || bps < 0 || bps > 10_000) throw new Error(`bps must be an integer in [0, 10000], got ${bps}`);
  return (value * BigInt(bps)) / 10_000n;
}

export function maxBigInt(a: bigint, b: bigint): bigint {
  return a > b ? a : b;
}

export function minBigInt(a: bigint, b: bigint): bigint {
  return a < b ? a : b;
}

export { Decimal };
