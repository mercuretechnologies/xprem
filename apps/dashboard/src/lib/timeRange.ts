// A time range as people type it: each end is either relative to now
// ("now", "now-7d"), an absolute local date ("2026-09-27 14:00"),
// or an RFC3339 timestamp returned by the API for a shareable frozen window.
export type TimeRange = { from: string; to: string };

export const defaultRange: TimeRange = { from: 'now-24h', to: 'now' };

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const unitMs: Record<string, number> = { m: MINUTE, h: HOUR, d: DAY, w: 7 * DAY };

const zonedTimePattern =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/;

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

export const isRelative = (expression: string) => expression.trim().startsWith('now');

const pad = (value: number) => String(value).padStart(2, '0');

// The spelling the From and To fields show for an absolute date, in local time.
export const formatAbsolute = (date: Date) =>
  `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;

// Reads one end of a range; null when it is neither a relative expression nor a date.
export const parseTimeExpression = (expression: string, now: number): Date | null => {
  const text = expression.trim();
  if (text === 'now') return new Date(now);
  const relative = /^now-(\d+)([mhdw])$/.exec(text);
  if (relative) {
    const date = new Date(now - Number(relative[1]) * unitMs[relative[2]]);
    return Number.isNaN(date.getTime()) ? null : date;
  }
  // Parse zoned API timestamps separately from browser-local input.
  const zoned = zonedTimePattern.exec(text);
  if (zoned) {
    const [, year, month, day, hours, minutes, seconds, fraction = '', zone] = zoned;
    const milliseconds = Number(fraction.padEnd(3, '0').slice(0, 3));
    const date = new Date(0);
    date.setUTCFullYear(Number(year), Number(month) - 1, Number(day));
    date.setUTCHours(Number(hours), Number(minutes), Number(seconds), milliseconds);
    // Date normalizes invalid dates and hours; reject them instead.
    if (
      date.getUTCFullYear() !== Number(year) ||
      date.getUTCMonth() !== Number(month) - 1 ||
      date.getUTCDate() !== Number(day) ||
      date.getUTCHours() !== Number(hours) ||
      date.getUTCMinutes() !== Number(minutes) ||
      date.getUTCSeconds() !== Number(seconds)
    )
      return null;
    if (zone !== 'Z') {
      const offsetHours = Number(zone.slice(1, 3));
      const offsetMinutes = Number(zone.slice(4, 6));
      if (offsetHours > 23 || offsetMinutes > 59) return null;
      const offset = (offsetHours * HOUR + offsetMinutes * MINUTE) * (zone[0] === '+' ? 1 : -1);
      date.setTime(date.getTime() - offset);
    }
    return date;
  }
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
  // A component past its range rolls the date over: February 31 is not a date.
  const exact =
    date.getFullYear() === Number(year) &&
    date.getMonth() === Number(month) - 1 &&
    date.getDate() === Number(day) &&
    date.getHours() === Number(hours) &&
    date.getMinutes() === Number(minutes) &&
    date.getSeconds() === Number(seconds);
  return exact ? date : null;
};

// Preserve the original bound on the wire; Date truncates API nanoseconds.
export const isZonedAbsolute = (expression: string) =>
  zonedTimePattern.test(expression.trim()) && parseTimeExpression(expression, 0) !== null;

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

export const sameDay = (a: Date, b: Date) => a.toDateString() === b.toDateString();

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

// Moves the window one length back or forward; the result is absolute.
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

// A duration as a relative expression, in the largest unit that divides it.
const relativeExpression = (ms: number) => {
  const unit = ['w', 'd', 'h', 'm'].find(u => ms % unitMs[u] === 0) ?? 'm';
  return `now-${Math.round(ms / unitMs[unit])}${unit}`;
};

// Doubles the window around its middle, never past maxMs; a window ending now
// keeps ending now.
export const zoomOutRange = (range: TimeRange, now: number, maxMs = Infinity): TimeRange | null => {
  const resolved = resolveRange(range, now);
  if (!resolved) return null;
  const length = Math.min(resolved.to.getTime() - resolved.from.getTime(), maxMs / 2);
  if (range.to === 'now') {
    return { from: relativeExpression(2 * length), to: 'now' };
  }
  const to = Math.min(resolved.to.getTime() + length / 2, now);
  return { from: formatAbsolute(new Date(to - 2 * length)), to: formatAbsolute(new Date(to)) };
};

const recentKey = 'timeRangePicker.recent';

// The last absolute ranges applied from the fields, newest first. Browser
// storage can be missing or refuse writes; the list is then simply empty.
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
