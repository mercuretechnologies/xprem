// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useEffect, useId, useRef, useState } from 'react';
import { useInfiniteQuery } from '@tanstack/react-query';
import { Link, useLocation } from 'react-router';
import { AlertCircle, ArrowLeft, Loader2 } from 'lucide-react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { exactNumber } from './format';
import { shortUUID, logTime } from './logRecords';
import { occurrenceEventsHref } from './occurrenceEvents';
import { deviceName } from './deviceNames';
import { errorsListHref, errorsListParams } from './errorNavigation';
import { ErrorBreakdown } from './ErrorBreakdown';
import { OccurrenceHistogram } from './OccurrenceHistogram';
import { LogDetails } from './LogDetails';
import { TelemetryUnavailable } from './TelemetryUnavailable';
import { useUpdateNames } from './useUpdateNames';

type OccurrencePageParam = { cursor?: string; asOf?: string };

export const ErrorDetailsView = ({ errorId }: { errorId: string }) => {
  const { state } = useLocation();
  // A new revision reopens a trace only on an explicit View trace click.
  const [selection, setSelection] = useState<{ eventKey: string; revision: number } | null>(null);
  const tracePanel = useRef<HTMLElement>(null);
  const tracePanelId = useId();
  useEffect(() => {
    if (!selection) return;
    tracePanel.current?.focus({ preventScroll: true });
    tracePanel.current?.scrollIntoView({ block: 'start', behavior: 'auto' });
  }, [selection]);
  const updateNames = useUpdateNames();
  const query = useInfiniteQuery({
    queryKey: ['observe', 'error-details', api.getAppId(), errorId],
    initialPageParam: {} as OccurrencePageParam,
    queryFn: ({ pageParam }) =>
      api.getObserveErrorDetails(errorId, {
        scope: 'all',
        limit: 50,
        ...pageParam,
      }),
    getNextPageParam: (last, pages): OccurrencePageParam | undefined =>
      last.nextCursor ? { cursor: last.nextCursor, asOf: pages[0].asOf } : undefined,
    // Keep the selected occurrence stable while its stack is open.
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
    occurrences.find(log => log.eventKey === selection?.eventKey) ??
    occurrences[0] ??
    details?.representativeOccurrence;
  const back = errorsListParams(state);

  return (
    <div className="space-y-4">
      <header className="space-y-3">
        <Link
          to={errorsListHref(back)}
          className="inline-flex items-center gap-2 text-xs text-primary hover:underline">
          <ArrowLeft className="h-3.5 w-3.5" />
          All errors
        </Link>
        <h1 className="font-display text-[26px] font-semibold tracking-tight">Error details</h1>
        <p className="text-sm text-muted-foreground">All retained occurrences of this error.</p>
      </header>
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
          This error has no retained occurrences.
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
            <OccurrenceHistogram
              series={details.series}
              from={details.from}
              to={details.to}
              bucketSeconds={details.bucketSeconds}
            />
          </section>
          {selected && (
            <section
              ref={tracePanel}
              id={tracePanelId}
              tabIndex={-1}
              aria-labelledby={`${tracePanelId}-heading`}
              className="scroll-mt-6 overflow-hidden rounded-xl border bg-card shadow-card focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">
              <header className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
                <h3 id={`${tracePanelId}-heading`} className="text-sm font-medium">
                  Selected occurrence
                </h3>
                <time dateTime={selected.timestamp} className="text-[11px] text-muted-foreground">
                  {new Date(selected.timestamp).toLocaleString()}
                </time>
              </header>
              <LogDetails key={`${selected.eventKey}:${selection?.revision ?? 0}`} log={selected} />
            </section>
          )}
          <div className="grid gap-4 lg:grid-cols-2">
            <ErrorBreakdown
              title="Updates"
              dimension="updates"
              segments={details.updates}
              listParams={back}
            />
            <ErrorBreakdown
              title="Device models"
              dimension="deviceModels"
              segments={details.deviceModels}
              listParams={back}
            />
            <ErrorBreakdown
              title="OS versions"
              dimension="osVersions"
              segments={details.osVersions}
              listParams={back}
            />
            <ErrorBreakdown
              title="Runtimes"
              dimension="runtimes"
              segments={details.runtimes}
              listParams={back}
            />
          </div>
          <section className="overflow-hidden rounded-xl border bg-card shadow-card">
            <header className="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-3">
              <h3 className="text-sm font-medium">Occurrences</h3>
              <span className="text-[11px] text-muted-foreground">{occurrences.length} loaded</span>
            </header>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs">
                <thead className="border-b text-[10px] uppercase tracking-wide text-muted-foreground">
                  <tr>
                    {['Date / time', 'Update', 'Runtime', 'Device', 'OS', 'Fatality', 'Trace'].map(
                      label => (
                        <th key={label} className="whitespace-nowrap px-4 py-2 font-medium">
                          {label}
                        </th>
                      )
                    )}
                  </tr>
                </thead>
                <tbody>
                  {occurrences.map(log => (
                    <tr
                      key={log.eventKey}
                      className={`border-b border-border/50 last:border-0 ${selected?.eventKey === log.eventKey ? 'bg-primary/[0.06]' : 'hover:bg-accent/30'}`}>
                      <td className="whitespace-nowrap px-4 py-3">
                        <Link
                          to={occurrenceEventsHref(log)}
                          title="View this event and the events leading up to it"
                          className="rounded font-mono text-primary hover:underline focus-visible:outline focus-visible:outline-ring">
                          <time dateTime={log.timestamp} title={log.timestamp}>
                            {logTime.format(new Date(log.timestamp))}
                          </time>
                        </Link>
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
                      <td className="whitespace-nowrap px-4 py-3">
                        <button
                          type="button"
                          onClick={() =>
                            setSelection(current => ({
                              eventKey: log.eventKey,
                              revision: (current?.revision ?? 0) + 1,
                            }))
                          }
                          aria-controls={tracePanelId}
                          aria-pressed={selected?.eventKey === log.eventKey}
                          className="rounded text-primary hover:underline focus-visible:outline focus-visible:outline-ring">
                          View trace
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <footer className="flex items-center justify-between gap-3 border-t bg-muted/20 px-4 py-2 text-[11px] text-muted-foreground">
              <span>
                {exactNumber.format(occurrences.length)} occurrences loaded
                {!query.hasNextPage && ' · end of history'}
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
