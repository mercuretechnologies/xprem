import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import {
  Check,
  ChevronRight,
  Columns3,
  MousePointerClick,
  Search,
  ServerCrash,
  Smartphone,
} from 'lucide-react';
import { api, IdentityDevice } from '@/lib/api';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/utils';
import { attributePair, liveInterval, uuidPattern, type ObserveFilters } from './filters';
import { useUpdateNames } from './useUpdateNames';
import { deviceName, osLabel } from './deviceNames';

const lastSeen = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
});

const ReportedValue = ({ value, muted }: { value?: string; muted?: string }) =>
  value ? (
    <span className="truncate">{value}</span>
  ) : (
    <span className="truncate text-muted-foreground/70">{muted ?? 'Not reported'}</span>
  );

// Metadata values are whatever $set sent for a declared key: a string, a number
// or a boolean. Anything else was rejected at ingestion, so this is display, not
// parsing.
const attributeValue = (value: unknown) => (typeof value === 'string' ? value : String(value));

// How many declared attributes get their own column, both by default and as a
// ceiling. Past five the fixed columns (device, model, OS, update) lose the
// width that makes them readable; everything else is one row expansion away.
const MAX_ATTRIBUTE_COLUMNS = 5;

// Which attributes get a column is a workspace preference, not a filter: it
// belongs to the browser rather than to the URL people paste to each other.
const columnsStorageKey = (appId: string | null) => `observe.device-columns.${appId ?? 'none'}`;

const storedColumns = (appId: string | null): string[] | null => {
  try {
    const raw = window.localStorage.getItem(columnsStorageKey(appId));
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) && parsed.every(entry => typeof entry === 'string')
      ? (parsed as string[])
      : null;
  } catch {
    // Unreadable or unparseable: fall back to the schema defaults.
    return null;
  }
};

// The fixed part of the row, then one column per shown attribute, then the last
// seen stamp and the two icon buttons.
const gridTemplate = (attributeCount: number) =>
  [
    '24px',
    'minmax(0,0.9fr)',
    'minmax(0,0.8fr)',
    'minmax(0,0.6fr)',
    'minmax(0,0.9fr)',
    ...Array(attributeCount).fill('minmax(0,0.8fr)'),
    '100px',
    '40px',
  ].join(' ');

const AttributeButton = ({
  attributeKey,
  value,
  onSelect,
  className,
}: {
  attributeKey: string;
  value: unknown;
  onSelect: (key: string, value: string) => void;
  className?: string;
}) => {
  if (value == null) return <span className="text-muted-foreground/50">-</span>;
  const text = attributeValue(value);
  return (
    <button
      type="button"
      title={`Filter on ${attributeKey} = ${text}`}
      onClick={() => onSelect(attributeKey, text)}
      className={cn(
        'block max-w-full truncate rounded px-1 py-0.5 text-left font-mono text-xs text-foreground hover:bg-primary/10 hover:text-primary',
        className
      )}>
      {text}
    </button>
  );
};

const DeviceRow = ({
  device,
  eventsHref,
  attributeKeys,
  updateNames,
  expanded,
  onToggleExpanded,
  onAttributeSelect,
}: {
  device: IdentityDevice;
  eventsHref: string;
  attributeKeys: string[];
  updateNames: Map<string, string>;
  expanded: boolean;
  onToggleExpanded: () => void;
  onAttributeSelect: (key: string, value: string) => void;
}) => {
  const model = device.deviceModel ? deviceName(device.deviceModel) : null;
  const metadata = device.metadata ?? {};
  const entries = Object.entries(metadata).filter(([, value]) => value != null);
  return (
    <div className="border-b last:border-0">
      <div
        className="grid items-center gap-x-4 px-5 py-3 text-sm"
        style={{ gridTemplateColumns: gridTemplate(attributeKeys.length) }}>
        <button
          type="button"
          aria-expanded={expanded}
          aria-label={expanded ? 'Hide every attribute' : 'Show every attribute'}
          title={expanded ? 'Hide every attribute' : 'Show every attribute'}
          onClick={onToggleExpanded}
          className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground">
          <ChevronRight
            className={cn('h-3.5 w-3.5 transition-transform', expanded && 'rotate-90')}
          />
        </button>
        <span className="min-w-0">
          {/* Short id, as everywhere else in the dashboard. The full one is a
              row away, and the search box is what takes a whole UUID. */}
          <code
            className="block truncate font-mono text-[13px] text-foreground"
            title={device.easClientId}>
            {device.easClientId.slice(0, 8)}
          </code>
          {(device.city || device.countryCode) && (
            <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
              {[device.city, device.countryCode].filter(Boolean).join(', ')}
            </span>
          )}
        </span>
        <span className="min-w-0">
          {model ? (
            <span className="truncate" title={device.deviceModel}>
              {model.label}
            </span>
          ) : (
            <ReportedValue muted="No telemetry yet" />
          )}
        </span>
        <span className="min-w-0">
          <ReportedValue
            value={device.osName ? osLabel(device.osName, device.osVersion ?? '') : undefined}
          />
        </span>
        <span className="min-w-0">
          {device.currentUpdateId ? (
            <>
              {/* The publish message when there is one, since it is what says
                  which change the device is running. */}
              <span
                className="block truncate text-xs"
                title={updateNames.get(device.currentUpdateId) ?? device.currentUpdateId}>
                {updateNames.get(device.currentUpdateId) ?? (
                  <code className="font-mono">{device.currentUpdateId.slice(0, 8)}</code>
                )}
              </span>
              <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                {device.branch
                  ? `${updateNames.has(device.currentUpdateId) ? `${device.currentUpdateId.slice(0, 8)} · ` : ''}${device.branch} · ${device.runtimeVersion ?? ''}`
                  : 'Embedded or unpublished'}
              </span>
            </>
          ) : (
            <ReportedValue muted="Unknown" />
          )}
        </span>
        {attributeKeys.map(key => (
          <span key={key} className="min-w-0">
            <AttributeButton
              attributeKey={key}
              value={metadata[key]}
              onSelect={onAttributeSelect}
            />
          </span>
        ))}
        <span className="truncate text-xs text-muted-foreground">
          {lastSeen.format(new Date(device.lastSeenAt))}
        </span>
        <Button variant="ghost" size="icon" asChild title="Open this device's events">
          <Link to={eventsHref}>
            <MousePointerClick className="h-3.5 w-3.5" />
          </Link>
        </Button>
      </div>
      {expanded && (
        <div className="space-y-2 border-t bg-muted/20 px-5 py-3 pl-[44px]">
          <span className="flex items-baseline gap-2">
            <span className="text-[11px] text-muted-foreground">eas client id</span>
            <code className="select-all font-mono text-xs text-foreground">
              {device.easClientId}
            </code>
          </span>
          {entries.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              This device has never sent an attribute.
            </p>
          ) : (
            <div className="flex flex-wrap gap-x-8 gap-y-2">
              {entries.map(([key, value]) => (
                <span key={key} className="flex min-w-0 items-baseline gap-2">
                  <span className="text-[11px] text-muted-foreground">{key}</span>
                  <AttributeButton
                    attributeKey={key}
                    value={value}
                    onSelect={onAttributeSelect}
                    className="whitespace-normal break-all"
                  />
                </span>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export const DevicesView = ({ filters }: { filters: ObserveFilters }) => {
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors[cursors.length - 1];
  const [search, setSearch] = useState('');
  const [expandedDevices, setExpandedDevices] = useState<string[]>([]);
  const appId = api.getAppId();
  // null means "whatever the schema declares, capped": the columns follow a
  // newly declared attribute until someone picks their own set. Kept per app,
  // since each one declares its own attributes.
  const [chosenColumns, setChosenColumns] = useState<string[] | null>(() => storedColumns(appId));
  useEffect(() => setChosenColumns(storedColumns(appId)), [appId]);
  const chooseColumns = (next: string[]) => {
    setChosenColumns(next);
    try {
      window.localStorage.setItem(columnsStorageKey(appId), JSON.stringify(next));
    } catch {
      // A full or blocked store costs the preference, never the table.
    }
  };

  const schemaQuery = useQuery({
    queryKey: ['identity', 'schema', appId],
    queryFn: () => api.getIdentitySchema(),
  });
  const declaredKeys = useMemo(
    () => (schemaQuery.data?.keys ?? []).map(spec => spec.key),
    [schemaQuery.data?.keys]
  );
  const updateNames = useUpdateNames();
  const attributeKeys = (
    chosenColumns?.filter(key => declaredKeys.includes(key)) ?? declaredKeys
  ).slice(0, MAX_ATTRIBUTE_COLUMNS);

  // The registry is queried by exact device id, so a partial paste is not a
  // search term yet. Saying so beats returning an empty list.
  const searchIsComplete = uuidPattern.test(search.trim());
  // Whatever the 'devices' scope honours, minus the window: the registry holds
  // a single last_seen_at per device, not a history to slice. Re-listing the
  // fields by hand would be a second source of truth beside the scope table in
  // filters.ts, and the two would drift the day a filter is added there.
  const query = useMemo(() => {
    const registry = { ...filters.query };
    delete registry.from;
    delete registry.to;
    return searchIsComplete ? { ...registry, easClientId: [search.trim()] } : registry;
  }, [filters.query, search, searchIsComplete]);

  // A keyset cursor only means something inside the result set it came from.
  // Changing the filters from the bar while on page three would carry that
  // cursor into the new set and start it in the middle, silently skipping the
  // rows before it with nothing to say the page is not the first.
  const querySignature = JSON.stringify(query);
  useEffect(() => setCursors([]), [querySignature]);

  const devicesQuery = useQuery({
    queryKey: ['identity', 'devices', api.getAppId(), query, cursor],
    queryFn: () => api.getIdentityDevices(query, cursor),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  const devices = devicesQuery.data?.devices ?? [];

  // Carries the whole filter set over to the record stream, with this device
  // pinned.
  const eventsHref = (device: IdentityDevice) => {
    const params = new URLSearchParams(window.location.search);
    params.set('device', device.easClientId);
    params.delete('cursor');
    return `/observe/events?${params.toString()}`;
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={event => {
              setSearch(event.target.value);
              setCursors([]);
            }}
            placeholder="Look up a device by its EAS client id…"
            className="pl-9 font-mono text-xs"
          />
        </div>
        {search.trim() !== '' && !searchIsComplete && (
          <span className="text-xs text-muted-foreground">Paste the full identifier</span>
        )}
        <Popover>
          <PopoverTrigger asChild>
            <Button variant="outline" disabled={declaredKeys.length === 0}>
              <Columns3 className="h-3.5 w-3.5" />
              Attributes
              {attributeKeys.length > 0 && (
                <Badge className="h-5 min-w-5 justify-center px-1.5 text-[10px]">
                  {attributeKeys.length}
                </Badge>
              )}
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" className="w-64 p-0">
            <div className="border-b px-3 py-2">
              <p className="text-xs font-medium">Attribute columns</p>
              <p className="mt-0.5 text-[11px] text-muted-foreground">
                Up to {MAX_ATTRIBUTE_COLUMNS} at a time. Expanding a row shows every attribute a
                device carries.
              </p>
            </div>
            <div className="max-h-64 overflow-y-auto py-1">
              {declaredKeys.map(key => {
                const shown = attributeKeys.includes(key);
                const full = !shown && attributeKeys.length >= MAX_ATTRIBUTE_COLUMNS;
                return (
                  <button
                    key={key}
                    type="button"
                    disabled={full}
                    title={
                      full
                        ? `${MAX_ATTRIBUTE_COLUMNS} columns is what the table can show and stay readable. Untick one, or expand a row to see every attribute.`
                        : undefined
                    }
                    onClick={() => {
                      const next = shown
                        ? attributeKeys.filter(entry => entry !== key)
                        : [...attributeKeys, key];
                      // Ordered by the schema, never by the order they were
                      // ticked: a column that comes back has to come back where
                      // it was.
                      chooseColumns(declaredKeys.filter(entry => next.includes(entry)));
                    }}
                    className={cn(
                      'flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs',
                      full ? 'text-muted-foreground/50' : 'hover:bg-accent/50'
                    )}>
                    <Check className={cn('h-3.5 w-3.5', shown ? 'opacity-100' : 'opacity-0')} />
                    <span className="truncate font-mono">{key}</span>
                  </button>
                );
              })}
            </div>
          </PopoverContent>
        </Popover>
      </div>

      <section className="overflow-hidden rounded-xl border bg-card shadow-card">
        <div
          className="grid gap-x-4 border-b bg-muted/20 px-5 py-2 text-[11px] text-muted-foreground"
          style={{ gridTemplateColumns: gridTemplate(attributeKeys.length) }}>
          <span />
          <span>Device</span>
          <span>Model</span>
          <span>OS</span>
          <span>Running</span>
          {attributeKeys.map(key => (
            <span key={key} className="truncate font-mono" title={key}>
              {key}
            </span>
          ))}
          <span>Last seen</span>
          <span />
        </div>

        {devicesQuery.isLoading &&
          [0, 1, 2, 3, 4].map(row => (
            <div key={row} className="border-b px-5 py-3.5">
              <Skeleton className="h-5 w-full" />
            </div>
          ))}

        {devicesQuery.isError && (
          <div className="flex min-h-60 flex-col items-center justify-center text-center">
            <ServerCrash className="h-7 w-7 text-destructive" />
            <p className="mt-3 text-sm">The device registry could not be read.</p>
          </div>
        )}

        {!devicesQuery.isLoading && !devicesQuery.isError && devices.length === 0 && (
          <div className="flex min-h-60 flex-col items-center justify-center px-6 text-center">
            <Smartphone className="h-7 w-7 text-muted-foreground" />
            <h2 className="mt-3 text-sm font-medium">No device matches these filters</h2>
            <p className="mt-1 max-w-md text-xs text-muted-foreground">
              Every client that polls for an update is registered here, with or without
              expo-observe. Hardware and OS only appear once a device sends telemetry.
            </p>
          </div>
        )}

        {devices.map(device => (
          <DeviceRow
            key={device.easClientId}
            device={device}
            eventsHref={eventsHref(device)}
            attributeKeys={attributeKeys}
            updateNames={updateNames}
            expanded={expandedDevices.includes(device.easClientId)}
            onToggleExpanded={() =>
              setExpandedDevices(current =>
                current.includes(device.easClientId)
                  ? current.filter(id => id !== device.easClientId)
                  : [...current, device.easClientId]
              )
            }
            onAttributeSelect={(key, value) => {
              filters.toggleFilter('attributes', attributePair(key, value));
              setCursors([]);
            }}
          />
        ))}
      </section>

      {(cursors.length > 0 || devicesQuery.data?.nextCursor) && (
        <div className="flex items-center justify-between">
          <Button
            variant="outline"
            size="sm"
            disabled={cursors.length === 0}
            onClick={() => setCursors(current => current.slice(0, -1))}>
            Previous
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!devicesQuery.data?.nextCursor}
            onClick={() =>
              setCursors(current => [...current, devicesQuery.data?.nextCursor ?? ''])
            }>
            Next
          </Button>
        </div>
      )}
    </div>
  );
};
