// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ObserveMetricPoint, ObserveMetricWindow } from '@/lib/api';
import type { TimeSeriesPoint } from '@/ee/components/charts/TimeSeriesChart';
import { duration } from './format';

// Median durations remain meaningful while an interval is filling. Unlike
// event counts, they do not fall towards zero just because it is incomplete.
export const metricChartPoints = (
  points: ObserveMetricPoint[],
  window?: ObserveMetricWindow
): TimeSeriesPoint[] =>
  points.map(point => {
    const timestamp = new Date(point.timestamp);
    const measurement = {
      timestamp,
      value: point.value,
      samples: point.samples,
      devices: point.devices,
    };
    if (!window) return measurement;
    const start = Math.max(timestamp.getTime(), new Date(window.from).getTime());
    const end = Math.max(
      start,
      Math.min(timestamp.getTime() + window.bucketSeconds * 1_000, new Date(window.to).getTime())
    );
    return {
      ...measurement,
      timestamp: new Date(start + (end - start) / 2),
      intervalStart: new Date(start),
      intervalEnd: new Date(end),
    };
  });

export const metricDuration = (value: number) => {
  if (value > 0 && value < 1) {
    const milliseconds = value * 1_000;
    return milliseconds < 0.01 ? '<0.01ms' : `${Number(milliseconds.toFixed(2))}ms`;
  }
  return duration(value);
};
