// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useId } from 'react';
import { ParentSize } from '@visx/responsive';
import { Axis, BarSeries, Tooltip, XYChart, buildChartTheme } from '@visx/xychart';
import type { ErrorSeriesPoint } from '@/lib/api';
import { exactNumber } from './format';
import { histogramBuckets, histogramIntervalLabel, occurrenceTicks } from './errorHistogram';

const chartTheme = buildChartTheme({
  backgroundColor: 'hsl(var(--popover))',
  colors: ['hsl(var(--primary))'],
  gridColor: 'hsl(var(--border))',
  gridColorDark: 'hsl(var(--border))',
  gridStyles: { strokeOpacity: 0.55, strokeDasharray: '2 5' },
  svgLabelSmall: {
    fill: 'hsl(var(--muted-foreground))',
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    fontSize: 10,
  },
  xAxisLineStyles: { stroke: 'hsl(var(--border))' },
  yAxisLineStyles: { stroke: 'transparent' },
  tickLength: 0,
});

const intervalTime = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

type HistogramProps = {
  series: ErrorSeriesPoint[];
  compact?: boolean;
  from?: string;
  to?: string;
  bucketSeconds?: number;
};

const DetailedHistogram = ({ series, from, to, bucketSeconds = 60 }: HistogramProps) => {
  const clipId = useId().replace(/:/g, '');
  const buckets = histogramBuckets(series, bucketSeconds, to);
  if (!buckets.length) {
    return <p className="py-12 text-center text-sm text-muted-foreground">No occurrences</p>;
  }
  const total = buckets.reduce((sum, point) => sum + point.count, 0);
  const ticks = occurrenceTicks(Math.max(...buckets.map(point => point.count)));
  const start = from ? new Date(from) : buckets[0].timestamp;
  const end = to ? new Date(to) : buckets[buckets.length - 1].end;
  const span = end.getTime() - start.getTime();
  const dateTicks = new Intl.DateTimeFormat(
    undefined,
    span < 60_000
      ? { hour: '2-digit', minute: '2-digit', second: '2-digit' }
      : span < 86_400_000
        ? { hour: '2-digit', minute: '2-digit' }
        : span < 365 * 86_400_000
          ? { month: 'short', day: 'numeric' }
          : { month: 'short', year: 'numeric' }
  );
  const margin = {
    top: 12,
    right: 24,
    bottom: 30,
    left: Math.max(36, exactNumber.format(ticks[ticks.length - 1]).length * 6 + 12),
  };

  return (
    <div>
      <div className="h-48">
        <ParentSize debounceTime={50}>
          {({ width, height }) => {
            if (width <= 0 || height <= 0) return null;
            const plotWidth = Math.max(0, width - margin.left - margin.right);
            const plotHeight = Math.max(0, height - margin.top - margin.bottom);
            return (
              <XYChart
                accessibilityLabel={`${exactNumber.format(total)} occurrences over time`}
                width={width}
                height={height}
                margin={margin}
                theme={chartTheme}
                xScale={{ type: 'time', domain: [start, end] }}
                yScale={{ type: 'linear', domain: [0, ticks[ticks.length - 1]], zero: true }}>
                <defs>
                  <clipPath id={clipId}>
                    <rect x={margin.left} y={margin.top} width={plotWidth} height={plotHeight} />
                  </clipPath>
                </defs>
                {ticks.map(value => (
                  <line
                    key={value}
                    x1={margin.left}
                    x2={width - margin.right}
                    y1={margin.top + plotHeight * (1 - value / ticks[ticks.length - 1])}
                    y2={margin.top + plotHeight * (1 - value / ticks[ticks.length - 1])}
                    stroke="hsl(var(--border))"
                    strokeDasharray="2 5"
                    strokeOpacity={0.55}
                  />
                ))}
                <Axis
                  orientation="left"
                  tickValues={ticks}
                  tickFormat={value => exactNumber.format(Number(value))}
                  hideAxisLine
                  hideTicks
                />
                <Axis
                  orientation="bottom"
                  numTicks={Math.max(2, Math.min(5, Math.floor(plotWidth / 100)))}
                  tickFormat={value => dateTicks.format(value as Date)}
                  hideTicks
                />
                <g clipPath={`url(#${clipId})`}>
                  <BarSeries
                    dataKey="occurrences"
                    data={buckets}
                    // visx also probes just beyond the final datum when finding
                    // the nearest bar, including inside the partial last interval.
                    xAccessor={point => point?.center}
                    yAccessor={point => point.count}
                    barPadding={Math.max(0.25, 1 - (14 * buckets.length) / plotWidth)}
                    radius={2}
                    radiusTop
                    // visx only makes bars focusable when focus handlers exist.
                    // Its series handler displays the same tooltip on focus.
                    onFocus={() => undefined}
                    onBlur={() => undefined}
                  />
                </g>
                <Tooltip<(typeof buckets)[number]>
                  snapTooltipToDatumX
                  showVerticalCrosshair
                  showDatumGlyph
                  verticalCrosshairStyle={{
                    stroke: 'hsl(var(--muted-foreground))',
                    strokeDasharray: '3 4',
                    strokeOpacity: 0.65,
                  }}
                  glyphStyle={{ fill: 'hsl(var(--background))', strokeWidth: 2 }}
                  unstyled
                  applyPositionStyle
                  className="z-50 rounded-lg border bg-popover/95 p-3 text-popover-foreground shadow-elevated backdrop-blur"
                  renderTooltip={({ tooltipData }) => {
                    const point = tooltipData?.nearestDatum?.datum;
                    if (!point) return null;
                    return (
                      <div role="tooltip" className="space-y-2">
                        <div className="space-y-0.5 font-mono text-[10px] text-muted-foreground">
                          <div>{intervalTime.format(point.timestamp)}</div>
                          <div>→ {intervalTime.format(point.end)}</div>
                        </div>
                        <div className="flex items-center gap-2 text-sm font-medium tabular-nums">
                          <span className="h-2 w-2 rounded-sm bg-primary" />
                          {exactNumber.format(point.count)}{' '}
                          {point.count === 1 ? 'occurrence' : 'occurrences'}
                        </div>
                      </div>
                    );
                  }}
                />
              </XYChart>
            );
          }}
        </ParentSize>
      </div>
      <p className="mt-2 text-[11px] text-muted-foreground">
        {histogramIntervalLabel(bucketSeconds)}
      </p>
      <div className="sr-only">
        <table>
          <caption>Occurrences over time</caption>
          <thead>
            <tr>
              <th scope="col">Interval start</th>
              <th scope="col">Interval end</th>
              <th scope="col">Occurrences</th>
            </tr>
          </thead>
          <tbody>
            {buckets.map((point, index) => (
              <tr key={index}>
                <td>{intervalTime.format(point.timestamp)}</td>
                <td>{intervalTime.format(point.end)}</td>
                <td>{exactNumber.format(point.count)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
};

// Keep every bucket, including the current one: the chart must account for
// exactly the same occurrences as the counters.
export const OccurrenceHistogram = ({
  series,
  compact = false,
  from,
  to,
  bucketSeconds,
}: HistogramProps) => {
  const titleId = useId();
  if (!compact) {
    return <DetailedHistogram series={series} from={from} to={to} bucketSeconds={bucketSeconds} />;
  }
  const max = Math.max(0, ...series.map(point => point.count));
  const total = series.reduce((sum, point) => sum + point.count, 0);
  const height = 32;
  const width = 600;
  const step = width / Math.max(1, series.length);
  return (
    <div className="w-28">
      <svg
        role="img"
        aria-labelledby={titleId}
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        className="h-8 w-full text-primary">
        <title id={titleId}>{exactNumber.format(total)} occurrences over time</title>
        {series.map((point, index) => {
          const barHeight = (point.count / Math.max(1, max)) * (height - 2);
          return (
            <rect
              key={point.timestamp}
              x={index * step}
              y={height - barHeight}
              width={Math.max(0.5, step * 0.8)}
              height={barHeight}
              fill="currentColor"
              opacity="0.75">
              <title>
                {new Date(point.timestamp).toLocaleString()} · {exactNumber.format(point.count)}{' '}
                occurrences
              </title>
            </rect>
          );
        })}
        <line x1="0" y1={height} x2={width} y2={height} stroke="currentColor" opacity="0.2" />
      </svg>
    </div>
  );
};
