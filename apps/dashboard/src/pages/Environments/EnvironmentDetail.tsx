import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router';
import { Box, Copy, Eye, EyeOff, Layers, Pencil, Plus, Trash2, Unlink } from 'lucide-react';
import { api, describeApiError, EnvironmentRecord, EnvVarRecord } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { AdminOnlyNote } from '@/components/ui/admin-only-note';
import { TimestampCell } from '@/components/ui/timestamp-cell';
import { Combobox } from '@/components/Combobox';
import { ApiError } from '@/components/APIError';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { EnvVarDialog } from './EnvVarDialog';
import { useChannelsByEnvironment } from './useChannelsByEnvironment';

const ChannelsCard = ({
  environment,
  canManage,
}: {
  environment: EnvironmentRecord;
  canManage: boolean;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { channelsByEnvironment, channelsQuery } = useChannelsByEnvironment();
  const [attaching, setAttaching] = useState<string | null>(null);
  const [detaching, setDetaching] = useState<string | null>(null);

  const attached = channelsByEnvironment.get(environment.name) ?? [];
  const attachable = (channelsQuery.data ?? []).filter(
    channel => channel.environmentName !== environment.name
  );

  const bind = async (channelName: string, environmentName: string | null) => {
    const setBusy = environmentName ? setAttaching : setDetaching;
    setBusy(channelName);
    try {
      await api.setChannelEnvironment(channelName, environmentName);
      await queryClient.invalidateQueries({ queryKey: ['channels', selectedAppId] });
      toast({
        title: environmentName ? 'Channel attached' : 'Channel detached',
        description: environmentName
          ? `Builds for "${channelName}" now use "${environmentName}".`
          : `"${channelName}" no longer points to "${environment.name}".`,
      });
    } catch (error) {
      const message = describeApiError(error, 'Could not update channel');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card>
      <CardHeader className="border-b py-4">
        <CardTitle className="flex items-center gap-2 text-base">
          <Box className="h-4 w-4 text-muted-foreground" />
          Channels
          <Badge variant="secondary" className="ml-1 font-normal">
            {attached.length}
          </Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {channelsQuery.isLoading ? (
          <Skeleton className="h-10 w-full" />
        ) : channelsQuery.error ? (
          <ApiError error={channelsQuery.error} />
        ) : attached.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No channel points to this environment yet.
          </p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {attached.map(channelName => (
              <li key={channelName} className="flex items-center justify-between gap-3 px-3 py-2">
                <button
                  type="button"
                  className="inline-flex items-center gap-2 text-sm font-medium hover:text-link"
                  onClick={() => navigate(`/channels/${encodeURIComponent(channelName)}`)}>
                  <Box className="h-4 w-4 text-muted-foreground" />
                  {channelName}
                </button>
                {canManage && (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={detaching === channelName}
                    onClick={() => void bind(channelName, null)}
                    className="h-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                    title="Detach channel">
                    <Unlink className="h-3.5 w-3.5" />
                    {detaching === channelName ? 'Detaching…' : 'Detach'}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}

        {canManage && (
          <div className="space-y-1.5">
            <Combobox
              className="w-full sm:w-80"
              label="Attach a channel…"
              loading={channelsQuery.isLoading || !!attaching}
              disabled={!!attaching || attachable.length === 0}
              options={attachable.map(channel => ({
                value: channel.releaseChannelName,
                label: channel.environmentName
                  ? `${channel.releaseChannelName} (currently ${channel.environmentName})`
                  : channel.releaseChannelName,
              }))}
              value=""
              onChange={channelName => channelName && void bind(channelName, environment.name)}
            />
            <p className="text-xs text-muted-foreground">
              A channel points to one environment at a time: attaching one bound elsewhere moves it
              here.
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  );
};

const RevealedValue = ({ value }: { value: string }) => {
  const { toast } = useToast();
  return (
    <div className="flex items-center gap-1.5">
      <code className="max-h-40 max-w-xl overflow-y-auto whitespace-pre-wrap break-all rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
        {value === '' ? <span className="italic text-muted-foreground">(empty)</span> : value}
      </code>
      <Button
        variant="ghost"
        size="sm"
        className="h-7 w-7 p-0 text-muted-foreground"
        title="Copy value"
        onClick={() => {
          void navigator.clipboard.writeText(value);
          toast({ title: 'Copied', description: 'Value copied to the clipboard.' });
        }}>
        <Copy className="h-3.5 w-3.5" />
      </Button>
    </div>
  );
};

const VariablesCard = ({
  environment,
  canManage,
  canReveal,
}: {
  environment: EnvironmentRecord;
  canManage: boolean;
  canReveal: boolean;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<{ open: boolean; existing: EnvVarRecord | null }>({
    open: false,
    existing: null,
  });
  const [varToDelete, setVarToDelete] = useState<EnvVarRecord | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);
  // A Map, not an object: keys like "constructor" are valid variable names.
  const [revealed, setRevealed] = useState<Map<string, string>>(new Map());
  const [revealing, setRevealing] = useState<string | null>(null);

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['environments', selectedAppId] });
  };

  const forget = (key: string) => {
    setRevealed(current => {
      const next = new Map(current);
      next.delete(key);
      return next;
    });
  };

  const reveal = async (key: string) => {
    if (revealed.has(key)) {
      forget(key);
      return;
    }
    setRevealing(key);
    try {
      const { value } = await api.revealEnvVar(environment.name, key);
      setRevealed(current => new Map(current).set(key, value));
    } catch (error) {
      const message = describeApiError(error, 'Could not reveal value');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
      // The entry may have gone away under us; refetch so the list agrees.
      await invalidate();
    } finally {
      setRevealing(null);
    }
  };

  const handleDelete = async () => {
    if (!varToDelete) return;
    setIsDeleting(true);
    try {
      await api.deleteEnvVar(environment.name, varToDelete.key);
      await invalidate();
      forget(varToDelete.key);
      toast({
        title: 'Variable deleted',
        description: `"${varToDelete.key}" was removed from "${environment.name}".`,
      });
      setVarToDelete(null);
    } catch (error) {
      const message = describeApiError(error, 'Could not delete variable');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0 border-b py-4">
        <CardTitle className="flex items-center gap-2 text-base">
          <Layers className="h-4 w-4 text-muted-foreground" />
          Variables
          <Badge variant="secondary" className="ml-1 font-normal">
            {environment.vars.length}
          </Badge>
        </CardTitle>
        {canManage && (
          <Button size="sm" onClick={() => setEditing({ open: true, existing: null })}>
            <Plus className="h-3.5 w-3.5" /> Add variable
          </Button>
        )}
      </CardHeader>
      <CardContent className="pt-4">
        {environment.vars.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No variable yet.
            {canManage ? ' Add one to configure builds using this environment.' : ''}
          </p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {environment.vars.map(envVar => (
              <li
                key={envVar.key}
                className="flex flex-col gap-2 px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0 space-y-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm font-medium">
                      {envVar.isPublic && (
                        <span className="text-muted-foreground">EXPO_PUBLIC_</span>
                      )}
                      {envVar.key}
                    </span>
                    <Badge
                      variant="outline"
                      className={
                        envVar.isPublic
                          ? 'border-sky-400/25 bg-sky-400/10 text-sky-700 dark:text-sky-300'
                          : 'border-amber-400/25 bg-amber-400/10 text-amber-700 dark:text-amber-300'
                      }>
                      {envVar.isPublic ? 'Public' : 'Secret'}
                    </Badge>
                  </div>
                  {revealed.has(envVar.key) ? (
                    <RevealedValue value={revealed.get(envVar.key) as string} />
                  ) : (
                    <span className="font-mono text-xs text-muted-foreground">••••••••</span>
                  )}
                </div>
                <div className="flex items-center gap-1 text-xs text-muted-foreground">
                  <span className="mr-2 hidden items-center gap-1.5 sm:inline-flex">
                    Updated <TimestampCell dateString={envVar.updatedAt} />
                  </span>
                  {canReveal && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-8 w-8 p-0"
                      disabled={revealing === envVar.key}
                      onClick={() => void reveal(envVar.key)}
                      title={revealed.has(envVar.key) ? 'Hide value' : 'Reveal value'}>
                      {revealed.has(envVar.key) ? (
                        <EyeOff className="h-3.5 w-3.5" />
                      ) : (
                        <Eye className="h-3.5 w-3.5" />
                      )}
                    </Button>
                  )}
                  {canManage && (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0"
                        onClick={() => setEditing({ open: true, existing: envVar })}
                        title="Update value">
                        <Pencil className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0 hover:bg-destructive/10 hover:text-destructive"
                        onClick={() => setVarToDelete(envVar)}
                        title="Delete variable">
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardContent>

      <EnvVarDialog
        environmentName={environment.name}
        existingKeys={environment.vars.map(envVar => envVar.key)}
        existing={editing.existing}
        isOpen={editing.open}
        onClose={() => setEditing(current => ({ ...current, open: false }))}
        onSaved={async () => {
          await invalidate();
          // The value on screen is stale once overwritten.
          if (editing.existing) {
            forget(editing.existing.key);
          }
        }}
      />

      <DeleteDialog
        isOpen={!!varToDelete}
        onClose={() => setVarToDelete(null)}
        onConfirm={handleDelete}
        isDeleting={isDeleting}
        title="Delete variable"
        resourceName={varToDelete?.key}
        descriptionText="The variable and its value will be permanently removed from this environment. This cannot be undone."
        confirmButtonText="Delete variable"
        isDeletingButtonText="Deleting…"
      />
    </Card>
  );
};

export const EnvironmentDetail = () => {
  const { environmentName } = useParams<{ environmentName: string }>();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  // Display gating only: the server re-checks the permission on its routes.
  const canManage = useAppPermission('env:manage', 'admin-only');
  const canReveal = useAppPermission('env:read', 'any-member');

  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  const environmentsQuery = useQuery({
    queryKey: ['environments', selectedAppId],
    queryFn: () => api.getEnvironments(),
    enabled: !!selectedAppId && CONTROL_PLANE_ENABLED,
  });
  // The router already decodes the param.
  const environment = environmentsQuery.data?.find(record => record.name === environmentName);

  const handleDelete = async () => {
    if (!environment) return;
    setIsDeleting(true);
    try {
      await api.deleteEnvironment(environment.name);
      await queryClient.invalidateQueries({ queryKey: ['environments', selectedAppId] });
      toast({
        title: 'Environment deleted',
        description: `"${environment.name}" and its variables were removed.`,
      });
      navigate('/environments');
    } catch (error) {
      const message = describeApiError(error, 'Could not delete environment');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
      setIsDeleting(false);
      setIsDeleteDialogOpen(false);
    }
  };

  if (!CONTROL_PLANE_ENABLED) {
    return (
      <div className="w-full">
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Environments need a database to be stored in, so they are not available on a stateless
          deployment.
        </div>
      </div>
    );
  }

  if (environmentsQuery.isLoading) {
    return (
      <div className="w-full space-y-4">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-24 w-full rounded-xl" />
        <Skeleton className="h-48 w-full rounded-xl" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </div>
    );
  }

  if (environmentsQuery.error) {
    return (
      <div className="w-full">
        <ApiError error={environmentsQuery.error} />
      </div>
    );
  }

  if (!environment) {
    return (
      <div className="w-full">
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          This environment does not exist or was deleted.
          <div className="mt-4">
            <Button variant="outline" onClick={() => navigate('/environments')}>
              Back to environments
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
            <BreadcrumbLink className="cursor-pointer" onClick={() => navigate('/environments')}>
              Environments
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>{environment.name}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <Card>
        <CardContent className="flex items-center justify-between gap-4 py-5">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg border bg-muted/40">
              <Layers className="h-5 w-5 text-muted-foreground" />
            </div>
            <div>
              <span className="font-display text-lg font-semibold tracking-tight">
                {environment.name}
              </span>
              <p className="flex items-center gap-1.5 text-sm text-muted-foreground">
                Environment · created <TimestampCell dateString={environment.createdAt} />
              </p>
            </div>
          </div>
          {canManage && (
            <Button
              variant="outline"
              onClick={() => setIsDeleteDialogOpen(true)}
              className="border-destructive/30 text-destructive hover:bg-destructive/10 hover:text-destructive">
              <Trash2 className="h-4 w-4" /> Delete environment
            </Button>
          )}
        </CardContent>
      </Card>

      {!canManage && (
        <AdminOnlyNote>
          You do not have permission to manage this app's environments. Ask an admin to grant you
          access.
        </AdminOnlyNote>
      )}

      <ChannelsCard
        key={`channels/${selectedAppId}/${environment.name}`}
        environment={environment}
        canManage={canManage}
      />
      <VariablesCard
        key={`vars/${selectedAppId}/${environment.name}`}
        environment={environment}
        canManage={canManage}
        canReveal={canReveal}
      />

      <DeleteDialog
        isOpen={isDeleteDialogOpen}
        onClose={() => setIsDeleteDialogOpen(false)}
        onConfirm={handleDelete}
        isDeleting={isDeleting}
        title="Delete environment"
        resourceName={environment.name}
        descriptionText="The environment and all of its variables will be permanently removed. Channels pointing to it must be detached first. This cannot be undone."
        confirmButtonText="Delete environment"
        isDeletingButtonText="Deleting…"
      />
    </div>
  );
};
