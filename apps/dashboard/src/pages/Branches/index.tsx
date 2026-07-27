import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, Plus } from 'lucide-react';
import { Link, useNavigate, useParams } from 'react-router';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { useBranchCurrentStatus } from '@/hooks/use-branch-current-status';
import { toBranchStatus } from '@/lib/branch-status';
import { PageHeader } from '@/components/PageHeader';
import { Button } from '@/components/ui/button';
import { ChannelBranchMapping } from '@/components/ChannelBranchMapping';
import { CreateBranchModal } from '@/components/branch-creation-modal';
import { BranchesTable } from '@/pages/Updates/components/BranchesTable';
import { RuntimeVersionsTable } from '@/pages/Updates/components/RuntimeVersionsTable';
import { UpdatesTable } from '@/pages/Updates/components/UpdatesTable';

export const Branches = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { branchName, runtimeVersion } = useParams();
  const { selectedAppId } = useSelectedApp();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const canCreateBranch = useAppPermission('branch:create', 'admin-only');
  const [createOpen, setCreateOpen] = useState(false);
  const decodedBranch = branchName ? decodeURIComponent(branchName) : '';
  const decodedRuntime = runtimeVersion ? decodeURIComponent(runtimeVersion) : undefined;
  const channelsQuery = useQuery({
    queryKey: ['channels', selectedAppId],
    queryFn: () => api.getChannels(),
    enabled: !!selectedAppId && !!branchName,
  });
  const branchesQuery = useQuery({
    queryKey: ['branches', selectedAppId],
    queryFn: () => api.getBranches(),
    enabled: !!selectedAppId && !!branchName && CONTROL_PLANE_ENABLED,
  });
  const statelessBranchStatus = useBranchCurrentStatus(
    CONTROL_PLANE_ENABLED ? undefined : decodedBranch
  );
  const selectedBranch = branchesQuery.data?.find(branch => branch.branchName === decodedBranch);
  const branchStatus = CONTROL_PLANE_ENABLED
    ? toBranchStatus(selectedBranch?.currentUpdate)
    : statelessBranchStatus;

  if (!branchName) {
    return (
      <div className="w-full">
        <PageHeader
          title="Branches"
          actions={
            CONTROL_PLANE_ENABLED && canCreateBranch ? (
              <Button onClick={() => setCreateOpen(true)}>
                <Plus className="h-4 w-4" />
                Create branch
              </Button>
            ) : undefined
          }
        />
        <BranchesTable />
        {CONTROL_PLANE_ENABLED && canCreateBranch && (
          <CreateBranchModal
            isOpen={createOpen}
            onClose={() => setCreateOpen(false)}
            onBranchCreated={async () => {
              await queryClient.invalidateQueries({ queryKey: ['branches', selectedAppId] });
            }}
          />
        )}
      </div>
    );
  }

  const mappedChannels = (channelsQuery.data ?? [])
    .filter(channel => channel.branchName === decodedBranch)
    .map(channel => channel.releaseChannelName);

  const selectRuntime = (selectedRuntime: string) => {
    if (CONTROL_PLANE_ENABLED) {
      const filters = new URLSearchParams({
        branch: decodedBranch,
        runtimeVersion: selectedRuntime,
      });
      navigate(`/updates?${filters.toString()}`);
      return;
    }
    navigate(
      `/branches/${encodeURIComponent(decodedBranch)}/runtime-versions/${encodeURIComponent(selectedRuntime)}`
    );
  };

  return (
    <div className="w-full">
      <Link
        to="/branches"
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-4 w-4" />
        All branches
      </Link>
      <PageHeader
        title={
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate">{decodedBranch}</span>
            {decodedRuntime && (
              <code className="rounded-md bg-muted px-2 py-1 font-mono text-sm font-medium">
                {decodedRuntime}
              </code>
            )}
          </span>
        }
      />
      <div className="space-y-7">
        <ChannelBranchMapping
          branchName={decodedBranch}
          channelNames={mappedChannels}
          focus="branch"
          branchStatus={branchStatus}
        />
        {decodedRuntime ? (
          <section className="space-y-3">
            <h2 className="text-base font-semibold">Updates</h2>
            <UpdatesTable
              branch={decodedBranch}
              runtimeVersion={decodedRuntime}
              showBreadcrumb={false}
            />
          </section>
        ) : (
          <section className="space-y-3">
            <h2 className="text-base font-semibold">Runtime versions</h2>
            <RuntimeVersionsTable branch={decodedBranch} onSelectRuntime={selectRuntime} />
          </section>
        )}
      </div>
    </div>
  );
};
