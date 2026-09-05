import { useState } from 'react';
import { ApiError } from '@/components/APIError';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { AlertTriangle, CheckCircle2, Plus, Trash2 } from 'lucide-react';
import { api, ApiProblemError, AppIdentifier } from '@/lib/api';
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
import { CreateIdentifierDialog } from './CreateIdentifierDialog';
import { PlatformLogo } from './PlatformLogo';
import { PLATFORMS, isCredentialsConfigured } from './platforms';

const CredentialsStatusBadge = ({ identifier }: { identifier: AppIdentifier }) => {
  if (isCredentialsConfigured(identifier)) {
    return (
      <span className="inline-flex items-center gap-1.5 text-sm font-medium text-emerald-700 dark:text-emerald-300">
        <CheckCircle2 className="h-3.5 w-3.5" /> Configured
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-sm font-medium text-amber-700 dark:text-amber-400">
      <AlertTriangle className="h-3.5 w-3.5" /> Not configured
    </span>
  );
};

export const BuildCredentials = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  // Display gating only: the server re-checks the permission on its routes.
  const canManage = useAppPermission('credentials:manage', 'admin-only');

  const [isCreateDialogOpen, setIsCreateDialogOpen] = useState(false);
  const [identifierToDelete, setIdentifierToDelete] = useState<AppIdentifier | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);

  const identifiersQuery = useQuery({
    queryKey: ['identifiers', selectedAppId],
    queryFn: () => api.getAppIdentifiers(),
    enabled: !!selectedAppId && CONTROL_PLANE_ENABLED,
  });

  const handleDeleteIdentifier = async () => {
    if (!identifierToDelete) return;
    setIsDeleting(true);
    try {
      await api.deleteAppIdentifier(identifierToDelete.id);
      queryClient.invalidateQueries({ queryKey: ['identifiers', selectedAppId] });
      toast({
        title: 'Identifier deleted',
        description: `"${identifierToDelete.identifier}" and its credentials were removed.`,
      });
      setIdentifierToDelete(null);
    } catch (error) {
      let errorTitle = 'Error deleting identifier';
      let errorMessage = 'An unexpected error occurred.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({ title: errorTitle, description: errorMessage, variant: 'destructive' });
    } finally {
      setIsDeleting(false);
    }
  };

  if (!CONTROL_PLANE_ENABLED) {
    return (
      <div className="w-full">
        <PageHeader
          title="Build credentials"
          description="Signing credentials used to build your app in the cloud."
        />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Build credentials need a database to be stored in, so they are not available on a
          stateless deployment.
        </div>
      </div>
    );
  }

  if (identifiersQuery.isError) {
    return <ApiError error={identifiersQuery.error} onRetry={() => void identifiersQuery.refetch()} />;
  }

  return (
    <div className="w-full">
      <PageHeader
        title="Build credentials"
        description="Application identifiers and the signing credentials used to build your app in the cloud."
        actions={
          canManage ? (
            <Button onClick={() => setIsCreateDialogOpen(true)}>
              <Plus className="h-4 w-4" /> New application identifier
            </Button>
          ) : undefined
        }
      />

      <div className="space-y-8">
        {!canManage && (
          <AdminOnlyNote>
            You do not have permission to manage this app's build credentials. Ask an admin to
            grant you access.
          </AdminOnlyNote>
        )}

        {PLATFORMS.map(section => {
          const identifiers = (identifiersQuery.data ?? []).filter(
            record => record.platform === section.platform
          );
          return (
            <section key={section.platform} className="space-y-3">
              <div className="flex items-center gap-2">
                <PlatformLogo platform={section.platform} className="h-4 w-4" />
                <h2 className="font-display text-lg font-semibold tracking-tight">
                  {section.label}
                </h2>
              </div>
              {section.enabled ? (
                <DataTable
                  loading={identifiersQuery.isLoading}
                  onRowClick={record => navigate(`/build-credentials/${record.id}`)}
                  columns={[
                    {
                      header: 'Identifier',
                      accessorKey: 'identifier',
                      cell: ({ row }) => (
                        <span className="font-medium">{row.original.identifier}</span>
                      ),
                    },
                    {
                      header: 'Credentials',
                      id: 'credentials',
                      cell: ({ row }: { row: { original: AppIdentifier } }) => (
                        <CredentialsStatusBadge identifier={row.original} />
                      ),
                    },
                    {
                      header: 'Build number',
                      accessorKey: 'buildNumber',
                      cell: ({ row }) => (
                        <span className="font-mono text-xs text-muted-foreground">
                          {row.original.buildNumber}
                        </span>
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
                            cell: ({ row }: { row: { original: AppIdentifier } }) => (
                              <div className="text-right">
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  onClick={e => {
                                    e.stopPropagation();
                                    setIdentifierToDelete(row.original);
                                  }}
                                  className="h-8 w-8 p-0 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                                  title="Delete identifier">
                                  <Trash2 />
                                </Button>
                              </div>
                            ),
                          },
                        ]
                      : []),
                  ]}
                  data={identifiers}
                  emptyMessage={`No ${section.label} application identifier yet. Create one to set up its build credentials.`}
                />
              ) : (
                <div className="rounded-xl border border-dashed bg-muted/30 p-6 text-center text-sm text-muted-foreground">
                  {section.disabledNote}
                </div>
              )}
            </section>
          );
        })}
      </div>

      <CreateIdentifierDialog
        isOpen={isCreateDialogOpen}
        onClose={() => setIsCreateDialogOpen(false)}
      />

      <DeleteDialog
        isOpen={!!identifierToDelete}
        onClose={() => setIdentifierToDelete(null)}
        onConfirm={handleDeleteIdentifier}
        isDeleting={isDeleting}
        title="Delete identifier"
        resourceName={identifierToDelete?.identifier}
        descriptionText="The application identifier and all of its build credentials will be permanently removed. This cannot be undone."
        confirmButtonText="Delete identifier"
        isDeletingButtonText="Deleting…"
      />
    </div>
  );
};
