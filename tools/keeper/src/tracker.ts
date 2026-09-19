// Read-only tracker client: the launch list (which mints we are responsible for) and the
// revenue summary (how much new revenue the buyback may spend).

import type { KeeperConfig } from './env.js';
import type { FetchLike } from './prices.js';
import { log } from './log.js';

export interface TrackerLaunch {
  mint: string;
  pool_id?: string;
  creator_wallet: string;
  quote_mint: string;
  name: string;
  symbol: string;
  transfer_fee_bps: number;
  platform_id?: string;
  status: string;
  created_at: string;
}

interface Paginated<T> {
  data: T[] | null;
  meta?: { total: number; limit: number; offset: number };
}

/**
 * The tracker emits every launch field twice: the stored snake_case name and a camelCase
 * duplicate for the portal. Read whichever is present so a future drop of either form is harmless.
 */
export function normaliseLaunch(raw: Record<string, unknown>): TrackerLaunch {
  const pick = <T>(snake: string, camel: string): T | undefined =>
    (raw[snake] !== undefined && raw[snake] !== null ? raw[snake] : raw[camel]) as T | undefined;
  const out: TrackerLaunch = {
    mint: String(pick<string>('mint', 'mint') ?? ''),
    creator_wallet: String(pick<string>('creator_wallet', 'creatorWallet') ?? ''),
    quote_mint: String(pick<string>('quote_mint', 'quoteMint') ?? ''),
    name: String(pick<string>('name', 'name') ?? ''),
    symbol: String(pick<string>('symbol', 'symbol') ?? ''),
    transfer_fee_bps: Number(pick<number>('transfer_fee_bps', 'transferFeeBps') ?? 0),
    status: String(pick<string>('status', 'status') ?? ''),
    created_at: String(pick<string>('created_at', 'createdAt') ?? ''),
  };
  const poolId = pick<string>('pool_id', 'poolId');
  if (poolId) out.pool_id = String(poolId);
  const platformId = pick<string>('platform_id', 'platformId');
  if (platformId) out.platform_id = String(platformId);
  return out;
}

export interface RevenueKindTotal {
  kind: string;
  count: number;
  amount_raw: number;
  amount_usd: number;
}

export interface RevenueSummary {
  totals: Record<string, RevenueKindTotal>;
  counts: Record<string, number>;
  total_entries: number;
  generated_at: string;
}

async function getJson<T>(cfg: KeeperConfig, pathAndQuery: string, fetchImpl: FetchLike): Promise<T> {
  const res = await fetchImpl(`${cfg.TRACKER_URL}${pathAndQuery}`, {
    headers: { accept: 'application/json' },
    signal: AbortSignal.timeout(cfg.TRACKER_TIMEOUT_MS),
  });
  const body = await res.text();
  if (!res.ok) throw new Error(`GET ${pathAndQuery} failed (HTTP ${res.status}): ${body.slice(0, 300)}`);
  return JSON.parse(body) as T;
}

/** Every launch recorded on our platform, following the tracker's offset pagination to the end. */
export async function fetchLaunches(cfg: KeeperConfig, fetchImpl: FetchLike = fetch): Promise<TrackerLaunch[]> {
  const out: TrackerLaunch[] = [];
  const limit = cfg.TRACKER_PAGE_SIZE;
  let offset = 0;
  let seen = 0;
  for (let page = 0; page < 1_000; page++) {
    const res = await getJson<Paginated<Record<string, unknown>>>(cfg, `/api/launches?limit=${limit}&offset=${offset}`, fetchImpl);
    const page = res.data ?? [];
    out.push(...page.map(normaliseLaunch).filter((l) => l.mint && l.quote_mint));
    seen += page.length;
    const total = res.meta?.total ?? seen;
    offset += limit;
    if (page.length === 0 || seen >= total) break;
  }
  log.debug('fetched launches', { count: out.length });
  return out;
}

export async function fetchRevenueSummary(cfg: KeeperConfig, fetchImpl: FetchLike = fetch): Promise<RevenueSummary> {
  const res = await getJson<{ data: RevenueSummary }>(cfg, '/api/revenue', fetchImpl);
  return res.data;
}

/** Distinct quote mints across recorded launches, plus anything pinned in QUOTE_MINTS. */
export function quoteMintsFrom(launches: TrackerLaunch[], extra: string[]): string[] {
  const set = new Set<string>();
  for (const l of launches) if (l.quote_mint) set.add(l.quote_mint);
  for (const m of extra) set.add(m);
  return Array.from(set).sort();
}

/** Launches on our platform config only (a launch recorded before platform_id was stored is kept). */
export function launchesOnPlatform(launches: TrackerLaunch[], platformId: string): TrackerLaunch[] {
  return launches.filter((l) => !l.platform_id || l.platform_id === platformId);
}
