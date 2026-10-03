// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { histogramBuckets, histogramIntervalLabel, occurrenceTicks } from './errorHistogram';

test('histogram intervals preserve empty and current buckets and all occurrence counts', () => {
  const series = [
    { timestamp: '2026-10-03T10:00:00Z', count: 2 },
    { timestamp: '2026-10-03T10:01:00Z', count: 0 },
    { timestamp: '2026-10-03T10:02:00Z', count: 1 },
  ];
  const original = structuredClone(series);
  const buckets = histogramBuckets(series, 60, '2026-10-03T10:02:20Z');
  assert.deepEqual(
    buckets.map(bucket => bucket.count),
    [2, 0, 1]
  );
  assert.deepEqual(
    buckets.map(bucket => bucket.timestamp.toISOString()),
    series.map(point => new Date(point.timestamp).toISOString())
  );
  assert.deepEqual(
    buckets.map(bucket => bucket.end.toISOString()),
    ['2026-10-03T10:01:00.000Z', '2026-10-03T10:02:00.000Z', '2026-10-03T10:02:20.000Z']
  );
  assert.deepEqual(
    buckets.map(bucket => bucket.center.toISOString()),
    ['2026-10-03T10:00:30.000Z', '2026-10-03T10:01:30.000Z', '2026-10-03T10:02:10.000Z']
  );
  assert.equal(
    buckets.reduce((sum, bucket) => sum + bucket.count, 0),
    3
  );
  assert.deepEqual(series, original, 'the API series must not be mutated');
});

test('a bucket at or after the snapshot endpoint has a nonnegative interval', () => {
  const series = [
    { timestamp: '2026-10-03T10:00:00Z', count: 1 },
    { timestamp: '2026-10-03T10:01:00Z', count: 0 },
  ];
  const buckets = histogramBuckets(series, 60, '2026-10-03T10:00:00Z');
  for (const bucket of buckets) {
    assert.equal(bucket.end.getTime(), bucket.timestamp.getTime());
    assert.equal(bucket.center.getTime(), bucket.timestamp.getTime());
  }
});

test('without a snapshot endpoint the real bucket duration closes each interval', () => {
  const [bucket] = histogramBuckets([{ timestamp: '2026-10-03T10:00:00Z', count: 4 }], 4_380);
  assert.equal(bucket.end.toISOString(), '2026-10-03T11:13:00.000Z');
  assert.equal(bucket.center.toISOString(), '2026-10-03T10:36:30.000Z');
  assert.deepEqual(histogramBuckets([], 60), []);
});

test('fractional UTC and offset timestamps retain a partial last bucket at chart precision', () => {
  const buckets = histogramBuckets(
    [
      { timestamp: '2026-10-03T10:00:00.123456789Z', count: 2 },
      { timestamp: '2026-10-03T12:01:00.123456789+02:00', count: 3 },
    ],
    60,
    '2026-10-03T10:01:20.987654321Z'
  );
  assert.equal(buckets[0].timestamp.toISOString(), '2026-10-03T10:00:00.123Z');
  assert.equal(buckets[0].end.toISOString(), '2026-10-03T10:01:00.123Z');
  assert.equal(buckets[1].timestamp.toISOString(), '2026-10-03T10:01:00.123Z');
  assert.equal(buckets[1].end.toISOString(), '2026-10-03T10:01:20.987Z');
  assert.equal(buckets[1].center.toISOString(), '2026-10-03T10:01:10.555Z');
  assert.equal(
    buckets.reduce((total, bucket) => total + bucket.count, 0),
    5
  );
});

test('occurrence axes use unique evenly spaced integers including zero and the maximum', () => {
  for (const maximum of [0, 1, 2, 3, 4, 5, 13, 25, 100, 999, 1_000_000]) {
    const ticks = occurrenceTicks(maximum);
    assert.equal(ticks[0], 0);
    assert.ok(ticks[ticks.length - 1] >= maximum);
    assert.ok(ticks.length >= 3 && ticks.length <= 5);
    assert.ok(ticks.every(Number.isInteger));
    assert.equal(new Set(ticks).size, ticks.length);
    const spacing = ticks[1] - ticks[0];
    for (let index = 1; index < ticks.length; index += 1) {
      assert.equal(ticks[index] - ticks[index - 1], spacing);
    }
  }
  assert.deepEqual(occurrenceTicks(1), [0, 1, 2]);
});

test('interval labels describe the actual server grouping without rounding away seconds', () => {
  const cases: Array<[number, string]> = [
    [1, '1s intervals'],
    [59, '59s intervals'],
    [60, '1m intervals'],
    [61, '1m 1s intervals'],
    [3_600, '1h intervals'],
    [4_380, '1h 13m intervals'],
    [4_381, '1h 13m 1s intervals'],
    [86_400, '1d intervals'],
    [266_400, '3d 2h intervals'],
    [266_461, '3d 2h 1m 1s intervals'],
  ];
  for (const [seconds, expected] of cases) {
    assert.equal(histogramIntervalLabel(seconds), expected);
  }
});
