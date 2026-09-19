// Runs the three jobs in the order the money moves: claim the platform fee first, then
// distribute the holder tax, then spend a share of what was just claimed on the buyback.
// Any flags (--execute, --mint) are passed through to every job.

import { runJob, parseJobArgs, type JobContext } from '../cli.js';
import { claimPlatformFees } from './claim-platform-fees.js';
import { distributeHolderTax } from './distribute-holder-tax.js';
import { buybackAndBurn } from './buyback-and-burn.js';
import { LedgerClient } from '../ledger.js';
import { log, print } from '../log.js';

const STEPS: [string, (ctx: JobContext) => Promise<void>][] = [
  ['claim-platform-fees', claimPlatformFees],
  ['distribute-holder-tax', distributeHolderTax],
  ['buyback-and-burn', buybackAndBurn],
];

async function main(ctx: JobContext): Promise<void> {
  const failures: string[] = [];
  for (const [name, step] of STEPS) {
    print('');
    print(`########## ${name} ##########`);
    try {
      await step(ctx);
    } catch (e) {
      failures.push(`${name}: ${(e as Error).message}`);
      log.error('step failed', { step: name, error: e as Error });
    }
  }

  // Anything the tracker refused earlier in the run gets one more chance before we exit.
  const flushed = await new LedgerClient(ctx.cfg).flushQueue();
  if (flushed.flushed || flushed.remaining) log.info('ledger queue flushed', flushed);

  if (failures.length) throw new Error(`${failures.length} step(s) failed: ${failures.join(' | ')}`);
}

// parseJobArgs runs first so a bad flag fails before any RPC call.
parseJobArgs();
await runJob('all', main);
