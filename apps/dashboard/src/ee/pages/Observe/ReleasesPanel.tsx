// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useMemo } from 'react';
import { Link } from 'react-router';
import { useQueries, useQuery } from '@tanstack/react-query';
import {
  api,
  type ObserveChannelAdoption,
  type UpdateFeedRecord,
  type UpdateHealthRecord,
} from '@/lib/api';
import { Skeleton } from '@/components/ui/skeleton';
import { TimeSeriesChart } from '@/ee/components/charts/TimeSeriesChart';
import { aggregateSeries, boundedFrom } from '@/ee/components/updateHealthSeries';
import { aggregateUpdateHealth } from '@/pages/Updates/components/updateHealth';
import { shortRuntimeVersion, updateDetailsPath } from '@/lib/update-format';
import { cn } from '@/lib/utils';
import { liveInterval, type ObserveFilters } from './filters';
import { compactNumber, exactNumber, sinceLabel } from './format';
import { seriesColors } from './dimensions';
import { buildUpdateGroups, groupTitle, type UpdateGroup } from './updateGroups';

type Status = 'trouble' | 'watch' | 'healthy' | 'unknown';

// The thresholds of the Health column on the Updates page, so both read the same.
const statusOf = (health: UpdateHealthRecord | undefined): Status => {
  const percent = health?.healthPercent;
  if (percent == null) return 'unknown';
  if (percent >= 98) return 'healthy';
  if (percent >= 90) return 'watch';
  return 'trouble';
};

const statusRank: Record<Status, number> = { trouble: 0, watch: 1, healthy: 2, unknown: 3 };

const statusStyle: Record<Status, { label: string; dot: string }> = {
  trouble: { label: 'In trouble', dot: 'bg-red-500' },
  watch: { label: 'To watch', dot: 'bg-amber-500' },
  healthy: { label: 'Healthy', dot: 'bg-emerald-500' },
  unknown: { label: 'No data', dot: 'bg-muted-foreground/40' },
};

// What a branch serves right now: its newest publish, plus the feed row the
// update details page is addressed by.
type Served = { group: UpdateGroup; record: UpdateFeedRecord };

const newestOf = (items: UpdateFeedRecord[] | undefined): Served | undefined => {
  const group = buildUpdateGroups(items ?? [])[0];
  const record = group && items?.find(item => item.updateUUID === group.updateUUIDs[0]);
  return group && record ? { group, record } : undefined;
};

// Floored, so 100% only ever means no crash at all.
const percentLabel = (value: number | null | undefined) =>
  value == null ? '–' : value === 100 ? '100%' : `${(Math.floor(value * 10) / 10).toFixed(1)}%`;

const Release = ({ served, prefix }: { served: Served | undefined; prefix?: string }) =>
  served ? (
    <div className="min-w-0">
      <Link
        to={updateDetailsPath(served.record, 'updates')}
        className="block truncate text-[13px] font-medium hover:underline">
        {prefix}
        {groupTitle(served.group)}
      </Link>
      <p className="truncate text-[12px] text-muted-foreground">
        {served.group.branch} · {shortRuntimeVersion(served.group.runtimeVersion)} ·{' '}
        {sinceLabel(served.group.createdAt)}
      </p>
    </div>
  ) : (
    <p className="text-[13px] text-muted-foreground">{prefix}Nothing published</p>
  );

const Adoption = ({ adoption }: { adoption: ObserveChannelAdoption | undefined }) => {
  if (!adoption || adoption.activeDevices === 0) {
    return <span className="text-[13px] text-muted-foreground">No active device</span>;
  }
  const upToDate = adoption.upToDateDevices / adoption.activeDevices;
  const embedded = adoption.embeddedDevices / adoption.activeDevices;
  return (
    <div
      className="flex items-center gap-3"
      title={`${exactNumber.format(adoption.upToDateDevices)} of ${exactNumber.format(adoption.activeDevices)} active devices run what this channel serves, ${exactNumber.format(adoption.embeddedDevices)} still run the embedded bundle`}>
      <div className="flex h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
        <div className="bg-primary" style={{ width: `${100 * upToDate}%` }} />
        <div className="bg-muted-foreground/30" style={{ width: `${100 * embedded}%` }} />
      </div>
      <span className="w-10 text-right text-[13px] tabular-nums">
        {Math.round(100 * upToDate)}%
      </span>
      <span className="w-20 shrink-0 text-right text-[12px] tabular-nums text-muted-foreground">
        {compactNumber.format(adoption.upToDateDevices)} /{' '}
        {compactNumber.format(adoption.activeDevices)}
      </span>
    </div>
  );
};

const columns = 'lg:grid-cols-[140px_minmax(0,1fr)_280px_96px_110px]';

export const ReleasesPanel = ({ filters }: { filters: ObserveFilters }) => {
  const releasesQuery = useQuery({
    queryKey: ['observe', 'releases', api.getAppId(), filters.query],
    queryFn: () => api.getObserveReleases(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  const channelsQuery = useQuery({
    queryKey: ['channels', api.getAppId()],
    queryFn: () => api.getChannels(),
  });

  const channels = useMemo(
    () =>
      (channelsQuery.data ?? []).filter(
        channel =>
          filters.state.channel.length === 0 ||
          filters.state.channel.includes(channel.releaseChannelName)
      ),
    [channelsQuery.data, filters.state.channel]
  );
  const branches = useMemo(
    () =>
      Array.from(
        new Set(
          channels.flatMap(channel => [
            channel.branchName ?? '',
            channel.rollout?.rolloutBranchName ?? '',
          ])
        )
      ).filter(Boolean),
    [channels]
  );
  const feeds = useQueries({
    queries: branches.map(branch => ({
      queryKey: ['observe', 'branch-feed', api.getAppId(), branch],
      queryFn: () => api.getUpdateFeed({ branch, limit: 20 }),
    })),
  });
  const servedByBranch = new Map(
    branches.map((branch, index) => [branch, newestOf(feeds[index]?.data?.items)])
  );
  const servedIds = Array.from(servedByBranch.values()).flatMap(
    served => served?.group.updateUUIDs ?? []
  );
  const healthQuery = useQuery({
    queryKey: ['update-health', 'releases', api.getAppId(), servedIds.join(',')],
    queryFn: () => api.getUpdateHealth(servedIds),
    enabled: servedIds.length > 0,
  });

  const rows = (() => {
    const healthOf = (served: Served | undefined) =>
      served
        ? aggregateUpdateHealth(served.group.updateUUIDs.map(id => healthQuery.data?.updates[id]))
        : undefined;
    const adoptionByChannel = new Map(
      (releasesQuery.data?.channels ?? []).map(entry => [entry.channel, entry])
    );
    return (
      channels
        .map(channel => {
          const served = servedByBranch.get(channel.branchName ?? '');
          const rollout = channel.rollout
            ? servedByBranch.get(channel.rollout.rolloutBranchName)
            : undefined;
          // A rollout in trouble is the channel in trouble, whatever the rest of it runs.
          const worst = [served, ...(channel.rollout ? [rollout] : [])]
            .map(entry => {
              const health = healthOf(entry);
              return { health, status: statusOf(health) };
            })
            .sort((left, right) => statusRank[left.status] - statusRank[right.status])[0];
          return {
            channel,
            served,
            rollout,
            health: worst.health,
            status: worst.status,
            adoption: adoptionByChannel.get(channel.releaseChannelName),
          };
        })
        // A channel that serves nothing to nobody has nothing to report.
        .filter(row => row.served || row.rollout || (row.adoption?.activeDevices ?? 0) > 0)
        .sort(
          (left, right) =>
            (right.adoption?.activeDevices ?? 0) - (left.adoption?.activeDevices ?? 0) ||
            statusRank[left.status] - statusRank[right.status]
        )
    );
  })();

  // One curve per publish a channel serves, within the 20 ids the history endpoint takes.
  const curves = (() => {
    const seen = new Set<string>();
    const picked: Array<{ key: string; label: string; updateUUIDs: string[] }> = [];
    let ids = 0;
    for (const row of rows) {
      for (const served of [row.served, row.rollout]) {
        if (!served || seen.has(served.group.key)) continue;
        if (ids + served.group.updateUUIDs.length > 20) continue;
        seen.add(served.group.key);
        ids += served.group.updateUUIDs.length;
        picked.push({
          key: served.group.key,
          label: `${row.channel.releaseChannelName} · ${groupTitle(served.group)}`,
          updateUUIDs: served.group.updateUUIDs,
        });
      }
    }
    return picked.slice(0, seriesColors.length);
  })();
  const curveIds = curves.flatMap(curve => curve.updateUUIDs);
  const historyFrom = boundedFrom(filters.query.from, filters.query.to);
  const historyQuery = useQuery({
    queryKey: [
      'update-health-history',
      api.getAppId(),
      curveIds.join(','),
      historyFrom,
      filters.query.to,
    ],
    queryFn: () => api.getUpdateHealthHistory(curveIds, historyFrom, filters.query.to),
    enabled: curveIds.length > 0,
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
  });
  // The state-only payload carries no device counts to draw.
  const history = historyQuery.data?.source === 'state' ? undefined : historyQuery.data;
  const adoptionSeries = curves.map((curve, index) => ({
    key: curve.key,
    label: curve.label,
    color: seriesColors[index],
    points: aggregateSeries(curve.updateUUIDs, history?.updates ?? {}).map(point => ({
      timestamp: new Date(point.timestamp),
      value: point.devicesOnUpdate,
    })),
  }));
  const hasCurves = adoptionSeries.some(series => series.points.length > 1);

  if (channelsQuery.isLoading || releasesQuery.isLoading) {
    return <Skeleton className="h-48 rounded-lg" />;
  }
  if (rows.length === 0) return null;

  return (
    <section>
      <h2 className="mb-2.5 text-[15px] font-semibold tracking-tight">Releases</h2>
      <div className="overflow-hidden rounded-lg border bg-card">
        <div
          className={cn(
            'hidden gap-6 border-b px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground lg:grid',
            columns
          )}>
          <span>Channel</span>
          <span>Serving</span>
          <span>Up to date</span>
          <span className="text-right">Crash-free</span>
          <span>Status</span>
        </div>
        <ul className="divide-y">
          {rows.map(row => (
            <li
              key={row.channel.releaseChannelId}
              className={cn('grid gap-2 px-4 py-3 lg:items-center lg:gap-6', columns)}>
              <div className="min-w-0">
                <p className="truncate text-[13px] font-medium">{row.channel.releaseChannelName}</p>
                {row.channel.rollout && (
                  <p className="text-[12px] text-muted-foreground">
                    Rollout {row.channel.rollout.percentage}%
                  </p>
                )}
              </div>
              <div className="min-w-0 space-y-1.5">
                <Release served={row.served} />
                {row.channel.rollout && (
                  <Release served={row.rollout} prefix={`${row.channel.rollout.percentage}% → `} />
                )}
              </div>
              <Adoption adoption={row.adoption} />
              <span
                className="text-[13px] tabular-nums lg:text-right"
                title={
                  row.health
                    ? `${exactNumber.format(row.health.successfulDevices)} launched fine · ${exactNumber.format(row.health.faultyDevices)} crashed`
                    : undefined
                }>
                {percentLabel(row.health?.healthPercent)}
              </span>
              <span className="inline-flex items-center gap-2 text-[13px]">
                <span className={cn('h-2 w-2 rounded-full', statusStyle[row.status].dot)} />
                {statusStyle[row.status].label}
              </span>
            </li>
          ))}
        </ul>
        {hasCurves && (
          <div className="border-t px-4 pb-3 pt-3.5">
            <div className="mb-1 flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
              <span className="text-[12px] font-medium text-muted-foreground">
                Devices running each release
              </span>
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {adoptionSeries.map(series => (
                  <span
                    key={series.key}
                    className="inline-flex max-w-[260px] items-center gap-1.5 text-[12px] text-muted-foreground">
                    <span
                      className="h-2 w-2 shrink-0 rounded-full"
                      style={{ backgroundColor: series.color }}
                    />
                    <span className="truncate">{series.label}</span>
                  </span>
                ))}
              </div>
            </div>
            <TimeSeriesChart
              series={adoptionSeries}
              formatValue={value => `${exactNumber.format(Math.round(value))} devices`}
              ariaLabel="Devices running each release over time"
              height={180}
            />
          </div>
        )}
      </div>
    </section>
  );
};
