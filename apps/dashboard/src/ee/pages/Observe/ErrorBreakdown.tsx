// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ErrorBreakdownSegment } from '@/lib/api';
import { useNavigate } from 'react-router';
import type { FilterKey } from './filters';
import { errorsListHref } from './errorNavigation';
import { deviceName } from './deviceNames';
import { exactNumber } from './format';

type Dimension = 'updates' | 'deviceModels' | 'osVersions' | 'runtimes';

const filterFor = (
  dimension: Dimension,
  segment: ErrorBreakdownSegment
): Partial<Record<FilterKey, string>> | null => {
  // Missing dimensions and the aggregate remainder have no filter value.
  if (!segment.key || segment.key === '__other__') return null;
  switch (dimension) {
    case 'updates':
      return segment.updateId
        ? { updateId: segment.updateId }
        : segment.updateGroupId
          ? { updateGroupId: segment.updateGroupId }
          : null;
    case 'deviceModels':
      return { deviceModel: segment.key };
    case 'osVersions':
      return segment.osName && segment.osVersion
        ? { osName: segment.osName, osVersion: segment.osVersion }
        : null;
    case 'runtimes':
      return { runtimeVersion: segment.key };
  }
};

export const ErrorBreakdown = ({
  title,
  dimension,
  segments,
  listParams,
}: {
  title: string;
  dimension: Dimension;
  segments: ErrorBreakdownSegment[];
  listParams: URLSearchParams;
}) => {
  const navigate = useNavigate();
  return (
    <section className="overflow-hidden rounded-xl border bg-card shadow-card">
      <header className="flex items-center justify-between border-b bg-muted/30 px-4 py-3">
        <h3 className="text-sm font-medium">{title}</h3>
        <span className="text-[11px] text-muted-foreground">Occurrences · share</span>
      </header>
      <div className="max-h-64 overflow-y-auto">
        {segments.length === 0 && (
          <p className="p-4 text-xs text-muted-foreground">No occurrences in this selection.</p>
        )}
        {segments.map(segment => {
          const patch = filterFor(dimension, segment);
          const label =
            dimension === 'deviceModels' && segment.key
              ? deviceName(segment.key).label
              : segment.label;
          return (
            <button
              type="button"
              key={segment.key}
              disabled={!patch}
              title={patch ? `View errors filtered by ${label}` : label}
              onClick={() => patch && navigate(errorsListHref(listParams, patch))}
              className="relative flex w-full items-center gap-3 border-b border-border/50 px-4 py-2.5 text-left text-xs last:border-0 enabled:hover:bg-accent/50 disabled:cursor-default">
              <span
                className="pointer-events-none absolute inset-y-0 left-0 bg-primary/[0.05]"
                style={{ width: `${Math.min(100, Math.max(0, segment.percentage))}%` }}
              />
              <span className="relative min-w-0 flex-1">
                <span className="block truncate">{label}</span>
                {dimension === 'updates' && segment.updateId && (
                  <span className="block truncate font-mono text-[10px] text-muted-foreground">
                    {segment.platform && `${segment.platform} · `}
                    {segment.updateId}
                  </span>
                )}
              </span>
              <span className="relative font-mono tabular-nums">
                {exactNumber.format(segment.occurrences)}
              </span>
              <span className="relative w-14 text-right font-mono tabular-nums text-muted-foreground">
                {segment.percentage.toLocaleString(undefined, { maximumFractionDigits: 1 })}%
              </span>
            </button>
          );
        })}
      </div>
    </section>
  );
};
