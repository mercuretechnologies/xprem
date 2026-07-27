// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { lazy, Suspense } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Activity,
  Box,
  GitBranch,
  MousePointerClick,
  MapPin,
  Radio,
  ServerCrash,
  Smartphone,
} from 'lucide-react';
import { api } from '@/lib/api';
import { Skeleton } from '@/components/ui/skeleton';
import { liveInterval, type ObserveFilters } from './filters';
import { ObserveNotice } from './ObserveNotice';
import { TelemetryUnavailable } from './TelemetryUnavailable';

const WorldActivityMap = lazy(() =>
  import('./WorldActivityMap').then(module => ({ default: module.WorldActivityMap }))
);

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const exact = new Intl.NumberFormat();

const StatTile = ({
  icon: Icon,
  label,
  value,
  hint,
}: {
  icon: typeof Activity;
  label: string;
  // null when the figure could not be read. Rendered as "n/a", never as 0: a
  // zero here reads as a measurement, and "no devices online" is a very
  // different statement from "we could not ask".
  value: number | null;
  hint: string;
}) => (
  <div
    className="rounded-xl border bg-card p-4 shadow-card"
    title={value == null ? `${label.toLowerCase()} unavailable` : `${exact.format(value)} ${label.toLowerCase()}`}>
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <Icon className="h-3.5 w-3.5 text-primary" />
      {label}
    </div>
    <div className="mt-2 font-mono text-2xl font-semibold tabular-nums">
      {value == null ? 'n/a' : compact.format(value)}
    </div>
    <p className="mt-1 text-[11px] leading-snug text-muted-foreground">{hint}</p>
  </div>
);

export const OverviewView = ({ filters }: { filters: ObserveFilters }) => {
  const overviewQuery = useQuery({
    queryKey: ['observe', 'overview', api.getAppId(), filters.query],
    queryFn: () => api.getObserveOverview(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  const overview = overviewQuery.data;
  // Live presence comes from the Postgres registry, not from telemetry: every
  // manifest poll bumps last_seen too, so this number is right even on a fleet
  // with no expo-observe. Refreshed on its own short cadence, since a 20 minute
  // window only means something if it moves. It narrows on the filters the
  // registry can honor, which is a subset of the ones this page offers: the
  // hint says so when the rest are in play, rather than letting the tile read
  // as if it answered the same question as its neighbours.
  const onlineQuery = useQuery({
    queryKey: ['identity', 'online', api.getAppId(), filters.registryQuery],
    queryFn: () => api.getOnlineDevices(filters.registryQuery),
    refetchInterval: filters.live ? 30_000 : false,
    placeholderData: previous => previous,
  });

  if (overviewQuery.isLoading) {
    return (
      <div className="space-y-5">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-7">
          {[0, 1, 2, 3, 4, 5, 6].map(tile => (
            <Skeleton key={tile} className="h-28 rounded-xl" />
          ))}
        </div>
        <Skeleton className="h-[440px] rounded-xl" />
      </div>
    );
  }

  if (overviewQuery.isError) {
    return (
      <ObserveNotice
        icon={ServerCrash}
        tone="error"
        title="Observe could not load"
        detail="Check the server and ClickHouse logs."
      />
    );
  }

  if (overview?.available === false) return <TelemetryUnavailable />;

  const summary = overview?.summary;

  return (
    <div className="space-y-5">
      {summary && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-7">
          <StatTile
            icon={Radio}
            label="Online now"
            value={onlineQuery.isError ? null : (onlineQuery.data?.online ?? 0)}
            hint={
              onlineQuery.isError
                ? 'The device registry did not answer, so this count is unknown.'
                : filters.registryHonorsAll
                  ? `Pinged in the last ${onlineQuery.data?.windowMinutes ?? 20} minutes, any route`
                  : `Pinged in the last ${onlineQuery.data?.windowMinutes ?? 20} minutes. Build and channel filters do not narrow this one.`
            }
          />
          {/* "Devices", not "users": expo-observe identifies an install, and
              one person with two phones is two of these. */}
          <StatTile
            icon={Smartphone}
            label="Devices"
            value={summary.users}
            hint="Installs that reported in"
          />
          <StatTile
            icon={Activity}
            label="Sessions"
            value={summary.sessions}
            hint="App runs observed"
          />
          <StatTile
            icon={MousePointerClick}
            label="Events"
            value={summary.events}
            hint="Logs and exceptions"
          />
          <StatTile
            icon={Box}
            label="OTA updates"
            value={summary.updates}
            hint="Distinct updates running"
          />
          <StatTile
            icon={GitBranch}
            label="App versions"
            value={summary.releases}
            hint="Store versions in the field"
          />
          <StatTile
            icon={Smartphone}
            label="Builds"
            value={summary.builds}
            hint="Native builds in the field"
          />
        </div>
      )}

      {(overview?.locations?.length ?? 0) > 0 ? (
        <Suspense fallback={<Skeleton className="h-[440px] rounded-xl" />}>
          <WorldActivityMap locations={overview?.locations ?? []} filters={filters} />
        </Suspense>
      ) : (
        <section className="flex items-center gap-2.5 rounded-xl border border-dashed bg-card px-5 py-4 text-sm text-muted-foreground">
          <MapPin className="h-4 w-4 shrink-0" />
          No install located yet.
        </section>
      )}
    </div>
  );
};
