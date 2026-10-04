// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { scaleLinear, scaleTime } from '@visx/scale';
import { metricChartPoints } from '../../pages/Observe/metricChart';
import {
  linePoints,
  pointDescription,
  pointSampleSize,
  sameBucket,
  seriesTimeDomain,
  seriesTimestamp,
  seriesValueDomain,
  timeAxisFormatter,
  type TimeSeriesPoint,
} from './timeSeries';

const point = (timestamp: string, value = 0): TimeSeriesPoint => ({
  timestamp: new Date(timestamp),
  value,
});

test('duration domains show milliseconds and every outlier without the counter floor', () => {
  const values = [0, 0.001, 0.012, 0.02];
  const [minimum, maximum] = seriesValueDomain(values, true);
  assert.equal(minimum, 0);
  assert.ok(maximum >= 0.02 && maximum < 0.025);
  const durations = [...Array<number>(99).fill(0.35), 12];
  assert.ok(seriesValueDomain(durations, true)[1] > 12, 'a real outlier stays inside the plot');
});

test('zero duration domains remain nondegenerate while counter and explicit domains stay intact', () => {
  assert.deepEqual(seriesValueDomain([0, 0, 0], true), [0, 0.001]);
  assert.deepEqual(seriesValueDomain([0, 1]), [0, 2.16]);
  assert.deepEqual(seriesValueDomain([30, 65], true, 100), [0, 100]);
  const [minimum, maximum] = seriesValueDomain([0.35, 0.73], true);
  assert.ok(minimum > 0 && minimum < 0.35);
  assert.ok(maximum > 0.73);
});

test('tiny positive durations retain a readable millisecond axis and zero baseline', () => {
  const [minimum, maximum] = seriesValueDomain([0.000001, 0.000008], true);
  assert.equal(minimum, 0);
  assert.ok(maximum >= 0.001 && maximum < 0.002);
});

test('an explicit period stays fixed across sparse series and publication annotations', () => {
  const domain: [Date, Date] = [new Date('2026-10-03T09:00:00Z'), new Date('2026-10-04T09:00:00Z')];
  const original = domain.map(date => date.getTime());
  const timestamps = [new Date('2026-10-04T08:45:00Z').getTime(), domain[0].getTime() - 86_400_000];
  assert.deepEqual(seriesTimeDomain(timestamps, domain), domain);
  assert.deepEqual(
    domain.map(date => date.getTime()),
    original
  );
  const timestamp = new Date('2026-10-04T08:45:00Z').getTime();
  assert.deepEqual(
    seriesTimeDomain([timestamp]).map(date => date.getTime()),
    [timestamp - 30_000, timestamp + 30_000]
  );
});

test('missing metric buckets break the line while keeping every real zero point and its metadata', () => {
  const points = [
    point('2026-10-04T08:00:00Z', 0.012),
    point('2026-10-04T08:15:00Z'),
    { ...point('2026-10-04T09:00:00Z'), samples: 3, devices: 2 },
  ];
  const original = structuredClone(points);
  const plotted = linePoints(points, 15 * 60_000);
  assert.equal(plotted.length, 4);
  assert.ok(Number.isNaN(plotted[2].value));
  assert.deepEqual(
    plotted.filter(entry => Number.isFinite(entry.value)),
    points
  );
  assert.deepEqual(points, original);
  assert.deepEqual(linePoints(points), points, 'other consumers retain their continuous lines');
});

test('actual interval boundaries connect partial adjacent buckets and split an empty interval', () => {
  const bucket = (from: string, to: string): TimeSeriesPoint => {
    const intervalStart = new Date(from);
    const intervalEnd = new Date(to);
    return {
      timestamp: new Date((intervalStart.getTime() + intervalEnd.getTime()) / 2),
      value: 0,
      intervalStart,
      intervalEnd,
    };
  };
  const points = [
    bucket('2026-10-04T09:03:00Z', '2026-10-04T09:15:00Z'),
    bucket('2026-10-04T09:15:00Z', '2026-10-04T09:30:00Z'),
    bucket('2026-10-04T09:45:00Z', '2026-10-04T09:46:00Z'),
  ];
  const plotted = linePoints(points, 15 * 60_000);
  assert.equal(plotted.length, 4);
  assert.equal(plotted[1], points[1]);
  assert.ok(Number.isNaN(plotted[2].value));
});

test('tooltips only compare the same real bucket, even when each series has one point', () => {
  const first = point('2026-10-04T09:07:30Z');
  const next = point('2026-10-04T09:22:30Z');
  assert.equal(sameBucket(first, first, 15 * 60_000), true);
  assert.equal(sameBucket(next, first, 15 * 60_000), false);
  assert.equal(sameBucket(next, first), false);
  const intervalStart = new Date('2026-10-04T09:00:00Z');
  assert.equal(sameBucket({ ...first, intervalStart }, { ...next, intervalStart }, 1), true);
  assert.equal(
    sameBucket(
      { ...first, intervalStart },
      { ...first, intervalStart: new Date('2026-10-04T08:45:00Z') },
      Infinity
    ),
    false
  );
});

test('time ticks distinguish dates when the selected period crosses midnight', () => {
  const start = new Date(2026, 9, 3, 23, 30).getTime();
  const end = new Date(2026, 9, 4, 0, 30).getTime();
  assert.equal(timeAxisFormatter(start, end).resolvedOptions().day, 'numeric');
  assert.equal(timeAxisFormatter(start, end + 23 * 3_600_000).resolvedOptions().day, 'numeric');
  assert.equal(timeAxisFormatter(start - 3_600_000, start).resolvedOptions().day, undefined);
});

test('point descriptions identify the interval and singular or plural measurement counts', () => {
  const measurement = {
    ...point('2026-10-04T09:07:30Z'),
    intervalStart: new Date('2026-10-04T09:00:00Z'),
    intervalEnd: new Date('2026-10-04T09:15:00Z'),
    samples: 1,
    devices: 1,
  };
  assert.equal(pointSampleSize(measurement), '1 measurement · 1 device');
  assert.equal(
    pointSampleSize({ ...measurement, samples: 2, devices: 3 }),
    '2 measurements · 3 devices'
  );
  assert.equal(pointSampleSize({ ...measurement, samples: undefined, devices: undefined }), '');
  const label = pointDescription(
    measurement,
    'Cold launch',
    value => `${value}ms`,
    date => date.toISOString()
  );
  assert.match(label, /2026-10-04T09:00:00\.000Z to 2026-10-04T09:15:00\.000Z/);
  assert.match(label, /Cold launch.*Median \(p50\) 0ms.*1 measurement · 1 device/);
  assert.match(
    pointDescription(
      measurement,
      'Cold launch',
      value => `${value}ms`,
      date => date.toISOString(),
      true
    ),
    /Latest interval/
  );
  assert.equal(
    pointDescription(
      point('2026-10-04T09:00:00Z', 2),
      'Devices',
      value => `${value}`,
      date => date.toISOString()
    ),
    'Devices, 2026-10-04T09:00:00.000Z, 2'
  );
});

test('the installed visx nearest-X picker can hover after a lone zero point in the full period', () => {
  // visx's picker is private; exercise the installed implementation that calls
  // the accessor with data[data.length] when a pointer passes the final point.
  const require = createRequire(join(process.cwd(), 'package.json'));
  const findNearestDatumX = require(
    join(dirname(require.resolve('@visx/xychart')), 'utils/findNearestDatumX.js')
  ).default;
  const measurement = point('2026-10-04T09:11:00Z');
  const picked = findNearestDatumX({
    dataKey: 'duration',
    data: [measurement],
    xAccessor: seriesTimestamp,
    yAccessor: (entry: TimeSeriesPoint) => entry.value,
    xScale: scaleTime({
      domain: [new Date('2026-10-04T08:12:00Z'), new Date('2026-10-04T09:12:00Z')],
      range: [0, 500],
    }),
    yScale: scaleLinear({ domain: [0, 0.001], range: [200, 0] }),
    point: { x: 499, y: 200 },
    width: 500,
    height: 200,
  });
  assert.equal(picked.datum, measurement);
  assert.equal(picked.datum.value, 0);
});

test('a thirty-day chart retains the final partial bucket with one submillisecond or zero measurement', () => {
  const require = createRequire(join(process.cwd(), 'package.json'));
  const React = require('react');
  const { renderToStaticMarkup } = require('react-dom/server');
  const visx = dirname(require.resolve('@visx/xychart'));
  const { BaseGlyphSeries } = require(join(visx, 'components/series/private/BaseGlyphSeries.js'));
  const DataContext = require(join(visx, 'context/DataContext.js')).default;
  const window = { from: '2026-09-04T07:15:00Z', to: '2026-10-04T07:15:00Z', bucketSeconds: 21600 };
  for (const latestValue of [0.000487583, 0]) {
    const points = metricChartPoints(
      [
        { timestamp: '2026-09-05T06:00:00Z', value: 0.4, samples: 2, devices: 1 },
        { timestamp: '2026-10-03T18:00:00Z', value: 0.001, samples: 1, devices: 1 },
        { timestamp: '2026-10-04T06:00:00Z', value: latestValue, samples: 1, devices: 1 },
      ],
      window
    );
    const xScale = scaleTime({
      domain: [new Date(window.from), new Date(window.to)],
      range: [42, 690],
    });
    const domain = seriesValueDomain(
      points.map(point => point.value),
      true
    );
    const yScale = scaleLinear({ domain, range: [172, 42], nice: true, zero: domain[0] === 0 });
    type PlottedGlyph = { key: string; datum: TimeSeriesPoint; x: number; y: number };
    let plotted: PlottedGlyph[] = [];
    const rendered = renderToStaticMarkup(
      React.createElement(
        DataContext.Provider,
        {
          value: { theme: {}, width: 700, height: 200, xScale, yScale, dataRegistry: {} },
        },
        React.createElement(BaseGlyphSeries, {
          data: points,
          dataKey: 'duration',
          xScale,
          yScale,
          xAccessor: seriesTimestamp,
          yAccessor: (point: TimeSeriesPoint) => point.value,
          enableEvents: false,
          renderGlyphs: ({ glyphs }: { glyphs: PlottedGlyph[] }) => {
            plotted = glyphs;
            return glyphs.map(glyph =>
              React.createElement('circle', {
                key: glyph.key,
                cx: glyph.x,
                cy: glyph.y,
                'data-latest': glyph.datum === points[points.length - 1],
              })
            );
          },
        })
      )
    );
    assert.equal(plotted.length, 3);
    // The installed visx glyph payload preserves datum identity, but does not
    // supply the index its TypeScript interface claims to provide.
    assert.equal(plotted.filter(glyph => glyph.datum === points[points.length - 1]).length, 1);
    assert.equal((rendered.match(/data-latest="true"/g) ?? []).length, 1);
    const latest = plotted[plotted.length - 1];
    assert.equal(latest.datum.value, latestValue);
    assert.equal(latest.datum.samples, 1);
    assert.equal(latest.datum.devices, 1);
    assert.equal(latest.datum.intervalStart?.toISOString(), '2026-10-04T06:00:00.000Z');
    assert.equal(latest.datum.intervalEnd?.toISOString(), '2026-10-04T07:15:00.000Z');
    assert.ok(
      latest.x - 6 >= 0 && latest.x + 6 <= 700,
      'the larger latest glyph fits inside the SVG'
    );
    assert.ok(latest.y - 6 >= 0 && latest.y + 6 <= 200);
  }
});
