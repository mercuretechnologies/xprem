// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import { Bug, ChevronRight, Code2, Loader2 } from 'lucide-react';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { formatTimestamp } from '@/lib/utils';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { errorDetailsHref } from '@/ee/pages/Observe/errorNavigation';
import { exactNumber } from '@/ee/pages/Observe/format';
import { ApiError } from '@/components/APIError';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';

const MAX_OFFSET = 10_000;

export const UpdateErrorsSection = ({ updateUUID }: { updateUUID: string }) => {
  const { selectedAppId } = useSelectedApp();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const canObserve = useAppPermission('observe:read', 'any-member');
  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
    enabled: CONTROL_PLANE_ENABLED,
  });
  const licensed = licenseQuery.data?.valid === true;
  const scope = JSON.stringify([selectedAppId, updateUUID]);
  const [pagination, setPagination] = useState({ scope, offset: 0 });
  const offset = pagination.scope === scope ? pagination.offset : 0;
  const setOffset = (value: number) => setPagination({ scope, offset: value });
  const limit = 25;
  const query = useQuery({
    queryKey: ['update-errors', selectedAppId, updateUUID, limit, offset],
    queryFn: () => api.getUpdateErrors(updateUUID, { limit, offset }),
    enabled: CONTROL_PLANE_ENABLED && licensed && canObserve && !!selectedAppId && !!updateUUID,
  });

  if (!CONTROL_PLANE_ENABLED || !licensed || !canObserve || !selectedAppId || !updateUUID)
    return null;

  const page = query.data;
  return (
    <section className="space-y-3">
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Errors</h2>
        <p className="text-xs text-muted-foreground">
          Error groups observed on this update. New means first observed on this update.
        </p>
      </div>
      <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
        {query.isError ? (
          <div className="space-y-3 p-4">
            <ApiError error={query.error} />
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={query.isFetching}
              onClick={() => void query.refetch()}>
              Retry
            </Button>
          </div>
        ) : query.isPending ? (
          <p className="flex items-center justify-center gap-2 p-10 text-sm text-muted-foreground">
            <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />
            Loading errors…
          </p>
        ) : page?.available === false ? (
          <p className="p-6 text-sm text-muted-foreground">
            Error tracking is unavailable for this app.
          </p>
        ) : page && page.errors.length === 0 ? (
          <div className="flex flex-col items-center px-6 py-10 text-center">
            <Bug aria-hidden="true" className="h-6 w-6 text-muted-foreground" />
            <p className="mt-3 text-sm font-medium">
              {offset > 0 ? 'No more error groups' : 'No errors observed on this update'}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              {offset > 0
                ? 'Return to the previous page to see the error groups already observed.'
                : 'Error groups appear here when devices report errors for this update.'}
            </p>
          </div>
        ) : page ? (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-[13px]">
              <caption className="sr-only">Error groups observed on this update</caption>
              <thead className="border-b bg-muted/60 text-xs text-muted-foreground">
                <tr>
                  {[
                    'Error',
                    'New',
                    'Occurrences',
                    'Devices',
                    'Crashes',
                    'First seen',
                    'Last seen',
                  ].map(label => (
                    <th
                      key={label}
                      scope="col"
                      className={`whitespace-nowrap px-4 py-3 font-medium ${['Occurrences', 'Devices', 'Crashes'].includes(label) ? 'text-right' : ''}`}>
                      {label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {page.errors.map(error => (
                  <tr
                    key={error.errorId}
                    className="group border-b border-border/70 last:border-0 hover:bg-primary/[0.035] focus-within:bg-primary/[0.035]">
                    <td className="w-full min-w-64 max-w-lg px-4 py-4">
                      <Link
                        to={errorDetailsHref(error.errorId)}
                        className="flex items-center gap-3 rounded focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring">
                        <span className="min-w-0 flex-1">
                          <span className="block font-semibold">{error.errorType || 'Error'}</span>
                          <span className="mt-1 block truncate" title={error.message}>
                            {error.message || 'No message'}
                          </span>
                          <span className="mt-1.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                            <Code2 aria-hidden="true" className="h-3 w-3 shrink-0" />
                            <span className="truncate" title={error.culprit}>
                              {error.culprit ||
                                (error.symbolicationStatus === 'ready'
                                  ? 'Unknown location'
                                  : error.symbolicationStatus === 'waiting'
                                    ? 'Symbolication pending'
                                    : 'Source map unavailable')}
                            </span>
                          </span>
                        </span>
                        <ChevronRight
                          aria-hidden="true"
                          className="h-4 w-4 shrink-0 text-muted-foreground/40 group-hover:text-primary"
                        />
                      </Link>
                    </td>
                    <td className="whitespace-nowrap px-4 py-4">
                      {error.new === true ? (
                        <Badge
                          variant="outline"
                          title="First observed on this update"
                          className="border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300">
                          New
                        </Badge>
                      ) : error.new === null ? (
                        <span
                          className="text-xs text-muted-foreground"
                          title="The first-observed update is not available yet">
                          Unknown
                        </span>
                      ) : (
                        <span
                          className="text-muted-foreground"
                          title="Observed on another update before or at the same time">
                          —
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-4 text-right font-medium tabular-nums">
                      {exactNumber.format(error.occurrences)}
                    </td>
                    <td className="px-4 py-4 text-right tabular-nums">
                      {exactNumber.format(error.impactedDevices)}
                    </td>
                    <td className="px-4 py-4 text-right tabular-nums">
                      <span
                        className={
                          error.crashOccurrences > 0
                            ? 'rounded-md bg-amber-500/10 px-2 py-0.5 text-amber-800 dark:text-amber-300'
                            : 'text-muted-foreground/60'
                        }>
                        {exactNumber.format(error.crashOccurrences)}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-4 text-xs text-muted-foreground">
                      <time dateTime={error.firstSeen}>
                        {formatTimestamp(error.firstSeen, true) || '—'}
                      </time>
                    </td>
                    <td className="whitespace-nowrap px-4 py-4 text-xs text-muted-foreground">
                      <time dateTime={error.lastSeen}>
                        {formatTimestamp(error.lastSeen, true) || '—'}
                      </time>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        {page?.available && !query.isError && (page.errors.length > 0 || offset > 0) && (
          <footer className="flex items-center justify-between gap-3 border-t bg-muted/30 px-4 py-3 text-xs text-muted-foreground">
            <span>
              {page.errors.length > 0
                ? `${offset + 1}–${offset + page.errors.length} error groups`
                : 'No more error groups'}
            </span>
            <div className="flex gap-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={offset === 0 || query.isFetching}
                onClick={() => setOffset(Math.max(0, offset - limit))}>
                Previous
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={!page.hasMore || query.isFetching || offset >= MAX_OFFSET}
                onClick={() => setOffset(offset + limit)}>
                Next
              </Button>
            </div>
          </footer>
        )}
      </div>
    </section>
  );
};
