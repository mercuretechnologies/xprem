// A time range as people type it: each end is either relative to now
// ("now", "now-7d") or an absolute local date ("2026-09-27 14:00").
export type TimeRange = { from: string; to: string };

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const unitMs: Record<string, number> = { m: MINUTE, h: HOUR, d: DAY, w: 7 * DAY };

export const quickRanges: Array<TimeRange & { label: string }> = [
  { from: 'now-5m', to: 'now', label: 'Last 5 minutes' },
  { from: 'now-15m', to: 'now', label: 'Last 15 minutes' },
  { from: 'now-30m', to: 'now', label: 'Last 30 minutes' },
  { from: 'now-1h', to: 'now', label: 'Last 1 hour' },
  { from: 'now-3h', to: 'now', label: 'Last 3 hours' },
  { from: 'now-6h', to: 'now', label: 'Last 6 hours' },
  { from: 'now-12h', to: 'now', label: 'Last 12 hours' },
  { from: 'now-24h', to: 'now', label: 'Last 24 hours' },
  { from: 'now-2d', to: 'now', label: 'Last 2 days' },
  { from: 'now-7d', to: 'now', label: 'Last 7 days' },
  { from: 'now-14d', to: 'now', label: 'Last 14 days' },
  { from: 'now-30d', to: 'now', label: 'Last 30 days' },
];

/**
 * Checks for a trimmed "now" prefix; this does not validate the expression.
 */
export const isRelative = (expression: string) => expression.trim().startsWith('now');

const pad = (value: number) => String(value).padStart(2, '0');

// The spelling the From and To fields show for an absolute date, in local time.
export const formatAbsolute = (date: Date) =>
  `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;

/**
 * Resolves now or now-N[mhdw] against now in Unix milliseconds, or reads a
 * local YYYY-MM-DD date with optional HH:mm[:ss]. Calendar fields use Date
 * normalization. Unrecognized syntax or an invalid absolute date returns null;
 * relative arithmetic can produce an invalid Date for out-of-range values.
 */
export const parseTimeExpression = (expression: string, now: number): Date | null => {
  const text = expression.trim();
  if (text === 'now') return new Date(now);
  const relative = /^now-(\d+)([mhdw])$/.exec(text);
  if (relative) return new Date(now - Number(relative[1]) * unitMs[relative[2]]);
  // "2026-09-27 14:00" and "2026-09-27" read as local time, like the fields show them.
  const absolute = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?$/.exec(text);
  if (!absolute) return null;
  const [, year, month, day, hours = '0', minutes = '0', seconds = '0'] = absolute;
  const date = new Date(
    Number(year),
    Number(month) - 1,
    Number(day),
    Number(hours),
    Number(minutes),
    Number(seconds)
  );
  return Number.isNaN(date.getTime()) ? null : date;
};

// Both ends as dates; null when either end does not read or they are out of order.
export const resolveRange = (range: TimeRange, now: number) => {
  const from = parseTimeExpression(range.from, now);
  const to = parseTimeExpression(range.to, now);
  if (!from || !to || from.getTime() >= to.getTime()) return null;
  return { from, to };
};

const dayLabel = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });
const timeLabel = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
});

const sameDay = (a: Date, b: Date) => a.toDateString() === b.toDateString();

// A quick range by its name; otherwise the shortest spelling of the bounds,
// with the day written once when both ends share it.
export const describeRange = (range: TimeRange) => {
  const quick = quickRanges.find(entry => entry.from === range.from && entry.to === range.to);
  if (quick) return quick.label;
  const resolved = resolveRange(range, Date.now());
  if (!resolved) return `${range.from} → ${range.to}`;
  const { from, to } = resolved;
  const start = `${dayLabel.format(from)} ${timeLabel.format(from)}`;
  if (range.to === 'now') return `${start} → now`;
  if (sameDay(from, to)) return `${start} – ${timeLabel.format(to)}`;
  return `${start} → ${dayLabel.format(to)} ${timeLabel.format(to)}`;
};

/**
 * Moves a resolved window one length backward (-1) or forward (1), clamping
 * its end to now in Unix milliseconds. Returns local absolute bounds with
 * second precision, or null when the range cannot be resolved.
 */
export const shiftRange = (range: TimeRange, direction: -1 | 1, now: number): TimeRange | null => {
  const resolved = resolveRange(range, now);
  if (!resolved) return null;
  const length = resolved.to.getTime() - resolved.from.getTime();
  const to = Math.min(resolved.to.getTime() + direction * length, now);
  return { from: formatAbsolute(new Date(to - length)), to: formatAbsolute(new Date(to)) };
};

// The length of a range in ms; 0 when it does not resolve.
export const rangeLengthMs = (range: TimeRange, now: number) => {
  const resolved = resolveRange(range, now);
  return resolved ? resolved.to.getTime() - resolved.from.getTime() : 0;
};

/**
 * Expands a resolved window to twice its duration, capped at maxMs milliseconds,
 * and clamps its end to now in Unix milliseconds. An end of "now" is retained;
 * other bounds become local absolute times with second precision. Returns null
 * when the range cannot be resolved.
 */
export const zoomOutRange = (range: TimeRange, now: number, maxMs = Infinity): TimeRange | null => {
  const resolved = resolveRange(range, now);
  if (!resolved) return null;
  const length = Math.min(resolved.to.getTime() - resolved.from.getTime(), maxMs / 2);
  if (range.to === 'now') {
    return { from: formatAbsolute(new Date(now - 2 * length)), to: 'now' };
  }
  const to = Math.min(resolved.to.getTime() + length / 2, now);
  return { from: formatAbsolute(new Date(to - 2 * length)), to: formatAbsolute(new Date(to)) };
};

const recentKey = 'timeRangePicker.recent';

/**
 * Reads recent ranges from browser storage, retaining entries with string bounds
 * without validating their expressions. Missing, unreadable, or malformed storage
 * returns an empty list.
 */
export const readRecentRanges = (): TimeRange[] => {
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(recentKey) ?? '[]');
    return Array.isArray(parsed)
      ? parsed.filter(
          (entry): entry is TimeRange =>
            typeof entry?.from === 'string' && typeof entry?.to === 'string'
        )
      : [];
  } catch {
    return [];
  }
};

/**
 * Stores a range first in a deduplicated history of at most four entries, unless
 * both bounds have a "now" prefix. Browser storage failures are ignored.
 */
export const rememberRange = (range: TimeRange) => {
  if (isRelative(range.from) && isRelative(range.to)) return;
  const recent = [
    range,
    ...readRecentRanges().filter(entry => entry.from !== range.from || entry.to !== range.to),
  ].slice(0, 4);
  try {
    window.localStorage.setItem(recentKey, JSON.stringify(recent));
  } catch {
    // Nothing to do: the list is a convenience.
  }
};
