import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Plus } from 'lucide-react';
import { api } from '@/lib/api.ts';
import { ApiError } from '@/components/APIError';
import { Combobox } from '@/components/Combobox';
import { CreateBranchModal } from '@/components/branch-creation-modal';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';

export const SelectBranch = ({
  currentBranch,
  onChange,
  loading,
  disabled,
  className,
  excludeBranchIds,
}: {
  // Receives the branch id (used to map a channel) and its name (used when
  // creating a channel, which takes a branch name).
  onChange: (branchId?: string | null, branchName?: string) => void;
  loading?: boolean;
  disabled?: boolean;
  currentBranch?: string | null;
  className?: string;
  // Branch ids to leave out of the list, e.g. the channel's own default
  // branch when picking a rollout branch.
  excludeBranchIds?: string[];
}) => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { selectedAppId } = useSelectedApp();
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['branches', selectedAppId],
    enabled: !!selectedAppId,
    queryFn: () => api.getBranches(),
  });
  // Only branches carrying an id can be mapped to a channel. On the control
  // plane every branch has one; on the bucket backend a branch that exists in
  // storage but has no Expo counterpart has none, and there is nothing to map
  // it with, so it is left out rather than offered and failing on select.
  const allBranches =
    data
      ?.filter(d => !!d.branchId)
      ?.filter(d => !excludeBranchIds?.includes(d.branchId as string))
      ?.map(d => {
        return {
          branchName: d.branchName,
          id: d.branchId as string,
        };
      }) ?? [];
  if (error) {
    return <ApiError error={error} />;
  }
  return (
    <>
      <Combobox
        className={className}
        loading={isLoading || loading}
        disabled={disabled}
        options={allBranches.map(b => {
          return {
            label: b.branchName,
            value: b.id,
          };
        })}
        value={currentBranch || ''}
        onChange={value => onChange(value, allBranches.find(b => b.id === value)?.branchName)}
        // Branches can only be created on the control plane, so the shortcut is
        // hidden on the stateless backend.
        action={
          CONTROL_PLANE_ENABLED
            ? {
                label: 'Create branch',
                icon: <Plus className="mr-2 h-4 w-4" />,
                onSelect: () => setIsCreateOpen(true),
              }
            : undefined
        }
      />
      <CreateBranchModal
        isOpen={isCreateOpen}
        onClose={() => setIsCreateOpen(false)}
        onBranchCreated={async ({ branchId, branchName }) => {
          // Select the new branch before refreshing so the parent form cannot
          // be submitted without its mapping if the refetch is slow.
          onChange(branchId, branchName);
          await refetch();
        }}
      />
    </>
  );
};
