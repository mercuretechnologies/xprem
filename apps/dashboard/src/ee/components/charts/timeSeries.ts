// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

export type TimeSeriesPoint = {
  timestamp: Date;
  value: number;
  intervalStart?: Date;
  intervalEnd?: Date;
  samples?: number;
  devices?: number;
};

// visx's nearest-X picker probes past the final datum at the plot's right edge.
export const seriesTimestamp = (point?: TimeSeriesPoint) => point?.timestamp;

const exactNumber = new Intl.NumberFormat();

export const pointSampleSize = (point: TimeSeriesPoint) =>
  [
    point.samples == null
      ? null
      : `${exactNumber.format(point.samples)} measurement${point.samples === 1 ? '' : 's'}`,
    point.devices == null
      ? null
      : `${exactNumber.format(point.devices)} device${point.devices === 1 ? '' : 's'}`,
  ]
    .filter(Boolean)
    .join(' · ');

export const pointDescription = (
  point: TimeSeriesPoint,
  label: string,
  formatValue: (value: number) => string,
  formatTimestamp: (date: Date) => string
) => {
  const period =
    point.intervalStart && point.intervalEnd
      ? `${formatTimestamp(point.intervalStart)} to ${formatTimestamp(point.intervalEnd)}`
      : formatTimestamp(point.timestamp);
  return [
    label,
    period,
    `${point.intervalStart ? 'Median (p50) ' : ''}${formatValue(point.value)}`,
    pointSampleSize(point),
  ]
    .filter(Boolean)
    .join(', ');
};

export const seriesValueDomain = (
  values: number[],
  frameToData = false,
  maximum?: number
): [number, number] => {
  if (maximum != null) return [0, maximum];
  const finiteValues = values.filter(Number.isFinite);
  const highest = finiteValues.length > 0 ? Math.max(...finiteValues) : 0;
  if (!frameToData) return [0, Math.max(2, highest) * 1.08];
  if (highest <= 0) return [0, 0.001];
  const ceiling = Math.max(0.001, highest);
  const lowest = Math.min(...finiteValues);
  const minimum = lowest > 0 && lowest / ceiling > 0.35 ? lowest * 0.9 : 0;
  return [minimum, ceiling * 1.08];
};

export const seriesTimeDomain = (timestamps: number[], requested?: [Date, Date]): [Date, Date] => {
  if (requested && requested[0].getTime() < requested[1].getTime()) return requested;
  const finiteTimestamps = timestamps.filter(Number.isFinite);
  const now = Date.now();
  const start = finiteTimestamps.length > 0 ? Math.min(...finiteTimestamps) : now;
  const end = finiteTimestamps.length > 0 ? Math.max(...finiteTimestamps) : now;
  return start === end
    ? [new Date(start - 30_000), new Date(end + 30_000)]
    : [new Date(start), new Date(end)];
};

// NaN is visx's line break. These render-only separators never enter the
// interactive glyph series, which retains the actual samples and their keys.
export const linePoints = (points: TimeSeriesPoint[], intervalMs?: number): TimeSeriesPoint[] => {
  if (!intervalMs || !Number.isFinite(intervalMs) || intervalMs <= 0) return points;
  const plotted: TimeSeriesPoint[] = [];
  points.forEach((point, index) => {
    const previous = points[index - 1];
    if (previous) {
      const missingBucket =
        previous.intervalEnd && point.intervalStart
          ? point.intervalStart.getTime() > previous.intervalEnd.getTime()
          : point.timestamp.getTime() - previous.timestamp.getTime() > intervalMs;
      if (missingBucket) {
        plotted.push({
          timestamp: new Date((previous.timestamp.getTime() + point.timestamp.getTime()) / 2),
          value: NaN,
        });
      }
    }
    plotted.push(point);
  });
  return plotted;
};

export const sameBucket = (
  point: TimeSeriesPoint,
  nearest: TimeSeriesPoint,
  intervalMs?: number
) => {
  if (point.intervalStart && nearest.intervalStart) {
    return point.intervalStart.getTime() === nearest.intervalStart.getTime();
  }
  const tolerance = intervalMs && Number.isFinite(intervalMs) ? intervalMs / 2 : 0;
  return Math.abs(point.timestamp.getTime() - nearest.timestamp.getTime()) <= tolerance;
};

const DAY_MS = 24 * 60 * 60 * 1_000;

export const timeAxisFormatter = (start: number, end: number) => {
  const span = end - start;
  const crossesDate = new Date(start).toDateString() !== new Date(end).toDateString();
  if (span < DAY_MS && !crossesDate) {
    return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
  }
  if (span < 7 * DAY_MS) {
    return new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      ...(span < DAY_MS ? { minute: '2-digit' } : {}),
    });
  }
  if (span < 365 * DAY_MS) {
    return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
  }
  return new Intl.DateTimeFormat(undefined, { month: 'short', year: 'numeric' });
};
