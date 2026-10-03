// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useId } from 'react';
import type { ErrorSeriesPoint } from '@/lib/api';
import { exactNumber } from './format';

// Keep every bucket, including the current one: the chart must account for
// exactly the same occurrences as the counters.
export const OccurrenceHistogram = ({
  series,
  compact = false,
  from,
  to,
}: {
  series: ErrorSeriesPoint[];
  compact?: boolean;
  from?: string;
  to?: string;
}) => {
  const titleId = useId();
  const max = Math.max(0, ...series.map(point => point.count));
  const total = series.reduce((sum, point) => sum + point.count, 0);
  const height = compact ? 32 : 150;
  const width = 600;
  const step = width / Math.max(1, series.length);
  return (
    <div className={compact ? 'w-28' : 'w-full'}>
      <svg
        role="img"
        aria-labelledby={titleId}
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        className={compact ? 'h-8 w-full text-primary' : 'h-[150px] w-full text-primary'}>
        <title id={titleId}>{exactNumber.format(total)} occurrences over time</title>
        {series.map((point, index) => {
          const barHeight = (point.count / Math.max(1, max)) * (height - 2);
          return (
            <rect
              key={point.timestamp}
              x={index * step}
              y={height - barHeight}
              width={Math.max(0.5, step * 0.8)}
              height={barHeight}
              fill="currentColor"
              opacity="0.75">
              <title>
                {new Date(point.timestamp).toLocaleString()} · {exactNumber.format(point.count)}{' '}
                occurrences
              </title>
            </rect>
          );
        })}
        <line x1="0" y1={height} x2={width} y2={height} stroke="currentColor" opacity="0.2" />
      </svg>
      {!compact && (
        <div className="mt-2 flex justify-between gap-3 text-[11px] text-muted-foreground">
          <time dateTime={from}>{from && new Date(from).toLocaleString()}</time>
          <span>{exactNumber.format(max)} max / bucket</span>
          <time dateTime={to}>{to && new Date(to).toLocaleString()}</time>
        </div>
      )}
    </div>
  );
};
