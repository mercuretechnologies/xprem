// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import { AlertCircle, Bug, Loader2, Search } from 'lucide-react';
import { api, type ErrorFatality, type ErrorSort } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { liveInterval, type ObserveFilters } from './filters';
import { exactNumber, sinceLabel } from './format';
import { OccurrenceHistogram } from './OccurrenceHistogram';
import { ErrorDetailsView } from './ErrorDetailsView';
import { TelemetryUnavailable } from './TelemetryUnavailable';

const errorFatality = (value: string | null): ErrorFatality =>
  value === 'fatal' || value === 'nonfatal' ? value : 'all';

export const ErrorFatalitySelect = ({
  value,
  onChange,
}: {
  value: ErrorFatality;
  onChange: (value: ErrorFatality) => void;
}) => (
  <select
    aria-label="Error fatality"
    value={value}
    onChange={event => onChange(event.target.value as ErrorFatality)}
    className="h-9 rounded-md border border-input bg-card px-3 text-sm outline-none focus:ring-2 focus:ring-ring/20">
    <option value="all">All errors</option>
    <option value="fatal">Fatal only</option>
    <option value="nonfatal">Non-fatal only</option>
  </select>
);

export const ErrorsView = ({ filters }: { filters: ObserveFilters }) => {
  const [params] = useSearchParams();
  const errorId = params.get('errorId');
  const fatality = errorFatality(params.get('errorFatality'));
  const search = params.get('errorSearch') ?? '';
  const sortParam = params.get('errorSort');
  const sort: ErrorSort =
    sortParam === 'impactedDevices' || sortParam === 'lastSeen' ? sortParam : 'occurrences';
  // Remount pagination and selection when the question changes. A live tick
  // alone keeps the page being read; a filter edit starts again at the head.
  const signature = JSON.stringify([
    api.getAppId(),
    filters.state,
    filters.range,
    filters.live,
    fatality,
    search,
    sort,
    errorId,
  ]);
  return errorId ? (
    <ErrorDetailsView key={signature} errorId={errorId} fatality={fatality} filters={filters} />
  ) : (
    <ErrorsList
      signature={signature}
      search={search}
      sort={sort}
      fatality={fatality}
      filters={filters}
    />
  );
};

const ErrorsList = ({
  filters,
  search,
  sort,
  fatality,
  signature,
}: {
  filters: ObserveFilters;
  search: string;
  sort: ErrorSort;
  fatality: ErrorFatality;
  signature: string;
}) => {
  const [params, setParams] = useSearchParams();
  const [draft, setDraft] = useState(search);
  const [pagination, setPagination] = useState({ signature, offset: 0 });
  const offset = pagination.signature === signature ? pagination.offset : 0;
  const setOffset = (value: number) => setPagination({ signature, offset: value });
  const limit = 50;
  const write = (key: string, value: string) =>
    setParams(
      current => {
        const next = new URLSearchParams(current);
        if (value) next.set(key, value);
        else next.delete(key);
        return next;
      },
      { replace: true }
    );

  useEffect(() => setDraft(search), [search]);
  useEffect(() => {
    if (draft.trim() === search) return;
    const timer = window.setTimeout(
      () =>
        setParams(
          current => {
            const next = new URLSearchParams(current);
            if (draft.trim()) next.set('errorSearch', draft.trim());
            else next.delete('errorSearch');
            return next;
          },
          { replace: true }
        ),
      300
    );
    return () => window.clearTimeout(timer);
  }, [draft, search, setParams]);

  const query = useQuery({
    queryKey: ['observe', 'errors', api.getAppId(), filters.query, search, fatality, sort, offset],
    queryFn: () =>
      api.getObserveErrors({
        ...filters.query,
        search,
        fatality,
        sort,
        limit,
        offset,
        includeSeries: true,
      }),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
  });
  const page = query.data;
  const errorHref = (errorId: string) => {
    const next = new URLSearchParams(params);
    next.set('errorId', errorId);
    // Freeze the exact response window, rather than resolving a relative
    // range again after navigation. This makes paused links shareable.
    if (!filters.live && page) {
      next.set('from', page.from);
      next.set('to', page.to);
      next.delete('period');
      next.set('live', '0');
    }
    return `/observe/errors?${next}`;
  };
  if (page?.available === false) return <TelemetryUnavailable />;

  return (
    <section className="overflow-hidden rounded-xl border bg-card shadow-card">
      <header className="flex flex-wrap items-center gap-3 border-b bg-muted/30 p-3">
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            aria-label="Search errors"
            value={draft}
            onChange={event => setDraft(event.target.value)}
            placeholder="Search type, message or location…"
            className="pl-9"
          />
        </div>
        <ErrorFatalitySelect
          value={fatality}
          onChange={value => write('errorFatality', value === 'all' ? '' : value)}
        />
        <select
          aria-label="Sort errors"
          value={sort}
          onChange={event => write('errorSort', event.target.value)}
          className="h-9 rounded-md border border-input bg-card px-3 text-sm outline-none focus:ring-2 focus:ring-ring/20">
          <option value="occurrences">Most occurrences</option>
          <option value="impactedDevices">Most impacted devices</option>
          <option value="lastSeen">Last seen</option>
        </select>
      </header>
      {query.isPending && (
        <p className="flex items-center justify-center gap-2 p-12 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading errors…
        </p>
      )}
      {query.isError && (
        <p className="flex items-center justify-center gap-2 p-12 text-sm text-destructive">
          <AlertCircle className="h-4 w-4" />
          Could not read errors.{' '}
          <button type="button" onClick={() => query.refetch()} className="underline">
            Retry
          </button>
        </p>
      )}
      {page && page.errors.length === 0 && (
        <div className="flex flex-col items-center p-12 text-muted-foreground">
          <Bug className="h-7 w-7" />
          <p className="mt-3 text-sm">No error matches these filters.</p>
        </div>
      )}
      {page && page.errors.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <thead className="border-b bg-muted/20 text-[10px] uppercase tracking-wide text-muted-foreground">
              <tr>
                {[
                  'Error',
                  'Occurrences',
                  'Impacted devices',
                  'Crashes',
                  'Histogram',
                  'Last seen',
                ].map(label => (
                  <th key={label} className="whitespace-nowrap px-4 py-2 font-medium">
                    {label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {page.errors.map(error => (
                <tr
                  key={error.errorId}
                  className="border-b border-border/50 last:border-0 hover:bg-accent/30">
                  <td className="min-w-64 max-w-md px-4 py-3">
                    <Link
                      to={errorHref(error.errorId)}
                      className="block rounded focus-visible:outline focus-visible:outline-ring">
                      <span className="font-medium text-primary">{error.errorType || 'Error'}</span>
                      <span className="mt-0.5 block truncate text-foreground" title={error.message}>
                        {error.message || 'No message'}
                      </span>
                      <span
                        className="mt-1 block truncate font-mono text-[10px] text-muted-foreground"
                        title={error.culprit}>
                        {error.culprit ||
                          (error.symbolicationStatus === 'ready'
                            ? 'Unknown location'
                            : error.symbolicationStatus === 'waiting'
                              ? 'Symbolication pending'
                              : 'Source map unavailable')}
                      </span>
                    </Link>
                  </td>
                  <td className="px-4 py-3 font-mono tabular-nums">
                    {exactNumber.format(error.occurrences)}
                  </td>
                  <td className="px-4 py-3 font-mono tabular-nums">
                    {exactNumber.format(error.impactedDevices)}
                  </td>
                  <td className="px-4 py-3 font-mono tabular-nums">
                    {exactNumber.format(error.crashOccurrences)}
                  </td>
                  <td className="px-4 py-3">
                    <OccurrenceHistogram compact series={error.series ?? []} />
                  </td>
                  <td className="whitespace-nowrap px-4 py-3">
                    <time
                      dateTime={error.lastSeen}
                      title={new Date(error.lastSeen).toLocaleString()}>
                      {sinceLabel(new Date(error.lastSeen))}
                    </time>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <footer className="flex items-center justify-between gap-3 border-t bg-muted/20 px-4 py-2 text-[11px] text-muted-foreground">
        <span>
          {page && page.errors.length > 0
            ? `${offset + 1}–${offset + page.errors.length} errors`
            : 'No errors loaded'}
          {page?.hasMore && offset + limit > 10_000 && ' · Narrow filters to see more'}
        </span>
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={offset === 0 || query.isFetching}
            onClick={() => setOffset(Math.max(0, offset - limit))}>
            Previous
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!page?.hasMore || query.isFetching || offset + limit > 10_000}
            onClick={() => setOffset(offset + limit)}>
            Next
          </Button>
        </div>
      </footer>
    </section>
  );
};
