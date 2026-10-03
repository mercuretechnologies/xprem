// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ErrorFatality } from '@/lib/api';
import { filterParam, type FilterKey } from './filters';

export const errorFatality = (value: string | null): ErrorFatality =>
  value === 'fatal' || value === 'nonfatal' ? value : 'all';

export const errorDetailsHref = (
  errorId: string,
  params: URLSearchParams,
  responseWindow?: { from: string; to: string }
) => {
  const next = new URLSearchParams(params);
  next.delete('errorId');
  // Details have no live controls. Keep the exact response window stable
  // even when the list is live, so the counts and stack stay consistent.
  next.set('live', '0');
  if (responseWindow) {
    next.set('from', responseWindow.from);
    next.set('to', responseWindow.to);
    next.delete('period');
  }
  return `/observe/errors/${encodeURIComponent(errorId)}?${next}`;
};

export const errorsListHref = (
  params: URLSearchParams,
  patch: Partial<Record<FilterKey, string>> = {}
) => {
  const next = new URLSearchParams(params);
  next.delete('errorId');
  for (const [key, value] of Object.entries(patch)) {
    next.set(filterParam(key as FilterKey), value);
  }
  const queryString = next.toString();
  return `/observe/errors${queryString ? `?${queryString}` : ''}`;
};
