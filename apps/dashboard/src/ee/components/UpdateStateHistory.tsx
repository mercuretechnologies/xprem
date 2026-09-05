// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useMemo, useState } from 'react';
import { Info, TrendingUp, AlertTriangle } from 'lucide-react';
import type { UpdateStateHistoryPoint } from '@/lib/api';
import {
  TimeSeriesChart,
  type TimeSeriesAnnotation,
  type TimeSeriesChartProps,
  type TimeSeriesDefinition,
} from '@/ee/components/charts/TimeSeriesChart';
// Type-only, so the cycle with the component that renders this one is erased at
// compile time and nothing circular survives at runtime.
import type { HealthHistorySeries } from '@/ee/components/UpdateHealthHistory';

const exactNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

type Metric = 'arrivals' | 'failing';

const metricOptions: Array<{ key: Metric; label: string; icon: typeof Info }> = [
  { key: 'arrivals', label: 'Arrivals', icon: TrendingUp },
  { key: 'failing', label: 'Failing', icon: AlertTriangle },
];

// Deliberately a component of its own rather than a mode of the projected
// chart. Mapping these two counts onto that one's fields would have inherited
// its title, its axis labels and its promise of one-minute buckets, none of
// which is true here, and the result would have been a chart that lies while
// looking identical to one that does not.
//
// One curve per caller series, and one metric at a time, exactly like the chart
// it stands in for. Folding every series into a single pair of lines instead
// erased the comparison the caller built them for: a rollout card passes its
// candidate and its control, and a candidate crashing at 20% against a healthy
// control would have looked like a uniform 10% failure across the fleet.
export const UpdateStateHistory = ({
  series,
  pointsByUpdate,
  annotations,
  renderAnnotationDetails,
  devicesLabel = 'Devices on this update now',
}: {
  series: HealthHistorySeries[];
  pointsByUpdate: Record<string, UpdateStateHistoryPoint[]>;
  annotations?: TimeSeriesAnnotation[];
  renderAnnotationDetails?: TimeSeriesChartProps['renderAnnotationDetails'];
  devicesLabel?: string;
}) => {
  const [metric, setMetric] = useState<Metric>('arrivals');

  // Each series sums its own updates. Summing is right within a series: a
  // device is on exactly one update at a time, so its members do not overlap.
  const aggregated = useMemo(
    () =>
      series.map(item => {
        const byTimestamp = new Map<string, UpdateStateHistoryPoint>();
        for (const updateUUID of item.updateUUIDs) {
          for (const point of pointsByUpdate[updateUUID] ?? []) {
            const current = byTimestamp.get(point.timestamp);
            if (current) {
              current.arrivedDevices += point.arrivedDevices;
              current.failingDevices += point.failingDevices;
            } else {
              // A copy, never the cached object: this map is mutated above.
              byTimestamp.set(point.timestamp, { ...point });
            }
          }
        }
        return {
          ...item,
          points: Array.from(byTimestamp.values()).sort((a, b) =>
            a.timestamp.localeCompare(b.timestamp)
          ),
        };
      }),
    [pointsByUpdate, series]
  );

  const chartSeries: TimeSeriesDefinition[] = useMemo(
    () =>
      aggregated
        .filter(item => item.points.length > 0)
        .map(item => ({
          key: item.key,
          label: item.label,
          color: item.color,
          points: item.points.map(point => ({
            timestamp: new Date(point.timestamp),
            value: metric === 'arrivals' ? point.arrivedDevices : point.failingDevices,
          })),
        })),
    [aggregated, metric]
  );

  if (chartSeries.length === 0) return null;

  // The tiles are the fleet, across every series: they answer "where does this
  // publish stand now", which is not a per-cohort question.
  const totals = aggregated.reduce(
    (sum, item) => {
      const latest = item.points[item.points.length - 1];
      if (!latest) return sum;
      return {
        arrived: sum.arrived + latest.arrivedDevices,
        failing: sum.failing + latest.failingDevices,
      };
    },
    { arrived: 0, failing: 0 }
  );

  return (
    <section className="space-y-3">
      <div>
        <h3 className="text-sm font-medium">Arrivals and failures over time</h3>
        <p className="text-xs text-muted-foreground">
          Rebuilt from the current state of each device, which is all this deployment records.
        </p>
      </div>

      <div className="overflow-hidden rounded-xl border bg-card shadow-card">
        <div className="grid grid-cols-2 border-b bg-muted/30">
          {metricOptions.map(option => {
            const selected = option.key === metric;
            const value = option.key === 'arrivals' ? totals.arrived : totals.failing;
            return (
              <button
                key={option.key}
                type="button"
                onClick={() => setMetric(option.key)}
                className={`flex flex-col items-start gap-0.5 border-r px-4 py-2.5 text-left last:border-r-0 ${
                  selected ? 'bg-background' : 'hover:bg-accent/40'
                }`}>
                <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <option.icon className="h-3 w-3" />
                  {option.key === 'arrivals' ? devicesLabel : 'Failing now'}
                </span>
                <span
                  className={`font-mono text-lg tabular-nums ${
                    option.key === 'failing' && value > 0 ? 'text-rose-600 dark:text-rose-400' : ''
                  }`}>
                  {exactNumber.format(value)}
                </span>
              </button>
            );
          })}
        </div>

        <div className="p-3">
          <TimeSeriesChart
            series={chartSeries}
            annotations={annotations}
            annotationNoun="updates"
            renderAnnotationDetails={renderAnnotationDetails}
            formatValue={value => exactNumber.format(Math.round(value))}
            ariaLabel={`${metric === 'arrivals' ? 'Arrivals' : 'Failing devices'} over time`}
            height={220}
          />
        </div>
      </div>

      {/* The caveat is part of the chart, not a footnote to it: the arrivals
          curve is genuinely misleading about the past, and a reader who does
          not know that will read a rollback as a plateau. */}
      <div className="flex gap-2 rounded-lg border border-dashed px-3 py-2 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <p>
          Without telemetry storage, only each device's current state is kept. The failure curve is
          exact and falls when a device recovers. The arrivals curve counts the devices running this
          update <em>today</em>, placed at the moment each of them arrived: it can only rise, and a
          device that has since moved to another update is missing from all of it, so a rollback
          shows up as a flat line rather than a drop.
        </p>
      </div>
    </section>
  );
};
