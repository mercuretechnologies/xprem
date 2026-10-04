// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { lazy, Suspense } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ServerCrash } from 'lucide-react';
import { api } from '@/lib/api';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/utils';
import { liveInterval, type ObserveFilters } from './filters';
import { ObserveNotice } from './ObserveNotice';
import { TelemetryUnavailable } from './TelemetryUnavailable';
import { FleetPanel } from './FleetPanel';
import { ReleasesPanel } from './ReleasesPanel';

const WorldActivityMap = lazy(() =>
  import('./WorldActivityMap').then(module => ({ default: module.WorldActivityMap }))
);

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const exact = new Intl.NumberFormat();

const Stat = ({
  label,
  value,
  detail,
  help,
  live,
}: {
  label: string;
  // Already formatted; null when the figure could not be read, which must never read as a zero.
  value: string | null;
  detail?: string;
  help: string;
  live?: boolean;
}) => (
  <div className="min-w-0 px-5 py-3 first:pl-0" title={help}>
    <p className="flex items-center gap-1.5 text-[13px] text-muted-foreground">
      {live && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />}
      {label}
    </p>
    <p className="mt-0.5 flex items-baseline gap-2">
      <span className="text-[26px] font-semibold tracking-tight tabular-nums">
        {value ?? 'n/a'}
      </span>
      {detail && <span className="truncate text-[13px] text-muted-foreground">{detail}</span>}
    </p>
  </div>
);

export const OverviewView = ({ filters }: { filters: ObserveFilters }) => {
  const fleetQuery = useQuery({
    queryKey: ['observe', 'fleet', api.getAppId(), filters.query],
    queryFn: () => api.getObserveFleet(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  const releasesQuery = useQuery({
    queryKey: ['observe', 'releases', api.getAppId(), filters.query],
    queryFn: () => api.getObserveReleases(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  // Only the map's city layer comes from here.
  const overviewQuery = useQuery({
    queryKey: ['observe', 'overview', api.getAppId(), filters.query],
    queryFn: () => api.getObserveOverview(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  // Live presence answers to identity:read while the rest of the page answers
  // to observe:read, so it is only asked for when the account holds it.
  const canBrowseDevices = useAppPermission('identity:read', 'any-member');
  const onlineQuery = useQuery({
    queryKey: ['identity', 'online', api.getAppId(), filters.registryQuery],
    queryFn: () => api.getOnlineDevices(filters.registryQuery),
    refetchInterval: filters.live ? 30_000 : false,
    placeholderData: previous => previous,
    enabled: canBrowseDevices,
  });

  if (fleetQuery.isLoading) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-20 rounded-lg" />
        <Skeleton className="h-40 rounded-xl" />
        <Skeleton className="h-[440px] rounded-xl" />
      </div>
    );
  }

  if (fleetQuery.isError) {
    return (
      <ObserveNotice
        icon={ServerCrash}
        tone="error"
        title="Observe could not load"
        detail="Check the server and PostgreSQL logs."
      />
    );
  }

  const fleet = fleetQuery.data;
  if (fleet?.available === false) return <TelemetryUnavailable />;

  const devices = fleet?.devices ?? 0;
  const facets = fleet?.facets ?? [];
  const withoutOTA =
    facets.find(facet => facet.dimension === 'update')?.values.find(value => value.value === '')
      ?.devices ?? 0;
  const adoption = (releasesQuery.data?.channels ?? []).reduce(
    (total, channel) => ({
      active: total.active + channel.activeDevices,
      upToDate: total.upToDate + channel.upToDateDevices,
    }),
    { active: 0, upToDate: 0 }
  );
  const percent = (part: number, whole: number) =>
    whole > 0 ? `${Math.round((100 * part) / whole)}%` : '–';
  const online = onlineQuery.isError || !canBrowseDevices ? null : (onlineQuery.data?.online ?? 0);
  const country = facets.find(facet => facet.dimension === 'country');
  const hasLocations = (overviewQuery.data?.locations?.length ?? 0) > 0;

  return (
    <div className="space-y-8">
      <div className="grid grid-cols-2 divide-border border-b pb-2 lg:grid-cols-4 lg:divide-x">
        <Stat
          label="Active devices"
          value={compact.format(devices)}
          help={`${exact.format(devices)} devices checked in during the selected period.`}
        />
        <Stat
          label="Online now"
          live={!!online}
          value={online == null ? null : compact.format(online)}
          help={
            !canBrowseDevices
              ? 'Counting live devices reads the device registry, which you do not have permission to browse.'
              : `Devices that pinged in the last ${onlineQuery.data?.windowMinutes ?? 20} minutes.${filters.registryHonorsAll ? '' : ' Channel and app version filters do not narrow this one.'}`
          }
        />
        <Stat
          label="Up to date"
          value={releasesQuery.isError ? null : percent(adoption.upToDate, adoption.active)}
          detail={
            adoption.active > 0
              ? `${compact.format(adoption.upToDate)} of ${compact.format(adoption.active)}`
              : undefined
          }
          help="Devices running what their channel serves them: its newest release for their runtime and platform, or either side of a rollout."
        />
        <Stat
          label="Embedded bundle"
          value={percent(withoutOTA, devices)}
          detail={devices > 0 ? compact.format(withoutOTA) : undefined}
          help="Devices that have not taken any OTA update yet and run the bundle shipped in their store build."
        />
      </div>

      <ReleasesPanel filters={filters} />

      <section>
        <h2 className="mb-2.5 text-[15px] font-semibold tracking-tight">Fleet</h2>
        {devices > 0 ? (
          <div className="grid gap-4 lg:grid-cols-2">
            <FleetPanel
              dimensions={['update', 'runtimeVersion', 'appVersion', 'channel']}
              facets={facets}
              total={devices}
              filters={filters}
            />
            <FleetPanel
              dimensions={['platform', 'deviceModel', 'osVersion']}
              facets={facets}
              total={devices}
              filters={filters}
            />
          </div>
        ) : (
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-[13px] text-muted-foreground">
            No device checked in during this period
          </p>
        )}
      </section>

      <div
        className={cn(
          'grid gap-4',
          country && devices > 0 && 'xl:grid-cols-[minmax(0,1fr)_320px]'
        )}>
        {hasLocations ? (
          <Suspense fallback={<Skeleton className="h-[620px] rounded-xl" />}>
            <WorldActivityMap locations={overviewQuery.data?.locations ?? []} filters={filters} />
          </Suspense>
        ) : (
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-[13px] text-muted-foreground">
            No install located yet
          </p>
        )}
        {country && devices > 0 && (
          <FleetPanel
            dimensions={['country']}
            facets={facets}
            total={devices}
            filters={filters}
            className="xl:self-start"
          />
        )}
      </div>
    </div>
  );
};
