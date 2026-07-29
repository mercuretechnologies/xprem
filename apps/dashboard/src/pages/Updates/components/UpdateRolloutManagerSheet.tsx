import { useQueries, useQuery } from '@tanstack/react-query';
import { Gauge } from 'lucide-react';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { ApiError } from '@/components/APIError';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';
import { UpdateRolloutCard } from '@/pages/Updates/components/UpdateRolloutCard';

export type ManagedUpdateRollout = {
  branch: string;
  runtimeVersion: string;
};

const isUuid = (value: string) =>
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);

export const UpdateRolloutManagerSheet = ({
  rollout,
  onClose,
  canManageRollout,
}: {
  rollout: ManagedUpdateRollout | null;
  onClose: () => void;
  canManageRollout: boolean;
}) => {
  const { selectedAppId } = useSelectedApp();
  const rolloutQuery = useQuery({
    queryKey: ['update-rollout', selectedAppId, rollout?.branch, rollout?.runtimeVersion],
    queryFn: () => api.getUpdateRollout(rollout!.branch, rollout!.runtimeVersion),
    enabled: !!selectedAppId && !!rollout,
  });
  const activeUpdates = rolloutQuery.data?.active ? rolloutQuery.data.updates : [];
  const updateIDs = [
    ...new Set(
      activeUpdates.flatMap(update =>
        update.controlUpdateId ? [update.updateId, update.controlUpdateId] : [update.updateId]
      )
    ),
  ];
  const updateDetailsQueries = useQueries({
    queries: updateIDs.map(updateId => ({
      queryKey: [
        'update-details',
        selectedAppId,
        rollout?.branch,
        rollout?.runtimeVersion,
        updateId,
      ],
      queryFn: () =>
        api.getUpdateDetails(rollout!.branch, rollout!.runtimeVersion, updateId),
      enabled: !!selectedAppId && !!rollout,
    })),
  });
  const updatesByID = new Map(
    updateDetailsQueries.flatMap((query, index) =>
      query.data ? [[updateIDs[index], query.data] as const] : []
    )
  );
  const updateDetailsError = updateDetailsQueries.find(query => query.error)?.error;
  const rolloutUpdateUUIDs = activeUpdates
    .map(update => updatesByID.get(update.updateId)?.updateUUID)
    .filter((updateUUID): updateUUID is string => !!updateUUID && isUuid(updateUUID));
  const controlUpdateUUIDs = activeUpdates
    .map(update =>
      update.controlUpdateId ? updatesByID.get(update.controlUpdateId)?.updateUUID : undefined
    )
    .filter((updateUUID): updateUUID is string => !!updateUUID && isUuid(updateUUID));

  return (
    <Sheet open={!!rollout} onOpenChange={open => !open && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader className="border-b pb-5 pr-8">
          <div className="mb-2 flex h-9 w-9 items-center justify-center rounded-md border border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300">
            <Gauge className="h-4 w-4" />
          </div>
          <SheetTitle>Manage update rollout</SheetTitle>
          <SheetDescription>
            {rollout ? (
              <>
                <span className="font-medium text-foreground">{rollout.branch}</span>
                <span className="px-1.5">·</span>
                {rollout.runtimeVersion}
              </>
            ) : (
              'Update rollout'
            )}
          </SheetDescription>
        </SheetHeader>

        <div className="pt-5">
          {rolloutQuery.isLoading && (
            <div className="space-y-3">
              <Skeleton className="h-6 w-40" />
              <Skeleton className="h-24 w-full" />
            </div>
          )}
          {!!rolloutQuery.error && <ApiError error={rolloutQuery.error} />}
          {!!updateDetailsError && <ApiError error={updateDetailsError} />}
          {!rolloutQuery.isLoading && !rolloutQuery.error && activeUpdates.length === 0 && (
            <div className="rounded-lg border border-dashed p-6 text-center">
              <p className="font-medium text-foreground">This rollout has ended</p>
              <p className="mt-1 text-sm text-muted-foreground">
                Refresh the feed to see the current update state.
              </p>
            </div>
          )}
          {rollout && activeUpdates.length > 0 && (
            <UpdateRolloutCard
              branch={rollout.branch}
              runtimeVersion={rollout.runtimeVersion}
              updates={activeUpdates}
              canManageRollout={canManageRollout}
              rolloutUpdateUUIDs={rolloutUpdateUUIDs}
              controlUpdateUUIDs={controlUpdateUUIDs}
            />
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
};
