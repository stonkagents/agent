// Client for the tracker's internal revenue ledger.
// POST {TRACKER_URL}/api/internal/revenue with X-Internal-Token, idempotent on `signature`.
// Anything that will not post lands in a local JSONL queue so a keeper run never loses a row.

import fs from 'node:fs';
import path from 'node:path';
import type { KeeperConfig } from './env.js';
import type { FetchLike } from './prices.js';
import { log } from './log.js';
import { Decimal } from './amounts.js';

export type RevenueKind =
  | 'launch_fee'
  | 'platform_fee_claim'
  | 'buyback'
  | 'burn'
  | 'nft_yield'
  | 'holder_distribution';

export interface RevenueRow {
  kind: RevenueKind;
  quoteMint: string;
  /** Raw base units of quoteMint. BigInt in, number on the wire (see toWire). */
  amountRaw: bigint;
  amountUsd?: Decimal | number;
  signature: string;
  mint?: string;
  occurredAt: string;
  meta?: Record<string, unknown>;
}

export interface WireRow {
  kind: RevenueKind;
  quoteMint: string;
  amountRaw: number;
  amountUsd?: number;
  signature: string;
  mint?: string;
  occurredAt: string;
  meta?: Record<string, unknown>;
}

const MAX_SAFE = BigInt(Number.MAX_SAFE_INTEGER);

/**
 * Converts a row to its JSON shape. amount_raw is a float64 column on the tracker, so an amount
 * above 2^53 cannot round-trip exactly; the exact BigInt is always carried in meta.amountRawExact
 * and a warning is logged when the number form is lossy.
 */
export function toWire(row: RevenueRow): WireRow {
  const lossy = row.amountRaw > MAX_SAFE;
  if (lossy) {
    log.warn('amountRaw exceeds float64 integer precision; exact value preserved in meta.amountRawExact', {
      kind: row.kind,
      signature: row.signature,
      amountRaw: row.amountRaw,
    });
  }
  const wire: WireRow = {
    kind: row.kind,
    quoteMint: row.quoteMint,
    amountRaw: Number(row.amountRaw),
    signature: row.signature,
    occurredAt: row.occurredAt,
    meta: { ...(row.meta ?? {}), amountRawExact: row.amountRaw.toString() },
  };
  if (row.mint) wire.mint = row.mint;
  if (row.amountUsd !== undefined) {
    wire.amountUsd = Number(new Decimal(row.amountUsd.toString()).toDecimalPlaces(6).toFixed());
  }
  return wire;
}

/** True when a success body says `{ data: { created: false } }`: the signature was already in the ledger. */
export function wasDuplicate(body: string): boolean {
  try {
    const json = JSON.parse(body) as { data?: { created?: unknown } };
    return json?.data?.created === false;
  } catch {
    return false;
  }
}

export interface LedgerClientOptions {
  fetchImpl?: FetchLike;
  attempts?: number;
  /** Backoff base in ms; the tests pass 0. */
  backoffMs?: number;
  sleep?: (ms: number) => Promise<void>;
}

export interface PostOutcome {
  posted: boolean;
  duplicate: boolean;
  queued: boolean;
  status?: number;
  error?: string;
}

export class LedgerClient {
  private readonly cfg: KeeperConfig;
  private readonly fetchImpl: FetchLike;
  private readonly attempts: number;
  private readonly backoffMs: number;
  private readonly sleep: (ms: number) => Promise<void>;
  readonly queuePath: string;

  constructor(cfg: KeeperConfig, opts: LedgerClientOptions = {}) {
    this.cfg = cfg;
    this.fetchImpl = opts.fetchImpl ?? fetch;
    this.attempts = opts.attempts ?? 3;
    this.backoffMs = opts.backoffMs ?? 500;
    this.sleep = opts.sleep ?? ((ms) => new Promise((r) => setTimeout(r, ms)));
    this.queuePath = path.join(cfg.stateDir, 'ledger-queue.jsonl');
  }

  /** Posts one row, retrying transient failures, queueing anything still failing. */
  async record(row: RevenueRow): Promise<PostOutcome> {
    return this.postWire(toWire(row));
  }

  async postWire(wire: WireRow): Promise<PostOutcome> {
    if (!this.cfg.INTERNAL_API_TOKEN) {
      this.enqueue(wire, 'INTERNAL_API_TOKEN unset');
      return { posted: false, duplicate: false, queued: true, error: 'INTERNAL_API_TOKEN unset' };
    }
    let lastError = '';
    let lastStatus: number | undefined;
    for (let attempt = 1; attempt <= this.attempts; attempt++) {
      try {
        const res = await this.fetchImpl(`${this.cfg.TRACKER_URL}/api/internal/revenue`, {
          method: 'POST',
          headers: {
            'content-type': 'application/json',
            accept: 'application/json',
            'X-Internal-Token': this.cfg.INTERNAL_API_TOKEN,
          },
          body: JSON.stringify(wire),
          signal: AbortSignal.timeout(this.cfg.TRACKER_TIMEOUT_MS),
        });
        lastStatus = res.status;
        const text = await res.text();
        if (res.status === 409) {
          log.debug('ledger row already recorded', { kind: wire.kind, signature: wire.signature });
          return { posted: true, duplicate: true, queued: false, status: 409 };
        }
        if (res.ok) {
          // The tracker answers 201 for a new row and 200 with data.created=false for a signature
          // it already holds. Both are success: the row is in the ledger either way.
          const duplicate = res.status === 200 && wasDuplicate(text);
          if (duplicate) log.debug('ledger row already recorded', { kind: wire.kind, signature: wire.signature });
          else log.info('ledger row recorded', { kind: wire.kind, signature: wire.signature, amountRaw: wire.amountRaw });
          return { posted: true, duplicate, queued: false, status: res.status };
        }
        lastError = `HTTP ${res.status}: ${text.slice(0, 300)}`;
        // 4xx other than 409 will not succeed on retry.
        if (res.status >= 400 && res.status < 500) break;
      } catch (e) {
        lastError = (e as Error).message;
      }
      if (attempt < this.attempts) await this.sleep(this.backoffMs * 2 ** (attempt - 1));
    }
    this.enqueue(wire, lastError);
    return { posted: false, duplicate: false, queued: true, status: lastStatus, error: lastError };
  }

  /** Appends a row to the on-disk queue. Never throws: losing the row is worse than a noisy log. */
  enqueue(wire: WireRow, reason: string): void {
    try {
      fs.mkdirSync(path.dirname(this.queuePath), { recursive: true });
      fs.appendFileSync(
        this.queuePath,
        JSON.stringify({ queuedAt: new Date().toISOString(), reason, row: wire }) + '\n',
        'utf8',
      );
      log.warn('ledger row queued locally', { signature: wire.signature, kind: wire.kind, reason, queue: this.queuePath });
    } catch (e) {
      log.error('failed to queue ledger row', { signature: wire.signature, error: e as Error });
    }
  }

  readQueue(): { queuedAt: string; reason: string; row: WireRow }[] {
    if (!fs.existsSync(this.queuePath)) return [];
    return fs
      .readFileSync(this.queuePath, 'utf8')
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => JSON.parse(l) as { queuedAt: string; reason: string; row: WireRow });
  }

  /** Re-posts every queued row; rewrites the queue with whatever still fails. */
  async flushQueue(): Promise<{ flushed: number; remaining: number }> {
    const entries = this.readQueue();
    if (entries.length === 0) return { flushed: 0, remaining: 0 };
    fs.rmSync(this.queuePath, { force: true });
    let flushed = 0;
    for (const entry of entries) {
      const out = await this.postWire(entry.row);
      if (out.posted) flushed++;
    }
    return { flushed, remaining: this.readQueue().length };
  }
}
