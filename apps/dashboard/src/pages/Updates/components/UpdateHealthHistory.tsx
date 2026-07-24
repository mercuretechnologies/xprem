import { useMemo, useState } from 'react';
import type { ComponentType, SVGProps } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Activity, AlertTriangle, Users } from 'lucide-react';
import { TimeSeriesChart, TimeSeriesDefinition } from '@/components/charts/TimeSeriesChart';
import { Skeleton } from '@/components/ui/skeleton';
import { api, UpdateHealthHistoryPoint } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { cn } from '@/lib/utils';

export type HealthHistorySeries = {
  key: string;
  label: string;
  updateUUIDs: string[];
  color: string;
};

type AggregatedPoint = Omit<UpdateHealthHistoryPoint, 'role'>;
type Metric = 'health' | 'adoption' | 'faults';

type MetricOption = {
  key: Metric;
  label: string;
  description: string;
  icon: ComponentType<SVGProps<SVGSVGElement>>;
};

const metricOptions: MetricOption[] = [
  {
    key: 'health',
    label: 'Health',
    description: 'Successful devices across all attempts',
    icon: Activity,
  },
  {
    key: 'adoption',
    label: 'Adoption',
    description: 'Devices currently running this update',
    icon: Users,
  },
  {
    key: 'faults',
    label: 'Faults',
    description: 'Unique faulty devices, stacked by root cause',
    icon: AlertTriangle,
  },
];

const aggregateSeries = (
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
  return Array.from(byTimestamp.values())
    .sort((a, b) => a.timestamp.localeCompare(b.timestamp))
    .map(point => {
      const attempts = point.successfulDevices + point.faultyDevices;
      return {
        ...point,
        healthPercent: attempts > 0 ? (100 * point.successfulDevices) / attempts : null,
      };
    });
};

const toTimeSeries = (
  series: Array<HealthHistorySeries & { points: AggregatedPoint[] }>,
  value: (point: AggregatedPoint) => number | null
): TimeSeriesDefinition[] =>
  series.map(item => ({
    key: item.key,
    label: item.label,
    color: item.color,
    points: item.points.flatMap(point => {
      const current = value(point);
      if (current == null) return [];
      return [{ timestamp: new Date(point.timestamp), value: current }];
    }),
  }));

const faultSeries = (
  series: Array<HealthHistorySeries & { points: AggregatedPoint[] }>
): TimeSeriesDefinition[] => {
  const byTimestamp = new Map<number, { native: number; js: number }>();
  for (const item of series) {
    for (const point of item.points) {
      const timestamp = new Date(point.timestamp).getTime();
      const current = byTimestamp.get(timestamp) ?? { native: 0, js: 0 };
      current.native += point.updateIssues;
      current.js += point.runtimeIssues;
      byTimestamp.set(timestamp, current);
    }
  }
  const timestamps = Array.from(byTimestamp.keys()).sort((a, b) => a - b);
  return [
    {
      key: 'native',
      label: 'Native',
      color: '#f59e0b',
      points: timestamps.map(timestamp => ({
        timestamp: new Date(timestamp),
        value: byTimestamp.get(timestamp)?.native ?? 0,
      })),
    },
    {
      key: 'javascript',
      label: 'JS',
      color: '#f43f5e',
      points: timestamps.map(timestamp => ({
        timestamp: new Date(timestamp),
        value: byTimestamp.get(timestamp)?.js ?? 0,
      })),
    },
  ];
};

const SeriesLegend = ({
  series,
  formatValue,
}: {
  series: TimeSeriesDefinition[];
  formatValue: (value: number) => string;
}) => (
  <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
    {series.map(item => {
      const latest = item.points[item.points.length - 1]?.value;
      return (
        <span key={item.key} className="flex items-center gap-1.5 text-xs">
          <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: item.color }} />
          <span className="text-muted-foreground">{item.label}</span>
          <span className="font-mono font-medium tabular-nums">
            {latest == null ? '-' : formatValue(latest)}
          </span>
        </span>
      );
    })}
  </div>
);

export const UpdateHealthHistory = ({
  series,
  from,
  live = false,
}: {
  series: HealthHistorySeries[];
  from?: string;
  live?: boolean;
}) => {
  const { selectedAppId } = useSelectedApp();
  const [metric, setMetric] = useState<Metric>('health');
  const updateUUIDs = useMemo(
    () => Array.from(new Set(series.flatMap(item => item.updateUUIDs))),
    [series]
  );
  const query = useQuery({
    queryKey: ['update-health-history', selectedAppId, updateUUIDs.join(','), from],
    queryFn: () => api.getUpdateHealthHistory(updateUUIDs, from),
    enabled: !!selectedAppId && updateUUIDs.length > 0,
    refetchInterval: live ? 5_000 : false,
  });
  const aggregated = useMemo(
    () =>
      series.map(item => ({
        ...item,
        points: aggregateSeries(item.updateUUIDs, query.data?.updates ?? {}),
      })),
    [query.data?.updates, series]
  );
  const healthSeries = useMemo(
    () => toTimeSeries(aggregated, point => point.healthPercent),
    [aggregated]
  );
  const adoptionSeries = useMemo(
    () => toTimeSeries(aggregated, point => point.devicesOnUpdate),
    [aggregated]
  );
  const faults = useMemo(() => faultSeries(aggregated), [aggregated]);
  const allPoints = aggregated.flatMap(item => item.points);
  const hasPoints = allPoints.length > 0;
  const timestamps = allPoints.map(point => point.timestamp).sort();
  const start = timestamps[0];
  const end = timestamps[timestamps.length - 1];
  const lastCapturedAt = allPoints
    .map(point => point.capturedAt)
    .filter(Boolean)
    .sort()
    .pop();
  const selectedOption = metricOptions.find(option => option.key === metric) ?? metricOptions[0];
  const chartSeries =
    metric === 'health' ? healthSeries : metric === 'adoption' ? adoptionSeries : faults;
  const formatValue =
    metric === 'health'
      ? (value: number) => `${value.toFixed(1)}%`
      : (value: number) => Math.round(value).toLocaleString();

  if (query.isLoading) {
    return <Skeleton className="h-80 w-full rounded-xl" />;
  }
  if (query.error || query.data?.available === false) return null;
  if (!hasPoints) {
    return (
      <div className="rounded-xl border border-dashed px-4 py-6 text-center text-xs text-muted-foreground">
        Health history will appear after the first one-minute snapshot.
      </div>
    );
  }

  return (
    <section className="space-y-3">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium">Health over time</h3>
          <p className="text-xs text-muted-foreground">
            Near-real-time health, retained in one-minute buckets.
          </p>
        </div>
        <div className="text-right font-mono text-[10px] text-muted-foreground">
          {start && end && (
            <div>
              {new Date(start).toLocaleString()} – {new Date(end).toLocaleString()}
            </div>
          )}
          {lastCapturedAt && <div>Synced {new Date(lastCapturedAt).toLocaleTimeString()}</div>}
        </div>
      </div>

      <div className="overflow-hidden rounded-xl border bg-card shadow-card">
        <div
          className="grid grid-cols-3 border-b bg-muted/30 p-1"
          role="tablist"
          aria-label="Health history metric">
          {metricOptions.map(option => {
            const Icon = option.icon;
            const active = option.key === metric;
            return (
              <button
                key={option.key}
                type="button"
                role="tab"
                aria-selected={active}
                onClick={() => setMetric(option.key)}
                className={cn(
                  'flex items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                  active
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}>
                <Icon className="h-3.5 w-3.5" />
                {option.label}
              </button>
            );
          })}
        </div>

        <div className="space-y-2.5 px-3 pb-2 pt-3">
          <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 px-1">
            <p className="text-[11px] text-muted-foreground">{selectedOption.description}</p>
            <SeriesLegend series={chartSeries} formatValue={formatValue} />
          </div>
          <TimeSeriesChart
            key={metric}
            ariaLabel={`${selectedOption.label} over time`}
            series={chartSeries}
            mode={metric === 'faults' ? 'stacked' : 'line'}
            maximum={metric === 'health' ? 100 : undefined}
            formatValue={formatValue}
            formatAxisValue={
              metric === 'health'
                ? value => `${Math.round(value)}%`
                : value => Math.round(value).toLocaleString()
            }
          />
        </div>
      </div>
    </section>
  );
};
