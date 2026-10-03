// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { FeaturePitch, SketchWindow } from '@/ee/components/FeaturePitch';
import { identityAttributesFeature } from '@/ee/lib/enterpriseFeatures';

// What the Attributes page shows in place of the allowlist without an enterprise license.
export const AttributesPitch = () => (
  <FeaturePitch
    feature={identityAttributesFeature}
    title="Analyze cohorts built from your own attributes"
    description="Create cohorts from attributes like tenant, region or plan, or from an identifier like the user ID."
    illustration={<AttributesSketch />}
  />
);

const SKETCH_FILTERS = [
  ['tenant', 'rider-co'],
  ['region', 'eu-west'],
  ['plan', 'enterprise'],
];
const SKETCH_LINE =
  'M0 22 L10 20 L20 21 L30 17 L40 18 L50 14 L60 15 L70 11 L80 12 L90 8 L100 9 L110 6 L120 7';

// One cohort on the Overview page, narrowed by three attributes.
const AttributesSketch = () => (
  <SketchWindow path="observe/overview">
    <div className="flex flex-wrap items-center gap-1.5 border-b px-4 py-3">
      {SKETCH_FILTERS.map(([key, value]) => (
        <span
          key={key}
          className="rounded-md border border-primary/30 bg-primary/10 px-2 py-0.5 text-[11px]">
          <span className="text-muted-foreground">{key}</span>{' '}
          <span className="font-medium text-primary">{value}</span>
        </span>
      ))}
      <span className="rounded-md border border-dashed px-2 py-0.5 text-[11px] text-muted-foreground">
        + Attribute
      </span>
    </div>

    <div className="grid grid-cols-3 divide-x border-b">
      {[
        ['Devices', '1,284'],
        ['Crash-free', '99.4%'],
        ['Errors', '3'],
      ].map(([label, value]) => (
        <div key={label} className="px-4 py-3">
          <p className="text-[11px] text-muted-foreground">{label}</p>
          <p className="text-base font-semibold tabular-nums">{value}</p>
        </div>
      ))}
    </div>

    <div className="p-4">
      <svg viewBox="0 0 120 30" preserveAspectRatio="none" className="block h-28 w-full">
        <path d={`${SKETCH_LINE} L120 30 L0 30 Z`} className="fill-primary/10" />
        <path
          d={SKETCH_LINE}
          fill="none"
          className="stroke-primary"
          strokeWidth="1.5"
          vectorEffect="non-scaling-stroke"
        />
      </svg>
    </div>
  </SketchWindow>
);
