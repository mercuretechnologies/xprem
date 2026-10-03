// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { Code2, GitCommitHorizontal, Layers, Smartphone } from 'lucide-react';
import {
  FeaturePitch,
  FeaturePitchPoint,
  SketchBar,
  SketchWindow,
} from '@/ee/components/FeaturePitch';
import { errorTrackingFeature } from '@/ee/lib/enterpriseFeatures';

const POINTS: FeaturePitchPoint[] = [
  {
    icon: Code2,
    text: 'Each stack trace points to the file, line and function in your source code, using the source map uploaded with the update.',
  },
  {
    icon: Layers,
    text: 'Each error shows its occurrences, the devices hit, and when it was first and last seen.',
  },
  {
    icon: Smartphone,
    text: 'Occurrences are broken down by update, device model, OS version and runtime.',
  },
  {
    icon: GitCommitHorizontal,
    text: 'The occurrence chart shows which update brought the error and whether the next one fixed it.',
  },
];

// What the Errors page shows in place of the explorer without an enterprise license.
export const ErrorTrackingPitch = () => (
  <FeaturePitch
    feature={errorTrackingFeature}
    title="Track the errors your devices report"
    description="Errors lists the crashes and errors your devices send to Observe, grouped by error."
    points={POINTS}
    illustration={<ErrorsSketch />}
  />
);

const SKETCH_ROWS = [
  { title: '58%', detail: '82%', crash: true, selected: true },
  { title: '44%', detail: '68%', crash: false, selected: false },
  { title: '62%', detail: '74%', crash: true, selected: false },
  { title: '38%', detail: '60%', crash: false, selected: false },
  { title: '50%', detail: '78%', crash: false, selected: false },
  { title: '42%', detail: '64%', crash: false, selected: false },
];
const SKETCH_BARS = [2, 3, 2, 3, 2, 4, 3, 2, 3, 14, 18, 16, 19, 17, 6, 3, 2, 3];
const SKETCH_SPIKE_START = 9;
const SKETCH_SPIKE_END = 13;
const SKETCH_BREAKDOWN = [
  ['Update', 'Add Apple Pay', '94%'],
  ['Device', 'iPhone 15', '46%'],
  ['OS', 'iOS 18.1', '58%'],
  ['Runtime', '2.4.0', '92%'],
];

// The Errors page as a wireframe: the error list beside one error's details.
const ErrorsSketch = () => (
  <SketchWindow path="observe/errors">
    <div className="grid grid-cols-4 divide-x border-b">
      {[
        ['Errors', '37'],
        ['Occurrences', '12.4k'],
        ['Devices', '1,862'],
        ['Crashes', '214'],
      ].map(([label, value]) => (
        <div key={label} className="px-3 py-2.5">
          <p className="text-[10px] text-muted-foreground">{label}</p>
          <p className="text-sm font-medium tabular-nums">{value}</p>
        </div>
      ))}
    </div>

    <div className="grid grid-cols-[minmax(0,1fr)_42%]">
      <div className="border-r">
        {SKETCH_ROWS.map((row, index) => (
          <div
            key={index}
            className={`flex items-center gap-2.5 border-b px-3 py-2.5 last:border-0 ${row.selected ? 'bg-primary/10' : ''}`}>
            <span
              className={`h-2 w-2 shrink-0 rounded-full ${row.selected ? 'bg-primary' : 'bg-muted-foreground/25'}`}
            />
            <span className="flex min-w-0 flex-1 flex-col gap-1.5">
              <SketchBar
                w={row.title}
                className={row.selected ? 'bg-foreground/80' : 'bg-muted-foreground/30'}
              />
              <SketchBar w={row.detail} h="h-1" />
            </span>
            <span
              className={`h-3.5 w-8 shrink-0 rounded-sm border ${row.crash ? 'border-amber-500/40 bg-amber-500/10' : 'border-primary/25 bg-primary/5'}`}
            />
          </div>
        ))}
      </div>

      <div className="flex min-w-0 flex-col gap-3 p-3">
        <div className="flex h-12 items-end gap-[2px]">
          {SKETCH_BARS.map((height, index) => (
            <span
              key={index}
              className={`flex-1 rounded-t-[1px] ${index >= SKETCH_SPIKE_START && index <= SKETCH_SPIKE_END ? 'bg-amber-500/80' : 'bg-primary/35'}`}
              style={{ height: `${(height / 19) * 100}%` }}
            />
          ))}
        </div>
        <div className="rounded-md border bg-card text-[10px] leading-relaxed">
          <p className="truncate border-b px-2 py-1 text-muted-foreground">
            src/screens/Checkout.tsx
          </p>
          <p className="truncate px-2 text-muted-foreground/70">41 const total = cart</p>
          <p className="truncate bg-amber-500/15 px-2 text-foreground">42 &nbsp;.items.reduce(</p>
          <p className="truncate px-2 pb-1 text-muted-foreground/70">43 &nbsp;&nbsp;sum, 0)</p>
        </div>
        <div className="divide-y rounded-md border bg-card text-[10px]">
          {SKETCH_BREAKDOWN.map(([dimension, label, percent]) => (
            <div key={dimension} className="flex items-center gap-2 px-2 py-1">
              <span className="w-12 shrink-0 text-muted-foreground">{dimension}</span>
              <span className="min-w-0 flex-1 truncate">{label}</span>
              <span className="tabular-nums text-muted-foreground">{percent}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  </SketchWindow>
);
