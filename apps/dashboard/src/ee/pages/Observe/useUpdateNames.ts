// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { buildUpdateGroups, groupTitle } from './updateGroups';

// What each update is called, keyed by every id it answers to: its own uuid
// and the publish group it belongs to. Eight hex characters identify an update
// but say nothing about it, so anywhere one is displayed can look up what the
// person who shipped it wrote.
//
// Shares its query key with the filter bar's update picker, so a page that
// shows both pays for one request.
export const useUpdateNames = () => {
  const updatesQuery = useQuery({
    queryKey: ['observe', 'update-options', api.getAppId(), []],
    queryFn: () => api.getUpdateFeed({ limit: 100 }),
  });

  return useMemo(() => {
    const names = new Map<string, string>();
    for (const group of buildUpdateGroups(updatesQuery.data?.items ?? [])) {
      const title = groupTitle(group);
      // A publish with no message names itself by its id, which the caller
      // already displays; nothing to add.
      if (title === group.shortId) continue;
      if (group.publishGroup) names.set(group.publishGroup, title);
      for (const updateUUID of group.updateUUIDs) names.set(updateUUID, title);
    }
    return names;
  }, [updatesQuery.data?.items]);
};
