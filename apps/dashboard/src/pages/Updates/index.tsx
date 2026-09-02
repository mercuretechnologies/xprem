import { Fragment, ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import { useInfiniteQuery, useQueries, useQuery } from '@tanstack/react-query';
import {
  ChevronDown,
  ChevronRight,
  Box,
  Gauge,
  GitBranch,
  GitCommitHorizontal,
  Layers3,
  Loader2,
  Search,
  SlidersHorizontal,
  Undo2,
  X,
} from 'lucide-react';
import { useNavigate, useSearchParams } from 'react-router';
import { api, UpdateFeedRecord } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { PageHeader } from '@/components/PageHeader';
import { ApiError } from '@/components/APIError';
import { Combobox } from '@/components/Combobox';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import {
  ManagedUpdateRollout,
  UpdateRolloutManagerSheet,
} from '@/pages/Updates/components/UpdateRolloutManagerSheet';
import {
  RepublishDialog,
  RepublishTarget,
  RollbackDialog,
} from '@/pages/Updates/components/PublishDialogs';
import apple from '@/assets/apple.svg';
import android from '@/assets/android.svg';
import { HealthBadge } from '@/pages/Updates/components/HealthBadge';
import { aggregateUpdateHealth } from '@/pages/Updates/components/updateHealth';
import { shortRuntimeVersion, updateDetailsPath, updateTitle } from '@/lib/update-format';

type FeedGroup = {
  key: string;
  publishGroup?: string;
  updates: UpdateFeedRecord[];
};

type FeedFilterKey =
  'branch' | 'runtimeVersion' | 'platform' | 'uuid' | 'groupId' | 'commitHash' | 'from' | 'to';

type FeedFilters = Record<FeedFilterKey, string>;
type DebouncedFilterKey = 'uuid' | 'groupId' | 'commitHash';

const filterDebounceMs = 300;
const healthBatchSize = 100;
const isUuid = (value: string) =>
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);

const FilterField = ({ label, children }: { label: string; children: ReactNode }) => (
  <label className="min-w-0 space-y-1.5">
    <span className="block text-xs font-medium text-muted-foreground">{label}</span>
    {children}
  </label>
);

const ClearableInput = ({
  value,
  onValueChange,
  onClear,
  icon,
  ...props
}: {
  value: string;
  onValueChange: (value: string) => void;
  onClear: () => void;
  icon?: ReactNode;
} & Omit<React.ComponentProps<typeof Input>, 'value' | 'onChange'>) => (
  <div className="relative">
    {icon && (
      <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground">
        {icon}
      </span>
    )}
    <Input
      {...props}
      value={value}
      onChange={event => onValueChange(event.target.value)}
      className={`${icon ? 'pl-9' : ''} pr-9 ${props.className ?? ''}`}
    />
    {value && (
      <button
        type="button"
        aria-label="Clear field"
        onClick={onClear}
        className="absolute right-2 top-1/2 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground">
        <X className="h-3.5 w-3.5" />
      </button>
    )}
  </div>
);

const PlatformIcon = ({ platform }: { platform: string }) => {
  const src = platform === 'ios' ? apple : platform === 'android' ? android : null;
  if (!src) return <span className="text-xs text-muted-foreground">{platform}</span>;
  return (
    <span
      className="inline-flex h-7 w-7 items-center justify-center rounded-md border bg-secondary"
      title={platform === 'ios' ? 'iOS' : 'Android'}>
      <img
        src={src}
        className="h-3.5 w-3.5 brightness-0 opacity-80 dark:invert"
        alt={platform === 'ios' ? 'iOS' : 'Android'}
      />
    </span>
  );
};

const shortId = (value: string) => (value.length > 10 ? value.slice(0, 8) : value);

const UpdateRolloutBadge = ({ percentage }: { percentage: number }) => (
  <Badge className="h-5 whitespace-nowrap border-emerald-400/25 bg-emerald-400/10 px-1.5 text-[11px] text-emerald-700 dark:text-emerald-300">
    <Gauge className="h-2.5 w-2.5" />
    {percentage}% rollout
  </Badge>
);

const RuntimeLabel = ({ value }: { value: string }) => (
  <span
    className="inline-flex max-w-full items-center gap-1.5 rounded-md border border-amber-400/25 bg-amber-400/[0.08] px-2 py-1 text-xs font-medium text-amber-800 dark:border-amber-300/20 dark:bg-amber-300/[0.07] dark:text-amber-100"
    title={value}>
    <Box className="h-3 w-3 shrink-0 text-amber-600 dark:text-amber-300/80" />
    <span className="truncate">{shortRuntimeVersion(value)}</span>
  </span>
);

const CommitLabel = ({ value }: { value: string }) => (
  <span className="inline-flex items-center gap-1.5 rounded-md border border-primary/20 bg-primary/[0.07] px-2 py-1 text-xs font-medium text-link">
    <GitCommitHorizontal className="h-3 w-3 text-link/80" />
    {shortId(value)}
  </span>
);

const BranchLabel = ({ branch }: { branch: string }) => (
  <span className="inline-flex max-w-full items-center gap-1.5 font-medium text-foreground">
    <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
    <span className="truncate">{branch}</span>
  </span>
);

export const Updates = () => {
  const { selectedAppId } = useSelectedApp();
  const canManageUpdateRollout = useAppPermission('update-rollout:manage', 'admin-only');
  const canPublishUpdate = useAppPermission('update:publish', 'admin-only');
  const [searchParams, setSearchParams] = useSearchParams();
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set());
  const [managedRollout, setManagedRollout] = useState<ManagedUpdateRollout | null>(null);
  const [isRollbackOpen, setIsRollbackOpen] = useState(false);
  const [republishTarget, setRepublishTarget] = useState<RepublishTarget | null>(null);
  const navigate = useNavigate();
  // The filters travel in the location state so the update page can bring the
  // reader back to the same view of the feed.
  const openUpdate = (update: UpdateFeedRecord) =>
    navigate(updateDetailsPath(update, 'updates'), { state: { search: searchParams.toString() } });

  const filters: FeedFilters = {
    branch: searchParams.get('branch') ?? '',
    runtimeVersion: searchParams.get('runtimeVersion') ?? '',
    platform: searchParams.get('platform') ?? '',
    uuid: searchParams.get('uuid') ?? '',
    groupId: searchParams.get('groupId') ?? '',
    commitHash: searchParams.get('commitHash') ?? '',
    from: searchParams.get('from') ?? '',
    to: searchParams.get('to') ?? '',
  };
  const [textFilterDrafts, setTextFilterDrafts] = useState<Record<DebouncedFilterKey, string>>({
    uuid: filters.uuid,
    groupId: filters.groupId,
    commitHash: filters.commitHash,
  });
  const filterTimers = useRef<Partial<Record<DebouncedFilterKey, ReturnType<typeof setTimeout>>>>(
    {}
  );

  useEffect(() => {
    const timer = filterTimers.current.uuid;
    if (timer) clearTimeout(timer);
    delete filterTimers.current.uuid;
    setTextFilterDrafts(current => ({ ...current, uuid: filters.uuid }));
  }, [filters.uuid]);

  useEffect(() => {
    const timer = filterTimers.current.groupId;
    if (timer) clearTimeout(timer);
    delete filterTimers.current.groupId;
    setTextFilterDrafts(current => ({ ...current, groupId: filters.groupId }));
  }, [filters.groupId]);

  useEffect(() => {
    const timer = filterTimers.current.commitHash;
    if (timer) clearTimeout(timer);
    delete filterTimers.current.commitHash;
    setTextFilterDrafts(current => ({ ...current, commitHash: filters.commitHash }));
  }, [filters.commitHash]);

  useEffect(
    () => () => {
      Object.values(filterTimers.current).forEach(clearTimeout);
    },
    []
  );

  const filterKey = [
    filters.branch,
    filters.runtimeVersion,
    filters.platform,
    filters.uuid,
    filters.groupId,
    filters.commitHash,
    filters.from,
    filters.to,
  ];

  const query = useInfiniteQuery({
    queryKey: ['update-feed', selectedAppId, ...filterKey],
    queryFn: ({ pageParam }) =>
      api.getUpdateFeed({
        ...filters,
        cursor: pageParam || undefined,
        limit: 50,
      }),
    initialPageParam: '',
    getNextPageParam: page => page.nextCursor,
    enabled: !!selectedAppId,
  });
  const branchesQuery = useQuery({
    queryKey: ['branches', selectedAppId],
    queryFn: () => api.getBranches(),
    enabled: !!selectedAppId,
  });
  const runtimeVersionsQuery = useQuery({
    queryKey: ['runtimeVersions', selectedAppId, filters.branch],
    queryFn: () => api.getRuntimeVersions(filters.branch),
    enabled: !!selectedAppId && !!filters.branch,
  });

  const updates = useMemo(() => query.data?.pages.flatMap(page => page.items) ?? [], [query.data]);
  const healthIdBatches = useMemo(() => {
    // Every update on the page, not only the ones the feed marks health
    // relevant. That flag means "heads its branch, or is the control of a
    // running rollout", which is not the same thing as "has devices on it":
    // the moment a release is rolled back, the version the fleet returns TO
    // stops being relevant and its device count vanished from the table
    // exactly when it started to matter.
    //
    // Asking for all of them is close to free. The feed is paginated, the ids
    // go out in batches, and the counts are GROUP BYs over an index on
    // (app_id, current_update_id) where an abandoned update matches nothing.
    const updateUUIDs = Array.from(
      new Set(updates.map(update => update.updateUUID).filter(isUuid))
    );
    const batches: string[][] = [];
    for (let index = 0; index < updateUUIDs.length; index += healthBatchSize) {
      batches.push(updateUUIDs.slice(index, index + healthBatchSize));
    }
    return batches;
  }, [updates]);
  const healthQueries = useQueries({
    queries: healthIdBatches.map(updateUUIDs => ({
      queryKey: ['update-health', 'feed', selectedAppId, updateUUIDs.join(',')],
      queryFn: () => api.getUpdateHealth(updateUUIDs),
      enabled: !!selectedAppId,
      refetchInterval: 30_000,
    })),
  });
  const healthByUuid = Object.assign(
    {},
    ...healthQueries.map(healthQuery => healthQuery.data?.updates ?? {})
  );
  const healthError = healthQueries.find(healthQuery => healthQuery.error)?.error;
  const branchOptions = useMemo(
    () =>
      (branchesQuery.data ?? [])
        .map(branch => ({ value: branch.branchName, label: branch.branchName }))
        .sort((a, b) => a.label.localeCompare(b.label)),
    [branchesQuery.data]
  );
  const platformOptions = [
    { value: 'ios', label: 'iOS' },
    { value: 'android', label: 'Android' },
  ];
  const runtimeVersionOptions = useMemo(
    () =>
      (runtimeVersionsQuery.data ?? [])
        .map(runtime => ({
          value: runtime.runtimeVersion,
          label: runtime.runtimeVersion,
        }))
        .sort((a, b) => a.label.localeCompare(b.label)),
    [runtimeVersionsQuery.data]
  );

  const setFilters = (values: Partial<FeedFilters>) =>
    setSearchParams(
      current => {
        const next = new URLSearchParams(current);
        for (const [key, value] of Object.entries(values)) {
          if (value) next.set(key, value);
          else next.delete(key);
        }
        return next;
      },
      { replace: true }
    );
  const setFilter = (key: FeedFilterKey, value: string) => setFilters({ [key]: value });
  const cancelPendingFilter = (key: DebouncedFilterKey) => {
    const timer = filterTimers.current[key];
    if (timer) clearTimeout(timer);
    delete filterTimers.current[key];
  };
  const setDebouncedFilter = (key: DebouncedFilterKey, value: string) => {
    setTextFilterDrafts(current => ({ ...current, [key]: value }));
    cancelPendingFilter(key);
    filterTimers.current[key] = setTimeout(() => {
      delete filterTimers.current[key];
      setFilter(key, value);
    }, filterDebounceMs);
  };
  const clearTextFilter = (key: DebouncedFilterKey) => {
    cancelPendingFilter(key);
    setTextFilterDrafts(current => ({ ...current, [key]: '' }));
    setFilter(key, '');
  };
  const clearAllFilters = () => {
    (Object.keys(filterTimers.current) as DebouncedFilterKey[]).forEach(cancelPendingFilter);
    setTextFilterDrafts({ uuid: '', groupId: '', commitHash: '' });
    setSearchParams({}, { replace: true });
  };

  const groups = useMemo(() => {
    const byKey = new Map<string, FeedGroup>();
    for (const update of updates) {
      const key = update.publishGroup
        ? `group:${update.publishGroup}`
        : `update:${update.updateId}:${update.branch}`;
      const current = byKey.get(key);
      if (current) current.updates.push(update);
      else
        byKey.set(key, { key, publishGroup: update.publishGroup ?? undefined, updates: [update] });
    }
    return Array.from(byKey.values());
  }, [updates]);
  const activeRollouts = useMemo(() => {
    const byBranchAndRuntime = new Map<
      string,
      { branch: string; runtimeVersion: string; percentage: number; commitHash: string }
    >();
    for (const update of updates) {
      if (update.rolloutPercentage == null) continue;
      const key = `${update.branch}:${update.runtimeVersion}`;
      if (!byBranchAndRuntime.has(key)) {
        byBranchAndRuntime.set(key, {
          branch: update.branch,
          runtimeVersion: update.runtimeVersion,
          percentage: update.rolloutPercentage,
          commitHash: update.commitHash,
        });
      }
    }
    return Array.from(byBranchAndRuntime.values());
  }, [updates]);

  const hasFilters = filterKey.some(Boolean);
  const advancedFilterCount = [
    filters.groupId,
    filters.commitHash,
    filters.from,
    filters.to,
  ].filter(Boolean).length;
  const activeFilters = (
    [
      ['branch', 'Branch', filters.branch],
      ['runtimeVersion', 'Runtime', filters.runtimeVersion],
      ['platform', 'Platform', filters.platform],
      ['uuid', 'Update ID', filters.uuid],
      ['groupId', 'Group', filters.groupId],
      ['commitHash', 'Commit', filters.commitHash],
      ['from', 'From', filters.from],
      ['to', 'To', filters.to],
    ] as const
  ).filter(([, , value]) => value);
  const columnCount = canPublishUpdate ? 9 : 8;
  // A rollback marker has no bundle to publish again, and both stores name it
  // with this literal in place of a UUID.
  const isRollbackRow = (update: UpdateFeedRecord) => update.updateUUID === 'Rollback to embedded';
  const toggleGroup = (key: string) => {
    setExpandedGroups(current => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  return (
    <div className="w-full">
      <PageHeader
        title="Updates"
        actions={
          canPublishUpdate && (
            <Button variant="outline" onClick={() => setIsRollbackOpen(true)}>
              <Undo2 className="h-4 w-4" />
              Create rollback
            </Button>
          )
        }
      />
      <UpdateRolloutManagerSheet
        rollout={managedRollout}
        onClose={() => setManagedRollout(null)}
        canManageRollout={canManageUpdateRollout}
      />
      <RollbackDialog
        open={isRollbackOpen}
        onClose={() => setIsRollbackOpen(false)}
        defaultBranch={filters.branch}
        defaultRuntimeVersion={filters.runtimeVersion}
      />
      <RepublishDialog target={republishTarget} onClose={() => setRepublishTarget(null)} />
      {!!query.error && <ApiError error={query.error} />}
      {!!healthError && <ApiError error={healthError} />}

      <section className="mb-5 overflow-hidden rounded-lg border bg-card shadow-card">
        <div className="flex flex-col gap-2.5 p-3 xl:flex-row xl:flex-wrap xl:items-center 2xl:flex-nowrap">
          <div className="w-full xl:w-[22rem] xl:shrink-0">
            <ClearableInput
              aria-label="Filter by update UUID"
              placeholder="Find an update by UUID"
              autoComplete="off"
              spellCheck={false}
              data-1p-ignore="true"
              value={textFilterDrafts.uuid}
              onValueChange={value => setDebouncedFilter('uuid', value)}
              onClear={() => clearTextFilter('uuid')}
              icon={<Search className="h-4 w-4" />}
            />
          </div>
          <div className="grid flex-1 grid-cols-1 gap-2.5 sm:grid-cols-[minmax(13rem,1.35fr)_minmax(10rem,1fr)_minmax(10rem,1fr)]">
            <Combobox
              className="w-full"
              label="All branches"
              loading={branchesQuery.isLoading}
              options={branchOptions}
              value={filters.branch}
              clearable
              onChange={value =>
                setFilters({
                  branch: value,
                  runtimeVersion: value === filters.branch ? filters.runtimeVersion : '',
                })
              }
            />
            <Combobox
              className="w-full"
              label={filters.branch ? 'All runtimes' : 'Select a branch first'}
              loading={runtimeVersionsQuery.isLoading}
              disabled={!filters.branch}
              options={runtimeVersionOptions}
              value={filters.runtimeVersion}
              clearable
              onChange={value => setFilter('runtimeVersion', value)}
            />
            <Combobox
              className="w-full"
              label="All platforms"
              options={platformOptions}
              value={filters.platform}
              clearable
              onChange={value => setFilter('platform', value)}
            />
          </div>
          <div className="flex items-center gap-2">
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="outline" className="flex-1 lg:flex-none">
                  <SlidersHorizontal className="h-4 w-4" />
                  More filters
                  {advancedFilterCount > 0 && (
                    <Badge variant="secondary" className="ml-0.5 h-5 min-w-5 justify-center px-1.5">
                      {advancedFilterCount}
                    </Badge>
                  )}
                </Button>
              </PopoverTrigger>
              <PopoverContent align="end" className="w-[min(26rem,calc(100vw-2rem))] space-y-4">
                <div>
                  <h2 className="text-sm font-semibold text-foreground">More filters</h2>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    Narrow the feed using publication metadata.
                  </p>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <FilterField label="Publish group ID">
                    <ClearableInput
                      aria-label="Filter by publish group ID"
                      placeholder="Group ID"
                      value={textFilterDrafts.groupId}
                      onValueChange={value => setDebouncedFilter('groupId', value)}
                      onClear={() => clearTextFilter('groupId')}
                    />
                  </FilterField>
                  <FilterField label="Commit hash">
                    <ClearableInput
                      aria-label="Filter by commit hash"
                      placeholder="Commit hash"
                      value={textFilterDrafts.commitHash}
                      onValueChange={value => setDebouncedFilter('commitHash', value)}
                      onClear={() => clearTextFilter('commitHash')}
                    />
                  </FilterField>
                  <FilterField label="Published from">
                    <ClearableInput
                      type="date"
                      aria-label="Filter updates published from date"
                      value={filters.from}
                      onValueChange={value => setFilter('from', value)}
                      onClear={() => setFilter('from', '')}
                    />
                  </FilterField>
                  <FilterField label="Published to">
                    <ClearableInput
                      type="date"
                      aria-label="Filter updates published to date"
                      value={filters.to}
                      onValueChange={value => setFilter('to', value)}
                      onClear={() => setFilter('to', '')}
                    />
                  </FilterField>
                </div>
              </PopoverContent>
            </Popover>
            {hasFilters && (
              <Button variant="ghost" size="sm" onClick={clearAllFilters}>
                Clear all
              </Button>
            )}
          </div>
        </div>
        {activeFilters.length > 0 && (
          <div className="flex flex-wrap items-center gap-2 border-t px-3 py-2.5">
            {activeFilters.map(([key, label, value]) => (
              <button
                key={key}
                type="button"
                title={`Clear ${label.toLowerCase()} filter`}
                onClick={() => setFilter(key, '')}
                className="inline-flex max-w-full items-center gap-1.5 rounded-md border bg-secondary px-2 py-1 text-xs text-muted-foreground transition-colors hover:border-input hover:text-foreground">
                <span className="font-medium text-foreground">{label}</span>
                <span className="max-w-48 truncate">{value}</span>
                <X className="h-3 w-3 shrink-0" />
              </button>
            ))}
          </div>
        )}
      </section>

      {activeRollouts.length > 0 && (
        <section className="mb-4 animate-in overflow-hidden rounded-lg border border-emerald-400/25 bg-emerald-400/[0.05] fade-in slide-in-from-top-1 duration-200 motion-reduce:animate-none">
          {activeRollouts.map((rollout, index) => (
            <div
              key={`${rollout.branch}:${rollout.runtimeVersion}`}
              className={`flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between ${
                index > 0 ? 'border-t border-emerald-400/15' : ''
              }`}>
              <div className="flex min-w-0 items-center gap-3">
                <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300">
                  <Gauge className="h-4 w-4" />
                </span>
                <div className="min-w-0">
                  <p className="text-sm font-semibold text-foreground">
                    Update rollout in progress
                  </p>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                    <span className="font-medium text-emerald-700 dark:text-emerald-300">
                      {rollout.percentage}%
                    </span>
                    <span>{rollout.branch}</span>
                    <span aria-hidden="true">·</span>
                    <span title={rollout.runtimeVersion}>
                      {shortRuntimeVersion(rollout.runtimeVersion)}
                    </span>
                    <span aria-hidden="true">·</span>
                    <span>{shortId(rollout.commitHash)}</span>
                  </div>
                </div>
              </div>
              {canManageUpdateRollout && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  onClick={() =>
                    setManagedRollout({
                      branch: rollout.branch,
                      runtimeVersion: rollout.runtimeVersion,
                    })
                  }>
                  Manage rollout
                </Button>
              )}
            </div>
          ))}
        </section>
      )}

      <div className="overflow-hidden rounded-lg border bg-card shadow-card">
        <Table className="min-w-[1150px] table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead className={canPublishUpdate ? 'w-[32%]' : 'w-[36%]'}>Update</TableHead>
              <TableHead className="w-[11%]">Branch</TableHead>
              <TableHead className="w-[8%]">Runtime</TableHead>
              <TableHead className="w-[7%]">Platform</TableHead>
              <TableHead className="w-[8%] text-right">Devices</TableHead>
              <TableHead className="w-[9%]">Health</TableHead>
              <TableHead className="w-[9%]">Commit</TableHead>
              <TableHead className={canPublishUpdate ? 'w-[10%]' : 'w-[12%]'}>Published</TableHead>
              {canPublishUpdate && <TableHead className="w-[6%]" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {query.isLoading &&
              Array.from({ length: 8 }).map((_, index) => (
                <TableRow key={index}>
                  {Array.from({ length: columnCount }).map((__, cell) => (
                    <TableCell key={cell}>
                      <Skeleton className="h-4 w-full" />
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            {!query.isLoading &&
              groups.map(group => {
                const primary = group.updates[0];
                const isGroup = !!group.publishGroup;
                const expanded = expandedGroups.has(group.key);
                const platforms = Array.from(new Set(group.updates.map(update => update.platform)));
                const rolloutPercentage = group.updates.find(
                  update => update.rolloutPercentage != null
                )?.rolloutPercentage;
                // Summed over every update of the group, not only the ones
                // heading their branch: a group is one publish across
                // platforms, and dropping the members that have been
                // superseded understated a fleet that had rolled back onto it.
                const groupHealth = aggregateUpdateHealth(
                  group.updates.map(update => healthByUuid[update.updateUUID])
                );
                return (
                  <Fragment key={group.key}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => (isGroup ? toggleGroup(group.key) : openUpdate(primary))}>
                      <TableCell className="min-w-0 overflow-hidden">
                        <div className="flex min-w-0 items-start gap-2.5">
                          <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center text-muted-foreground">
                            {isGroup ? (
                              expanded ? (
                                <ChevronDown className="h-4 w-4" />
                              ) : (
                                <ChevronRight className="h-4 w-4" />
                              )
                            ) : (
                              <span className="h-1.5 w-1.5 rounded-full bg-muted-foreground/50" />
                            )}
                          </span>
                          <div className="min-w-0">
                            <p
                              className="block max-w-full truncate font-medium text-foreground"
                              title={primary.message || `Update ${primary.updateId}`}>
                              {updateTitle(primary.message, primary.commitHash) ||
                                `Update ${primary.updateId}`}
                            </p>
                            <div className="mt-1 flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                              {isGroup && (
                                <Badge variant="secondary" className="px-1.5 py-0">
                                  <Layers3 className="h-3 w-3" />
                                  {group.updates.length}
                                </Badge>
                              )}
                              {rolloutPercentage != null && (
                                <UpdateRolloutBadge percentage={rolloutPercentage} />
                              )}
                              <span
                                className="truncate"
                                title={group.publishGroup ?? primary.updateUUID}>
                                {isGroup ? `group:${group.publishGroup}` : primary.updateUUID}
                              </span>
                            </div>
                          </div>
                        </div>
                      </TableCell>
                      <TableCell>
                        <BranchLabel branch={primary.branch} />
                      </TableCell>
                      <TableCell className="min-w-0">
                        <RuntimeLabel value={primary.runtimeVersion} />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          {platforms.map(value => (
                            <PlatformIcon key={value} platform={value} />
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="text-right font-medium tabular-nums">
                        {groupHealth ? groupHealth.devicesOnUpdate.toLocaleString() : '—'}
                      </TableCell>
                      <TableCell>
                        <HealthBadge health={groupHealth} />
                      </TableCell>
                      <TableCell>
                        <CommitLabel value={primary.commitHash} />
                      </TableCell>
                      <TableCell>
                        <TimestampCell dateString={primary.createdAt} compact />
                      </TableCell>
                      {canPublishUpdate && (
                        <TableCell className="text-right">
                          {/* A group republishes as a group: its members are
                              the per-platform halves of one publish, and
                              putting back only one of them would leave the
                              branch serving two different builds. */}
                          {!group.updates.every(isRollbackRow) && (
                            <Button
                              variant="ghost"
                              size="sm"
                              title="Republish"
                              onClick={event => {
                                event.stopPropagation();
                                setRepublishTarget({
                                  branch: primary.branch,
                                  runtimeVersion: primary.runtimeVersion,
                                  label:
                                    updateTitle(primary.message, primary.commitHash) ||
                                    `Update ${primary.updateId}`,
                                  platforms,
                                  ...(isGroup
                                    ? { publishGroup: group.publishGroup as string }
                                    : { updateId: primary.updateId }),
                                });
                              }}>
                              <Undo2 className="h-4 w-4" />
                            </Button>
                          )}
                        </TableCell>
                      )}
                    </TableRow>
                    {isGroup &&
                      expanded &&
                      group.updates.map(update => (
                        <TableRow
                          key={`${group.key}:${update.updateId}`}
                          className="animate-in cursor-pointer bg-muted/20 fade-in slide-in-from-top-1 duration-150 motion-reduce:animate-none"
                          onClick={() => openUpdate(update)}>
                          <TableCell>
                            <div className="ml-9 min-w-0 border-l-2 border-link/40 pl-4">
                              <div className="flex flex-wrap items-center gap-2">
                                <p className="font-medium text-foreground">
                                  {update.platform === 'ios'
                                    ? 'iOS update'
                                    : update.platform === 'android'
                                      ? 'Android update'
                                      : `${update.platform} update`}
                                </p>
                                {update.rolloutPercentage != null && (
                                  <UpdateRolloutBadge percentage={update.rolloutPercentage} />
                                )}
                              </div>
                              <p className="mt-0.5 truncate text-xs text-muted-foreground">
                                {update.updateUUID}
                              </p>
                            </div>
                          </TableCell>
                          <TableCell>
                            <BranchLabel branch={update.branch} />
                          </TableCell>
                          <TableCell className="min-w-0">
                            <RuntimeLabel value={update.runtimeVersion} />
                          </TableCell>
                          <TableCell>
                            <PlatformIcon platform={update.platform} />
                          </TableCell>
                          {/* Every update carries its own figures, whether or
                              not it still heads its branch. A dash now means
                              "not measured yet", never "no longer the current
                              one": a version the fleet rolled back TO is the
                              case where the count matters most, and it stops
                              being health relevant at the exact moment it
                              starts carrying devices again. */}
                          <TableCell className="text-right font-medium tabular-nums">
                            {healthByUuid[update.updateUUID]
                              ? healthByUuid[update.updateUUID].devicesOnUpdate.toLocaleString()
                              : '—'}
                          </TableCell>
                          <TableCell>
                            <HealthBadge health={healthByUuid[update.updateUUID]} />
                          </TableCell>
                          <TableCell>
                            <CommitLabel value={update.commitHash} />
                          </TableCell>
                          <TableCell>
                            <TimestampCell dateString={update.createdAt} compact />
                          </TableCell>
                          {/* No per-member republish: see the group row. */}
                          {canPublishUpdate && <TableCell />}
                        </TableRow>
                      ))}
                  </Fragment>
                );
              })}
            {!query.isLoading && groups.length === 0 && (
              <TableRow>
                <TableCell colSpan={columnCount} className="h-28 text-center text-muted-foreground">
                  {hasFilters ? 'No updates match these filters.' : 'No updates published yet.'}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        {query.hasNextPage && (
          <div className="flex justify-center border-t p-3">
            <Button
              variant="outline"
              onClick={() => void query.fetchNextPage()}
              disabled={query.isFetchingNextPage}>
              {query.isFetchingNextPage ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <ChevronDown className="h-4 w-4" />
              )}
              Load more
            </Button>
          </div>
        )}
      </div>
    </div>
  );
};
