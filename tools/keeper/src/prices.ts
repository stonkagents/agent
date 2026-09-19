// USD prices from Jupiter's public price API (no key). Used for plan tables, thresholds and the
// amount_usd column of the revenue ledger. Prices never size a raw amount except through usdToRaw.

import type { KeeperConfig } from './env.js';

export type FetchLike = typeof fetch;

interface JupPriceEntry {
  usdPrice?: number;
  decimals?: number;
}

export interface PriceBook {
  /** USD per whole token, or undefined when Jupiter has no price for that mint. */
  get(mint: string): number | undefined;
  /** Same, but throws — use where a missing price would silently mis-size a decision. */
  require(mint: string): number;
  readonly missing: string[];
}

/**
 * Fetches USD prices for `mints`. A mint Jupiter does not know (a token that just launched,
 * a devnet mint) comes back missing rather than throwing, so a dry-run plan still renders.
 */
export async function fetchPrices(
  cfg: KeeperConfig,
  mints: string[],
  fetchImpl: FetchLike = fetch,
): Promise<PriceBook> {
  const unique = Array.from(new Set(mints.filter(Boolean)));
  const prices = new Map<string, number>();
  const missing: string[] = [];

  for (const chunk of chunkArray(unique, 50)) {
    if (chunk.length === 0) continue;
    const url = `${cfg.JUPITER_PRICE_URL}?ids=${chunk.join(',')}`;
    let json: Record<string, JupPriceEntry | null> = {};
    try {
      const res = await fetchImpl(url, { headers: { accept: 'application/json' } });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      json = (await res.json()) as Record<string, JupPriceEntry | null>;
    } catch {
      // Leave the whole chunk missing; callers decide whether that blocks them.
      json = {};
    }
    for (const mint of chunk) {
      const price = json[mint]?.usdPrice;
      if (typeof price === 'number' && Number.isFinite(price) && price > 0) prices.set(mint, price);
    }
  }
  for (const mint of unique) if (!prices.has(mint)) missing.push(mint);

  return {
    get: (mint) => prices.get(mint),
    require: (mint) => {
      const p = prices.get(mint);
      if (p === undefined) throw new Error(`No USD price for ${mint}; refusing to size a decision without it`);
      return p;
    },
    missing,
  };
}

export function chunkArray<T>(items: T[], size: number): T[][] {
  if (size < 1) throw new Error('chunk size must be >= 1');
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}
