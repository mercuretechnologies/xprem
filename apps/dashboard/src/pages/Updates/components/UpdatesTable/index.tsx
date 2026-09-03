import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { api, UpdateRecord } from '@/lib/api.ts';
import { ApiError } from '@/components/APIError';
import { DataTable } from '@/components/DataTable';
import { Badge } from '@/components/ui/badge.tsx';
import apple from '@/assets/apple.svg';
import android from '@/assets/android.svg';
import { useNavigate } from 'react-router';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import { UpdatesBreadcrumb } from '@/pages/Updates/components/UpdatesBreadcrumb';
import { UpdateRolloutCard } from '@/pages/Updates/components/UpdateRolloutCard';
import { aggregateUpdateHealth } from '@/pages/Updates/components/updateHealth';
import { Button } from '@/components/ui/button';
import { updateDetailsPath, updateTitle } from '@/lib/update-format';
import { GitCommitLink } from '@/components/GitCommitLink';
import { LinkedUpdateTitle } from '@/components/LinkedUpdateTitle';

const UPDATES_PAGE_SIZE = 20;

export const UpdatesTable = ({
  branch,
  runtimeVersion,
  showBreadcrumb = true,
}: {
  branch: string;
  runtimeVersion: string;
  showBreadcrumb?: boolean;
}) => {
  const navigate = useNavigate();
  const { selectedAppId } = useSelectedApp();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const canManageUpdateRollout = useAppPermission('update-rollout:manage', 'admin-only');
  const updatesQuery = useInfiniteQuery({
    queryKey: ['updates', selectedAppId, branch, runtimeVersion, UPDATES_PAGE_SIZE],
    queryFn: ({ pageParam }) =>
      api.getUpdates(branch, runtimeVersion, pageParam, UPDATES_PAGE_SIZE),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: lastPage => lastPage.nextCursor ?? undefined,
    enabled: !!selectedAppId,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  const updates = updatesQuery.data?.pages.flatMap(page => page.items) ?? [];
  const appDetailsQuery = useQuery({
    queryKey: ['appDetails', selectedAppId],
    queryFn: () => api.getApp(selectedAppId!),
    enabled: !!selectedAppId && CONTROL_PLANE_ENABLED,
  });

  // Rollout state is read fresh (control-plane only). It drives the card above
  // the table and the "Control" markers in the passive Rollout column.
  const rolloutQuery = useQuery({
    queryKey: ['update-rollout', selectedAppId, branch, runtimeVersion],
    queryFn: () => api.getUpdateRollout(branch, runtimeVersion),
    enabled: !!selectedAppId && CONTROL_PLANE_ENABLED,
  });
  const activeRollout = rolloutQuery.data?.active ? rolloutQuery.data.updates : [];
  const controlIds = new Set(activeRollout.map(u => u.controlUpdateId).filter(Boolean));

  // Update health (adoption + launch failures) comes from the device
  // registry, uncached server-side. It only feeds the rollout card here (the
  // per-update health display belongs to the dedicated Observe section), so
  // it is fetched, and polled fast, only while a rollout is live. Rollback
  // rows carry a literal "Rollback to embedded" instead of a UUID: they have
  // no health and must not reach the endpoint. The server caps one request
  // at 100 ids.
  const isUuid = (value: string) =>
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
  const byNumericId = new Map(updates.map(u => [u.updateId, u]));
  const rolloutUuids = activeRollout
    .map(r => byNumericId.get(r.updateId)?.updateUUID)
    .filter((uuid): uuid is string => !!uuid && isUuid(uuid));
  const controlUuids = activeRollout
    .map(r => (r.controlUpdateId ? byNumericId.get(r.controlUpdateId)?.updateUUID : undefined))
    .filter((uuid): uuid is string => !!uuid && isUuid(uuid));
  const updateUUIDs = [...new Set([...rolloutUuids, ...controlUuids])].slice(0, 100);
  const healthQuery = useQuery({
    queryKey: ['update-health', selectedAppId, branch, runtimeVersion, updateUUIDs.join(',')],
    queryFn: () => api.getUpdateHealth(updateUUIDs),
    enabled:
      !!selectedAppId &&
      CONTROL_PLANE_ENABLED &&
      updateUUIDs.length > 0 &&
      activeRollout.length > 0,
    refetchInterval: 5_000,
  });
  const healthByUuid = healthQuery.data?.updates;

  // A rollout spans one row per platform, each a DISTINCT update: aggregate
  // the raw cohorts across the rollout set (and the control set) so an
  // iOS-only crash storm cannot hide behind a healthy Android row.
  const rolloutHealth = aggregateUpdateHealth(rolloutUuids.map(uuid => healthByUuid?.[uuid]));
  const controlHealth = aggregateUpdateHealth(controlUuids.map(uuid => healthByUuid?.[uuid]));

  return (
    <div className="w-full flex-1">
      {showBreadcrumb && <UpdatesBreadcrumb branch={branch} runtimeVersion={runtimeVersion} />}
      {!!updatesQuery.error && <ApiError error={updatesQuery.error} />}
      {!!rolloutQuery.error && <ApiError error={rolloutQuery.error} />}
      {!!healthQuery.error && <ApiError error={healthQuery.error} />}
      {CONTROL_PLANE_ENABLED && activeRollout.length > 0 && (
        <UpdateRolloutCard
          branch={branch}
          runtimeVersion={runtimeVersion}
          updates={activeRollout}
          canManageRollout={canManageUpdateRollout}
          rolloutHealth={rolloutHealth}
          controlHealth={controlHealth}
          rolloutUpdateUUIDs={rolloutUuids}
          controlUpdateUUIDs={controlUuids}
        />
      )}
      <DataTable
        loading={updatesQuery.isLoading}
        columns={[
          {
            header: 'Update',
            accessorKey: 'updateId',
            cell: ({ row }) => <span className="font-medium">{row.original.updateId}</span>,
          },
          {
            header: 'UUID',
            accessorKey: 'updateUUID',
            cell: ({ row }) => (
              <span className="font-mono text-xs text-muted-foreground">
                {row.original.updateUUID}
              </span>
            ),
          },
          {
            header: 'Platform',
            accessorKey: 'platform',
            cell: ({ row }) => {
              const isIos = row.original.platform === 'ios';
              const isAndroid = row.original.platform === 'android';
              return (
                <div className="flex items-center gap-2">
                  {isIos && <img src={apple} className="w-4 brightness-0 dark:invert" alt="iOS" />}
                  {isAndroid && (
                    <img src={android} className="w-4 brightness-0 dark:invert" alt="Android" />
                  )}
                </div>
              );
            },
          },
          {
            header: 'Message',
            accessorKey: 'message',
            cell: ({ row }) => {
              const msg = updateTitle(row.original.message, row.original.commitHash);
              return msg ? (
                <span className="block max-w-[200px] truncate text-sm text-muted-foreground">
                  <LinkedUpdateTitle title={msg} gitUrl={appDetailsQuery.data?.gitUrl} />
                </span>
              ) : (
                <span className="text-sm text-muted-foreground/60">No message</span>
              );
            },
          },
          {
            header: 'Commit',
            accessorKey: 'commitHash',
            cell: ({ row }) => (
              <GitCommitLink
                commitHash={row.original.commitHash}
                gitUrl={appDetailsQuery.data?.gitUrl}
              />
            ),
          },
          ...(CONTROL_PLANE_ENABLED
            ? [
                {
                  header: 'Rollout',
                  id: 'rollout',
                  cell: ({ row }: { row: { original: UpdateRecord } }) => {
                    const update = row.original;
                    if (update.rolloutPercentage != null) {
                      return (
                        <Badge className="border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300">
                          {update.rolloutPercentage}% rollout
                        </Badge>
                      );
                    }
                    // A rollout used to run on this update but has ended
                    // (finished or reverted, the record does not distinguish).
                    if (update.controlUpdateId != null) {
                      return (
                        <span className="text-xs text-muted-foreground/60">Rollout ended</span>
                      );
                    }
                    // This update is the control an active rollout falls back to.
                    if (controlIds.has(update.updateId)) {
                      return <Badge variant="outline">Control</Badge>;
                    }
                    return <span className="text-muted-foreground/40">None</span>;
                  },
                },
              ]
            : []),
          {
            header: 'Published',
            accessorKey: 'createdAt',
            cell: ({ row }) => <TimestampCell dateString={row.original.createdAt} showSeconds />,
          },
        ]}
        data={updates}
        defaultSorting={[{ id: 'createdAt', desc: true }]}
        emptyMessage="No updates published for this runtime version yet."
        onRowClick={row =>
          navigate(updateDetailsPath({ branch, runtimeVersion, updateId: row.updateId }, 'branch'))
        }
      />
      {updatesQuery.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button
            variant="outline"
            size="sm"
            disabled={updatesQuery.isFetchingNextPage}
            onClick={() => void updatesQuery.fetchNextPage()}>
            {updatesQuery.isFetchingNextPage ? 'Loading…' : 'Load more'}
          </Button>
        </div>
      )}
    </div>
  );
};
