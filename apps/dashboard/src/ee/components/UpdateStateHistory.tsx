// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useMemo } from 'react';
import { Info } from 'lucide-react';
import type { UpdateStateHistoryPoint } from '@/lib/api';
import {
  TimeSeriesChart,
  type TimeSeriesAnnotation,
  type TimeSeriesChartProps,
  type TimeSeriesDefinition,
} from '@/ee/components/charts/TimeSeriesChart';

const exactNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

// Deliberately a component of its own rather than a mode of the projected
// chart. Mapping these two counts onto that one's fields would have inherited
// its title, its axis labels and its promise of one-minute buckets, none of
// which is true here, and the result would have been a chart that lies while
// looking identical to one that does not.
export const UpdateStateHistory = ({
  pointsByUpdate,
  updateUUIDs,
  annotations,
  renderAnnotationDetails,
}: {
  pointsByUpdate: Record<string, UpdateStateHistoryPoint[]>;
  updateUUIDs: string[];
  annotations?: TimeSeriesAnnotation[];
  renderAnnotationDetails?: TimeSeriesChartProps['renderAnnotationDetails'];
}) => {
  // Several updates land on one pair of curves. Summing is right here: the
  // sheet shows one update group, and a device is on exactly one of its
  // updates at a time, so the sets do not overlap.
  const points = useMemo(() => {
    const byTimestamp = new Map<string, UpdateStateHistoryPoint>();
    for (const updateUUID of updateUUIDs) {
      for (const point of pointsByUpdate[updateUUID] ?? []) {
        const current = byTimestamp.get(point.timestamp);
        if (current) {
          current.arrivedDevices += point.arrivedDevices;
          current.failingDevices += point.failingDevices;
        } else {
          byTimestamp.set(point.timestamp, { ...point });
        }
      }
    }
    return Array.from(byTimestamp.values()).sort((a, b) => a.timestamp.localeCompare(b.timestamp));
  }, [pointsByUpdate, updateUUIDs]);

  const series: TimeSeriesDefinition[] = useMemo(
    () => [
      {
        key: 'arrived',
        // Literal, like the fault curves next door: a CSS variable resolves to
        // nothing here and the line comes out black on a dark page.
        label: 'Devices that arrived',
        color: '#3b82f6',
        points: points.map(point => ({
          timestamp: new Date(point.timestamp),
          value: point.arrivedDevices,
        })),
      },
      {
        key: 'failing',
        label: 'Devices failing',
        color: '#f43f5e',
        points: points.map(point => ({
          timestamp: new Date(point.timestamp),
          value: point.failingDevices,
        })),
      },
    ],
    [points]
  );

  if (points.length === 0) return null;

  const latest = points[points.length - 1];

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
          <div className="border-r px-4 py-2.5">
            <div className="text-xs text-muted-foreground">Devices on this update now</div>
            <div className="font-mono text-lg tabular-nums">
              {exactNumber.format(latest.arrivedDevices)}
            </div>
          </div>
          <div className="px-4 py-2.5">
            <div className="text-xs text-muted-foreground">Failing now</div>
            <div
              className={`font-mono text-lg tabular-nums ${
                latest.failingDevices > 0 ? 'text-rose-600 dark:text-rose-400' : ''
              }`}>
              {exactNumber.format(latest.failingDevices)}
            </div>
          </div>
        </div>

        <div className="p-3">
          <TimeSeriesChart
            series={series}
            annotations={annotations}
            annotationNoun="updates"
            renderAnnotationDetails={renderAnnotationDetails}
            formatValue={value => exactNumber.format(Math.round(value))}
            ariaLabel="Arrivals and failures over time"
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
