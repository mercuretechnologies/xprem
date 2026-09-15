import { useState } from 'react';
import { ApiError } from '@/components/APIError';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router';
import { Trash2 } from 'lucide-react';
import { api, ApiProblemError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { AdminOnlyNote } from '@/components/ui/admin-only-note';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { AndroidCredentialsSection } from './components/AndroidCredentialsSection';
import { BuildNumberCard } from './components/BuildNumberCard';
import { IosCredentialsSection } from './components/IosCredentialsSection';
import { PlatformLogo } from './components/PlatformLogo';
import { platformLabel } from './platforms';

export const AppIdentifierDetail = () => {
  const { identifierId } = useParams<{ identifierId: string }>();
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  // Display gating only: the server re-checks the permission on its routes.
  const canManage = useAppPermission('credentials:manage', 'admin-only');

  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  const identifiersQuery = useQuery({
    queryKey: ['identifiers', selectedAppId],
    queryFn: () => api.getAppIdentifiers(),
    enabled: !!selectedAppId,
  });

  const identifier = identifiersQuery.data?.find(record => record.id === identifierId);

  const handleDeleteIdentifier = async () => {
    if (!identifier) return;
    setIsDeleting(true);
    try {
      await api.deleteAppIdentifier(identifier.id);
      queryClient.invalidateQueries({ queryKey: ['identifiers', selectedAppId] });
      toast({
        title: 'Configuration deleted',
        description: `"${identifier.identifier}" and its credentials were removed.`,
      });
      navigate('/build-credentials');
    } catch (error) {
      let errorTitle = 'Error deleting configuration';
      let errorMessage = 'An unexpected error occurred.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({ title: errorTitle, description: errorMessage, variant: 'destructive' });
      setIsDeleting(false);
      setIsDeleteDialogOpen(false);
    }
  };

  if (identifiersQuery.isPending) {
    return (
      <div className="w-full space-y-4">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-28 w-full rounded-xl" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
    );
  }

  if (identifiersQuery.isError) {
    return (
      <ApiError error={identifiersQuery.error} onRetry={() => void identifiersQuery.refetch()} />
    );
  }

  if (!identifier) {
    return (
      <div className="w-full">
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          This application identifier does not exist or was deleted.
          <div className="mt-4">
            <Button variant="outline" onClick={() => navigate('/build-credentials')}>
              Back to build credentials
            </Button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="w-full space-y-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink
              className="cursor-pointer"
              onClick={() => navigate('/build-credentials')}>
              Build credentials
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage className="flex items-center gap-1.5">
              <PlatformLogo platform={identifier.platform} className="h-3.5 w-3.5" />
              {identifier.identifier}
            </BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <Card>
        <CardContent className="flex items-center justify-between gap-4 py-5">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg border bg-muted/40">
              <PlatformLogo platform={identifier.platform} className="h-5 w-5" />
            </div>
            <div>
              <div className="flex items-center gap-2">
                <span className="font-display text-lg font-semibold tracking-tight">
                  {identifier.identifier}
                </span>
                <Badge variant="outline">{platformLabel(identifier.platform)}</Badge>
              </div>
              <p className="text-sm text-muted-foreground">Application identifier</p>
            </div>
          </div>
          {canManage && (
            <Button
              variant="outline"
              onClick={() => setIsDeleteDialogOpen(true)}
              className="border-destructive/30 text-destructive hover:bg-destructive/10 hover:text-destructive">
              <Trash2 className="h-4 w-4" /> Delete configuration
            </Button>
          )}
        </CardContent>
      </Card>

      {!canManage && (
        <AdminOnlyNote>
          You do not have permission to manage this app's build credentials. Ask an admin to grant
          you access.
        </AdminOnlyNote>
      )}

      <BuildNumberCard key={identifier.id} identifier={identifier} canManage={canManage} />

      {identifier.platform === 'android' ? (
        <AndroidCredentialsSection identifier={identifier} canManage={canManage} />
      ) : (
        <IosCredentialsSection
          key={`ios-${identifier.id}`}
          identifier={identifier}
          canManage={canManage}
        />
      )}

      <DeleteDialog
        isOpen={isDeleteDialogOpen}
        onClose={() => setIsDeleteDialogOpen(false)}
        onConfirm={handleDeleteIdentifier}
        isDeleting={isDeleting}
        title="Delete configuration"
        resourceName={identifier.identifier}
        descriptionText="The application identifier and all of its build credentials will be permanently removed. This cannot be undone."
        confirmButtonText="Delete configuration"
        isDeletingButtonText="Deleting…"
      />
    </div>
  );
};
