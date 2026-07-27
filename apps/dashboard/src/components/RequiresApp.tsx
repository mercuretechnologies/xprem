import { ReactNode, useState } from 'react';
import { PackageOpen, Plus } from 'lucide-react';
import { ApiError } from '@/components/APIError';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { useSettings } from '@/lib/SettingsContext';
import { usePermissions } from '@/ee/lib/PermissionsContext';
import { Button } from '@/components/ui/button';
import { CreateAppModal } from '@/components/app-creation-modal';

// Wraps the app-scoped pages (updates, channels, app info, tokens): when the
// account can see no app at all, the page is replaced by an explicit empty
// state instead of empty tables that read like a bug. Admins get the create
// path right there; members learn who to ask.
export const RequiresApp = ({ children }: { children: ReactNode }) => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { apps, isLoading, error, refreshApps, setSelectedAppId } = useSelectedApp();
  const { isAdmin } = useCurrentUser();
  const { rbacEnabled } = usePermissions();
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);

  // While the list loads the pages render their own skeletons; the empty
  // state only takes over once we know there is truly nothing to show.
  if (isLoading || apps.length > 0) {
    return <>{children}</>;
  }

  // A failed app list is not an empty one: show the error instead of a
  // misleading "no app available".
  if (error) {
    return (
      <div className="w-full pt-8">
        <ApiError error={error} />
      </div>
    );
  }

  const canCreateApp = CONTROL_PLANE_ENABLED && isAdmin;
  const description = !CONTROL_PLANE_ENABLED
    ? 'No apps are configured on this server. Check the server configuration.'
    : canCreateApp
      ? 'Create your first app to start shipping over-the-air updates.'
      : rbacEnabled
        ? 'You do not have access to any app yet. Ask an admin to grant you access.'
        : 'No apps yet. Ask an admin to create one.';

  return (
    <div className="flex min-h-[60vh] w-full items-center justify-center">
      <div className="flex max-w-md flex-col items-center gap-3 rounded-2xl border border-dashed bg-muted/20 px-10 py-12 text-center">
        <span className="flex h-11 w-11 items-center justify-center rounded-full border bg-background text-muted-foreground">
          <PackageOpen className="h-5 w-5" strokeWidth={1.75} />
        </span>
        <h2 className="text-base font-semibold">No app available</h2>
        <p className="text-sm text-muted-foreground">{description}</p>
        {canCreateApp && (
          <Button size="sm" className="mt-2" onClick={() => setIsCreateModalOpen(true)}>
            <Plus className="h-4 w-4" /> Create app
          </Button>
        )}
      </div>
      {canCreateApp && (
        <CreateAppModal
          isOpen={isCreateModalOpen}
          onClose={() => setIsCreateModalOpen(false)}
          onAppCreated={async appId => {
            await refreshApps();
            setSelectedAppId(appId);
          }}
        />
      )}
    </div>
  );
};
