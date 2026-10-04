// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ErrorFatality } from '@/lib/api';
import { filterParam, type FilterKey } from './filters';

export const errorFatality = (value: string | null): ErrorFatality =>
  value === 'fatal' || value === 'nonfatal' ? value : 'all';

export const errorDetailsHref = (errorId: string) =>
  `/observe/errors/${encodeURIComponent(errorId)}`;

// The detail URL always describes the global group. Only navigation state
// remembers the list selection; pasted detail links return to the default list.
export const errorsListParams = (state: unknown) => {
  const search =
    state && typeof state === 'object' && 'errorsSearch' in state ? state.errorsSearch : null;
  return new URLSearchParams(typeof search === 'string' ? search : '');
};

export const errorsListHref = (
  params: URLSearchParams,
  patch: Partial<Record<FilterKey, string>> = {}
) => {
  const next = new URLSearchParams(params);
  next.delete('errorId');
  // The release picker treats update IDs and publish groups as alternative
  // selections. A breakdown from the global detail can select another release.
  if (patch.updateId !== undefined || patch.updateGroupId !== undefined) {
    next.delete(filterParam('updateId'));
    next.delete(filterParam('updateGroupId'));
  }
  for (const [key, value] of Object.entries(patch)) {
    next.set(filterParam(key as FilterKey), value);
  }
  const queryString = next.toString();
  return `/observe/errors${queryString ? `?${queryString}` : ''}`;
};
