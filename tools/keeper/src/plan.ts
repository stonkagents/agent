// Plan tables. Every job prints one before it does anything, in dry-run and in execute mode,
// so the operator (and later an auditor reading the log) sees exactly what was about to happen.

export type Align = 'left' | 'right';

export interface Column {
  key: string;
  label: string;
  align?: Align;
}

export type Row = Record<string, string>;

export interface PlanInput {
  title: string;
  mode: 'dry-run' | 'execute';
  columns: Column[];
  rows: Row[];
  /** Rendered under the table as `label: value` lines. Totals, thresholds, wallet addresses. */
  facts?: [string, string][];
  /** Free-form lines: warnings, skip reasons, the next manual step. */
  notes?: string[];
  /** Shown instead of the table when there are no rows. */
  emptyMessage?: string;
}

const PAD = 2;

function widthOf(columns: Column[], rows: Row[]): number[] {
  return columns.map((c) => {
    const cells = rows.map((r) => (r[c.key] ?? '').length);
    return Math.max(c.label.length, ...(cells.length ? cells : [0]));
  });
}

function pad(text: string, width: number, align: Align): string {
  if (text.length >= width) return text;
  const fill = ' '.repeat(width - text.length);
  return align === 'right' ? fill + text : text + fill;
}

/** Fixed-width table. Column order is the caller's; no sorting happens here. */
export function renderTable(columns: Column[], rows: Row[]): string {
  const widths = widthOf(columns, rows);
  const sep = ' '.repeat(PAD);
  const header = columns.map((c, i) => pad(c.label, widths[i] ?? 0, c.align ?? 'left')).join(sep);
  const rule = columns.map((_, i) => '-'.repeat(widths[i] ?? 0)).join(sep);
  const body = rows.map((r) => columns.map((c, i) => pad(r[c.key] ?? '', widths[i] ?? 0, c.align ?? 'left')).join(sep));
  return [header, rule, ...body].join('\n');
}

/** The whole plan block: banner, table, facts, notes. Returned as a string so tests can assert on it. */
export function renderPlan(input: PlanInput): string {
  const banner = input.mode === 'dry-run' ? 'DRY RUN — nothing is sent' : 'EXECUTE — transactions will be sent';
  const lines: string[] = ['', `=== ${input.title} — ${banner} ===`, ''];
  if (input.rows.length === 0) {
    lines.push(input.emptyMessage ?? 'Nothing to do.');
  } else {
    lines.push(renderTable(input.columns, input.rows));
  }
  if (input.facts?.length) {
    lines.push('');
    const width = Math.max(...input.facts.map(([k]) => k.length));
    for (const [k, v] of input.facts) lines.push(`${pad(k, width, 'left')} : ${v}`);
  }
  if (input.notes?.length) {
    lines.push('');
    for (const n of input.notes) lines.push(`! ${n}`);
  }
  lines.push('');
  return lines.join('\n');
}

/** Shortens a base58 address for a table cell while keeping it recognisable. */
export function shortAddr(address: string, keep = 6): string {
  if (address.length <= keep * 2 + 3) return address;
  return `${address.slice(0, keep)}..${address.slice(-keep)}`;
}
