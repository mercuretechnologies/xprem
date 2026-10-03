// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { useInfiniteQuery } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import { AlertCircle, ArrowLeft, Loader2 } from 'lucide-react';
import { api, type ErrorFatality } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { liveInterval, type ObserveFilters } from './filters';
import { exactNumber } from './format';
import { shortUUID, logTime } from './logRecords';
import { deviceName } from './deviceNames';
import { ErrorFatalitySelect } from './ErrorsView';
import { ErrorBreakdown } from './ErrorBreakdown';
import { OccurrenceHistogram } from './OccurrenceHistogram';
import { LogDetails } from './LogDetails';
import { TelemetryUnavailable } from './TelemetryUnavailable';
import { useUpdateNames } from './useUpdateNames';

type OccurrencePageParam = { cursor?: string; from?: string; to?: string };

export const ErrorDetailsView = ({
  errorId,
  fatality,
  filters,
}: {
  errorId: string;
  fatality: ErrorFatality;
  filters: ObserveFilters;
}) => {
  const [params, setParams] = useSearchParams();
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const updateNames = useUpdateNames();
  const query = useInfiniteQuery({
    // A live tick advances the next read, but does not discard pages while
    // somebody is reading a stack. Explicit filter changes remount this view.
    queryKey: [
      'observe',
      'error-details',
      api.getAppId(),
      errorId,
      filters.state,
      filters.range,
      filters.live,
      fatality,
    ],
    initialPageParam: {} as OccurrencePageParam,
    queryFn: ({ pageParam }) =>
      api.getObserveErrorDetails(errorId, {
        ...filters.query,
        fatality,
        limit: 50,
        ...pageParam,
      }),
    getNextPageParam: (last, pages): OccurrencePageParam | undefined =>
      last.nextCursor
        ? { cursor: last.nextCursor, from: pages[0].from, to: pages[0].to }
        : undefined,
    // Once an occurrence or older page is selected, keep all panels on the
    // head's exact window. Cursor pages use those same absolute bounds.
    refetchInterval: state =>
      selectedKey !== null || (state.state.data?.pages.length ?? 0) > 1
        ? false
        : liveInterval(filters.live, filters.periodSpec),
    refetchOnWindowFocus: false,
  });
  const details = query.data?.pages[0];
  const summary = details?.summary;
  const occurrences = [
    ...new Map(
      (query.data?.pages.flatMap(page => page.occurrences) ?? []).map(log => [log.eventKey, log])
    ).values(),
  ];
  const selected =
    occurrences.find(log => log.eventKey === selectedKey) ??
    details?.representativeOccurrence ??
    occurrences[0];
  const back = new URLSearchParams(params);
  back.delete('errorId');
  const pausedWhileReading =
    filters.live && (selectedKey !== null || (query.data?.pages.length ?? 0) > 1);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Link
          to={`/observe/errors${back.size ? `?${back}` : ''}`}
          className="inline-flex items-center gap-2 text-xs text-primary hover:underline">
          <ArrowLeft className="h-3.5 w-3.5" />
          All errors
        </Link>
        <ErrorFatalitySelect
          value={fatality}
          onChange={value =>
            setParams(
              current => {
                const next = new URLSearchParams(current);
                if (value === 'all') next.delete('errorFatality');
                else next.set('errorFatality', value);
                return next;
              },
              { replace: true }
            )
          }
        />
      </div>
      {query.isPending && (
        <p className="flex items-center gap-2 p-8 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading error…
        </p>
      )}
      {query.isError && (
        <p className="flex items-center gap-2 p-8 text-sm text-destructive">
          <AlertCircle className="h-4 w-4" />
          Could not read this error.{' '}
          <button type="button" onClick={() => query.refetch()} className="underline">
            Retry
          </button>
        </p>
      )}
      {details?.available === false && <TelemetryUnavailable />}
      {details?.available && !summary && (
        <p className="rounded-xl border bg-card p-8 text-sm text-muted-foreground">
          This error has no occurrences in the selected period and filters.
        </p>
      )}
      {details?.available && summary && (
        <>
          <section className="rounded-xl border bg-card p-5 shadow-card">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="text-lg font-semibold">{summary.errorType || 'Error'}</h2>
              <span className="rounded-full border px-2 py-0.5 text-[10px] text-muted-foreground">
                {summary.symbolicationStatus === 'ready'
                  ? 'Symbolicated'
                  : summary.symbolicationStatus === 'waiting'
                    ? 'Symbolication pending'
                    : 'Source map unavailable'}
              </span>
            </div>
            <p className="mt-2 break-words text-sm">{summary.message || 'No message'}</p>
            {summary.culprit && (
              <p className="mt-2 break-all font-mono text-xs text-muted-foreground">
                {summary.culprit}
              </p>
            )}
            <dl className="mt-5 grid gap-4 sm:grid-cols-3 lg:grid-cols-5">
              {[
                ['Occurrences', exactNumber.format(summary.occurrences)],
                ['Impacted devices', exactNumber.format(summary.impactedDevices)],
                ['Crashes', exactNumber.format(summary.crashOccurrences)],
                ['First seen', new Date(summary.firstSeen).toLocaleString()],
                ['Last seen', new Date(summary.lastSeen).toLocaleString()],
              ].map(([label, value]) => (
                <div key={label}>
                  <dt className="text-[11px] text-muted-foreground">{label}</dt>
                  <dd className="mt-1 font-mono text-sm tabular-nums">{value}</dd>
                </div>
              ))}
            </dl>
          </section>
          <section className="rounded-xl border bg-card p-5 shadow-card">
            <h3 className="mb-4 text-sm font-medium">Occurrences over time</h3>
            <OccurrenceHistogram series={details.series} from={details.from} to={details.to} />
          </section>
          {selected && (
            <section className="overflow-hidden rounded-xl border bg-card shadow-card">
              <header className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
                <h3 className="text-sm font-medium">Selected occurrence</h3>
                <time dateTime={selected.timestamp} className="text-[11px] text-muted-foreground">
                  {new Date(selected.timestamp).toLocaleString()}
                </time>
              </header>
              {/* LogDetails delegates exception stacks to StackTraceView using
                  this occurrence's update ID and raw fingerprint. */}
              <LogDetails key={selected.eventKey} log={selected} />
            </section>
          )}
          <div className="grid gap-4 lg:grid-cols-2">
            <ErrorBreakdown
              title="Updates"
              dimension="updates"
              segments={details.updates}
              filters={filters}
            />
            <ErrorBreakdown
              title="Device models"
              dimension="deviceModels"
              segments={details.deviceModels}
              filters={filters}
            />
            <ErrorBreakdown
              title="OS versions"
              dimension="osVersions"
              segments={details.osVersions}
              filters={filters}
            />
            <ErrorBreakdown
              title="Runtimes"
              dimension="runtimes"
              segments={details.runtimes}
              filters={filters}
            />
          </div>
          <section className="overflow-hidden rounded-xl border bg-card shadow-card">
            <header className="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
              <h3 className="text-sm font-medium">Occurrences</h3>
              <span className="text-[11px] text-muted-foreground">
                {pausedWhileReading
                  ? 'Live refresh paused while you read'
                  : `${occurrences.length} loaded`}
              </span>
            </header>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs">
                <thead className="border-b text-[10px] uppercase tracking-wide text-muted-foreground">
                  <tr>
                    {['Date / time', 'Update', 'Runtime', 'Device', 'OS', 'Fatality'].map(label => (
                      <th key={label} className="whitespace-nowrap px-4 py-2 font-medium">
                        {label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {occurrences.map(log => (
                    <tr
                      key={log.eventKey}
                      className={`border-b border-border/50 last:border-0 ${selected?.eventKey === log.eventKey ? 'bg-primary/[0.06]' : 'hover:bg-accent/30'}`}>
                      <td className="whitespace-nowrap px-4 py-3">
                        <button
                          type="button"
                          onClick={() => setSelectedKey(log.eventKey)}
                          aria-pressed={selected?.eventKey === log.eventKey}
                          className="rounded font-mono text-primary hover:underline">
                          <time dateTime={log.timestamp} title={log.timestamp}>
                            {logTime.format(new Date(log.timestamp))}
                          </time>
                        </button>
                      </td>
                      <td className="max-w-52 truncate px-4 py-3 font-mono" title={log.updateId}>
                        {updateNames.get(log.updateId) ||
                          (shortUUID(log.updateId) === '-'
                            ? 'Embedded bundle'
                            : shortUUID(log.updateId))}
                      </td>
                      <td className="px-4 py-3 font-mono">{log.runtimeVersion || 'Unknown'}</td>
                      <td className="px-4 py-3">
                        <span className="block font-mono" title={log.easClientId}>
                          {shortUUID(log.easClientId)}
                        </span>
                        <span className="text-[10px] text-muted-foreground">
                          {log.deviceModel ? deviceName(log.deviceModel).label : 'Unknown model'}
                        </span>
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        {`${log.osName} ${log.osVersion}`.trim() || 'Unknown'}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        {log.isFatal || log.eventName === 'xprem_js_crash' ? 'Fatal' : 'Non-fatal'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <footer className="flex items-center justify-between gap-3 border-t bg-muted/20 px-4 py-2 text-[11px] text-muted-foreground">
              <span>
                {exactNumber.format(occurrences.length)} occurrences loaded
                {!query.hasNextPage && ' · end of range'}
              </span>
              <Button
                size="sm"
                variant="outline"
                disabled={!query.hasNextPage || query.isFetching}
                onClick={() => query.fetchNextPage()}>
                {query.isFetchingNextPage && <Loader2 className="h-3.5 w-3.5 animate-spin" />}Load
                more
              </Button>
            </footer>
            {query.isFetchNextPageError && (
              <p className="px-4 pb-3 text-xs text-destructive">
                Could not load older occurrences. Use Load more to retry.
              </p>
            )}
          </section>
        </>
      )}
    </div>
  );
};
