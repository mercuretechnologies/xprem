import { useState, useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiProblemError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { ShieldAlert, Download, Edit2, Check, X, Trash2, Copy } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { PageHeader } from '@/components/PageHeader';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { KeystoreCard } from './components/KeystoreCard';
import { AdminOnlyNote } from '@/components/ui/admin-only-note';
import { useSettings } from '@/lib/SettingsContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';

// The app id is what every CLI flag, every environment variable and every
// support thread asks for, and it is a UUID: selecting thirty-six characters by
// hand is where a wrong one comes from.
const CopyAppIdButton = ({ value }: { value: string }) => {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="h-6 w-6 shrink-0 align-middle text-muted-foreground hover:text-foreground"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          // Long enough to be seen, short enough that the button is ready
          // again before anyone reaches for it a second time.
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // Clipboard access can be refused, and over plain HTTP the API is not
          // there at all. The id stays selectable either way, so the failure
          // costs a manual copy rather than an error nobody can act on.
          setCopied(false);
        }
      }}>
      {copied ? (
        <Check className="h-3.5 w-3.5 text-emerald-700 dark:text-emerald-300" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
      <span className="sr-only">{copied ? 'App ID copied' : 'Copy app ID'}</span>
    </Button>
  );
};

export const AppInfo = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  // Display gating only: the server re-checks each permission on its route.
  const canRenameApp = useAppPermission('app:rename', 'admin-only');
  const canDeleteApp = useAppPermission('app:delete', 'admin-only');
  const canReadCertificate = useAppPermission('certificate:read', 'admin-only');
  const { selectedAppId, refreshApps } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [isEditingName, setIsEditingName] = useState(false);
  const [editNameValue, setEditNameValue] = useState('');
  const [isUpdatingName, setIsUpdatingName] = useState(false);

  const [showDeleteAppDialog, setShowDeleteAppDialog] = useState(false);
  const [isDeletingApp, setIsDeletingApp] = useState(false);

  const appDetailsQuery = useQuery({
    queryKey: ['appDetails', selectedAppId],
    queryFn: () => api.getApp(selectedAppId!),
    enabled: !!selectedAppId,
  });

  const appData = appDetailsQuery.data;
  const isDownloadAvailable = appData?.keys?.mode === 'database';

  useEffect(() => {
    if (appData?.name) {
      setEditNameValue(appData.name);
    }
  }, [appData?.name]);

  if (!selectedAppId) {
    return (
      <div className="flex min-h-[400px] flex-col items-center justify-center rounded-xl border border-dashed bg-background p-8 text-center">
        <ShieldAlert className="mb-2 h-8 w-8 text-muted-foreground" />
        <p className="text-sm font-medium text-foreground">No app selected</p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Pick an app from the selector in the sidebar.
        </p>
      </div>
    );
  }

  const handleUpdateAppName = async () => {
    if (!editNameValue.trim() || editNameValue.trim() === appData?.name) {
      setIsEditingName(false);
      return;
    }
    setIsUpdatingName(true);
    try {
      await api.updateApp({ name: editNameValue.trim() });
      await queryClient.invalidateQueries({ queryKey: ['appDetails', selectedAppId] });
      toast({ title: 'Name updated', description: 'The app was renamed.' });
      setIsEditingName(false);
      await refreshApps();
    } catch (error) {
      let errorTitle = 'Error renaming app';
      let errorMessage = 'An unexpected server error occurred.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) errorMessage = error.message;
      toast({
        title: errorTitle,
        description: errorMessage,
        variant: 'destructive',
      });
    } finally {
      setIsUpdatingName(false);
    }
  };

  const handleExecuteAppDeletion = async () => {
    setIsDeletingApp(true);
    try {
      await api.deleteApp();
      toast({
        title: 'App deleted',
        description: 'The app and everything it contained were removed.',
      });
      setShowDeleteAppDialog(false);
      setIsDeletingApp(false);
      await refreshApps();
    } catch (error) {
      let errorTitle = 'Deletion failed';
      let errorMessage = 'Could not delete the app.';
      if (error instanceof ApiProblemError) {
        errorMessage = error.detail;
        errorTitle = error.title;
      } else if (error instanceof Error) errorMessage = error.message;
      toast({
        title: errorTitle,
        description: errorMessage,
        variant: 'destructive',
      });
      setIsDeletingApp(false);
    }
  };

  const handleDownloadCertificate = async () => {
    try {
      const certificateText = await api.downloadAppCertificate(selectedAppId);
      const blob = new Blob([certificateText], { type: 'text/plain' });
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = `app-${selectedAppId}-certificate.txt`;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      URL.revokeObjectURL(url);
      toast({ title: 'Certificate downloaded', description: 'Check your downloads folder.' });
    } catch (error) {
      let errorTitle = 'Download failed';
      let errorMessage = 'Could not download the certificate.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({
        title: errorTitle,
        description: errorMessage,
        variant: 'destructive',
      });
    }
  };

  return (
    <div className="w-full">
      <PageHeader
        title={
          isEditingName ? (
            <span className="flex max-w-md items-center gap-2">
              <Input
                value={editNameValue}
                onChange={e => setEditNameValue(e.target.value)}
                disabled={isUpdatingName}
                className="text-lg font-medium"
                autoFocus
                onKeyDown={e => e.key === 'Enter' && handleUpdateAppName()}
              />
              <Button
                size="icon"
                variant="outline"
                className="h-9 w-9 shrink-0"
                onClick={handleUpdateAppName}
                disabled={isUpdatingName}>
                <Check className="h-4 w-4" />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                className="h-9 w-9 shrink-0 text-muted-foreground"
                onClick={() => {
                  setIsEditingName(false);
                  setEditNameValue(appData?.name || '');
                }}
                disabled={isUpdatingName}>
                <X className="h-4 w-4" />
              </Button>
            </span>
          ) : (
            <span className="flex items-center gap-2">
              {appData?.name || selectedAppId}
              {CONTROL_PLANE_ENABLED && canRenameApp && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 shrink-0 text-muted-foreground hover:text-foreground"
                  onClick={() => setIsEditingName(true)}>
                  <Edit2 className="h-3.5 w-3.5" />
                </Button>
              )}
            </span>
          )
        }
        description={
          <span className="inline-flex items-center gap-1">
            App ID:
            <code className="select-all rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
              {selectedAppId}
            </code>
            <CopyAppIdButton value={selectedAppId} />
          </span>
        }
        actions={
          // Key material: the server only serves it with certificate:read.
          isDownloadAvailable && canReadCertificate ? (
            <Button variant="outline" size="sm" onClick={handleDownloadCertificate}>
              <Download className="h-4 w-4" />
              Download certificate
            </Button>
          ) : undefined
        }
      />

      <div className="max-w-2xl space-y-6">
        {CONTROL_PLANE_ENABLED && !canRenameApp && !canDeleteApp && !canReadCertificate && (
          <AdminOnlyNote>
            You do not have permission to rename or delete this app or download its signing
            certificate. Ask an admin to grant you access.
          </AdminOnlyNote>
        )}

        <KeystoreCard isLoading={appDetailsQuery.isLoading} appData={appData} />

        {CONTROL_PLANE_ENABLED && canDeleteApp && (
          <div className="overflow-hidden rounded-xl border border-destructive/30">
            <div className="flex flex-wrap items-center justify-between gap-4 p-5">
              <div>
                <h3 className="text-sm font-medium">Delete this app</h3>
                <p className="mt-0.5 max-w-md text-xs leading-relaxed text-muted-foreground">
                  Removes the app with all of its branches, channels, tokens and published updates.
                  This cannot be undone.
                </p>
              </div>
              <Button variant="destructive" size="sm" onClick={() => setShowDeleteAppDialog(true)}>
                <Trash2 className="h-3.5 w-3.5" /> Delete app
              </Button>
            </div>
          </div>
        )}
      </div>

      {CONTROL_PLANE_ENABLED && canDeleteApp && (
        <DeleteDialog
          isOpen={showDeleteAppDialog}
          onClose={() => setShowDeleteAppDialog(false)}
          onConfirm={handleExecuteAppDeletion}
          isDeleting={isDeletingApp}
          title="Delete app"
          resourceName={appData?.name || selectedAppId}
          descriptionText="All branches, channels, tokens and published updates will be permanently deleted. Devices running this app will stop receiving OTA updates."
          confirmButtonText="Delete app"
          isDeletingButtonText="Deleting…"
        />
      )}
    </div>
  );
};
