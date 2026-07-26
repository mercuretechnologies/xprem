import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Activity, AlertTriangle, Users } from 'lucide-react';
import { api, type ObserveBreakdownDimension } from '@/lib/api';
import { Skeleton } from '@/components/ui/skeleton';
import {
  TimeSeriesChart,
  type TimeSeriesAnnotation,
  type TimeSeriesChartProps,
} from '@/components/charts/TimeSeriesChart';
import { liveInterval, type ObserveFilters } from './filters';
import { dimensionSpec, seriesColors } from './dimensions';
import { HealthRankTable } from '@/components/HealthRankTable';
import { exactNumber, withoutPartialBucket } from './format';

type Metric = 'health' | 'adoption' | 'faults';

const metricOptions: Array<{
  key: Metric;
  label: string;
  description: string;
  icon: typeof Activity;
}> = [
  {
    key: 'health',
    label: 'Health',
    description: 'Successful devices across all attempts',
    icon: Activity,
  },
  {
    key: 'adoption',
    label: 'Adoption',
    description: 'Devices currently running the selected update groups',
    icon: Users,
  },
  {
    key: 'faults',
    label: 'Faults',
    description: 'Devices that failed to launch',
    icon: AlertTriangle,
  },
];

// Segment values are raw column values, so they read like the rest of the page:
// a board name becomes a phone, an OS version gets its OS. One catalogue
// decides that, the same one the breakdown tables use. 'unknown' is the only
// value it cannot name: the query puts it there for a device with no telemetry
// at all, so no dimension owns it.
const segmentLabelFor = (dimension: ObserveBreakdownDimension, value: string) =>
  value === 'unknown' ? 'No telemetry' : dimensionSpec(dimension).labelFor(value);

export const HealthBySegment = ({
  filters,
  updateUUIDs,
  dimension,
  annotations,
  renderAnnotationDetails,
}: {
  filters: ObserveFilters;
  updateUUIDs: string[];
  dimension: ObserveBreakdownDimension;
  annotations: TimeSeriesAnnotation[];
  renderAnnotationDetails?: TimeSeriesChartProps['renderAnnotationDetails'];
}) => {
  const [metric, setMetric] = useState<Metric>('health');
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const segmentsQuery = useQuery({
    queryKey: [
      'observe',
      'health-segments',
      api.getAppId(),
      updateUUIDs,
      dimension,
      filters.query.from,
    ],
    queryFn: () => api.getUpdateHealthSegments(updateUUIDs, dimension, filters.query.from),
    enabled: updateUUIDs.length > 0,
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    // Only while the question stays the same. Held across a change of
    // dimension, the previous answer draws one dimension's curves under the
    // next one's legend.
    // Index 4, not 3: the key starts with 'observe'. Comparing index 3 reads
    // updateUUIDs, an array, which never equals a string, so the placeholder
    // was always discarded and the chart re-skeletoned on every refetch.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey?.[4] === dimension ? previous : undefined,
  });

  const series = useMemo(() => {
    const segments = Object.entries(segmentsQuery.data?.segments ?? {});
    return segments
      .map(([segment, points], index) => ({
        key: segment,
        label: segmentLabelFor(dimension, segment),
        color: seriesColors[index % seriesColors.length],
        latest: points[points.length - 1],
        points: withoutPartialBucket(
          points.flatMap(point => {
            const value =
              metric === 'health'
                ? point.healthPercent
                : metric === 'adoption'
                  ? point.devicesOnUpdate
                  : point.faultyDevices;
            if (value == null) return [];
            return [{ timestamp: new Date(point.timestamp), value }];
          })
        ),
      }))
      .filter(entry => entry.points.length > 0);
  }, [dimension, metric, segmentsQuery.data?.segments]);

  const ranked = useMemo(
    () =>
      series.map(entry => ({
        key: entry.key,
        label: entry.label,
        color: entry.color,
        devices: entry.latest?.devicesOnUpdate ?? 0,
        faulty: entry.latest?.faultyDevices ?? 0,
        health: entry.latest?.healthPercent ?? null,
      })),
    [series]
  );

  if (segmentsQuery.isLoading) return <Skeleton className="h-72 w-full rounded-xl" />;
  if (segmentsQuery.isError || segmentsQuery.data?.available === false) return null;

  const selected = metricOptions.find(option => option.key === metric) ?? metricOptions[0];
  const formatValue =
    metric === 'health'
      ? (value: number) => `${value.toFixed(1)}%`
      : (value: number) => exactNumber.format(Math.round(value));

  return (
    <section className="space-y-3">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium">
            Health over time, by {dimensionSpec(dimension).label.toLowerCase()}
          </h3>
          <p className="text-xs text-muted-foreground">
            Rebuilt from the adoption and failure events of each device.
          </p>
        </div>
      </div>

      <div className="overflow-hidden rounded-xl border bg-card shadow-card">
        <div className="grid grid-cols-3 border-b">
          {metricOptions.map(option => {
            const Icon = option.icon;
            const active = option.key === metric;
            return (
              <button
                key={option.key}
                type="button"
                aria-pressed={active}
                onClick={() => setMetric(option.key)}
                className={`flex items-center justify-center gap-2 py-2.5 text-sm transition ${
                  active
                    ? 'bg-muted/40 font-medium text-foreground'
                    : 'text-muted-foreground hover:text-foreground'
                }`}>
                <Icon className="h-3.5 w-3.5" />
                {option.label}
              </button>
            );
          })}
        </div>

        <div className="px-4 pt-3">
          <span className="text-xs text-muted-foreground">{selected.description}</span>
        </div>

        <div className="px-2 pb-2">
          {series.length > 0 ? (
            <TimeSeriesChart
              series={series}
              annotations={annotations}
              annotationNoun="update groups"
              renderAnnotationDetails={renderAnnotationDetails}
              formatValue={formatValue}
              formatAxisValue={formatValue}
              // Health is a percentage of a whole, so the axis is the whole,
              // exactly like the per-update chart it splits. Adoption and
              // faults are counts and keep their zero baseline.
              maximum={metric === 'health' ? 100 : undefined}
              highlightedKey={highlighted}
              ariaLabel={`${selected.label} by segment over time`}
              height={220}
            />
          ) : (
            <div className="flex h-[220px] items-center justify-center text-sm text-muted-foreground">
              No device reported this segment in the window.
            </div>
          )}
        </div>

        <HealthRankTable
          dimensionLabel={dimensionSpec(dimension).label}
          rows={ranked}
          onSelect={key => filters.setFilters(dimensionSpec(dimension).filtersFor(key))}
          onHover={setHighlighted}
        />
      </div>
    </section>
  );
};
