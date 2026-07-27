// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { Activity, AlertTriangle, Users } from 'lucide-react';
import { TimeSeriesChart } from '@/ee/components/charts/TimeSeriesChart';
import { seriesColors } from './dimensions';

// Sample curves, drawn once. A sentence saying "pick a branch" describes an
// empty box; the box itself, blurred behind the sentence, shows what the
// choice buys. Deterministic on purpose: a placeholder that flickers on every
// render reads as live data.
const sampleSeries = (() => {
  const now = Date.now();
  const shapes = [
    [96, 97, 99, 98, 99, 99, 98, 99, 99, 99, 98, 99],
    [88, 91, 93, 92, 94, 95, 96, 95, 96, 97, 96, 97],
    [99, 99, 98, 99, 99, 99, 99, 98, 99, 99, 99, 99],
  ];
  return shapes.map((values, index) => ({
    key: `sample-${index}`,
    label: `Sample ${index + 1}`,
    color: seriesColors[index % seriesColors.length],
    points: values.map((value, step) => ({
      timestamp: new Date(now - (values.length - 1 - step) * 2 * 3_600_000),
      value,
    })),
  }));
})();

const tabs = [
  { label: 'Health', icon: Activity },
  { label: 'Adoption', icon: Users },
  { label: 'Faults', icon: AlertTriangle },
];

// Only the plot is masked. The tabs and the caption stay legible, so the card
// reads as "this chart needs something" rather than as a whole panel switched
// off, and the shape of what is missing stays visible behind it.
export const HealthPlaceholder = ({ title, detail }: { title: string; detail: string }) => (
  <section className="overflow-hidden rounded-xl border bg-card shadow-card">
    <div className="grid grid-cols-3 border-b">
      {tabs.map((tab, index) => {
        const Icon = tab.icon;
        return (
          <div
            key={tab.label}
            className={`flex items-center justify-center gap-2 py-2.5 text-sm ${
              index === 0 ? 'bg-muted/40 font-medium text-foreground' : 'text-muted-foreground'
            }`}>
            <Icon className="h-3.5 w-3.5" />
            {tab.label}
          </div>
        );
      })}
    </div>
    <div className="px-4 pt-3 text-xs text-muted-foreground">
      Successful devices across all attempts
    </div>

    <div className="relative px-2 pb-2">
      <div
        aria-hidden
        inert={'' as unknown as boolean}
        className="pointer-events-none select-none opacity-60">
        <TimeSeriesChart
          series={sampleSeries}
          formatValue={value => `${value.toFixed(1)}%`}
          formatAxisValue={value => `${value.toFixed(0)}%`}
          // Same axis as the real health chart this stands in for.
          maximum={100}
          ariaLabel="Example of health over time"
          height={200}
        />
      </div>

      <div className="absolute inset-0 flex items-center justify-center bg-background/60 backdrop-blur-[2px]">
        <div className="flex max-w-sm flex-col items-center gap-1.5 rounded-lg border bg-card px-6 py-4 text-center shadow-elevated">
          <Activity className="h-4 w-4 text-primary" />
          <p className="text-sm font-semibold">{title}</p>
          <p className="text-[11px] leading-relaxed text-muted-foreground">{detail}</p>
        </div>
      </div>
    </div>
  </section>
);
