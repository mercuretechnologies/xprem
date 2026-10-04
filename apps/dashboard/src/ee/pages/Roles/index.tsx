// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Pencil, Trash2 } from 'lucide-react';
import { api, ApiProblemError, RoleRecord } from '@/lib/api';
import { useSettings } from '@/lib/SettingsContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { PageHeader } from '@/components/PageHeader';
import { ApiError } from '@/components/APIError';
import { DataTable } from '@/components/DataTable';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { useToast } from '@/hooks/use-toast';
import { EnterpriseFeatureGate } from '@/ee/components/EnterpriseFeatureGate';
import { rolesFeature } from '@/ee/lib/enterpriseFeatures';
import { RoleFormDialog } from '@/ee/components/RoleFormDialog';
import { PERMISSION_GROUPS } from '@/ee/lib/permissionCatalog';

const PERMISSION_LABELS = new Map(
  PERMISSION_GROUPS.flatMap(group =>
    group.permissions.map(permission => [permission.value as string, permission.label])
  )
);

// The Roles page of the Access & Security sidebar group: named permission
// bundles for enterprise user roles. Follows the SSO page conventions: the
// content stays visible behind the frosted enterprise gate without a license.
export const Roles = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { isAdmin } = useCurrentUser();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [formState, setFormState] = useState<{ open: boolean; role: RoleRecord | null }>({
    open: false,
    role: null,
  });
  const [roleToDelete, setRoleToDelete] = useState<RoleRecord | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);

  const rolesQuery = useQuery({
    queryKey: ['roles'],
    queryFn: () => api.getRoles(),
    enabled: CONTROL_PLANE_ENABLED && isAdmin,
  });

  const refreshRoles = () => queryClient.invalidateQueries({ queryKey: ['roles'] });

  const handleExecuteDeletion = async () => {
    if (!roleToDelete) return;
    setIsDeleting(true);
    try {
      await api.deleteRole(roleToDelete.id);
      toast({ title: 'Role deleted', description: `"${roleToDelete.name}" was removed.` });
      setRoleToDelete(null);
      refreshRoles();
    } catch (error) {
      // The server refuses deleting a role that is still assigned (409) and
      // says so; surface its message.
      let errorMessage = 'Could not delete the role.';
      if (error instanceof ApiProblemError) {
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({ title: 'Deletion failed', description: errorMessage, variant: 'destructive' });
    } finally {
      setIsDeleting(false);
    }
  };

  if (!CONTROL_PLANE_ENABLED) {
    return (
      <div className="w-full">
        <PageHeader title="Roles" description="Reusable permission sets for your team." />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          User roles live in the database, so they require control-plane (DB) mode. Stateless
          deployments have a single admin account.
        </div>
      </div>
    );
  }

  if (!isAdmin) {
    return (
      <div className="w-full">
        <PageHeader title="Roles" description="Reusable permission sets for your team." />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Only admins can manage roles. Ask an admin if you need access.
        </div>
      </div>
    );
  }

  return (
    <div className="w-full">
      <PageHeader
        title="Roles"
        description="A role is a reusable set of permissions, like Release manager. Assign one to a user on an app from the Users page; admins bypass roles entirely."
      />
      <EnterpriseFeatureGate feature={rolesFeature}>
        <div className="space-y-4">
          {!!rolesQuery.error && <ApiError error={rolesQuery.error} />}

          <div className="flex justify-end">
            <Button size="sm" onClick={() => setFormState({ open: true, role: null })}>
              <Plus className="h-4 w-4" /> New role
            </Button>
          </div>

          <DataTable
            loading={rolesQuery.isLoading}
            columns={[
              {
                header: 'Name',
                accessorKey: 'name',
                cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
              },
              {
                header: 'Permissions',
                id: 'permissions',
                cell: ({ row }) => {
                  const permissions = row.original.permissions;
                  if (permissions.length === 0) {
                    return <span className="text-muted-foreground/60">None (read-only)</span>;
                  }
                  return (
                    <div className="flex max-w-md flex-wrap gap-1">
                      {permissions.map(permission => (
                        <Badge key={permission} variant="outline" className="font-normal">
                          {PERMISSION_LABELS.get(permission) ?? permission}
                        </Badge>
                      ))}
                    </div>
                  );
                },
              },
              {
                header: 'Created',
                accessorKey: 'createdAt',
                cell: ({ row }) => <TimestampCell dateString={row.original.createdAt ?? ''} />,
              },
              {
                header: '',
                id: 'actions',
                cell: ({ row }) => (
                  <div className="flex justify-end gap-1 pr-2">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setFormState({ open: true, role: row.original })}
                      className="h-8 w-8 p-0 text-muted-foreground hover:text-foreground"
                      title="Edit role">
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setRoleToDelete(row.original)}
                      className="h-8 w-8 p-0 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                      title="Delete role">
                      <Trash2 />
                    </Button>
                  </div>
                ),
              },
            ]}
            data={rolesQuery.data ?? []}
            emptyMessage="No roles yet. Create one, like Release manager, then assign it from the Users page."
          />
        </div>
      </EnterpriseFeatureGate>

      <RoleFormDialog
        open={formState.open}
        role={formState.role}
        onClose={() => setFormState({ open: false, role: null })}
        onSaved={refreshRoles}
      />

      <DeleteDialog
        isOpen={!!roleToDelete}
        onClose={() => setRoleToDelete(null)}
        onConfirm={handleExecuteDeletion}
        isDeleting={isDeleting}
        title="Delete role"
        resourceName={roleToDelete?.name}
        descriptionText="Users holding this role through a grant keep their other permissions. A role still assigned to someone cannot be deleted."
        confirmButtonText="Delete role"
        isDeletingButtonText="Deleting…"
      />
    </div>
  );
};
