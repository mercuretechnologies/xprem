import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, CircleSlash, Loader2, TriangleAlert, X } from 'lucide-react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { usePermissions } from '@/ee/lib/PermissionsContext';
import { useSettings } from '@/lib/SettingsContext';

export const ExpoImportBanner = () => {
  const { selectedAppId } = useSelectedApp();
  const { isAdmin } = usePermissions();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const queryClient = useQueryClient();
  const [dismissedJobId, setDismissedJobId] = useState<string | null>(null);
  const [cancelRequestedJobId, setCancelRequestedJobId] = useState<string | null>(null);
  // Without this, a job finished hours ago would greet every page load.
  const sawRunningJobIdRef = useRef<string | null>(null);
  const invalidatedTerminalJobIdRef = useRef<string | null>(null);

  const enabled = Boolean(selectedAppId) && isAdmin && CONTROL_PLANE_ENABLED;
  const jobQuery = useQuery({
    queryKey: ['expo-import-app-job', selectedAppId],
    queryFn: () => api.getExpoImportJobForApp(selectedAppId!),
    enabled,
    refetchInterval: query => (query.state.data?.status?.state === 'running' ? 2_000 : 15_000),
  });

  const cancelMutation = useMutation({
    mutationFn: (jobId: string) => api.cancelExpoImportJob(jobId),
    onSuccess: (_, jobId) => setCancelRequestedJobId(jobId),
    onSettled: () => jobQuery.refetch(),
  });

  const jobId = jobQuery.data?.jobId ?? null;
  const status = jobQuery.data?.status ?? null;

  useEffect(() => {
    if (jobId && status?.state === 'running') {
      sawRunningJobIdRef.current = jobId;
    }
    if (jobId && status && status.state !== 'running' && invalidatedTerminalJobIdRef.current !== jobId) {
      for (const key of ['branches', 'runtimeVersions', 'updates']) {
        queryClient.invalidateQueries({ queryKey: [key, selectedAppId] });
      }
      invalidatedTerminalJobIdRef.current = jobId;
    }
  }, [jobId, status, queryClient, selectedAppId]);

  if (!enabled || !jobId || !status) return null;
  if (jobId === dismissedJobId) return null;
  const running = status.state === 'running';
  if (!running && sawRunningJobIdRef.current !== jobId) return null;

  const counts = `${status.processed}/${status.total} processed · ${status.imported} copied${
    status.skipped?.length ? ` · ${status.skipped.length} skipped` : ''
  }`;
  const cancelRequested = cancelRequestedJobId === jobId || status.cancelRequested;

  return (
    <div className="border-b border-border bg-muted/40 px-4 py-2.5">
      <div className="mx-auto flex max-w-[1480px] items-center gap-3">
        {running ? (
          <Loader2 className="h-4 w-4 shrink-0 animate-spin text-primary" />
        ) : status.state === 'done' ? (
          <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-500" />
        ) : status.state === 'canceled' ? (
          <CircleSlash className="h-4 w-4 shrink-0 text-muted-foreground" />
        ) : (
          <TriangleAlert className="h-4 w-4 shrink-0 text-destructive" />
        )}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-foreground">
            {running
              ? 'Copying the Expo update history of this app…'
              : status.state === 'done'
                ? 'Expo update history copied.'
                : status.state === 'canceled'
                  ? 'Update history import canceled.'
                  : 'Update history import failed.'}
            <span className="ml-2 font-normal text-muted-foreground">{counts}</span>
          </p>
          {running && (
            <div className="mt-1.5 h-1 w-full max-w-md overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-primary transition-all duration-500"
                style={{
                  width: status.total
                    ? `${Math.round((status.processed / status.total) * 100)}%`
                    : '5%',
                }}
              />
            </div>
          )}
        </div>
        {running ? (
          <Button
            size="sm"
            variant="outline"
            className="h-7 shrink-0 text-xs"
            disabled={cancelMutation.isPending || cancelRequested}
            onClick={() => cancelMutation.mutate(jobId)}>
            {cancelRequested ? 'Stopping…' : 'Cancel import'}
          </Button>
        ) : (
          <Button
            size="icon"
            variant="ghost"
            aria-label="Dismiss"
            className="h-7 w-7 shrink-0"
            onClick={() => setDismissedJobId(jobId)}>
            <X className="h-4 w-4" />
          </Button>
        )}
      </div>
    </div>
  );
};
