// Structured JSON logging. One object per line so CloudWatch / Loki can parse every field,
// including transaction signatures, without a regex.

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

const ORDER: Record<LogLevel, number> = { debug: 10, info: 20, warn: 30, error: 40 };

let threshold: number = ORDER.info;
let job = 'keeper';

export function configureLogger(opts: { level?: LogLevel; job?: string }): void {
  if (opts.level) threshold = ORDER[opts.level];
  if (opts.job) job = opts.job;
}

export type LogFields = Record<string, unknown>;

function emit(level: LogLevel, msg: string, fields: LogFields = {}): void {
  if (ORDER[level] < threshold) return;
  const line = JSON.stringify({ ts: new Date().toISOString(), level, job, msg, ...serialise(fields) });
  if (level === 'error' || level === 'warn') process.stderr.write(line + '\n');
  else process.stdout.write(line + '\n');
}

/** BigInt, BN, PublicKey and Error are not JSON-serialisable by default; make them readable. */
function serialise(fields: LogFields): LogFields {
  const out: LogFields = {};
  for (const [k, v] of Object.entries(fields)) {
    if (typeof v === 'bigint') out[k] = v.toString();
    else if (v instanceof Error) out[k] = { name: v.name, message: v.message };
    else if (v && typeof v === 'object' && typeof (v as { toBase58?: unknown }).toBase58 === 'function') {
      out[k] = (v as { toBase58: () => string }).toBase58();
    } else if (v && typeof v === 'object' && typeof (v as { toString?: unknown }).toString === 'function' && isBnLike(v)) {
      out[k] = String(v);
    } else out[k] = v;
  }
  return out;
}

function isBnLike(v: object): boolean {
  return typeof (v as { toTwos?: unknown }).toTwos === 'function';
}

export const log = {
  debug: (msg: string, fields?: LogFields) => emit('debug', msg, fields),
  info: (msg: string, fields?: LogFields) => emit('info', msg, fields),
  warn: (msg: string, fields?: LogFields) => emit('warn', msg, fields),
  error: (msg: string, fields?: LogFields) => emit('error', msg, fields),
};

/** Human-facing output (plan tables, runbook hints). Never mixed into the JSON stream parsers read. */
export function print(line = ''): void {
  process.stdout.write(line + '\n');
}
