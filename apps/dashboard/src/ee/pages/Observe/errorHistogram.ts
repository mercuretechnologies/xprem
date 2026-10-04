// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ErrorSeriesPoint } from '@/lib/api';

export const histogramBuckets = (
  series: ErrorSeriesPoint[],
  bucketSeconds: number,
  to?: string
) => {
  const bucketMs = Math.max(0, bucketSeconds) * 1_000;
  const snapshotEnd = to ? new Date(to).getTime() : Infinity;
  return series.map(point => {
    const timestamp = new Date(point.timestamp);
    const start = timestamp.getTime();
    const end = Math.max(start, Math.min(start + bucketMs, snapshotEnd));
    return {
      timestamp,
      end: new Date(end),
      center: new Date(start + (end - start) / 2),
      count: point.count,
    };
  });
};

export const occurrenceTicks = (max: number) => {
  const ceiling = Math.max(2, Math.ceil(Number.isFinite(max) ? max : 0));
  const step = Math.max(1, Math.ceil(ceiling / 4));
  const upper = Math.ceil(ceiling / step) * step;
  const ticks: number[] = [];
  for (let value = 0; value <= upper; value += step) ticks.push(value);
  return ticks;
};

export const histogramIntervalLabel = (bucketSeconds: number) => {
  let remaining = Math.max(0, Number.isFinite(bucketSeconds) ? bucketSeconds : 0);
  const parts: string[] = [];
  for (const [seconds, label] of [
    [86_400, 'd'],
    [3_600, 'h'],
    [60, 'm'],
  ] as const) {
    const value = Math.floor(remaining / seconds);
    if (value > 0) parts.push(`${value}${label}`);
    remaining -= value * seconds;
  }
  if (remaining > 0 || parts.length === 0) parts.push(`${remaining}s`);
  return `${parts.join(' ')} intervals`;
};
