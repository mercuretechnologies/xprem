import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useSearchParams } from 'react-router';
import {
  AlertCircle,
  ChevronDown,
  ChevronRight,
  CirclePause,
  CirclePlay,
  Loader2,
  Search,
} from 'lucide-react';
import { api, ObserveLog, ObserveLogsQuery } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { liveInterval, type ObserveFilters } from './filters';
import { MultiSelect } from './MultiSelect';
import { DeviceSheet } from './DeviceSheet';
import { deviceName, osLabel } from './deviceNames';
import { compactNumber, sinceLabel } from './format';
import { exactTime, logMessage, severityDot, shortID } from './logRecords';
import { LogDetails } from './LogDetails';
import { useLogStream } from './useLogStream';
import { useUpdateNames } from './useUpdateNames';
import { TelemetryUnavailable } from './TelemetryUnavailable';

type Severity = NonNullable<ObserveLogsQuery['severity']>;

const severityOptions: Array<{ value: Severity; label: string }> = [
  { value: '', label: 'All levels' },
  { value: 'debug', label: 'Debug' },
  { value: 'info', label: 'Info' },
  { value: 'warn', label: 'Warning' },
  { value: 'error', label: 'Error' },
  { value: 'fatal', label: 'Fatal' },
];

const isSeverity = (value: string | null): value is Severity =>
  severityOptions.some(option => option.value === value);

// Shared by the header and every row so the columns cannot drift apart.
const rowGrid =
  'grid grid-cols-[22px_minmax(0,1.5fr)_minmax(0,1fr)_86px] md:grid-cols-[22px_minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)_86px] xl:grid-cols-[22px_minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)_140px_86px]';

const EventRow = ({
  log,
  updateName,
  expanded,
  onToggle,
  onOpenDevice,
}: {
  log: ObserveLog;
  updateName?: string;
  expanded: boolean;
  onToggle: () => void;
  onOpenDevice: () => void;
}) => {
  const message = logMessage(log);
  const timestamp = new Date(log.timestamp);
  return (
    <>
      {/* A div rather than a button: the device opens its own panel, and a
          button inside a button is not a thing a browser will render. */}
      <div
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        onClick={onToggle}
        onKeyDown={event => {
          if (event.key !== 'Enter' && event.key !== ' ') return;
          event.preventDefault();
          onToggle();
        }}
        className={`${rowGrid} w-full cursor-default items-center px-3 py-2 text-left text-[11px] text-muted-foreground outline-none transition-colors hover:bg-accent/50 focus-visible:bg-accent`}>
        {expanded ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 opacity-60" />
        )}
        <span className="flex min-w-0 items-center gap-2 pr-4">
          <i className={`h-1.5 w-1.5 shrink-0 rounded-full ${severityDot(log)}`} />
          <span className="min-w-0">
            <span className="block truncate font-mono text-xs text-foreground">
              {log.eventName || 'Log record'}
            </span>
            {message !== log.eventName && (
              <span className="mt-0.5 block truncate font-sans text-[11px] opacity-80">
                {message}
              </span>
            )}
          </span>
        </span>
        <button
          type="button"
          title={log.easClientId}
          // The row expands on click; this one has somewhere else to go.
          onClick={event => {
            event.stopPropagation();
            onOpenDevice();
          }}
          className="flex min-w-0 items-center gap-2 rounded px-1 py-0.5 text-left hover:bg-primary/10">
          <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/15 font-mono text-[9px] text-primary">
            {log.easClientId.slice(0, 2)}
          </span>
          <span className="min-w-0">
            <span className="block truncate font-mono text-xs text-foreground">
              {log.easClientId.slice(0, 8)}
            </span>
            {log.deviceModel && (
              <span className="mt-0.5 block truncate text-[11px] opacity-80">
                {deviceName(log.deviceModel).label}
              </span>
            )}
          </span>
        </button>
        <span className="hidden min-w-0 pr-4 md:block" title={log.updateId}>
          <span className="block truncate text-xs text-foreground">
            {updateName || shortID(log.updateId)}
          </span>
          {log.branch && <span className="mt-0.5 block truncate text-[11px]">{log.branch}</span>}
        </span>
        <span className="hidden min-w-0 pr-4 xl:block">
          <span className="block truncate text-xs text-foreground">
            {osLabel(log.osName, log.osVersion) || '-'}
          </span>
          {log.appVersion && (
            <span className="mt-0.5 block truncate font-mono text-[11px]">
              {log.appVersion}
              {log.appBuildNumber ? ` (${log.appBuildNumber})` : ''}
            </span>
          )}
        </span>
        <time dateTime={log.timestamp} title={exactTime.format(timestamp)} className="text-right">
          {sinceLabel(timestamp)}
        </time>
      </div>
      {expanded && <LogDetails log={log} />}
    </>
  );
};

export const EventsView = ({ filters }: { filters: ObserveFilters }) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const [expanded, setExpanded] = useState<string | null>(null);
  const [device, setDevice] = useState<ObserveLog | null>(null);
  // Joined then split so the array is the same object across renders: it feeds
  // a query key and a stream signature, and a fresh array on every render
  // would restart the stream on every render.
  const eventSelection = searchParams.getAll('event').join('\n');
  const selectedEvents = useMemo(
    () => (eventSelection ? eventSelection.split('\n') : []),
    [eventSelection]
  );
  // Search and level live in the URL too: this is where people land from a
  // device or an error, and where they paste a link to a colleague.
  const search = searchParams.get('q') ?? '';
  const severity: Severity = isSeverity(searchParams.get('level'))
    ? (searchParams.get('level') as Severity)
    : '';
  const [draftSearch, setDraftSearch] = useState(search);

  useEffect(() => setDraftSearch(search), [search]);
  useEffect(() => {
    if (draftSearch === search) return;
    const timer = window.setTimeout(
      () =>
        setSearchParams(
          current => {
            const next = new URLSearchParams(current);
            if (draftSearch.trim()) next.set('q', draftSearch.trim());
            else next.delete('q');
            return next;
          },
          { replace: true }
        ),
      300
    );
    return () => window.clearTimeout(timer);
  }, [draftSearch, search, setSearchParams]);

  // The event names double as the filter's options and as the volume behind
  // each one, which is what tells a name worth reading from a name worth
  // ignoring.
  const namesQuery = useQuery({
    queryKey: ['observe', 'events', api.getAppId(), filters.query],
    queryFn: () => api.getObserveEvents(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  const eventOptions = (namesQuery.data?.events ?? []).map(event => ({
    value: event.name,
    label: event.name,
    detail: `${compactNumber.format(event.count)} records · ${compactNumber.format(event.users)} devices`,
  }));

  const toggleEvent = (name: string) =>
    setSearchParams(
      current => {
        const next = new URLSearchParams(current);
        const chosen = next.getAll('event');
        next.delete('event');
        for (const value of chosen.includes(name)
          ? chosen.filter(entry => entry !== name)
          : [...chosen, name]) {
          next.append('event', value);
        }
        return next;
      },
      { replace: true }
    );

  const query = useMemo<ObserveLogsQuery>(
    () => ({ ...filters.query, eventName: selectedEvents, search, severity, limit: 200 }),
    [filters.query, selectedEvents, search, severity]
  );

  // Any change of what is being asked starts a new stream. Keyed on the
  // filters themselves, never on `query`: in live mode the window slides on
  // every tick, and depending on it would reset the stream once a minute,
  // which is exactly what pausing is supposed to prevent.
  const streamSignature = useMemo(
    () => JSON.stringify([filters.state, selectedEvents, search, severity]),
    [filters.state, selectedEvents, search, severity]
  );
  useEffect(() => setExpanded(null), [streamSignature]);

  const { scrollRef, logs, headQuery, older, paused, setPaused, resume, virtualizer } =
    useLogStream({
      query,
      signature: streamSignature,
      live: filters.live,
      periodSpec: filters.periodSpec,
      rowHeight: 48,
    });

  const updateNames = useUpdateNames();
  const tailing = filters.live && !paused;

  if (!headQuery.isLoading && headQuery.data?.available === false) return <TelemetryUnavailable />;

  return (
    <section className="overflow-hidden rounded-xl border bg-card shadow-card">
      <header className="flex flex-col gap-3 border-b bg-muted/30 p-3 lg:flex-row lg:items-center">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={draftSearch}
            onChange={event => setDraftSearch(event.target.value)}
            placeholder="Search event name, message or attributes…"
            className="pl-9 font-mono"
          />
        </div>
        <div className="flex items-center gap-2">
          <MultiSelect
            className="w-48"
            label="Event"
            loading={namesQuery.isLoading}
            values={selectedEvents}
            onToggle={toggleEvent}
            onClear={() =>
              setSearchParams(
                current => {
                  const next = new URLSearchParams(current);
                  next.delete('event');
                  return next;
                },
                { replace: true }
              )
            }
            options={eventOptions}
          />
          <select
            aria-label="Severity"
            value={severity}
            onChange={event =>
              setSearchParams(
                current => {
                  const next = new URLSearchParams(current);
                  if (event.target.value) next.set('level', event.target.value);
                  else next.delete('level');
                  return next;
                },
                { replace: true }
              )
            }
            className="h-9 rounded-md border border-input bg-card px-3 text-sm text-foreground outline-none focus:border-ring focus:ring-2 focus:ring-ring/20">
            {severityOptions.map(option => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
          {paused && filters.live && (
            <Button variant="outline" size="sm" onClick={resume}>
              <CirclePlay className="h-3.5 w-3.5" />
              Back to live
            </Button>
          )}
        </div>
      </header>

      <div
        className={`${rowGrid} border-b bg-muted/20 px-3 py-2 font-mono text-[10px] text-muted-foreground`}>
        <span />
        <span>Event</span>
        <span>Device</span>
        <span className="hidden md:block">Update</span>
        <span className="hidden xl:block">OS / app</span>
        <span className="text-right">Time</span>
      </div>

      <div
        ref={scrollRef}
        className="h-[min(680px,calc(100vh-300px))] min-h-[420px] overflow-auto"
        // Scrolling away from the head means reading, not tailing: new records
        // arriving would shift the row under the cursor.
        onScroll={event => {
          if (!paused && event.currentTarget.scrollTop > 80) setPaused(true);
        }}>
        {headQuery.isLoading && (
          <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            Loading events…
          </div>
        )}
        {headQuery.isError && (
          <div className="flex h-full items-center justify-center text-sm text-destructive">
            <AlertCircle className="mr-2 h-4 w-4" />
            Could not read the event stream.
          </div>
        )}
        {!headQuery.isLoading && !headQuery.isError && logs.length === 0 && (
          <div className="flex h-full flex-col items-center justify-center px-6 text-center text-muted-foreground">
            <CirclePause className="h-7 w-7" />
            <p className="mt-3 text-sm">No event matches these filters.</p>
            <p className="mt-1 text-xs">
              Events appear once an SDK 56+ client calls Observe.logEvent(), and JS exceptions show
              up here on their own.
            </p>
          </div>
        )}
        {logs.length > 0 && (
          <div className="relative w-full" style={{ height: `${virtualizer.getTotalSize()}px` }}>
            {virtualizer.getVirtualItems().map(virtualRow => {
              const log = logs[virtualRow.index];
              if (!log) return null;
              return (
                <div
                  key={log.eventKey}
                  ref={virtualizer.measureElement}
                  data-index={virtualRow.index}
                  className="absolute left-0 top-0 w-full border-b border-border/60"
                  style={{ transform: `translateY(${virtualRow.start}px)` }}>
                  <EventRow
                    log={log}
                    updateName={updateNames.get(log.updateId)}
                    expanded={expanded === log.eventKey}
                    onToggle={() => setExpanded(expanded === log.eventKey ? null : log.eventKey)}
                    onOpenDevice={() => setDevice(log)}
                  />
                </div>
              );
            })}
          </div>
        )}
        {older.loading && (
          <div className="sticky bottom-3 mx-auto flex w-fit items-center rounded-full border bg-card/90 px-3 py-1.5 text-xs text-muted-foreground backdrop-blur">
            <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
            Loading older events
          </div>
        )}
        {older.failed && (
          <div className="sticky bottom-3 mx-auto flex w-fit items-center gap-2 rounded-full border bg-card/90 px-3 py-1.5 text-xs text-muted-foreground backdrop-blur">
            Could not load older events
            <button
              type="button"
              onClick={older.retry}
              className="font-medium text-primary hover:underline">
              Retry
            </button>
          </div>
        )}
      </div>

      <footer className="flex items-center justify-between border-t bg-muted/20 px-3 py-1.5 text-[11px] text-muted-foreground">
        <span>
          {logs.length > 0 ? `${logs.length} records loaded` : 'No record loaded'}
          {!older.hasMore && !older.failed && logs.length > 0 && ' · end of range'}
        </span>
        <span className="flex items-center gap-1.5">
          {tailing ? (
            <>
              <i className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500 motion-reduce:animate-none" />
              Tailing
            </>
          ) : (
            <>
              <CirclePause className="h-3 w-3" />
              {filters.live ? 'Paused while you read' : 'Live refresh off'}
            </>
          )}
        </span>
      </footer>

      <DeviceSheet
        log={device}
        onClose={() => setDevice(null)}
        onFilter={easClientId => {
          filters.setFilters({ easClientId: [easClientId] });
          setDevice(null);
        }}
      />
    </section>
  );
};
