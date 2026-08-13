import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { toBranchStatus } from '@/lib/branch-status';

export const useBranchCurrentStatus = (branchName?: string | null) => {
  const { selectedAppId } = useSelectedApp();
  const branch = branchName ?? '';

  const runtimeVersionsQuery = useQuery({
    queryKey: ['runtimeVersions', selectedAppId, branch],
    queryFn: () => api.getRuntimeVersions(branch),
    enabled: !!selectedAppId && !!branch,
  });
  const latestRuntime = useMemo(
    () =>
      [...(runtimeVersionsQuery.data ?? [])].sort(
        (left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt)
      )[0],
    [runtimeVersionsQuery.data]
  );
  const updatesQuery = useQuery({
    queryKey: ['updates', selectedAppId, branch, latestRuntime?.runtimeVersion],
    queryFn: () => api.getUpdates(branch, latestRuntime!.runtimeVersion),
    enabled: !!selectedAppId && !!branch && !!latestRuntime,
  });
  return useMemo(() => {
    const updatesByRecency = [...(updatesQuery.data?.items ?? [])].sort(
      (left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt)
    );
    const currentRollout = updatesByRecency.find(update => update.rolloutPercentage != null);
    const currentUpdate = currentRollout ?? updatesByRecency[0];

    if (!currentUpdate) return undefined;
    return toBranchStatus({
      runtimeVersion: latestRuntime?.runtimeVersion ?? '',
      commitHash: currentUpdate.commitHash,
      createdAt: currentUpdate.createdAt,
      rolloutPercentage: currentUpdate.rolloutPercentage,
    });
  }, [latestRuntime?.runtimeVersion, updatesQuery.data]);
};
