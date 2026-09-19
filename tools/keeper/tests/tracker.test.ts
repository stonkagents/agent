import { describe, expect, it, vi } from 'vitest';
import { fetchLaunches, launchesOnPlatform, normaliseLaunch, quoteMintsFrom, type TrackerLaunch } from '../src/tracker.js';
import { loadConfig } from '../src/env.js';

const cfg = loadConfig(
  { CLUSTER: 'devnet', TRACKER_URL: 'http://tracker.test', TRACKER_PAGE_SIZE: '2' } as NodeJS.ProcessEnv,
  process.cwd(),
);

const SOL = 'So11111111111111111111111111111111111111112';
const USDC = 'EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v';

function launch(over: Partial<TrackerLaunch>): TrackerLaunch {
  return {
    mint: 'mint', creator_wallet: 'c', quote_mint: SOL, name: 'n', symbol: 's',
    transfer_fee_bps: 100, status: 'launched', created_at: '2026-09-12T00:00:00Z', ...over,
  };
}

describe('normaliseLaunch', () => {
  it('reads the stored snake_case fields', () => {
    const l = normaliseLaunch({
      mint: 'M', pool_id: 'P', creator_wallet: 'C', quote_mint: SOL, name: 'N', symbol: 'S',
      transfer_fee_bps: 100, platform_id: 'PLAT', status: 'launched', created_at: 't',
    });
    expect(l).toEqual({
      mint: 'M', pool_id: 'P', creator_wallet: 'C', quote_mint: SOL, name: 'N', symbol: 'S',
      transfer_fee_bps: 100, platform_id: 'PLAT', status: 'launched', created_at: 't',
    });
  });

  it('falls back to the camelCase duplicates the portal reads', () => {
    const l = normaliseLaunch({
      mint: 'M', poolId: 'P', creatorWallet: 'C', quoteMint: USDC, name: 'N', symbol: 'S',
      transferFeeBps: 100, platformId: 'PLAT', status: 'launched', createdAt: 't',
    });
    expect(l.pool_id).toBe('P');
    expect(l.quote_mint).toBe(USDC);
    expect(l.transfer_fee_bps).toBe(100);
    expect(l.platform_id).toBe('PLAT');
  });

  it('leaves optional ids absent rather than empty strings', () => {
    const l = normaliseLaunch({ mint: 'M', quote_mint: SOL, pool_id: '', platformId: '' });
    expect(l.pool_id).toBeUndefined();
    expect(l.platform_id).toBeUndefined();
    expect(l.transfer_fee_bps).toBe(0);
  });
});

describe('fetchLaunches', () => {
  it('follows offset pagination to meta.total and drops rows without a mint', async () => {
    const pages: Record<string, unknown> = {
      '0': { data: [{ mint: 'A', quote_mint: SOL }, { mint: 'B', quoteMint: USDC }], meta: { total: 3, limit: 2, offset: 0 } },
      '2': { data: [{ mint: '', quote_mint: SOL }], meta: { total: 3, limit: 2, offset: 2 } },
    };
    const fetchImpl = vi.fn(async (url: string) => {
      const offset = new URL(url).searchParams.get('offset') ?? '0';
      return new Response(JSON.stringify(pages[offset] ?? { data: [] }), { status: 200 });
    });
    const out = await fetchLaunches(cfg, fetchImpl as unknown as typeof fetch);
    expect(out.map((l) => l.mint)).toEqual(['A', 'B']);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect((fetchImpl.mock.calls[0] as unknown as [string])[0]).toBe('http://tracker.test/api/launches?limit=2&offset=0');
  });

  it('stops on an empty page when meta is missing', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ data: null }), { status: 200 }));
    const out = await fetchLaunches(cfg, fetchImpl as unknown as typeof fetch);
    expect(out).toEqual([]);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it('throws with the status when the tracker fails', async () => {
    const fetchImpl = vi.fn(async () => new Response('down', { status: 503 }));
    await expect(fetchLaunches(cfg, fetchImpl as unknown as typeof fetch)).rejects.toThrow(/HTTP 503/);
  });
});

describe('quoteMintsFrom / launchesOnPlatform', () => {
  it('returns distinct sorted quote mints plus pinned extras', () => {
    const out = quoteMintsFrom([launch({ quote_mint: USDC }), launch({ quote_mint: SOL }), launch({ quote_mint: SOL })], [SOL, 'zzz']);
    expect(out).toEqual([USDC, SOL, 'zzz']);
  });

  it('keeps launches on our platform and those recorded before platform_id existed', () => {
    const ours = launch({ mint: 'ours', platform_id: 'PLAT' });
    const legacy = launch({ mint: 'legacy' });
    const other = launch({ mint: 'other', platform_id: 'SOMEONE' });
    expect(launchesOnPlatform([ours, legacy, other], 'PLAT').map((l) => l.mint)).toEqual(['ours', 'legacy']);
  });
});
