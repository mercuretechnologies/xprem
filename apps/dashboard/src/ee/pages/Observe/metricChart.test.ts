// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { scaleLinear } from '@visx/scale';
import { seriesValueDomain } from '@/ee/components/charts/timeSeries';
import { metricChartPoints, metricDuration } from './metricChart';

test('a sparse 24h series retains the zero median containing the 09:10 measurement', () => {
  const points = [
    { timestamp: '2026-10-03T21:00:00Z', value: 0.008, samples: 2, devices: 1 },
    { timestamp: '2026-10-04T08:00:00Z', value: 0.003, samples: 1, devices: 1 },
    { timestamp: '2026-10-04T09:00:00Z', value: 0, samples: 1, devices: 1 },
  ];
  const original = structuredClone(points);
  const plotted = metricChartPoints(points, {
    from: '2026-10-03T09:20:00Z',
    to: '2026-10-04T09:20:00Z',
    bucketSeconds: 900,
  });
  assert.equal(plotted.length, 3);
  assert.equal(plotted[2].value, 0);
  assert.equal(plotted[2].intervalStart?.toISOString(), '2026-10-04T09:00:00.000Z');
  assert.equal(plotted[2].intervalEnd?.toISOString(), '2026-10-04T09:15:00.000Z');
  assert.equal(plotted[2].timestamp.toISOString(), '2026-10-04T09:07:30.000Z');
  assert.equal(plotted[2].samples, 1);
  assert.deepEqual(points, original);
});

test('a current median is retained and its interval ends at the API snapshot', () => {
  const [point] = metricChartPoints(
    [{ timestamp: '2026-10-04T09:10:00Z', value: 0, samples: 1, devices: 1 }],
    { from: '2026-10-04T08:12:00Z', to: '2026-10-04T09:12:00Z', bucketSeconds: 300 }
  );
  assert.equal(point.intervalEnd?.toISOString(), '2026-10-04T09:12:00.000Z');
  assert.equal(point.timestamp.toISOString(), '2026-10-04T09:11:00.000Z');
  assert.equal(point.value, 0);
});

test('a first bucket is clipped to the requested window rather than plotted outside it', () => {
  const [point] = metricChartPoints(
    [{ timestamp: '2026-10-04T08:00:00Z', value: 0.1, samples: 2, devices: 2 }],
    { from: '2026-10-04T08:02:00Z', to: '2026-10-04T09:02:00Z', bucketSeconds: 300 }
  );
  assert.equal(point.intervalStart?.toISOString(), '2026-10-04T08:02:00.000Z');
  assert.equal(point.intervalEnd?.toISOString(), '2026-10-04T08:05:00.000Z');
  assert.equal(point.timestamp.toISOString(), '2026-10-04T08:03:30.000Z');
  assert.equal(point.devices, 2);
});

test('older responses without interval metadata retain every point', () => {
  const points = [
    { timestamp: '2026-10-04T08:00:00Z', value: 0, samples: 1, devices: 1 },
    { timestamp: '2026-10-04T09:00:00Z', value: 0.1, samples: 2, devices: 1 },
  ];
  assert.deepEqual(
    metricChartPoints(points).map(point => point.value),
    [0, 0.1]
  );
  assert.equal(metricChartPoints(points)[1].timestamp.toISOString(), '2026-10-04T09:00:00.000Z');
  assert.equal(metricChartPoints(points)[1].intervalStart, undefined);
});

test('submillisecond durations do not claim to be zero and axis labels remain distinct', () => {
  assert.equal(metricDuration(0), '0ms');
  assert.equal(metricDuration(0.0005), '0.5ms');
  assert.equal(metricDuration(0.000001), '<0.01ms');
  assert.equal(metricDuration(0.008), '8ms');
  assert.equal(metricDuration(0.576), '576ms');
  assert.equal(metricDuration(1.15), '1.15s');
});

test('tight millisecond axes use distinct labels around a single duration', () => {
  for (const value of [0, 0.000001, 0.0005, 0.001, 0.007, 0.576]) {
    const domain = seriesValueDomain([value], true);
    const ticks = scaleLinear({ domain, nice: true }).ticks(3);
    const labels = ticks.map(metricDuration);
    assert.equal(new Set(labels).size, labels.length, `duration ${value}: ${labels.join(', ')}`);
  }
});
