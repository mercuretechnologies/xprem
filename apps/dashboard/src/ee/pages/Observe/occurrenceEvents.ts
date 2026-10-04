// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ObserveLog } from '@/lib/api';
import { filterParam, uuidPattern } from './filters';

// End on the occurrence so the descending stream starts at the error and
// continues into the events that led to it. A fresh query drops error/search/
// severity filters, which would otherwise hide that preceding context.
export const occurrenceEventsHref = (log: ObserveLog) => {
  const params = new URLSearchParams({
    from: new Date(new Date(log.timestamp).getTime() - 5 * 60_000).toISOString(),
    // Keep the API's fractional seconds; Date truncates them to milliseconds.
    to: log.timestamp,
    live: '0',
    eventKey: log.eventKey,
  });
  if (uuidPattern.test(log.easClientId) && !/^[0-]+$/.test(log.easClientId)) {
    params.set(filterParam('easClientId'), log.easClientId);
  }
  if (uuidPattern.test(log.updateId)) params.set(filterParam('updateId'), log.updateId);
  if (log.platform === 'ios' || log.platform === 'android') {
    params.set(filterParam('platform'), log.platform);
  }
  if (log.runtimeVersion) params.set(filterParam('runtimeVersion'), log.runtimeVersion);
  return `/observe/events?${params}`;
};
