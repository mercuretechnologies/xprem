// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useEffect, useId, useMemo, useState, type ReactNode } from 'react';
import { ParentSize } from '@visx/responsive';
import { ChevronDown } from 'lucide-react';
import {
  Annotation,
  AnnotationLineSubject,
  AreaSeries,
  Axis,
  buildChartTheme,
  GlyphSeries,
  Grid,
  LineSeries,
  Tooltip,
  XYChart,
  type GlyphProps,
} from '@visx/xychart';
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover';
import { cn } from '@/lib/utils';
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

export type { TimeSeriesPoint } from './timeSeries';

export type TimeSeriesDefinition = {
  key: string;
  label: string;
  color: string;
  points: TimeSeriesPoint[];
};

export type TimeSeriesAnnotation = {
  key: string;
  label: string;
  timestamp: Date;
};

export type TimeSeriesChartProps = {
  series: TimeSeriesDefinition[];
  annotations?: TimeSeriesAnnotation[];
  // What a marker stands for, plural. Folded markers read "3 update groups",
  // and only the caller knows which it plotted.
  annotationNoun?: string;
  // Makes the markers clickable: clicking one opens this content in a popover
  // anchored under it. Without it they stay decorative, which is what a label
  // saying "9 updates" and nothing else amounts to.
  renderAnnotationDetails?: (cluster: AnnotationCluster, close: () => void) => ReactNode;
  formatValue: (value: number) => string;
  formatAxisValue?: (value: number) => string;
  // The one curve to bring forward. Everything else fades rather than
  // disappears: a comparison you cannot see the other side of is not a
  // comparison, so the rest stays on screen as context.
  highlightedKey?: string | null;
  maximum?: number;
  // Frames durations around their real values, including zero and outliers.
  // Counters keep their zero baseline and a nondegenerate all-zero domain.
  frameToData?: boolean;
  timeDomain?: [Date, Date];
  pointIntervalMs?: number;
  // Discrete measurements use dots and a line, without an area implying that
  // missing measurements were observed continuously.
  showPoints?: boolean;
  ariaLabel: string;
  height?: number;
  className?: string;
};

const xAccessor = seriesTimestamp;
const yAccessor = (point: TimeSeriesPoint) => point.value;

const chartTheme = buildChartTheme({
  backgroundColor: 'hsl(var(--popover))',
  colors: ['hsl(var(--primary))'],
  gridColor: 'hsl(var(--border))',
  gridColorDark: 'hsl(var(--border))',
  gridStyles: {
    strokeOpacity: 0.55,
    strokeDasharray: '2 5',
  },
  svgLabelBig: {
    fill: 'hsl(var(--foreground))',
    fontFamily: 'Inter, system-ui, sans-serif',
    fontSize: 11,
  },
  svgLabelSmall: {
    fill: 'hsl(var(--muted-foreground))',
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    fontSize: 10,
  },
  htmlLabel: {
    color: 'hsl(var(--foreground))',
    fontFamily: 'Inter, system-ui, sans-serif',
    fontSize: 12,
  },
  xAxisLineStyles: {
    stroke: 'hsl(var(--border))',
  },
  yAxisLineStyles: {
    stroke: 'transparent',
  },
  xTickLineStyles: {
    stroke: 'transparent',
  },
  yTickLineStyles: {
    stroke: 'transparent',
  },
  tickLength: 0,
});

const formatCompactNumber = (value: number) =>
  Intl.NumberFormat(undefined, {
    notation: Math.abs(value) >= 1_000 ? 'compact' : 'standard',
    maximumFractionDigits: Math.abs(value) >= 1_000 ? 1 : 0,
  }).format(value);

const timestampFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

// visx installs focus/blur emitters only when callbacks are supplied; its
// internal handlers show and hide the tooltip before invoking these callbacks.
const enableGlyphFocus = () => undefined;

const PointGlyph = ({
  x,
  y,
  color,
  single,
  opacity = 1,
  ariaLabel,
  onFocus,
  onBlur,
  onPointerMove,
  onPointerOut,
  onPointerUp,
}: {
  x: number;
  y: number;
  color: string;
  single: boolean;
  opacity?: number;
  ariaLabel?: string;
} & Pick<
  GlyphProps<TimeSeriesPoint>,
  'onFocus' | 'onBlur' | 'onPointerMove' | 'onPointerOut' | 'onPointerUp'
>) => (
  <g
    pointerEvents={ariaLabel ? undefined : 'none'}
    opacity={opacity}
    tabIndex={ariaLabel ? 0 : undefined}
    role={ariaLabel ? 'img' : undefined}
    aria-label={ariaLabel}
    aria-hidden={ariaLabel ? undefined : true}
    className={
      ariaLabel
        ? 'focus:outline focus:outline-2 focus:outline-offset-2 focus:outline-primary'
        : undefined
    }
    onFocus={onFocus}
    onBlur={onBlur}
    onPointerMove={onPointerMove}
    onPointerOut={onPointerOut}
    onPointerUp={onPointerUp}>
    <circle
      cx={x}
      cy={y}
      r={single ? 6 : 3.5}
      fill="hsl(var(--background))"
      stroke={color}
      strokeWidth={single ? 2 : 1.5}
    />
    {single && <circle cx={x} cy={y} r={2.25} fill={color} />}
  </g>
);

export type AnnotationCluster = {
  key: string;
  label: string;
  timestamp: Date;
  // Everything folded into this marker. A marker reading "9 updates" is only
  // useful if you can find out which nine.
  members: TimeSeriesAnnotation[];
};

const clusterAnnotations = (
  annotations: TimeSeriesAnnotation[],
  start: number,
  end: number,
  width: number,
  noun: string
): AnnotationCluster[] => {
  const visible = annotations
    .filter(annotation => {
      const timestamp = annotation.timestamp.getTime();
      return Number.isFinite(timestamp) && timestamp >= start && timestamp <= end;
    })
    .sort((a, b) => a.timestamp.getTime() - b.timestamp.getTime());
  if (visible.length === 0) return [];

  // A badge is now a number, so markers only merge when they would genuinely
  // overlap rather than whenever their labels would.
  const maximumMarkers = Math.max(1, Math.floor((width - 52) / 34));
  const proximity = Math.max(1, (end - start) / maximumMarkers);
  const groups: TimeSeriesAnnotation[][] = [];
  for (const annotation of visible) {
    const group = groups[groups.length - 1];
    const previous = group?.[group.length - 1];
    if (
      group &&
      previous &&
      annotation.timestamp.getTime() - previous.timestamp.getTime() <= proximity
    ) {
      group.push(annotation);
    } else {
      groups.push([annotation]);
    }
  }
  return groups.map(group => {
    const latest = group[group.length - 1] as TimeSeriesAnnotation;
    return {
      key: group.map(annotation => annotation.key).join(':'),
      label: group.length === 1 ? latest.label : `${group.length} ${noun}`,
      timestamp: latest.timestamp,
      members: group,
    };
  });
};

// Where a marker badge is pinned, and where it ends. The plot keeps a 42px top
// margin when annotations are on, so the badge sits in that band rather than
// over the curves.
const ANNOTATION_LABEL_TOP = 8;
const ANNOTATION_LABEL_BOTTOM = 24;

export const TimeSeriesChart = ({
  series,
  annotations = [],
  annotationNoun = 'updates',
  renderAnnotationDetails,
  formatValue,
  formatAxisValue = formatCompactNumber,
  maximum,
  frameToData = false,
  timeDomain,
  pointIntervalMs,
  showPoints = false,
  ariaLabel,
  highlightedKey,
  height = 192,
  className,
}: TimeSeriesChartProps) => {
  const gradientPrefix = useId().replace(/:/g, '');
  // Only ever dims when something else is being pointed at, and never dims a
  // lone curve: fading the only line on screen would just look broken.
  const dimmed = (key: string) =>
    Boolean(highlightedKey) && highlightedKey !== key && series.length > 1;
  // The cluster only. Storing the pixel it was at would freeze the anchor at
  // the width the chart had when it was clicked, and ParentSize re-renders on
  // every resize: the marker moves, the popover stays behind.
  const [openAnnotation, setOpenAnnotation] = useState<AnnotationCluster | null>(null);
  // A new period, a new filter: whatever was open describes publishes that are
  // no longer on screen. Keyed on the markers themselves rather than on the
  // array, which a live refetch hands back new every time.
  const markerSignature = annotations.map(annotation => annotation.key).join('|');
  useEffect(() => setOpenAnnotation(null), [markerSignature]);
  const timestamps = series.flatMap(item =>
    item.points.map(point => point.timestamp.getTime()).filter(Number.isFinite)
  );
  const annotationTimestamps = annotations
    .map(annotation => annotation.timestamp.getTime())
    .filter(Number.isFinite);
  const domainTimestamps = [...timestamps, ...annotationTimestamps];
  const xDomain = seriesTimeDomain(domainTimestamps, timeDomain);
  const [start, end] = xDomain.map(date => date.getTime());
  const formatTime = timeAxisFormatter(start, end);
  const [yMinimum, yMaximum] = useMemo(
    () =>
      seriesValueDomain(
        series.flatMap(item => item.points.map(point => point.value)),
        frameToData,
        maximum
      ),
    [series, frameToData, maximum]
  );
  // Callers with sparse bucketed measurements supply the real interval. Other
  // charts retain their inferred spacing for cross-series tooltip matching.
  const bucketMs = (() => {
    if (pointIntervalMs && pointIntervalMs > 0) return pointIntervalMs;
    let smallest = Infinity;
    for (const item of series) {
      for (let index = 1; index < item.points.length; index += 1) {
        const gap =
          item.points[index].timestamp.getTime() - item.points[index - 1].timestamp.getTime();
        if (gap > 0 && gap < smallest) smallest = gap;
      }
    }
    return smallest;
  })();

  // Publication markers can still give an otherwise empty chart context.
  if (domainTimestamps.length === 0) return null;

  // The left margin holds the y labels, so it has to be as wide as the widest
  // of them. Fixed at 42px it fit a duration ("3.00s") and cut a device count
  // ("300,000") down to "00,000", which reads as a real number and is off by a
  // factor of three. The labels are monospace at 10px, so a character is 6px,
  // and the domain bounds bound the tick widths.
  const yLabelWidth =
    Math.max(formatAxisValue(yMinimum).length, formatAxisValue(yMaximum).length) * 6;
  const margin = {
    top: annotations.length > 0 ? 42 : 10,
    right: 10,
    bottom: 28,
    left: Math.max(42, Math.ceil(yLabelWidth) + 12),
  };
  // A tick label is centred on its instant, so one wider than the gap to the
  // next tick overlaps it. Same 6px monospace character as the y labels, and
  // the span decides the format, so both have to be read to know how many fit.
  const xLabelWidth =
    Math.max(formatTime.format(xDomain[0]).length, formatTime.format(xDomain[1]).length) * 6;
  const xTickCount = (plotWidth: number) =>
    Math.max(2, Math.min(5, Math.floor(plotWidth / (xLabelWidth + 20))));
  // Where a timestamp lands in the plot. The x scale is linear over an explicit
  // domain, so the markers can be placed without reaching into visx internals.
  // Two readings of the same point, on purpose: the badge is positioned in
  // pixels inside ParentSize, where the width is known, and the popover anchor
  // in percent through a calc(), so it keeps tracking its marker across a
  // resize that no re-render of the anchor would otherwise catch.
  const ratioOf = (timestamp?: Date) => {
    if (!timestamp) return 0;
    const [from, to] = [xDomain[0].getTime(), xDomain[1].getTime()];
    return to === from ? 0.5 : (timestamp.getTime() - from) / (to - from);
  };
  const positionOf = (timestamp: Date, width: number) =>
    margin.left + ratioOf(timestamp) * Math.max(0, width - margin.left - margin.right);

  return (
    <div className={cn('relative', className)} style={{ height }}>
      <ParentSize debounceTime={50}>
        {({ width, height: plotHeight }) => {
          if (width <= 0 || plotHeight <= 0) return null;
          // Clustered once, drawn twice: the vertical lines live inside the
          // SVG and the badges are HTML on top of it, but they must describe
          // the same markers.
          const markers = clusterAnnotations(annotations, start, end, width, annotationNoun);
          return (
            <>
              <XYChart
                accessibilityLabel={ariaLabel}
                width={width}
                height={plotHeight}
                margin={margin}
                theme={chartTheme}
                xScale={{ type: 'time', domain: xDomain }}
                yScale={{
                  type: 'linear',
                  domain: [yMinimum, yMaximum],
                  nice: true,
                  zero: yMinimum === 0,
                }}>
                <defs>
                  <clipPath id={`${gradientPrefix}-plot`}>
                    <rect
                      x={margin.left}
                      y={margin.top}
                      width={Math.max(0, width - margin.left - margin.right)}
                      height={Math.max(0, height - margin.top - margin.bottom)}
                    />
                  </clipPath>
                  {series.map((item, index) => (
                    <linearGradient
                      key={item.key}
                      id={`${gradientPrefix}-${index}`}
                      x1="0"
                      x2="0"
                      y1="0"
                      y2="1">
                      <stop offset="0%" stopColor={item.color} stopOpacity={0.2} />
                      <stop offset="100%" stopColor={item.color} stopOpacity={0.015} />
                    </linearGradient>
                  ))}
                </defs>
                <Grid columns={false} numTicks={3} />
                <Axis
                  orientation="bottom"
                  numTicks={xTickCount(Math.max(0, width - margin.left - margin.right))}
                  tickFormat={value => formatTime.format(value as Date)}
                  tickLabelProps={(_value, index, ticks) => ({
                    textAnchor:
                      index === 0 ? 'start' : index === ticks.length - 1 ? 'end' : 'middle',
                  })}
                  hideTicks
                />
                <Axis
                  orientation="left"
                  numTicks={3}
                  tickFormat={value => formatAxisValue(Number(value))}
                  hideAxisLine
                  hideTicks
                />
                {series.map((item, index) => {
                  const lineProps = {
                    stroke: item.color,
                    strokeWidth: dimmed(item.key)
                      ? 1.25
                      : highlightedKey === item.key
                        ? 2.75
                        : series.length > 1
                          ? 1.75
                          : 2.25,
                    strokeOpacity: dimmed(item.key) ? 0.22 : 1,
                    clipPath: `url(#${gradientPrefix}-plot)`,
                  };
                  return showPoints ? (
                    <LineSeries
                      key={`${item.key}-line`}
                      dataKey={`${item.key}-line`}
                      data={linePoints(item.points, pointIntervalMs)}
                      xAccessor={xAccessor}
                      yAccessor={yAccessor}
                      enableEvents={false}
                      {...lineProps}
                    />
                  ) : (
                    <AreaSeries
                      key={item.key}
                      dataKey={item.key}
                      data={item.points}
                      xAccessor={xAccessor}
                      yAccessor={yAccessor}
                      // Overlapping areas would hide comparisons between series.
                      fill={series.length > 1 ? 'transparent' : `url(#${gradientPrefix}-${index})`}
                      clipPath={`url(#${gradientPrefix}-plot)`}
                      renderLine
                      lineProps={lineProps}
                    />
                  );
                })}
                {series.map(item =>
                  showPoints || item.points.length === 1 ? (
                    <GlyphSeries
                      key={`${item.key}-points`}
                      dataKey={showPoints ? item.key : `${item.key}-single-point`}
                      data={item.points}
                      xAccessor={xAccessor}
                      yAccessor={yAccessor}
                      enableEvents={showPoints}
                      onFocus={showPoints ? enableGlyphFocus : undefined}
                      onBlur={showPoints ? enableGlyphFocus : undefined}
                      renderGlyph={({
                        x,
                        y,
                        datum,
                        onFocus,
                        onBlur,
                        onPointerMove,
                        onPointerOut,
                        onPointerUp,
                      }) => (
                        <PointGlyph
                          x={x}
                          y={y}
                          color={item.color}
                          single={item.points.length === 1}
                          opacity={dimmed(item.key) ? 0.22 : 1}
                          ariaLabel={
                            showPoints
                              ? pointDescription(datum, item.label, formatValue, date =>
                                  timestampFormatter.format(date)
                                )
                              : undefined
                          }
                          onFocus={onFocus}
                          onBlur={onBlur}
                          onPointerMove={onPointerMove}
                          onPointerOut={onPointerOut}
                          onPointerUp={onPointerUp}
                        />
                      )}
                    />
                  ) : null
                )}
                {markers.map(annotation => (
                  <Annotation
                    key={annotation.key}
                    datum={{ timestamp: annotation.timestamp, value: yMaximum }}
                    xAccessor={xAccessor}
                    yAccessor={yAccessor}
                    dx={0}
                    dy={-9}>
                    <AnnotationLineSubject
                      orientation="vertical"
                      stroke="hsl(var(--primary))"
                      strokeDasharray="3 5"
                      strokeOpacity={0.42}
                    />
                  </Annotation>
                ))}
                <Tooltip<TimeSeriesPoint>
                  snapTooltipToDatumX
                  showVerticalCrosshair
                  showSeriesGlyphs={!showPoints}
                  showDatumGlyph={showPoints}
                  renderGlyph={
                    showPoints
                      ? ({ x, y, key }) => (
                          <circle
                            cx={x}
                            cy={y}
                            r={5}
                            fill="hsl(var(--background))"
                            stroke={series.find(item => item.key === key)?.color}
                            strokeWidth={2}
                          />
                        )
                      : undefined
                  }
                  verticalCrosshairStyle={{
                    stroke: 'hsl(var(--muted-foreground))',
                    strokeDasharray: '3 4',
                    strokeOpacity: 0.65,
                  }}
                  glyphStyle={{
                    fill: 'hsl(var(--background))',
                    strokeWidth: 2,
                  }}
                  unstyled
                  applyPositionStyle
                  className="z-50 min-w-40 rounded-lg border bg-popover/95 p-2.5 text-popover-foreground shadow-elevated backdrop-blur"
                  renderTooltip={({ tooltipData }) => {
                    const nearest = tooltipData?.nearestDatum?.datum;
                    if (!nearest) return null;
                    return (
                      <div className="space-y-2">
                        <div className="font-mono text-[10px] text-muted-foreground">
                          {nearest.intervalStart && nearest.intervalEnd ? (
                            <>
                              {timestampFormatter.format(nearest.intervalStart)}
                              {' – '}
                              {timestampFormatter.format(nearest.intervalEnd)}
                            </>
                          ) : (
                            timestampFormatter.format(nearest.timestamp)
                          )}
                        </div>
                        <div className="space-y-1">
                          {series.map(item => {
                            const point = tooltipData?.datumByKey[item.key]?.datum;
                            // datumByKey hands back each series' CLOSEST point,
                            // which for a series that only starts hours later
                            // is still a point hours away. Reading it here
                            // would show its latest value under a timestamp
                            // where it had none, so a series that misses this
                            // bucket is left out of the tooltip entirely.
                            if (!point || !sameBucket(point, nearest, bucketMs)) return null;
                            return (
                              <div key={item.key} className="space-y-0.5 text-xs">
                                <div className="flex items-center justify-between gap-5">
                                  <span className="flex items-center gap-1.5 text-muted-foreground">
                                    <span
                                      className="h-1.5 w-1.5 rounded-full"
                                      style={{ backgroundColor: item.color }}
                                    />
                                    {item.label}
                                  </span>
                                  <span className="font-mono font-medium tabular-nums">
                                    {formatValue(point.value)}
                                  </span>
                                </div>
                                {point.intervalStart && (
                                  <div className="flex items-center justify-between gap-5 pl-3 text-[10px] text-muted-foreground">
                                    <span>Median (p50)</span>
                                    <span>{pointSampleSize(point)}</span>
                                  </div>
                                )}
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    );
                  }}
                />
              </XYChart>
              {/* Markers as HTML rather than visx labels: a real button carries
                  the theme colours, a chevron and a focus ring, none of which an
                  SVG text label drawn from the chart theme can do. */}
              <div className="pointer-events-none absolute inset-0">
                {markers.map(annotation => {
                  const shared =
                    'absolute -translate-x-1/2 whitespace-nowrap rounded-full bg-primary px-1.5 py-[3px] text-[10px] font-semibold leading-none tabular-nums text-primary-foreground';
                  // Kept inside the plot: a marker at either end would
                  // otherwise sit half outside it, and the one at the start
                  // was being cut off by the axis.
                  const edge = 16;
                  const position = {
                    left: Math.min(
                      Math.max(positionOf(annotation.timestamp, width), margin.left + edge),
                      Math.max(margin.left + edge, width - margin.right - edge)
                    ),
                    top: ANNOTATION_LABEL_TOP,
                  };
                  return renderAnnotationDetails ? (
                    <button
                      key={annotation.key}
                      type="button"
                      title={annotation.label}
                      onClick={() => setOpenAnnotation(annotation)}
                      style={position}
                      className={cn(
                        shared,
                        'pointer-events-auto inline-flex items-center gap-1 pr-1.5 shadow-sm transition hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1'
                      )}>
                      {annotation.members.length}
                      <ChevronDown className="h-3 w-3 opacity-70" />
                    </button>
                  ) : (
                    <span key={annotation.key} style={position} className={shared}>
                      {annotation.members.length}
                    </span>
                  );
                })}
              </div>
            </>
          );
        }}
      </ParentSize>
      {renderAnnotationDetails && (
        <Popover
          open={openAnnotation != null}
          onOpenChange={open => !open && setOpenAnnotation(null)}>
          {/* A zero-size anchor sitting under the marker that was clicked. The
              content itself is portalled, so the card's overflow-hidden cannot
              clip a list that is taller than the chart. */}
          {/* Positioned in CSS rather than in pixels, so the anchor tracks the
              marker through any resize: the plot area is the box minus the
              left and right margins, and the marker sits at its own share of
              the time domain inside it. */}
          <PopoverAnchor
            className="pointer-events-none absolute h-0 w-0"
            style={{
              left: `calc(${margin.left}px + ${ratioOf(openAnnotation?.timestamp)} * (100% - ${margin.left + margin.right}px))`,
              top: ANNOTATION_LABEL_BOTTOM,
            }}
          />
          <PopoverContent
            align="center"
            side="bottom"
            sideOffset={6}
            className="w-72 overflow-hidden p-0">
            {openAnnotation &&
              renderAnnotationDetails(openAnnotation, () => setOpenAnnotation(null))}
          </PopoverContent>
        </Popover>
      )}
    </div>
  );
};
