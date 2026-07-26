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
  Tooltip,
  XYChart,
} from '@visx/xychart';
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover';
import { cn } from '@/lib/utils';

export type TimeSeriesPoint = {
  timestamp: Date;
  value: number;
};

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
  // Frames the axis around the data instead of anchoring it at zero, and caps
  // it at the 98th percentile. For durations, where nothing starts at zero and
  // one 12-second outlier flattens everything else. Off by default: on a
  // counter, zero is the reference and the spike is the information, so both
  // of those would be lies.
  frameToData?: boolean;
  ariaLabel: string;
  height?: number;
  className?: string;
};

const xAccessor = (point: TimeSeriesPoint) => point.timestamp;
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

const timeFormatter = (start: number, end: number) => {
  const spansMultipleDays = end - start >= 24 * 60 * 60 * 1_000;
  return new Intl.DateTimeFormat(undefined, {
    ...(spansMultipleDays ? { month: 'short', day: 'numeric' } : {}),
    hour: '2-digit',
    minute: '2-digit',
  });
};

const timestampFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

// A single unlucky bucket can sit three times above every other point, and
// framing the axis on the raw maximum then flattens the whole comparison into
// a thin band. Percentiles frame what the data does; the outlier still draws,
// it just no longer dictates the scale.
const quantile = (sorted: number[], fraction: number) => {
  if (sorted.length === 0) return 0;
  const position = (sorted.length - 1) * fraction;
  const lower = Math.floor(position);
  const upper = Math.ceil(position);
  if (lower === upper) return sorted[lower];
  return sorted[lower] + (sorted[upper] - sorted[lower]) * (position - lower);
};

const SinglePointGlyph = ({ x, y, color }: { x: number; y: number; color: string }) => (
  <g pointerEvents="none">
    <circle cx={x} cy={y} r={6} fill="hsl(var(--background))" stroke={color} strokeWidth={2} />
    <circle cx={x} cy={y} r={2.25} fill={color} />
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
  const now = Date.now();
  const start = domainTimestamps.length > 0 ? Math.min(...domainTimestamps) : now;
  const end = domainTimestamps.length > 0 ? Math.max(...domainTimestamps) : now;
  const xDomain =
    start === end
      ? [new Date(start - 30_000), new Date(end + 30_000)]
      : [new Date(start), new Date(end)];
  const formatTime = timeFormatter(start, end);
  // Sorted once: both bounds below read the same distribution, and rebuilding
  // it in each of them made the two memos look independent when they are not.
  const sortedValues = useMemo(
    () =>
      series
        .flatMap(item => item.points.map(point => point.value))
        .sort((left, right) => left - right),
    [series]
  );
  const calculatedMaximum = useMemo(() => {
    if (maximum != null) return maximum;
    const values = sortedValues;
    if (values.length === 0) return 2;
    const highest = values[values.length - 1];
    // A counter is read against zero and its peak is the point, so the axis
    // covers the real maximum. The floor of 2 keeps an all-zero series (a
    // healthy update reporting no faults) pinned to the bottom of the plot
    // instead of collapsing the domain to [0, 0), which puts a flat zero line
    // through the middle of the chart.
    if (!frameToData) return Math.max(2, highest);
    // Enough points for a percentile to mean anything, otherwise the maximum
    // IS the data.
    const top = values.length >= 12 ? quantile(values, 0.98) : highest;
    return Math.max(2, top, highest * 0.25);
  }, [maximum, sortedValues, frameToData]);
  const yMaximum = maximum ?? calculatedMaximum * 1.08;
  // Durations rarely start at zero. Anchoring the axis there squeezes six
  // series that all sit between 350ms and 730ms into a twelve-pixel band,
  // where a 70% gap between two devices looks like no gap at all. Only under
  // frameToData: zero is kept whenever the data reaches down to it, when the
  // caller fixed the maximum.
  const yMinimum = useMemo(() => {
    if (maximum != null || !frameToData) return 0;
    if (sortedValues.length === 0) return 0;
    const lowest = sortedValues[0];
    // The ceiling BEFORE the 8% headroom, which is what the low-to-high ratio
    // has to measure. Reading it back off yMaximum meant dividing the margin
    // out again three lines after applying it.
    const highest = calculatedMaximum;
    if (lowest <= 0 || highest <= 0) return 0;
    return lowest / highest > 0.35 ? lowest * 0.9 : 0;
  }, [maximum, sortedValues, calculatedMaximum, frameToData]);
  // The spacing the series are bucketed at, taken as the smallest real gap:
  // it is what decides whether a point belongs to the timestamp being hovered.
  const bucketMs = (() => {
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

  // Annotations count: they widen the x domain a few lines up, and a period
  // with publish markers but no telemetry is exactly when seeing where the
  // publishes landed matters.
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
                  {series.map(item => (
                    <linearGradient
                      key={item.key}
                      id={`${gradientPrefix}-${item.key}`}
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
                  numTicks={width < 420 ? 3 : 5}
                  tickFormat={value => formatTime.format(value as Date)}
                  hideTicks
                />
                <Axis
                  orientation="left"
                  numTicks={3}
                  tickFormat={value => formatAxisValue(Number(value))}
                  hideAxisLine
                  hideTicks
                />
                {series.map(item => (
                  <AreaSeries
                    key={item.key}
                    dataKey={item.key}
                    data={item.points}
                    xAccessor={xAccessor}
                    yAccessor={yAccessor}
                    // The gradient under the curve reads well for a single
                    // series and turns into an opaque pile as soon as several
                    // overlap, hiding the very comparison the chart is for.
                    fill={series.length > 1 ? 'transparent' : `url(#${gradientPrefix}-${item.key})`}
                    // The ceiling sits on the 98th percentile so one outlier
                    // cannot flatten the comparison; clipping keeps whatever
                    // sits above it inside the plot instead of drawing over
                    // the axis.
                    clipPath={`url(#${gradientPrefix}-plot)`}
                    renderLine
                    lineProps={{
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
                    }}
                  />
                ))}
                {series.map(item =>
                  item.points.length === 1 ? (
                    <GlyphSeries
                      key={`${item.key}-single-point`}
                      dataKey={`${item.key}-single-point`}
                      data={item.points}
                      xAccessor={xAccessor}
                      yAccessor={yAccessor}
                      enableEvents={false}
                      renderGlyph={({ x, y }) => (
                        <SinglePointGlyph x={x} y={y} color={item.color} />
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
                  showSeriesGlyphs
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
                          {timestampFormatter.format(nearest.timestamp)}
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
                            if (
                              !point ||
                              Math.abs(point.timestamp.getTime() - nearest.timestamp.getTime()) >
                                bucketMs / 2
                            )
                              return null;
                            return (
                              <div
                                key={item.key}
                                className="flex items-center justify-between gap-5 text-xs">
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
