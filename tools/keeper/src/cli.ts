// Shared CLI surface for the three jobs. Dry run is the default and --execute is the only
// way to send a transaction; there is no config key that turns execution on implicitly.

import { parseArgs } from 'node:util';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { configureLogger, log, print, type LogLevel } from './log.js';
import { loadConfig, type KeeperConfig } from './env.js';

/** True when this module is the entry point, so `npm run all` can import a job without running it. */
export function isMain(importMetaUrl: string): boolean {
  const entry = process.argv[1];
  if (!entry) return false;
  return pathToFileURL(path.resolve(entry)).href === importMetaUrl;
}

export interface JobArgs {
  execute: boolean;
  /** Restrict a job to one base mint. distribute-holder-tax only. */
  mint?: string;
  /** Restrict claim-platform-fees to one quote mint. */
  quoteMint?: string;
  /** Skip the per-pool fallback sweep in claim-platform-fees. */
  vaultOnly: boolean;
  json: boolean;
}

export function parseJobArgs(argv = process.argv.slice(2)): JobArgs {
  const { values } = parseArgs({
    args: argv,
    options: {
      'dry-run': { type: 'boolean', default: false },
      execute: { type: 'boolean', default: false },
      mint: { type: 'string' },
      'quote-mint': { type: 'string' },
      'vault-only': { type: 'boolean', default: false },
      json: { type: 'boolean', default: false },
    },
    strict: true,
    allowPositionals: false,
  });
  if (values.execute && values['dry-run']) throw new Error('--dry-run and --execute are mutually exclusive');
  return {
    execute: Boolean(values.execute),
    mint: values.mint,
    quoteMint: values['quote-mint'],
    vaultOnly: Boolean(values['vault-only']),
    json: Boolean(values.json),
  };
}

export interface JobContext {
  cfg: KeeperConfig;
  args: JobArgs;
}

/** Boots a job: parses args, loads config, configures logging. */
export function bootstrap(jobName: string, argv?: string[]): JobContext {
  const args = parseJobArgs(argv);
  const cfg = loadConfig();
  configureLogger({ level: cfg.LOG_LEVEL as LogLevel, job: jobName });
  log.info('job start', {
    mode: args.execute ? 'execute' : 'dry-run',
    cluster: cfg.CLUSTER,
    rpc: cfg.rpcUrl,
    tracker: cfg.TRACKER_URL,
    stateDir: cfg.stateDir,
  });
  return { cfg, args };
}

/** Wraps a job body so every failure exits non-zero with one structured error line. */
export async function runJob(jobName: string, body: (ctx: JobContext) => Promise<void>, argv?: string[]): Promise<void> {
  let ctx: JobContext;
  try {
    ctx = bootstrap(jobName, argv);
  } catch (e) {
    configureLogger({ job: jobName });
    log.error('job failed to start', { error: e as Error });
    process.exitCode = 2;
    return;
  }
  try {
    await body(ctx);
    log.info('job complete', { mode: ctx.args.execute ? 'execute' : 'dry-run' });
  } catch (e) {
    log.error('job failed', { error: e as Error });
    print('');
    print(`ERROR: ${(e as Error).message}`);
    process.exitCode = 1;
  }
}
