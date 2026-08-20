import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { Layers, Plus } from 'lucide-react';
import { api, ChannelRecord, describeApiError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/Combobox';
import { ApiError } from '@/components/APIError';

export const ChannelEnvironmentCard = ({
  channel,
  canManage,
  onUpdated,
}: {
  channel: ChannelRecord;
  canManage: boolean;
  onUpdated: () => Promise<void>;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const navigate = useNavigate();
  const [saving, setSaving] = useState(false);

  const environmentsQuery = useQuery({
    queryKey: ['environments', selectedAppId],
    queryFn: () => api.getEnvironments(),
    enabled: !!selectedAppId && canManage,
  });

  const current = channel.environmentName ?? null;

  const save = async (environmentName: string | null) => {
    if (environmentName === current) return;
    setSaving(true);
    try {
      await api.setChannelEnvironment(channel.releaseChannelName, environmentName);
      await onUpdated();
      toast({
        title: environmentName ? 'Environment updated' : 'Environment detached',
        description: environmentName
          ? `Builds for "${channel.releaseChannelName}" now use "${environmentName}".`
          : `"${channel.releaseChannelName}" no longer points to an environment.`,
      });
    } catch (error) {
      const message = describeApiError(error, 'Could not update environment');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="rounded-lg border p-5">
      <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-start">
        <div className="min-w-0">
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <Layers className="h-4 w-4 text-muted-foreground" />
            Environment
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {current ? (
              <>
                Builds for this channel are configured with{' '}
                <button
                  type="button"
                  className="font-medium text-foreground hover:text-link"
                  onClick={() => navigate(`/environments/${encodeURIComponent(current)}`)}>
                  {current}
                </button>
                .
              </>
            ) : (
              'No environment: builds for this channel get no variables.'
            )}
          </p>
        </div>
        {canManage && (
          <Combobox
            className="w-full sm:w-72"
            label="Select an environment…"
            loading={environmentsQuery.isLoading || saving}
            disabled={saving}
            clearable
            options={(environmentsQuery.data ?? []).map(environment => ({
              value: environment.name,
              label: environment.name,
            }))}
            value={current ?? ''}
            onChange={value => void save(value || null)}
            action={{
              label: 'Manage environments',
              icon: <Plus className="mr-2 h-4 w-4" />,
              onSelect: () => navigate('/environments'),
            }}
          />
        )}
      </div>
      {canManage && environmentsQuery.error && (
        <div className="mt-4">
          <ApiError error={environmentsQuery.error} />
        </div>
      )}
      {canManage && environmentsQuery.isSuccess && environmentsQuery.data.length === 0 && (
        <div className="mt-4 flex items-center justify-between gap-3 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
          No environment exists yet.
          <Button variant="outline" size="sm" onClick={() => navigate('/environments')}>
            Create one
          </Button>
        </div>
      )}
    </section>
  );
};
