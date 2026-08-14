import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router';
import { CheckCircle2, Pencil, Trash2 } from 'lucide-react';
import { api, ApiProblemError, AndroidCredentialsMetadata, AppIdentifier } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { AdminOnlyNote } from '@/components/ui/admin-only-note';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { AndroidCredentialsForm } from './AndroidCredentialsForm';
import { PlatformLogo } from './PlatformLogo';
import { platformLabel } from './platforms';

const MetadataRow = ({ label, value }: { label: string; value: React.ReactNode }) => (
  <div className="flex items-center justify-between gap-4 py-2.5">
    <span className="text-sm text-muted-foreground">{label}</span>
    <span className="text-sm font-medium">{value}</span>
  </div>
);

const AndroidCredentialsSection = ({
  identifier,
  canManage,
}: {
  identifier: AppIdentifier;
  canManage: boolean;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [isReplacing, setIsReplacing] = useState(false);
  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  const credentialsQuery = useQuery({
    queryKey: ['androidCredentials', selectedAppId, identifier.id],
    queryFn: () => api.getAndroidCredentials(identifier.id),
    enabled: !!selectedAppId,
  });

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['identifiers', selectedAppId] });
    queryClient.invalidateQueries({
      queryKey: ['androidCredentials', selectedAppId, identifier.id],
    });
  };

  const handleDeleteCredentials = async () => {
    setIsDeleting(true);
    try {
      await api.deleteAndroidCredentials(identifier.id);
      invalidate();
      toast({
        title: 'Credentials deleted',
        description: `Builds for "${identifier.identifier}" can no longer be signed until new credentials are set up.`,
      });
      setIsDeleteDialogOpen(false);
    } catch (error) {
      let errorTitle = 'Error deleting credentials';
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

  if (credentialsQuery.isLoading) {
    return <Skeleton className="h-48 w-full rounded-xl" />;
  }

  const metadata: AndroidCredentialsMetadata | null | undefined = credentialsQuery.data;

  if (!metadata && !canManage) {
    return (
      <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
        No build credentials are configured for this identifier yet. Ask an admin to set them up.
      </div>
    );
  }

  if (!metadata || isReplacing) {
    return (
      <AndroidCredentialsForm
        identifierId={identifier.id}
        mode={metadata ? 'replace' : 'setup'}
        initialKeyAlias={metadata?.keyAlias}
        onCancel={() => setIsReplacing(false)}
        onSaved={() => setIsReplacing(false)}
      />
    );
  }

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="flex-row items-center justify-between space-y-0 border-b py-4">
          <CardTitle className="text-base">Signing keystore</CardTitle>
          {canManage && (
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => setIsReplacing(true)}>
                <Pencil className="h-3.5 w-3.5" /> Replace
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setIsDeleteDialogOpen(true)}
                className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive">
                <Trash2 className="h-3.5 w-3.5" /> Delete
              </Button>
            </div>
          )}
        </CardHeader>
        <CardContent className="divide-y pt-2">
          <MetadataRow label="Key alias" value={<span className="font-mono text-xs">{metadata.keyAlias}</span>} />
          <MetadataRow label="Created" value={<TimestampCell dateString={metadata.createdAt} />} />
          <MetadataRow label="Updated" value={<TimestampCell dateString={metadata.updatedAt} />} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="border-b py-4">
          <CardTitle className="text-base">Google Play service account</CardTitle>
        </CardHeader>
        <CardContent className="pt-4">
          {metadata.hasGoogleServiceAccountKey ? (
            <span className="inline-flex items-center gap-1.5 text-sm font-medium text-emerald-700 dark:text-emerald-300">
              <CheckCircle2 className="h-4 w-4" /> Configured
            </span>
          ) : (
            <span className="text-sm text-muted-foreground">Not configured</span>
          )}
          {canManage && (
            <p className="mt-2 text-xs text-muted-foreground">
              Credentials are saved as a whole: to add, change or remove the service account key,
              use 'Replace' above and re-upload the keystore.
            </p>
          )}
        </CardContent>
      </Card>

      <DeleteDialog
        isOpen={isDeleteDialogOpen}
        onClose={() => setIsDeleteDialogOpen(false)}
        onConfirm={handleDeleteCredentials}
        isDeleting={isDeleting}
        title="Delete build credentials"
        resourceName={`${identifier.identifier} credentials`}
        descriptionText="The keystore, its passwords and the service account key will be permanently removed. Builds for this identifier can no longer be signed. This cannot be undone."
        confirmButtonText="Delete credentials"
        isDeletingButtonText="Deleting…"
      />
    </div>
  );
};

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

  if (identifiersQuery.isLoading) {
    return (
      <div className="w-full space-y-4">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-28 w-full rounded-xl" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
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

      {identifier.platform === 'android' ? (
        <AndroidCredentialsSection identifier={identifier} canManage={canManage} />
      ) : (
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Credentials management for {platformLabel(identifier.platform)} is not supported yet.
        </div>
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
