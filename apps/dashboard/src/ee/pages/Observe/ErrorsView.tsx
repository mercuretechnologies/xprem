// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import {
  AlertCircle,
  ArrowDownWideNarrow,
  Bug,
  ChevronDown,
  ChevronRight,
  Code2,
  Loader2,
  Search,
  X,
} from 'lucide-react';
import { api, type ErrorFatality, type ErrorSort } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { liveInterval, type ObserveFilters } from './filters';
import { exactNumber, sinceLabel } from './format';
import { OccurrenceHistogram } from './OccurrenceHistogram';
import { errorDetailsHref, errorFatality } from './errorNavigation';
import { TelemetryUnavailable } from './TelemetryUnavailable';

export const ErrorFatalitySelect = ({
  value,
  onChange,
}: {
  value: ErrorFatality;
  onChange: (value: ErrorFatality) => void;
}) => (
  <div className="relative">
    <select
      aria-label="Error fatality"
      value={value}
      onChange={event => onChange(event.target.value as ErrorFatality)}
      className="h-9 appearance-none rounded-md border border-input bg-card pl-3 pr-9 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/20">
      <option value="all">All errors</option>
      <option value="fatal">Fatal only</option>
      <option value="nonfatal">Non-fatal only</option>
    </select>
    <ChevronDown
      aria-hidden="true"
      className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
    />
  </div>
);

export const ErrorsView = ({ filters }: { filters: ObserveFilters }) => {
  const [params] = useSearchParams();
  const fatality = errorFatality(params.get('errorFatality'));
  const search = params.get('errorSearch') ?? '';
  const sortParam = params.get('errorSort');
  const sort: ErrorSort =
    sortParam === 'occurrences' || sortParam === 'impactedDevices' ? sortParam : 'lastSeen';
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
  ]);
  return (
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
  if (page?.available === false) return <TelemetryUnavailable />;

  return (
    <section className="overflow-hidden rounded-xl border bg-card shadow-card">
      <header className="flex flex-wrap items-center gap-2.5 border-b p-3">
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            aria-label="Search errors"
            value={draft}
            onChange={event => setDraft(event.target.value)}
            placeholder="Search type, message or location…"
            className="bg-muted/40 pl-9 pr-9 shadow-none dark:bg-muted/40"
          />
          {draft && (
            <button
              type="button"
              aria-label="Clear error search"
              onClick={() => setDraft('')}
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline focus-visible:outline-ring">
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        <ErrorFatalitySelect
          value={fatality}
          onChange={value => write('errorFatality', value === 'all' ? '' : value)}
        />
        <div className="relative">
          <ArrowDownWideNarrow className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <select
            aria-label="Sort errors"
            value={sort}
            onChange={event => write('errorSort', event.target.value)}
            className="h-9 appearance-none rounded-md border border-input bg-card pl-9 pr-9 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/20">
            <option value="lastSeen">Last seen</option>
            <option value="occurrences">Most occurrences</option>
            <option value="impactedDevices">Most impacted devices</option>
          </select>
          <ChevronDown
            aria-hidden="true"
            className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
          />
        </div>
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
        <div className="flex flex-col items-center px-6 py-16 text-center">
          <span className="rounded-2xl bg-primary/10 p-3 text-primary">
            <Bug className="h-6 w-6" />
          </span>
          <p className="mt-4 text-sm font-medium">No errors here</p>
          <p className="mt-1 text-sm text-muted-foreground">
            Try another time range or adjust your filters.
          </p>
        </div>
      )}
      {page && page.errors.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-[13px]">
            <caption className="sr-only">Error groups in the selected time range</caption>
            <thead className="border-b bg-muted/60 text-xs text-muted-foreground">
              <tr>
                {[
                  'Error',
                  'Last seen',
                  'First seen',
                  'Trend',
                  'Occurrences',
                  'Devices',
                  'Crashes',
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
                  <td className="w-full min-w-80 max-w-lg px-4 py-4">
                    <Link
                      to={errorDetailsHref(error.errorId)}
                      state={{ errorsSearch: params.toString() }}
                      className="flex items-center gap-4 rounded focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring">
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 font-semibold">
                          <span
                            aria-hidden="true"
                            className="h-2 w-2 shrink-0 rounded-full bg-primary/80"
                          />
                          {error.errorType || 'Error'}
                        </span>
                        <span
                          className={`ml-3.5 mt-1 block truncate border-l-2 pl-2 ${error.crashOccurrences > 0 ? 'border-amber-500/80' : 'border-primary/35'}`}
                          title={error.message}>
                          {error.message || 'No message'}
                        </span>
                        <span className="ml-3.5 mt-1.5 flex items-center gap-1.5 text-xs text-muted-foreground">
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
                  <td className="whitespace-nowrap px-4 py-4 text-muted-foreground">
                    <time
                      dateTime={error.lastSeen}
                      title={new Date(error.lastSeen).toLocaleString()}>
                      {sinceLabel(new Date(error.lastSeen))}
                    </time>
                  </td>
                  <td className="whitespace-nowrap px-4 py-4 text-muted-foreground">
                    <time
                      dateTime={error.firstSeen}
                      title={new Date(error.firstSeen).toLocaleString()}>
                      {sinceLabel(new Date(error.firstSeen))}
                    </time>
                  </td>
                  <td className="px-4 py-4">
                    <OccurrenceHistogram compact series={error.series ?? []} />
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
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <footer className="flex items-center justify-between gap-3 border-t bg-muted/30 px-4 py-3 text-xs text-muted-foreground">
        <span>
          {page && page.errors.length > 0
            ? `${offset + 1}–${offset + page.errors.length} error groups`
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
