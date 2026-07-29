import { useMemo, useState } from 'react';
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { Copy, GitBranch, Lock, Search, ShieldAlert, Trash2 } from 'lucide-react';
import { useNavigate } from 'react-router';
import { api, BranchRecord, describeApiError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { useToast } from '@/hooks/use-toast';
import { ApiError } from '@/components/APIError';
import { DataTable } from '@/components/DataTable';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { EnterpriseExplainerDialog } from '@/ee/components/EnterpriseExplainerDialog';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { AdminOnlyNote } from '@/components/ui/admin-only-note';

type BranchSummary = BranchRecord & {
  latestRuntimeVersion?: string;
  latestUpdateAt?: string;
};

export const BranchesTable = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const { selectedAppId } = useSelectedApp();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const canDeleteBranch = useAppPermission('branch:delete', 'admin-only');
  const canProtectBranch = useAppPermission('branch:protect', 'admin-only');
  const [search, setSearch] = useState('');
  const [branchToDelete, setBranchToDelete] = useState<BranchRecord | null>(null);
  const [branchBeingToggled, setBranchBeingToggled] = useState<string | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);
  // Branch protection is enterprise: without a valid license the toggle opens
  // the explainer dialog instead of calling the API.
  const [isExplainerOpen, setIsExplainerOpen] = useState(false);
  // Protecting is the deliberate direction, so it goes through a confirmation;
  // unprotecting is immediate.
  const [branchToProtect, setBranchToProtect] = useState<BranchRecord | null>(null);

  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
    enabled: CONTROL_PLANE_ENABLED,
  });
  const isEnterprise = !!licenseQuery.data?.valid;

  const branchesQuery = useQuery({
    queryKey: ['branches', selectedAppId],
    queryFn: () => api.getBranches(),
    enabled: !!selectedAppId,
  });
  const runtimeQueries = useQueries({
    queries: CONTROL_PLANE_ENABLED
      ? []
      : (branchesQuery.data ?? []).map(branch => ({
          queryKey: ['runtimeVersions', selectedAppId, branch.branchName],
          queryFn: () => api.getRuntimeVersions(branch.branchName),
          enabled: !!selectedAppId,
        })),
  });

  const summaries = useMemo<BranchSummary[]>(() => {
    const normalized = search.trim().toLowerCase();
    return (branchesQuery.data ?? [])
      .map((branch, index) => {
        const latestRuntime = runtimeQueries[index]?.data?.[0];
        return {
          ...branch,
          latestRuntimeVersion:
            branch.currentUpdate?.runtimeVersion ?? latestRuntime?.runtimeVersion,
          latestUpdateAt: branch.currentUpdate?.createdAt ?? latestRuntime?.lastUpdatedAt,
        };
      })
      .filter(branch => !normalized || branch.branchName.toLowerCase().includes(normalized));
  }, [branchesQuery.data, runtimeQueries, search]);

  const protectionMutation = useMutation({
    mutationFn: ({
      branchName,
      protected: isProtected,
    }: {
      branchName: string;
      protected: boolean;
    }) => api.setBranchProtection(branchName, isProtected),
  });

  const applyProtection = async (branch: BranchRecord, next: boolean) => {
    setBranchBeingToggled(branch.branchName);
    try {
      await protectionMutation.mutateAsync({ branchName: branch.branchName, protected: next });
      await queryClient.invalidateQueries({ queryKey: ['branches', selectedAppId] });
      toast({
        title: next ? 'Branch protected' : 'Branch unprotected',
        description: next
          ? `"${branch.branchName}" can no longer be deleted, by anyone.`
          : `"${branch.branchName}" can be deleted again.`,
      });
      setBranchToProtect(null);
    } catch (error) {
      const message = describeApiError(error, 'Could not update branch protection');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setBranchBeingToggled(null);
    }
  };

  const toggleProtection = (branch: BranchRecord, next: boolean) => {
    if (!isEnterprise) {
      setIsExplainerOpen(true);
      return;
    }
    if (next) {
      setBranchToProtect(branch);
      return;
    }
    void applyProtection(branch, false);
  };

  const deleteBranch = async () => {
    if (!branchToDelete) return;
    setIsDeleting(true);
    try {
      await api.deleteBranch(branchToDelete.branchName);
      await queryClient.invalidateQueries({ queryKey: ['branches', selectedAppId] });
      toast({
        title: 'Branch deleted',
        description: `"${branchToDelete.branchName}" was removed.`,
      });
      setBranchToDelete(null);
    } catch (error) {
      const message = describeApiError(error, 'Could not delete branch');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsDeleting(false);
    }
  };

  const columns = [
    {
      header: 'Branch',
      accessorKey: 'branchName',
      cell: ({ row }: { row: { original: BranchSummary } }) => (
        <span className="flex items-center gap-2.5 font-medium">
          <GitBranch className="h-4 w-4 text-muted-foreground" />
          {row.original.branchName}
          {row.original.protected && (
            <Lock className="h-3.5 w-3.5 text-emerald-700 dark:text-emerald-300" />
          )}
        </span>
      ),
    },
    {
      header: 'Latest runtime',
      accessorKey: 'latestRuntimeVersion',
      cell: ({ row }: { row: { original: BranchSummary } }) =>
        row.original.latestRuntimeVersion ? (
          <code className="font-mono text-xs">{row.original.latestRuntimeVersion}</code>
        ) : (
          <span className="text-muted-foreground/60">None</span>
        ),
    },
    {
      header: 'Latest update',
      accessorKey: 'latestUpdateAt',
      cell: ({ row }: { row: { original: BranchSummary } }) =>
        row.original.latestUpdateAt ? (
          <TimestampCell dateString={row.original.latestUpdateAt} />
        ) : (
          <span className="text-muted-foreground/60">None</span>
        ),
    },
    {
      header: 'ID',
      accessorKey: 'branchId',
      cell: ({ row }: { row: { original: BranchSummary } }) => (
        <div className="flex items-center gap-1">
          <code className="max-w-32 truncate font-mono text-xs text-muted-foreground">
            {row.original.branchId || 'Legacy'}
          </code>
          {row.original.branchId && (
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              title="Copy branch ID"
              onClick={event => {
                event.stopPropagation();
                void navigator.clipboard.writeText(row.original.branchId as string);
              }}>
              <Copy className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      ),
    },
    ...(CONTROL_PLANE_ENABLED && canProtectBranch
      ? [
          {
            header: 'Protected',
            id: 'protected',
            cell: ({ row }: { row: { original: BranchSummary } }) => (
              <div onClick={event => event.stopPropagation()}>
                <Switch
                  checked={row.original.protected}
                  disabled={branchBeingToggled === row.original.branchName}
                  onCheckedChange={next => toggleProtection(row.original, next)}
                  aria-label={row.original.protected ? 'Unprotect branch' : 'Protect branch'}
                />
              </div>
            ),
          },
        ]
      : []),
    ...(CONTROL_PLANE_ENABLED && canDeleteBranch
      ? [
          {
            header: '',
            id: 'actions',
            cell: ({ row }: { row: { original: BranchSummary } }) => (
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                title={
                  row.original.protected ? 'Unlock this branch before deleting it' : 'Delete branch'
                }
                disabled={row.original.protected}
                onClick={event => {
                  event.stopPropagation();
                  setBranchToDelete(row.original);
                }}>
                <Trash2 className="h-4 w-4" />
              </Button>
            ),
          },
        ]
      : []),
  ];

  return (
    <div className="space-y-4">
      {!!branchesQuery.error && <ApiError error={branchesQuery.error} />}
      {CONTROL_PLANE_ENABLED && !canDeleteBranch && !canProtectBranch && (
        <AdminOnlyNote>Branch management is read-only for your account.</AdminOnlyNote>
      )}
      <div className="relative max-w-sm">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          aria-label="Search branches"
          placeholder="Search branches"
          value={search}
          onChange={event => setSearch(event.target.value)}
          className="pl-9"
        />
      </div>
      <DataTable
        loading={
          branchesQuery.isLoading || runtimeQueries.some(runtimeQuery => runtimeQuery.isLoading)
        }
        columns={columns}
        data={summaries}
        emptyMessage={search ? 'No branches match this search.' : 'No branches yet.'}
        onRowClick={row => navigate(`/branches/${encodeURIComponent(row.branchName)}`)}
      />
      {CONTROL_PLANE_ENABLED && canDeleteBranch && (
        <DeleteDialog
          isOpen={!!branchToDelete}
          onClose={() => setBranchToDelete(null)}
          onConfirm={deleteBranch}
          isDeleting={isDeleting}
          title="Delete branch"
          resourceName={branchToDelete?.branchName}
          descriptionText="Updates on this branch will no longer be reachable from the dashboard. This cannot be undone."
          confirmButtonText="Delete branch"
          isDeletingButtonText="Deleting..."
        />
      )}
      <Dialog
        open={!!branchToProtect}
        onOpenChange={open => !open && !protectionMutation.isPending && setBranchToProtect(null)}>
        <DialogContent className="sm:max-w-[420px]">
          <DialogHeader className="flex flex-col items-start gap-2">
            <div className="flex h-9 w-9 items-center justify-center rounded-full border border-emerald-200 bg-emerald-50 text-emerald-600">
              <ShieldAlert className="h-5 w-5" />
            </div>
            <DialogTitle className="mt-2 text-lg font-semibold tracking-tight">
              Protect this branch?
            </DialogTitle>
            <DialogDescription className="pt-1 text-left text-muted-foreground">
              Once{' '}
              <strong className="font-semibold text-foreground">
                "{branchToProtect?.branchName}"
              </strong>{' '}
              is protected, it cannot be deleted, by you or by anyone else, until the protection is
              lifted. It changes nothing about what is published on it: which tokens may publish
              there is decided by their own access rules, on the API tokens page.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="mt-4 gap-2 border-t pt-3 sm:gap-0">
            <Button
              type="button"
              variant="outline"
              onClick={() => setBranchToProtect(null)}
              disabled={protectionMutation.isPending}>
              Cancel
            </Button>
            <Button
              type="button"
              onClick={() => branchToProtect && void applyProtection(branchToProtect, true)}
              disabled={protectionMutation.isPending}
              className="bg-emerald-600 text-white hover:bg-emerald-700">
              {protectionMutation.isPending ? 'Protecting...' : 'Protect branch'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <EnterpriseExplainerDialog
        open={isExplainerOpen}
        onOpenChange={setIsExplainerOpen}
        feature={{
          name: 'Branch protection',
          description:
            'Protect critical branches like production so they cannot be deleted, by anyone, until the protection is lifted. Restricting who may publish on a branch is a separate feature: it is decided per API token, on the API tokens page.',
        }}
      />
    </div>
  );
};
