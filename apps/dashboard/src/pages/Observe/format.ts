export const compactNumber = new Intl.NumberFormat(undefined, {
  notation: 'compact',
  maximumFractionDigits: 1,
});

export const exactNumber = new Intl.NumberFormat();

const relativeTime = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

// How long ago, in the coarsest unit that still says something: minutes for
// the last hour, hours for the last two days, days beyond.
export const sinceLabel = (date: Date) => {
  const minutes = Math.round((date.getTime() - Date.now()) / 60_000);
  if (Math.abs(minutes) < 60) return relativeTime.format(minutes, 'minute');
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) return relativeTime.format(hours, 'hour');
  return relativeTime.format(Math.round(hours / 24), 'day');
};

// Every metric expo-observe emits is a duration in seconds, so sub-second
// values read as milliseconds and anything above stays in seconds.
export const duration = (value: number) => {
  if (!Number.isFinite(value)) return '-';
  if (value <= 0) return '0ms';
  return value < 1 ? `${Math.round(value * 1_000)}ms` : `${value.toFixed(2)}s`;
};

// Signed percentage against a baseline. Null when the baseline is zero, where
// a ratio would be meaningless rather than infinite.
export const relativeChange = (value: number, baseline: number) =>
  baseline > 0 ? (value - baseline) / baseline : null;

export const formatChange = (change: number | null) => {
  if (change == null) return '-';
  const percent = 100 * change;
  // Below a percent the number is churn, not a regression worth a colour.
  if (Math.abs(percent) < 1) return 'no change';
  return `${percent > 0 ? '+' : ''}${percent.toFixed(0)}%`;
};

// The last bucket of a live window is still filling: plotted as-is it dives to
// zero and reads as an outage that is not happening. Dropped when the series
// has enough points to infer its own interval.
export const withoutPartialBucket = <T extends { timestamp: Date }>(points: T[]): T[] => {
  if (points.length < 3) return points;
  const last = points[points.length - 1];
  const previous = points[points.length - 2];
  const interval = last.timestamp.getTime() - previous.timestamp.getTime();
  if (interval <= 0) return points;
  const closesAt = last.timestamp.getTime() + interval;
  return closesAt > Date.now() ? points.slice(0, -1) : points;
};
