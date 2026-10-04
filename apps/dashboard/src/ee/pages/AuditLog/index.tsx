// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useMemo, useState } from 'react';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { api, AuditEventRecord, AuditEventsQuery } from '@/lib/api';
import { useSettings } from '@/lib/SettingsContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { PageHeader } from '@/components/PageHeader';
import { ApiError } from '@/components/APIError';
import { DataTable } from '@/components/DataTable';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { TimeRangePicker } from '@/components/TimeRangePicker';
import { resolveRange, type TimeRange } from '@/lib/timeRange';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';
import { EnterpriseFeatureGate } from '@/ee/components/EnterpriseFeatureGate';
import { auditLogFeature } from '@/ee/lib/enterpriseFeatures';
import { AUDIT_ACTION_GROUPS } from '@/ee/lib/auditCatalog';

const PAGE_SIZE = 50;

const selectClassName =
  'h-9 rounded-md border border-input bg-background px-3 text-sm shadow-sm ' +
  'focus:outline-none focus:ring-1 focus:ring-ring';

const OutcomeBadge = ({ outcome }: { outcome: AuditEventRecord['outcome'] }) => {
  if (outcome === 'success') {
    return (
      <Badge className="border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300">
        success
      </Badge>
    );
  }
  if (outcome === 'denied') {
    return (
      <Badge className="border-red-400/25 bg-red-400/10 text-red-700 dark:text-red-300">
        denied
      </Badge>
    );
  }
  if (outcome === 'failure') {
    return (
      <Badge className="border-amber-400/25 bg-amber-400/10 text-amber-700 dark:text-amber-300">
        failure
      </Badge>
    );
  }
  // An empty outcome is an incomplete call site the server refused to paper
  // over; show it honestly.
  return <Badge variant="outline">unknown</Badge>;
};

const ActorCell = ({ event }: { event: AuditEventRecord }) => (
  <div className="flex flex-col">
    <span className="text-sm">{event.actorDisplay || '(unknown)'}</span>
    {event.actorType !== 'user' && (
      <span className="text-xs text-muted-foreground">{event.actorType || 'unknown'}</span>
    )}
  </div>
);

// The Audit log page of the Access & Security sidebar group. Admin-only
// (routes are gated server-side too); reads work without a license so the
// collected history stays reachable, the frosted gate only fronts the upsell.
export const AuditLog = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { isAdmin } = useCurrentUser();

  const [filters, setFilters] = useState({
    action: '',
    appId: '',
    outcome: '',
  });
  // null reads the whole history. A relative range resolves once, when it is
  // chosen, so the query key does not change on every render.
  const [range, setRange] = useState<TimeRange | null>(null);
  const resolvedRange = useMemo(() => (range ? resolveRange(range, Date.now()) : null), [range]);
  // The actor filter carries its display too: when it was set by clicking a
  // row (api key, deleted account), the users select cannot represent it and
  // the chip below shows it instead.
  const [actorFilter, setActorFilter] = useState<{ id: string; display: string } | null>(null);
  const [selectedEvent, setSelectedEvent] = useState<AuditEventRecord | null>(null);

  // Apps feed the filter dropdown and the app column's display names.
  const appsQuery = useQuery({
    queryKey: ['apps'],
    queryFn: () => api.getApps(),
    enabled: CONTROL_PLANE_ENABLED && isAdmin,
  });
  // Users feed the actor select: admins filter by email, the id travels.
  const usersQuery = useQuery({
    queryKey: ['users'],
    queryFn: () => api.getUsers(),
    enabled: CONTROL_PLANE_ENABLED && isAdmin,
  });
  // Every app's API keys feed it too: keys are audit actors (CLI publishes).
  // Apps are few, so the fan-out stays cheap and cached.
  const apiKeysQuery = useQuery({
    queryKey: ['audit-actor-api-keys', (appsQuery.data ?? []).map(app => app.id)],
    queryFn: async () => {
      const perApp = await Promise.all(
        (appsQuery.data ?? []).map(async app => {
          const keys = await api.getApiKeysForApp(app.id);
          return keys.map(key => ({ ...key, appId: app.id }));
        })
      );
      return perApp.flat();
    },
    enabled: CONTROL_PLANE_ENABLED && isAdmin && !!appsQuery.data,
  });
  const knownActorIds = useMemo(() => {
    const ids = new Set((usersQuery.data ?? []).map(user => user.id));
    for (const key of apiKeysQuery.data ?? []) {
      ids.add(key.id);
    }
    return ids;
  }, [usersQuery.data, apiKeysQuery.data]);
  const appNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const app of appsQuery.data ?? []) {
      names.set(app.id, app.name || app.id);
    }
    return names;
  }, [appsQuery.data]);

  const queryFilters: AuditEventsQuery = {
    actorId: actorFilter?.id || undefined,
    action: filters.action || undefined,
    appId: filters.appId || undefined,
    outcome: filters.outcome || undefined,
    from: resolvedRange?.from.toISOString(),
    to: range?.to === 'now' ? undefined : resolvedRange?.to.toISOString(),
  };

  const eventsQuery = useInfiniteQuery({
    queryKey: ['audit-events', queryFilters],
    queryFn: ({ pageParam }) =>
      api.getAuditEvents({ ...queryFilters, beforeId: pageParam, limit: PAGE_SIZE }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: lastPage => lastPage.nextCursor ?? undefined,
    enabled: CONTROL_PLANE_ENABLED && isAdmin,
  });

  const events = eventsQuery.data?.pages.flatMap(page => page.events) ?? [];
  const totalCount = eventsQuery.data?.pages[0]?.count ?? 0;

  if (!CONTROL_PLANE_ENABLED) {
    return (
      <div className="w-full">
        <PageHeader title="Audit log" description="Who did what, and when." />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          The audit log lives in the database, so it requires control-plane (DB) mode.
        </div>
      </div>
    );
  }

  if (!isAdmin) {
    return (
      <div className="w-full">
        <PageHeader title="Audit log" description="Who did what, and when." />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Only admins can consult the audit log.
        </div>
      </div>
    );
  }

  return (
    <div className="w-full">
      <PageHeader
        title="Audit log"
        description="Every state-changing action on this server: who did it, on what, and with which outcome. Entries are append-only."
      />
      <EnterpriseFeatureGate feature={auditLogFeature}>
        <div className="space-y-4">
          {!!eventsQuery.error && <ApiError error={eventsQuery.error} />}

          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <select
                className={`${selectClassName} w-44`}
                value={actorFilter && knownActorIds.has(actorFilter.id) ? actorFilter.id : ''}
                onChange={event => {
                  const id = event.target.value;
                  const user = (usersQuery.data ?? []).find(u => u.id === id);
                  if (user) {
                    setActorFilter({ id: user.id, display: user.email });
                    return;
                  }
                  const key = (apiKeysQuery.data ?? []).find(k => k.id === id);
                  setActorFilter(key ? { id: key.id, display: key.name } : null);
                }}>
                <option value="">All actors</option>
                <optgroup label="Users">
                  {(usersQuery.data ?? []).map(user => (
                    <option key={user.id} value={user.id}>
                      {user.email}
                    </option>
                  ))}
                </optgroup>
                <optgroup label="API keys">
                  {(apiKeysQuery.data ?? []).map(key => (
                    <option key={key.id} value={key.id}>
                      {key.name} · {appNames.get(key.appId) ?? key.appId}
                    </option>
                  ))}
                </optgroup>
              </select>
              <select
                className={`${selectClassName} w-44`}
                value={filters.action}
                onChange={event => setFilters(f => ({ ...f, action: event.target.value }))}>
                <option value="">All actions</option>
                {AUDIT_ACTION_GROUPS.map(group => (
                  <optgroup key={group.label} label={group.label}>
                    {group.actions.map(action => (
                      <option key={action} value={action}>
                        {action}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </select>
              <select
                className={`${selectClassName} w-36`}
                value={filters.appId}
                onChange={event => setFilters(f => ({ ...f, appId: event.target.value }))}>
                <option value="">All apps</option>
                {(appsQuery.data ?? []).map(app => (
                  <option key={app.id} value={app.id}>
                    {app.name || app.id}
                  </option>
                ))}
              </select>
              <select
                className={`${selectClassName} w-36`}
                value={filters.outcome}
                onChange={event => setFilters(f => ({ ...f, outcome: event.target.value }))}>
                <option value="">All outcomes</option>
                <option value="success">success</option>
                <option value="denied">denied</option>
                <option value="failure">failure</option>
              </select>
              <TimeRangePicker value={range} onChange={setRange} allowAllTime />
            </div>
            <span className="text-sm text-muted-foreground">
              {totalCount} event{totalCount === 1 ? '' : 's'}
            </span>
          </div>

          {actorFilter && !knownActorIds.has(actorFilter.id) && (
            <div className="flex items-center gap-2 text-sm">
              <Badge variant="outline" className="gap-1">
                Actor: {actorFilter.display}
                <button
                  className="ml-1 text-muted-foreground hover:text-foreground"
                  onClick={() => setActorFilter(null)}
                  aria-label="Clear actor filter">
                  ×
                </button>
              </Badge>
            </div>
          )}

          <DataTable
            loading={eventsQuery.isLoading}
            columns={[
              {
                header: 'When',
                enableSorting: false,
                accessorKey: 'occurredAt',
                cell: ({ row }) => (
                  <TimestampCell dateString={row.original.occurredAt} showSeconds />
                ),
              },
              {
                header: 'Actor',
                enableSorting: false,
                accessorKey: 'actorDisplay',
                cell: ({ row }) =>
                  row.original.actorId ? (
                    <button
                      className="text-left hover:underline"
                      title="Filter by this actor"
                      onClick={event => {
                        event.stopPropagation();
                        setActorFilter({
                          id: row.original.actorId!,
                          display: row.original.actorDisplay || row.original.actorId!,
                        });
                      }}>
                      <ActorCell event={row.original} />
                    </button>
                  ) : (
                    <ActorCell event={row.original} />
                  ),
              },
              {
                header: 'Action',
                enableSorting: false,
                accessorKey: 'action',
                cell: ({ row }) => (
                  <Badge variant="outline" className="font-mono text-xs">
                    {row.original.action}
                  </Badge>
                ),
              },
              {
                header: 'Target',
                enableSorting: false,
                accessorKey: 'targetDisplay',
                cell: ({ row }) => (
                  <span className="block max-w-[220px] truncate text-sm">
                    {row.original.targetDisplay || row.original.targetId}
                  </span>
                ),
              },
              {
                header: 'App',
                enableSorting: false,
                accessorKey: 'appId',
                cell: ({ row }) =>
                  row.original.appId ? (
                    <span className="block max-w-[160px] truncate text-sm text-muted-foreground">
                      {appNames.get(row.original.appId) ?? row.original.appId}
                    </span>
                  ) : (
                    <span className="text-xs text-muted-foreground/60">account</span>
                  ),
              },
              {
                header: 'Outcome',
                enableSorting: false,
                accessorKey: 'outcome',
                cell: ({ row }) => <OutcomeBadge outcome={row.original.outcome} />,
              },
            ]}
            data={events}
            emptyMessage="No audit events match these filters."
            onRowClick={row => setSelectedEvent(row)}
          />

          {eventsQuery.hasNextPage && (
            <div className="flex justify-center">
              <Button
                variant="outline"
                size="sm"
                disabled={eventsQuery.isFetchingNextPage}
                onClick={() => eventsQuery.fetchNextPage()}>
                {eventsQuery.isFetchingNextPage ? 'Loading…' : 'Load more'}
              </Button>
            </div>
          )}
        </div>
      </EnterpriseFeatureGate>

      <Sheet open={!!selectedEvent} onOpenChange={open => !open && setSelectedEvent(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          {selectedEvent && (
            <>
              <SheetHeader>
                <SheetTitle className="font-mono text-base">{selectedEvent.action}</SheetTitle>
                <SheetDescription>
                  <TimestampCell dateString={selectedEvent.occurredAt} showSeconds />
                </SheetDescription>
              </SheetHeader>
              <div className="mt-4 space-y-3 text-sm">
                <DetailRow label="Actor">
                  {selectedEvent.actorDisplay || '(unknown)'}
                  {selectedEvent.actorType && (
                    <span className="ml-2 text-xs text-muted-foreground">
                      {selectedEvent.actorType}
                      {selectedEvent.actorId ? ` · ${selectedEvent.actorId}` : ''}
                    </span>
                  )}
                </DetailRow>
                <DetailRow label="Target">
                  {selectedEvent.targetDisplay || selectedEvent.targetId}
                  <span className="ml-2 text-xs text-muted-foreground">
                    {selectedEvent.targetType} · {selectedEvent.targetId}
                  </span>
                </DetailRow>
                <DetailRow label="Outcome">
                  <OutcomeBadge outcome={selectedEvent.outcome} />
                </DetailRow>
                {selectedEvent.appId && (
                  <DetailRow label="App">
                    {appNames.get(selectedEvent.appId) ?? selectedEvent.appId}
                  </DetailRow>
                )}
                {selectedEvent.ip && <DetailRow label="IP">{selectedEvent.ip}</DetailRow>}
                {selectedEvent.userAgent && (
                  <DetailRow label="User agent">
                    <span className="break-all text-xs">{selectedEvent.userAgent}</span>
                  </DetailRow>
                )}
                {selectedEvent.metadata && Object.keys(selectedEvent.metadata).length > 0 && (
                  <DetailRow label="Metadata">
                    <pre className="mt-1 overflow-x-auto rounded-md bg-muted/50 p-3 text-xs">
                      {JSON.stringify(selectedEvent.metadata, null, 2)}
                    </pre>
                  </DetailRow>
                )}
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
};

const DetailRow = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <div>
    <div className="text-xs font-medium text-muted-foreground">{label}</div>
    <div className="mt-0.5">{children}</div>
  </div>
);
