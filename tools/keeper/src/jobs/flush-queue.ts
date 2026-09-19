// Re-posts ledger rows that the tracker refused during an earlier run.
// Read-only against the chain: it touches nothing but state/ledger-queue.jsonl.

import { runJob, type JobContext } from '../cli.js';
import { LedgerClient } from '../ledger.js';
import { renderPlan, type Row } from '../plan.js';
import { print } from '../log.js';

async function main(ctx: JobContext): Promise<void> {
  const client = new LedgerClient(ctx.cfg);
  const queued = client.readQueue();
  const rows: Row[] = queued.map((q) => ({
    kind: q.row.kind,
    signature: q.row.signature,
    amount: String(q.row.amountRaw),
    queuedAt: q.queuedAt,
    reason: q.reason.slice(0, 60),
  }));

  print(renderPlan({
    title: 'flush-queue',
    mode: ctx.args.execute ? 'execute' : 'dry-run',
    columns: [
      { key: 'kind', label: 'KIND' },
      { key: 'signature', label: 'SIGNATURE' },
      { key: 'amount', label: 'AMOUNT RAW', align: 'right' },
      { key: 'queuedAt', label: 'QUEUED AT' },
      { key: 'reason', label: 'REASON' },
    ],
    rows,
    facts: [['queue file', client.queuePath]],
    emptyMessage: 'Queue is empty; every ledger row reached the tracker.',
  }));

  if (!ctx.args.execute) {
    print('Dry run. Add --execute to re-post.');
    return;
  }
  const result = await client.flushQueue();
  print(`Flushed ${result.flushed}, still queued ${result.remaining}.`);
}

await runJob('flush-queue', main);
