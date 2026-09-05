import { useState } from 'react';
import { ApiError } from '@/components/APIError';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { Plus, Trash2 } from 'lucide-react';
import { api, describeApiError, EnvironmentRecord } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { PageHeader } from '@/components/PageHeader';
import { DataTable } from '@/components/DataTable';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { AdminOnlyNote } from '@/components/ui/admin-only-note';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { CreateEnvironmentDialog } from './CreateEnvironmentDialog';
import { ChannelBadges } from './ChannelBadges';
import { useChannelsByEnvironment } from './useChannelsByEnvironment';

export const Environments = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  // Display gating only: the server re-checks the permission on its routes.
  const canManage = useAppPermission('env:manage', 'admin-only');

  const [isCreateDialogOpen, setIsCreateDialogOpen] = useState(false);
  const [environmentToDelete, setEnvironmentToDelete] = useState<EnvironmentRecord | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);

  const environmentsQuery = useQuery({
    queryKey: ['environments', selectedAppId],
    queryFn: () => api.getEnvironments(),
    enabled: !!selectedAppId && CONTROL_PLANE_ENABLED,
  });
  const { channelsByEnvironment } = useChannelsByEnvironment();

  const handleDelete = async () => {
    if (!environmentToDelete) return;
    setIsDeleting(true);
    try {
      await api.deleteEnvironment(environmentToDelete.name);
      await queryClient.invalidateQueries({ queryKey: ['environments', selectedAppId] });
      toast({
        title: 'Environment deleted',
        description: `"${environmentToDelete.name}" and its variables were removed.`,
      });
      setEnvironmentToDelete(null);
    } catch (error) {
      const message = describeApiError(error, 'Could not delete environment');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsDeleting(false);
    }
  };

  if (!CONTROL_PLANE_ENABLED) {
    return (
      <div className="w-full">
        <PageHeader
          title="Environments"
          description="Define sets of variables per environment, used when building and publishing updates."
        />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Environments need a database to be stored in, so they are not available on a stateless
          deployment.
        </div>
      </div>
    );
  }

  if (environmentsQuery.isError) {
    return <ApiError error={environmentsQuery.error} onRetry={() => void environmentsQuery.refetch()} />;
  }

  return (
    <div className="w-full">
      <PageHeader
        title="Environments"
        description="Define sets of variables per environment, used when building and publishing updates."
        actions={
          canManage ? (
            <Button onClick={() => setIsCreateDialogOpen(true)}>
              <Plus className="h-4 w-4" /> New environment
            </Button>
          ) : undefined
        }
      />

      <div className="space-y-4">
        {!canManage && (
          <AdminOnlyNote>
            You do not have permission to manage this app's environments. Ask an admin to grant you
            access.
          </AdminOnlyNote>
        )}

        <DataTable
          loading={environmentsQuery.isLoading}
          onRowClick={record => navigate(`/environments/${encodeURIComponent(record.name)}`)}
          columns={[
            {
              header: 'Environment',
              accessorKey: 'name',
              cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
            },
            {
              header: 'Variables',
              id: 'vars',
              cell: ({ row }: { row: { original: EnvironmentRecord } }) => (
                <span className="text-muted-foreground">{row.original.vars.length}</span>
              ),
            },
            {
              header: 'Channels',
              id: 'channels',
              cell: ({ row }: { row: { original: EnvironmentRecord } }) => (
                <ChannelBadges channelNames={channelsByEnvironment.get(row.original.name) ?? []} />
              ),
            },
            {
              header: 'Created',
              accessorKey: 'createdAt',
              cell: ({ row }) => <TimestampCell dateString={row.original.createdAt} />,
            },
            ...(canManage
              ? [
                  {
                    header: '',
                    id: 'actions',
                    cell: ({ row }: { row: { original: EnvironmentRecord } }) => (
                      <div className="text-right">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={e => {
                            e.stopPropagation();
                            setEnvironmentToDelete(row.original);
                          }}
                          className="h-8 w-8 p-0 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                          title="Delete environment">
                          <Trash2 />
                        </Button>
                      </div>
                    ),
                  },
                ]
              : []),
          ]}
          data={environmentsQuery.data ?? []}
          emptyMessage="No environment yet. Create one, add its variables, then point channels at it."
        />
      </div>

      <CreateEnvironmentDialog
        isOpen={isCreateDialogOpen}
        onClose={() => setIsCreateDialogOpen(false)}
      />

      <DeleteDialog
        isOpen={!!environmentToDelete}
        onClose={() => setEnvironmentToDelete(null)}
        onConfirm={handleDelete}
        isDeleting={isDeleting}
        title="Delete environment"
        resourceName={environmentToDelete?.name}
        descriptionText="The environment and all of its variables will be permanently removed. Channels pointing to it must be detached first. This cannot be undone."
        confirmButtonText="Delete environment"
        isDeletingButtonText="Deleting…"
      />
    </div>
  );
};
