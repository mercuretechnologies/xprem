// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import type { ObserveFleetDimension, ObserveFleetFacet } from '@/lib/api';
import { shortRuntimeVersion } from '@/lib/update-format';
import { cn } from '@/lib/utils';
import type { FilterKey, ObserveFilters } from './filters';
import { deviceName, osLabel } from './deviceNames';
import { exactNumber } from './format';
import { useUpdateNames } from './useUpdateNames';

const regionNames = new Intl.DisplayNames(undefined, { type: 'region' });
const countryName = (code: string) => {
  if (!/^[A-Za-z]{2}$/.test(code)) return code;
  try {
    return regionNames.of(code.toUpperCase()) ?? code;
  } catch {
    return code;
  }
};

// Rows a panel shows before "View all".
const VISIBLE_ROWS = 8;

type Value = ObserveFleetFacet['values'][number];

const facetSpecs: Record<
  ObserveFleetDimension,
  {
    title: string;
    unknown: string;
    label: (value: Value, names: Map<string, string>) => string;
    filters: (value: Value) => Partial<Record<FilterKey, string>>;
  }
> = {
  channel: {
    title: 'Channel',
    unknown: 'Not recorded yet',
    label: ({ value }) => value,
    filters: ({ value }) => ({ channel: value }),
  },
  runtimeVersion: {
    title: 'Runtime',
    unknown: 'Not recorded yet',
    label: ({ value }) => shortRuntimeVersion(value),
    filters: ({ value }) => ({ runtimeVersion: value }),
  },
  update: {
    title: 'Update',
    unknown: 'Embedded bundle',
    label: ({ value }, names) => names.get(value) ?? value.slice(0, 8),
    filters: ({ value, context }) =>
      context === 'group' ? { updateGroupId: value, updateId: '' } : { updateId: value },
  },
  platform: {
    title: 'Platform',
    unknown: 'Not recorded yet',
    label: ({ value }) => (value === 'ios' ? 'iOS' : value === 'android' ? 'Android' : value),
    filters: ({ value }) => ({ platform: value }),
  },
  appVersion: {
    title: 'App version',
    unknown: 'Not reported',
    label: ({ value }) => value,
    filters: ({ value }) => ({ appVersion: value }),
  },
  deviceModel: {
    title: 'Device',
    unknown: 'Not reported',
    label: ({ value }) => deviceName(value).label,
    filters: ({ value }) => ({ deviceModel: value }),
  },
  osVersion: {
    title: 'OS',
    unknown: 'Not reported',
    label: ({ value, context }) => osLabel(context ?? '', value),
    filters: ({ value, context }) => ({ osVersion: value, osName: context ?? '' }),
  },
  country: {
    title: 'Country',
    unknown: 'Not located',
    label: ({ value }) => countryName(value),
    filters: ({ value }) => ({ countryCode: value }),
  },
};

// One breakdown panel with a tab per dimension: a row per value, its share of
// the fleet behind it as a bar, and a click narrowing the whole page to it.
export const FleetPanel = ({
  dimensions,
  facets,
  total,
  filters,
  className,
}: {
  dimensions: ObserveFleetDimension[];
  facets: ObserveFleetFacet[];
  total: number;
  filters: ObserveFilters;
  className?: string;
}) => {
  const names = useUpdateNames();
  const [active, setActive] = useState(dimensions[0]);
  const [expanded, setExpanded] = useState(false);
  const facet = facets.find(entry => entry.dimension === active);
  const spec = facetSpecs[active];
  const values = facet?.values ?? [];
  const shown = expanded ? values : values.slice(0, VISIBLE_ROWS);
  const hidden = values.length - shown.length + (facet?.otherValues ?? 0);

  const isSelected = (value: Value) =>
    Object.entries(spec.filters(value)).every(
      ([key, filterValue]) => !filterValue || filters.state[key as FilterKey].includes(filterValue)
    );
  const select = (value: Value) => {
    const patch = spec.filters(value);
    filters.setFilters(
      isSelected(value) ? Object.fromEntries(Object.keys(patch).map(key => [key, ''])) : patch
    );
  };

  return (
    <section className={cn('flex flex-col rounded-lg border bg-card', className)}>
      <header className="flex items-center justify-between gap-2 border-b px-3 py-2">
        <div className="flex gap-0.5" role="tablist">
          {dimensions.map(dimension => (
            <button
              key={dimension}
              type="button"
              role="tab"
              aria-selected={dimension === active}
              onClick={() => {
                setActive(dimension);
                setExpanded(false);
              }}
              className={cn(
                'rounded-md px-2.5 py-1 text-[13px] transition-colors',
                dimension === active
                  ? 'bg-muted font-medium text-foreground'
                  : 'text-muted-foreground hover:text-foreground'
              )}>
              {facetSpecs[dimension].title}
            </button>
          ))}
        </div>
        <span className="pr-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Devices
        </span>
      </header>

      <ul className={cn('flex-1 p-1.5', expanded && 'max-h-96 overflow-y-auto')}>
        {shown.length === 0 && (
          <li className="px-3 py-6 text-center text-[13px] text-muted-foreground">
            No device in this selection
          </li>
        )}
        {shown.map(value => {
          const known = value.value !== '';
          const selected = known && isSelected(value);
          const share = total > 0 ? value.devices / total : 0;
          return (
            <li key={`${value.value}:${value.context ?? ''}`}>
              <button
                type="button"
                disabled={!known}
                onClick={() => select(value)}
                title={`${exactNumber.format(value.devices)} ${value.devices === 1 ? 'device' : 'devices'}`}
                className="flex h-8 w-full items-center gap-3 rounded-md px-2.5 text-left text-[13px] enabled:hover:bg-muted/60">
                <span
                  className={cn(
                    'min-w-0 flex-1 truncate',
                    !known && 'text-muted-foreground',
                    selected && 'font-medium text-primary'
                  )}>
                  {known ? spec.label(value, names) : spec.unknown}
                </span>
                <span
                  aria-hidden
                  className="h-0.5 w-28 shrink-0 overflow-hidden rounded-full bg-muted">
                  <span
                    className={cn(
                      'block h-full rounded-full',
                      selected ? 'bg-primary' : 'bg-primary/40'
                    )}
                    style={{ width: `${100 * share}%` }}
                  />
                </span>
                <span className="w-10 shrink-0 text-right tabular-nums text-muted-foreground">
                  {share >= 0.01 || share === 0 ? `${Math.round(100 * share)}%` : '<1%'}
                </span>
              </button>
            </li>
          );
        })}
      </ul>

      {(hidden > 0 || expanded) && (
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          className="border-t px-4 py-2 text-left text-[12px] text-muted-foreground hover:text-foreground">
          {expanded ? 'Show less' : `View all · ${hidden} more`}
        </button>
      )}
    </section>
  );
};
