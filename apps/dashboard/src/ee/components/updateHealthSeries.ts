// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { UpdateHealthHistoryPoint } from '@/lib/api';

export type AggregatedPoint = Omit<UpdateHealthHistoryPoint, 'role'>;

// Snapshots are retained per minute. Plotted as-is over a day that is 1440
// points per series, which draws as a comb rather than a trend, and any minute
// without a snapshot dips to zero as though the fleet had vanished. Health and
// adoption are states, not counters: within a bucket the last known value is
// the truth, so resampling both smooths the line and removes the false zeros.
const RESAMPLE_TARGET_POINTS = 140;

// The history route refuses a window wider than 90 days, and callers pass the
// update's publication date as 'from': an update older than that asked for a
// window the API rejects outright, so the chart 404'd rather than showing the
// last 90 days of it. Rounded up to a day boundary so the value is stable
// across renders (it is part of the query key) and stays inside the ceiling
// whatever the server stamps as 'to'.
const MAX_HISTORY_WINDOW_MS = 90 * 24 * 60 * 60 * 1_000;

export const boundedFrom = (from?: string, to?: string) => {
  if (!from) return from;
  const requested = new Date(from).getTime();
  if (Number.isNaN(requested)) return from;
  // The ceiling counts back from 'to' when the caller sets one, exactly; from
  // now otherwise, rounded to a day so the query key stays stable.
  const end = to ? new Date(to).getTime() : NaN;
  const day = 24 * 60 * 60 * 1_000;
  const earliest = Number.isNaN(end)
    ? Math.ceil((Date.now() - MAX_HISTORY_WINDOW_MS) / day) * day
    : end - MAX_HISTORY_WINDOW_MS;
  return requested >= earliest ? from : new Date(earliest).toISOString();
};

const resampleInterval = (points: AggregatedPoint[]): number => {
  if (points.length < 2) return 0;
  const first = new Date(points[0].timestamp).getTime();
  const last = new Date(points[points.length - 1].timestamp).getTime();
  const span = last - first;
  if (span <= 0) return 0;
  const target = span / RESAMPLE_TARGET_POINTS;
  // Round up to a boundary a human reads on an axis.
  const steps = [60_000, 300_000, 900_000, 1_800_000, 3_600_000, 10_800_000, 21_600_000];
  return steps.find(step => step >= target) ?? steps[steps.length - 1];
};

// Keeps the last snapshot of each bucket: a state, sampled.
const resample = (points: AggregatedPoint[]): AggregatedPoint[] => {
  const interval = resampleInterval(points);
  if (interval <= 60_000) return points;
  const byBucket = new Map<number, AggregatedPoint>();
  for (const point of points) {
    const bucket = Math.floor(new Date(point.timestamp).getTime() / interval) * interval;
    const current = byBucket.get(bucket);
    if (!current || point.timestamp > current.timestamp) {
      byBucket.set(bucket, { ...point, timestamp: new Date(bucket).toISOString() });
    }
  }
  return Array.from(byBucket.values()).sort((a, b) => a.timestamp.localeCompare(b.timestamp));
};

export const aggregateSeries = (
  updateUUIDs: string[],
  pointsByUpdate: Record<string, UpdateHealthHistoryPoint[]>
) => {
  const byTimestamp = new Map<string, AggregatedPoint>();
  for (const updateUUID of updateUUIDs) {
    for (const point of pointsByUpdate[updateUUID] ?? []) {
      const current = byTimestamp.get(point.timestamp);
      if (current) {
        current.devicesOnUpdate += point.devicesOnUpdate;
        current.successfulDevices += point.successfulDevices;
        current.faultyDevices += point.faultyDevices;
        current.updateIssues += point.updateIssues;
        current.runtimeIssues += point.runtimeIssues;
        if (point.capturedAt > current.capturedAt) current.capturedAt = point.capturedAt;
      } else {
        byTimestamp.set(point.timestamp, { ...point });
      }
    }
  }
  return resample(
    Array.from(byTimestamp.values()).sort((a, b) => a.timestamp.localeCompare(b.timestamp))
  ).map(point => {
    const attempts = point.successfulDevices + point.faultyDevices;
    return {
      ...point,
      healthPercent: attempts > 0 ? (100 * point.successfulDevices) / attempts : null,
    };
  });
};
