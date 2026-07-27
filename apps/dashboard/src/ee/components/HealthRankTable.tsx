import { ChevronRight } from 'lucide-react';

const exactNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

export type HealthRankRow = {
  // Identifies the row for React and for the click-through; never displayed.
  key: string;
  label: string;
  // What the label alone does not say: a branch, a runtime, a publish date. A
  // short id identifies a group, it does not describe it.
  detail?: string;
  // Rows that belong together under one heading. A release history spans
  // several branches, and the same message ships on all of them: without the
  // branch as a heading, eight rows read as eight unrelated things.
  group?: string;
  color: string;
  devices: number;
  faulty: number;
  health: number | null;
};

// The counts that go with a health chart, one row per curve, worst first. The
// chart says when something went wrong, the table says who it happened to and
// how many of them: an OS version, an update group and a country all answer
// that same question, so they share this rather than each growing their own
// legend.
export const HealthRankTable = ({
  dimensionLabel,
  rows,
  onSelect,
  onHover,
}: {
  dimensionLabel: string;
  rows: HealthRankRow[];
  // Absent where there is nothing to narrow to (a single update's own page),
  // which also makes the rows non-interactive.
  onSelect?: (key: string) => void;
  // Pointing at a row says which curve above it is: eight lines of the same
  // colour family are impossible to pair with their row by eye.
  onHover?: (key: string | null) => void;
}) => {
  if (rows.length === 0) return null;

  // A segment matters when devices fail on it, then by how ill it is, then by
  // how many devices carry it at all.
  const worstFirst = (left: HealthRankRow, right: HealthRankRow) =>
    right.faulty - left.faulty ||
    (left.health ?? 100) - (right.health ?? 100) ||
    right.devices - left.devices;

  // Groups are ordered by the audience they carry, so production comes before
  // the channels a handful of people are on, and each group keeps the same
  // worst-first order inside.
  const reach = new Map<string, number>();
  for (const row of rows) {
    const group = row.group ?? '';
    reach.set(group, Math.max(reach.get(group) ?? 0, row.devices));
  }
  const ranked = [...rows].sort(
    (left, right) =>
      (reach.get(right.group ?? '') ?? 0) - (reach.get(left.group ?? '') ?? 0) ||
      worstFirst(left, right)
  );
  const columns = `grid-cols-[minmax(0,1fr)_80px_80px_90px_24px]`;
  const grouped = rows.some(row => row.group);

  return (
    <>
      <div
        className={`grid ${columns} border-y bg-muted/20 px-5 py-1.5 text-[11px] text-muted-foreground`}>
        <span>{dimensionLabel}</span>
        <span className="text-right">Devices</span>
        <span className="text-right">Failed</span>
        <span className="text-right">Health</span>
        <span />
      </div>
      {ranked.map((row, index) => {
        const heading =
          grouped && row.group && row.group !== ranked[index - 1]?.group ? row.group : null;
        const cells = (
          <>
            <span className="flex min-w-0 items-baseline gap-2">
              <i
                className="h-2 w-2 shrink-0 translate-y-[-1px] rounded-full"
                style={{ backgroundColor: row.color }}
              />
              <span className="shrink-0 text-[13px]">{row.label}</span>
              {row.detail && (
                <span className="truncate text-[11px] text-muted-foreground">{row.detail}</span>
              )}
            </span>
            <span className="text-right font-mono text-[13px] tabular-nums text-muted-foreground">
              {exactNumber.format(row.devices)}
            </span>
            <span
              className={`text-right font-mono text-[13px] tabular-nums ${
                row.faulty > 0 ? 'text-rose-600 dark:text-rose-400' : 'text-muted-foreground'
              }`}>
              {exactNumber.format(row.faulty)}
            </span>
            <span
              className={`text-right font-mono text-[13px] tabular-nums ${
                row.health != null && row.health < 99 ? 'text-amber-600 dark:text-amber-400' : ''
              }`}>
              {row.health == null ? '-' : `${row.health.toFixed(1)}%`}
            </span>
            {onSelect ? <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" /> : <span />}
          </>
        );
        const className = `grid w-full ${columns} items-center border-b px-5 py-2 text-left last:border-0`;
        // The row is a CSS grid of spans, so assistive tech reads its cells as
        // one run of numbers with no column names attached. Spelling the row
        // out is what makes it say which number is which.
        const spoken = [
          `${row.label}${row.detail ? `, ${row.detail}` : ''}`,
          `${exactNumber.format(row.devices)} devices`,
          `${exactNumber.format(row.faulty)} failed`,
          row.health == null ? 'health unknown' : `health ${row.health.toFixed(1)}%`,
        ].join(', ');

        const line = onSelect ? (
          <button
            key={row.key}
            type="button"
            onClick={() => onSelect(row.key)}
            onMouseEnter={() => onHover?.(row.key)}
            onMouseLeave={() => onHover?.(null)}
            onFocus={() => onHover?.(row.key)}
            onBlur={() => onHover?.(null)}
            title={`Filter on ${row.label}`}
            aria-label={`Filter on ${spoken}`}
            className={`${className} hover:bg-accent/50`}>
            {cells}
          </button>
        ) : (
          <div
            key={row.key}
            onMouseEnter={() => onHover?.(row.key)}
            onMouseLeave={() => onHover?.(null)}
            aria-label={spoken}
            className={className}>
            {cells}
          </div>
        );

        if (!heading) return line;
        return (
          <div key={`${row.group}:${row.key}`}>
            <div className="border-b bg-muted/10 px-5 py-1 text-[11px] font-medium text-muted-foreground">
              {heading}
            </div>
            {line}
          </div>
        );
      })}
    </>
  );
};
