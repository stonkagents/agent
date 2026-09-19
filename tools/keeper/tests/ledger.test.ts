import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LedgerClient, toWire } from '../src/ledger.js';
import { loadConfig, type KeeperConfig } from '../src/env.js';
import { Decimal } from '../src/amounts.js';

let dir: string;

function config(overrides: Record<string, string> = {}): KeeperConfig {
  return loadConfig(
    { CLUSTER: 'devnet', TRACKER_URL: 'http://tracker.test', INTERNAL_API_TOKEN: 'secret', STATE_DIR: dir, ...overrides } as NodeJS.ProcessEnv,
    dir,
  );
}

function ok(status = 200) {
  return new Response(JSON.stringify({ data: { id: 1 } }), { status });
}

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'keeper-ledger-'));
});
afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe('toWire', () => {
  it('carries the exact raw amount in meta alongside the float64 field', () => {
    const wire = toWire({
      kind: 'platform_fee_claim',
      quoteMint: 'STONK',
      amountRaw: 123n,
      amountUsd: new Decimal('1.5'),
      signature: 'sig',
      occurredAt: '2026-09-12T00:00:00Z',
      meta: { source: 'vault' },
    });
    expect(wire.amountRaw).toBe(123);
    expect(wire.amountUsd).toBe(1.5);
    expect(wire.meta).toEqual({ source: 'vault', amountRawExact: '123' });
  });

  it('preserves an amount beyond float64 integer precision as a string', () => {
    const huge = 2n ** 70n;
    const wire = toWire({ kind: 'burn', quoteMint: 'X', amountRaw: huge, signature: 's', occurredAt: 'now' });
    expect(wire.meta?.amountRawExact).toBe(huge.toString());
  });

  it('omits optional fields that were not supplied', () => {
    const wire = toWire({ kind: 'buyback', quoteMint: 'X', amountRaw: 1n, signature: 's', occurredAt: 'now' });
    expect(wire.mint).toBeUndefined();
    expect(wire.amountUsd).toBeUndefined();
  });
});

describe('LedgerClient', () => {
  const row = {
    kind: 'buyback' as const,
    quoteMint: 'STONK',
    amountRaw: 10n,
    signature: 'sig-1',
    occurredAt: '2026-09-12T00:00:00Z',
  };

  it('posts with the internal token header', async () => {
    const fetchImpl = vi.fn(async () => ok());
    const client = new LedgerClient(config(), { fetchImpl: fetchImpl as unknown as typeof fetch, backoffMs: 0 });
    const out = await client.record(row);
    expect(out).toMatchObject({ posted: true, duplicate: false, queued: false });
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('http://tracker.test/api/internal/revenue');
    expect((init.headers as Record<string, string>)['X-Internal-Token']).toBe('secret');
  });

  it('treats 200 with data.created=false as already recorded (the tracker idempotent reply)', async () => {
    const body = JSON.stringify({ data: { id: 7, kind: 'buyback', signature: 'sig-1', created: false } });
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => new Response(body, { status: 200 })) as unknown as typeof fetch,
      backoffMs: 0,
    });
    const out = await client.record(row);
    expect(out).toMatchObject({ posted: true, duplicate: true, queued: false, status: 200 });
    expect(fs.existsSync(client.queuePath)).toBe(false);
  });

  it('treats 201 with data.created=true as a fresh row', async () => {
    const body = JSON.stringify({ data: { id: 8, kind: 'buyback', signature: 'sig-1', created: true } });
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => new Response(body, { status: 201 })) as unknown as typeof fetch,
      backoffMs: 0,
    });
    const out = await client.record(row);
    expect(out).toMatchObject({ posted: true, duplicate: false, queued: false, status: 201 });
  });

  it('treats 409 as already recorded, not as a failure', async () => {
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => new Response('duplicate', { status: 409 })) as unknown as typeof fetch,
      backoffMs: 0,
    });
    const out = await client.record(row);
    expect(out).toMatchObject({ posted: true, duplicate: true, queued: false });
    expect(client.readQueue()).toHaveLength(0);
  });

  it('retries a 500 and succeeds on a later attempt', async () => {
    let calls = 0;
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => {
        calls++;
        return calls < 3 ? new Response('boom', { status: 500 }) : ok();
      }) as unknown as typeof fetch,
      backoffMs: 0,
    });
    const out = await client.record(row);
    expect(calls).toBe(3);
    expect(out.posted).toBe(true);
    expect(client.readQueue()).toHaveLength(0);
  });

  it('queues the row after exhausting retries', async () => {
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => new Response('boom', { status: 503 })) as unknown as typeof fetch,
      attempts: 2,
      backoffMs: 0,
    });
    const out = await client.record(row);
    expect(out).toMatchObject({ posted: false, queued: true });
    const queued = client.readQueue();
    expect(queued).toHaveLength(1);
    expect(queued[0]?.row.signature).toBe('sig-1');
    expect(queued[0]?.reason).toContain('503');
  });

  it('does not retry a 400, it queues immediately', async () => {
    let calls = 0;
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => {
        calls++;
        return new Response('bad kind', { status: 400 });
      }) as unknown as typeof fetch,
      backoffMs: 0,
    });
    await client.record(row);
    expect(calls).toBe(1);
    expect(client.readQueue()).toHaveLength(1);
  });

  it('queues without calling the tracker when no internal token is configured', async () => {
    const fetchImpl = vi.fn(async () => ok());
    const client = new LedgerClient(config({ INTERNAL_API_TOKEN: '' }), {
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
    });
    const out = await client.record(row);
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(out.queued).toBe(true);
    expect(client.readQueue()[0]?.reason).toContain('INTERNAL_API_TOKEN');
  });

  it('queues a network error and flushes it once the tracker is back', async () => {
    let up = false;
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => {
        if (!up) throw new Error('ECONNREFUSED');
        return ok();
      }) as unknown as typeof fetch,
      attempts: 1,
      backoffMs: 0,
    });
    await client.record(row);
    expect(client.readQueue()).toHaveLength(1);

    up = true;
    const flushed = await client.flushQueue();
    expect(flushed).toEqual({ flushed: 1, remaining: 0 });
    expect(client.readQueue()).toHaveLength(0);
  });

  it('keeps rows queued when the flush also fails', async () => {
    const client = new LedgerClient(config(), {
      fetchImpl: (async () => new Response('down', { status: 502 })) as unknown as typeof fetch,
      attempts: 1,
      backoffMs: 0,
    });
    await client.record(row);
    await client.record({ ...row, signature: 'sig-2' });
    const flushed = await client.flushQueue();
    expect(flushed.flushed).toBe(0);
    expect(flushed.remaining).toBe(2);
  });

  it('reports an empty flush when nothing is queued', async () => {
    const client = new LedgerClient(config(), { fetchImpl: (async () => ok()) as unknown as typeof fetch });
    expect(await client.flushQueue()).toEqual({ flushed: 0, remaining: 0 });
  });
});
