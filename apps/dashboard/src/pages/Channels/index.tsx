import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, Compass, Copy, GitBranch, Plus, Search, Split, Trash2 } from 'lucide-react';
import { Link, useNavigate, useParams } from 'react-router';
import { api, ChannelRecord, describeApiError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { useToast } from '@/hooks/use-toast';
import { useBranchCurrentStatus } from '@/hooks/use-branch-current-status';
import { toBranchStatus } from '@/lib/branch-status';
import { PageHeader } from '@/components/PageHeader';
import { ApiError } from '@/components/APIError';
import { DataTable } from '@/components/DataTable';
import { ChannelBranchMapping } from '@/components/ChannelBranchMapping';
import { SelectBranch } from '@/pages/Channels/components/SelectBranch';
import { StartRolloutDialog } from '@/pages/Channels/components/StartRolloutDialog';
import { ManageRolloutDialog } from '@/pages/Channels/components/ManageRolloutDialog';
import { BranchSurfingCard } from '@/pages/Channels/components/BranchSurfingCard';
import { RolloutBar } from '@/components/rollout/RolloutBar';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { DeleteDialog } from '@/components/ui/delete-dialog';

const CreateChannelDialog = ({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => Promise<void>;
}) => {
  const { toast } = useToast();
  const [name, setName] = useState('');
  const [branch, setBranch] = useState<{ id: string; name: string } | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const close = () => {
    setName('');
    setBranch(null);
    onClose();
  };
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    const channelName = name.trim();
    if (!channelName) return;
    setSubmitting(true);
    try {
      await api.createChannel({
        channelName,
        ...(branch && { branchName: branch.name }),
      });
      await onCreated();
      toast({ title: 'Channel created', description: `"${channelName}" is ready.` });
      close();
    } catch (error) {
      const message = describeApiError(error, 'Could not create channel');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={next => !next && close()}>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Create channel</DialogTitle>
            <DialogDescription>
              Create the channel, then map it to the branch that should serve updates.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-5">
            <div className="space-y-1.5">
              <Label htmlFor="channel-name">Channel name</Label>
              <Input
                id="channel-name"
                value={name}
                onChange={event => setName(event.target.value)}
                placeholder="production"
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label>Branch (optional)</Label>
              <SelectBranch
                className="w-full"
                currentBranch={branch?.id ?? ''}
                disabled={submitting}
                onChange={(id, branchName) =>
                  setBranch(id && branchName ? { id, name: branchName } : null)
                }
              />
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || !name.trim()}>
              {submitting ? 'Creating...' : 'Create channel'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
};

export const Channels = () => {
  const { channelName } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const { selectedAppId } = useSelectedApp();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const canCreateChannel = useAppPermission('channel:create', 'admin-only');
  const canDeleteChannel = useAppPermission('channel:delete', 'admin-only');
  const canEditChannelBranch = useAppPermission('channel:edit-branch', 'admin-only');
  const canManageRollout = useAppPermission('channel-rollout:manage', 'admin-only');
  const canManageBranchSurfing = useAppPermission('channel:branch-surfing', 'admin-only');
  const [search, setSearch] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [editingMapping, setEditingMapping] = useState(false);
  const [channelToDelete, setChannelToDelete] = useState<ChannelRecord | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [rolloutAction, setRolloutAction] = useState<'start' | 'manage' | null>(null);

  const channelsQuery = useQuery({
    queryKey: ['channels', selectedAppId],
    queryFn: () => api.getChannels(),
    enabled: !!selectedAppId,
  });
  const selectedChannel = channelName
    ? channelsQuery.data?.find(
        channel => channel.releaseChannelName === decodeURIComponent(channelName)
      )
    : undefined;
  const statelessBranchStatus = useBranchCurrentStatus(
    CONTROL_PLANE_ENABLED ? undefined : selectedChannel?.branchName
  );
  const branchStatus = CONTROL_PLANE_ENABLED
    ? toBranchStatus(selectedChannel?.branchCurrentUpdate)
    : statelessBranchStatus;
  const rolloutBranchStatus = toBranchStatus(selectedChannel?.rolloutBranchCurrentUpdate);

  const mappingMutation = useMutation({
    mutationFn: ({ branchId, channel }: { branchId: string; channel: ChannelRecord }) =>
      api.updateChannelBranchMapping(branchId, {
        releaseChannelId: channel.releaseChannelId,
        releaseChannelName: channel.releaseChannelName,
      }),
  });

  const remap = async (branchId?: string | null) => {
    if (!selectedChannel || !branchId) return;
    try {
      await mappingMutation.mutateAsync({ branchId, channel: selectedChannel });
      await queryClient.invalidateQueries({ queryKey: ['channels', selectedAppId] });
      setEditingMapping(false);
      toast({
        title: 'Channel updated',
        description: `"${selectedChannel.releaseChannelName}" now uses the selected branch.`,
      });
    } catch (error) {
      const message = describeApiError(error, 'Could not update channel mapping');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    }
  };

  const deleteChannel = async () => {
    if (!channelToDelete) return;
    setDeleting(true);
    try {
      await api.deleteChannel(channelToDelete.releaseChannelName);
      await queryClient.invalidateQueries({ queryKey: ['channels', selectedAppId] });
      if (selectedChannel?.releaseChannelId === channelToDelete.releaseChannelId) {
        navigate('/channels');
      }
      toast({
        title: 'Channel deleted',
        description: `"${channelToDelete.releaseChannelName}" was removed.`,
      });
      setChannelToDelete(null);
    } catch (error) {
      const message = describeApiError(error, 'Could not delete channel');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setDeleting(false);
    }
  };

  const filteredChannels = useMemo(() => {
    const normalized = search.trim().toLowerCase();
    return (channelsQuery.data ?? []).filter(
      channel =>
        !normalized ||
        channel.releaseChannelName.toLowerCase().includes(normalized) ||
        channel.branchName?.toLowerCase().includes(normalized)
    );
  }, [channelsQuery.data, search]);

  const columns = useMemo(
    () => [
      {
        header: 'Channel',
        accessorKey: 'releaseChannelName',
        cell: ({ row }: { row: { original: ChannelRecord } }) => {
          const surfing = row.original.branchSurfing;
          return (
            <div className="flex items-center gap-2">
              <span className="font-medium">{row.original.releaseChannelName}</span>
              {surfing?.enabled && (
                <Badge
                  variant="outline"
                  // Amber on "*" echoes the detail page: the channel exposes
                  // every branch, which is the setting worth noticing at a
                  // glance.
                  className={
                    surfing.pattern === '*'
                      ? 'border-amber-400/25 bg-amber-400/10 text-amber-700 dark:text-amber-300'
                      : 'border-sky-400/25 bg-sky-400/10 text-sky-700 dark:text-sky-300'
                  }
                  title={`Devices on this channel can surf to branches matching ${surfing.pattern}`}>
                  <Compass className="h-3 w-3" />
                  <span className="font-mono">{surfing.pattern}</span>
                </Badge>
              )}
            </div>
          );
        },
      },
      {
        header: 'Status',
        id: 'status',
        cell: ({ row }: { row: { original: ChannelRecord } }) => (
          <Badge
            variant="outline"
            className={
              row.original.branchName
                ? 'border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300'
                : 'border-red-400/25 bg-red-400/10 text-red-700 dark:text-red-300'
            }>
            {row.original.branchName ? 'Active' : 'Unmapped'}
          </Badge>
        ),
      },
      {
        header: 'Linked branch',
        accessorKey: 'branchName',
        cell: ({ row }: { row: { original: ChannelRecord } }) =>
          row.original.branchName ? (
            <button
              type="button"
              className="inline-flex items-center gap-1.5 font-medium hover:text-link"
              onClick={event => {
                event.stopPropagation();
                navigate(`/branches/${encodeURIComponent(row.original.branchName as string)}`);
              }}>
              <GitBranch className="h-4 w-4 text-muted-foreground" />
              {row.original.branchName}
            </button>
          ) : (
            <span className="text-muted-foreground/60">None</span>
          ),
      },
      ...(CONTROL_PLANE_ENABLED
        ? [
            {
              header: 'Rollout',
              id: 'rollout',
              cell: ({ row }: { row: { original: ChannelRecord } }) =>
                row.original.rollout ? (
                  <div className="flex items-center gap-2">
                    <RolloutBar value={row.original.rollout.percentage} />
                    <span className="text-xs text-muted-foreground">
                      to {row.original.rollout.rolloutBranchName}
                    </span>
                  </div>
                ) : (
                  <span className="text-muted-foreground/60">None</span>
                ),
            },
          ]
        : []),
      {
        header: 'ID',
        accessorKey: 'releaseChannelId',
        cell: ({ row }: { row: { original: ChannelRecord } }) => (
          <div className="flex items-center gap-1">
            <code className="max-w-32 truncate font-mono text-xs text-muted-foreground">
              {row.original.releaseChannelId}
            </code>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              title="Copy channel ID"
              onClick={event => {
                event.stopPropagation();
                void navigator.clipboard.writeText(row.original.releaseChannelId);
              }}>
              <Copy className="h-3.5 w-3.5" />
            </Button>
          </div>
        ),
      },
      ...(CONTROL_PLANE_ENABLED && canDeleteChannel
        ? [
            {
              header: '',
              id: 'actions',
              cell: ({ row }: { row: { original: ChannelRecord } }) => (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  title="Delete channel"
                  onClick={event => {
                    event.stopPropagation();
                    setChannelToDelete(row.original);
                  }}>
                  <Trash2 className="h-4 w-4" />
                </Button>
              ),
            },
          ]
        : []),
    ],
    [CONTROL_PLANE_ENABLED, canDeleteChannel, navigate]
  );

  const deleteDialog = CONTROL_PLANE_ENABLED && canDeleteChannel && (
    <DeleteDialog
      isOpen={!!channelToDelete}
      onClose={() => setChannelToDelete(null)}
      onConfirm={deleteChannel}
      isDeleting={deleting}
      title="Delete channel"
      resourceName={channelToDelete?.releaseChannelName}
      descriptionText="Builds configured with this channel will stop receiving updates. This cannot be undone."
      confirmButtonText="Delete channel"
      isDeletingButtonText="Deleting..."
    />
  );

  if (channelName) {
    const decodedChannel = decodeURIComponent(channelName);
    return (
      <div className="w-full">
        <Link
          to="/channels"
          className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-4 w-4" />
          All channels
        </Link>
        <PageHeader
          title={decodedChannel}
          actions={
            CONTROL_PLANE_ENABLED && canDeleteChannel && selectedChannel ? (
              <Button variant="outline" onClick={() => setChannelToDelete(selectedChannel)}>
                <Trash2 className="h-4 w-4" />
                Delete
              </Button>
            ) : undefined
          }
        />
        {!!channelsQuery.error && <ApiError error={channelsQuery.error} />}
        {!channelsQuery.isLoading && !selectedChannel && !channelsQuery.error && (
          <div className="rounded-lg border border-dashed p-10 text-center text-muted-foreground">
            Channel not found.
          </div>
        )}
        {selectedChannel && (
          <div className="space-y-6">
            <ChannelBranchMapping
              branchName={selectedChannel.branchName}
              channelNames={[selectedChannel.releaseChannelName]}
              branchStatus={branchStatus}
              rolloutStatus={rolloutBranchStatus}
              rollout={
                selectedChannel.rollout
                  ? {
                      branchName: selectedChannel.rollout.rolloutBranchName,
                      percentage: selectedChannel.rollout.percentage,
                    }
                  : undefined
              }
              onEdit={
                canEditChannelBranch && !selectedChannel.rollout
                  ? () => setEditingMapping(true)
                  : undefined
              }
            />
            {editingMapping && (
              <section className="flex flex-col gap-3 rounded-lg border p-4 sm:flex-row sm:items-end">
                <div className="min-w-0 flex-1 space-y-1.5">
                  <Label>Branch</Label>
                  <SelectBranch
                    className="w-full"
                    currentBranch={selectedChannel.branchId ?? ''}
                    loading={mappingMutation.isPending}
                    disabled={mappingMutation.isPending}
                    onChange={branchId => void remap(branchId)}
                  />
                </div>
                <Button variant="ghost" onClick={() => setEditingMapping(false)}>
                  Cancel
                </Button>
              </section>
            )}
            {CONTROL_PLANE_ENABLED && (
              <section className="rounded-lg border p-5">
                <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
                  <div>
                    <h2 className="text-sm font-semibold">Progressive rollout</h2>
                    {selectedChannel.rollout ? (
                      <div className="mt-2 flex items-center gap-3">
                        <RolloutBar value={selectedChannel.rollout.percentage} />
                        <span className="text-sm text-muted-foreground">
                          to {selectedChannel.rollout.rolloutBranchName}
                        </span>
                      </div>
                    ) : (
                      <p className="mt-1 text-sm text-muted-foreground">No active rollout</p>
                    )}
                  </div>
                  {canManageRollout && selectedChannel.branchId && (
                    <Button
                      variant={selectedChannel.rollout ? 'outline' : 'default'}
                      onClick={() =>
                        setRolloutAction(selectedChannel.rollout ? 'manage' : 'start')
                      }>
                      <Split className="h-4 w-4" />
                      {selectedChannel.rollout ? 'Manage rollout' : 'Start rollout'}
                    </Button>
                  )}
                </div>
              </section>
            )}
            {CONTROL_PLANE_ENABLED && (
              <BranchSurfingCard
                channel={selectedChannel}
                canManage={canManageBranchSurfing}
                onUpdated={async () => {
                  await queryClient.invalidateQueries({ queryKey: ['channels', selectedAppId] });
                }}
              />
            )}
          </div>
        )}
        {selectedChannel && canManageRollout && (
          <>
            <StartRolloutDialog
              channel={rolloutAction === 'start' ? selectedChannel : null}
              onClose={() => setRolloutAction(null)}
              onStarted={async () => {
                await channelsQuery.refetch();
              }}
            />
            <ManageRolloutDialog
              channel={rolloutAction === 'manage' ? selectedChannel : null}
              onClose={() => setRolloutAction(null)}
              onDone={async () => {
                await channelsQuery.refetch();
              }}
            />
          </>
        )}
        {deleteDialog}
      </div>
    );
  }

  return (
    <div className="w-full">
      <PageHeader
        title="Channels"
        actions={
          CONTROL_PLANE_ENABLED && canCreateChannel ? (
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="h-4 w-4" />
              Create channel
            </Button>
          ) : undefined
        }
      />
      {!!channelsQuery.error && <ApiError error={channelsQuery.error} />}
      <div className="mb-4 max-w-sm">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            aria-label="Search channels"
            placeholder="Search channels"
            value={search}
            onChange={event => setSearch(event.target.value)}
            className="pl-9"
          />
        </div>
      </div>
      <DataTable
        loading={channelsQuery.isLoading}
        columns={columns}
        data={filteredChannels}
        emptyMessage={search ? 'No channels match this search.' : 'No channels yet.'}
        onRowClick={channel =>
          navigate(`/channels/${encodeURIComponent(channel.releaseChannelName)}`)
        }
      />
      {CONTROL_PLANE_ENABLED && canCreateChannel && (
        <CreateChannelDialog
          open={createOpen}
          onClose={() => setCreateOpen(false)}
          onCreated={async () => {
            await queryClient.invalidateQueries({ queryKey: ['channels', selectedAppId] });
          }}
        />
      )}
      {deleteDialog}
    </div>
  );
};
