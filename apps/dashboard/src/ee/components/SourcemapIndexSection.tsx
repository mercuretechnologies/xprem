// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Loader2, RefreshCw } from 'lucide-react';
import { api, SourcemapIndexStatus, UpdateSourcemapRecord, describeApiError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { EnterpriseFeatureGate } from '@/ee/components/EnterpriseFeatureGate';
import { sourcemapFeature } from '@/ee/lib/sourcemapFeature';
import { useToast } from '@/hooks/use-toast';
import { formatTimestamp } from '@/lib/utils';
import { ApiError } from '@/components/APIError';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';

const STATUS: Record<SourcemapIndexStatus, { label: string; className: string }> = {
  pending: { label: 'Queued', className: 'border-border bg-muted/60 text-muted-foreground' },
  running: {
    label: 'Indexing',
    className: 'border-sky-400/25 bg-sky-400/10 text-sky-700 dark:text-sky-300',
  },
  stored: {
    label: 'Indexed',
    className: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300',
  },
  failed: {
    label: 'Failed',
    className: 'border-red-400/25 bg-red-400/10 text-red-700 dark:text-red-300',
  },
  cancelled: {
    label: 'Unusable',
    className: 'border-amber-400/25 bg-amber-400/10 text-amber-700 dark:text-amber-300',
  },
};

// The server's reason codes. A failed or cancelled row carries the code as a
// prefix of the underlying error, which stays available on hover.
const REASONS: Record<string, string> = {
  map_missing: 'Source map missing from the store',
  map_invalid: 'Source map is not a usable version 3 map',
  map_too_large: 'Source map above the size limit for indexing',
};

/**
 * Maps a known reason prefix to a label, retaining any longer reason as hover
 * detail. Unknown reasons remain the label.
 */
const describeReason = (reason: string) => {
  const code = reason.split(':')[0].trim();
  const label = REASONS[code];
  return label ? { label, detail: reason === code ? undefined : reason } : { label: reason };
};

/**
 * Formats a byte count using B, KB, or MB labels with powers of 1024.
 */
const formatBytes = (bytes: number) => {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

/**
 * Reports whether an index record is queued or running and should keep polling.
 */
const isLive = (record: UpdateSourcemapRecord | undefined) =>
  record?.index?.status === 'pending' || record?.index?.status === 'running';

/**
 * Displays the recorded index status, result, and attempts, or explains a missing job.
 */
const IndexOutcome = ({ record }: { record: UpdateSourcemapRecord }) => {
  const index = record.index;
  if (!index) {
    return (
      <p className="text-sm text-muted-foreground">
        No index job recorded. The map was uploaded before indexing existed, or the job never
        started. Reindex schedules one.
      </p>
    );
  }
  const status = STATUS[index.status];
  const reason = index.reason ? describeReason(index.reason) : null;
  const updated = formatTimestamp(index.updatedAt, true);
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className={status.className}>
          {index.status === 'running' && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
          {status.label}
        </Badge>
        {index.status === 'stored' && index.segments != null && (
          <span className="text-xs text-muted-foreground">
            {index.segments.toLocaleString()} segments
            {index.indexSize != null ? ` · ${formatBytes(index.indexSize)}` : ''}
          </span>
        )}
      </div>
      {(index.status === 'pending' || index.status === 'running') && (
        <p className="text-xs text-muted-foreground">
          {index.status === 'pending' ? 'Waiting for a worker' : 'Decoding the map'}
        </p>
      )}
      {reason && (
        <p className="text-xs text-muted-foreground" title={reason.detail}>
          {reason.label}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        {index.attempts} {index.attempts === 1 ? 'attempt' : 'attempts'}
        {updated ? ` · ${updated}` : ''}
      </p>
    </div>
  );
};

/**
 * Shows source-map indexing for an update, polling every three seconds while
 * queued or running. Users with publish permission can request reindexing;
 * request failures appear in the section or a toast.
 */
export const SourcemapIndexSection = ({
  branch,
  runtimeVersion,
  updateId,
  sourcemapHash,
}: {
  branch: string;
  runtimeVersion: string;
  updateId: string;
  sourcemapHash: string;
}) => {
  const { selectedAppId } = useSelectedApp();
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const canReindex = useAppPermission('update:publish', 'admin-only');
  const queryKey = ['update-sourcemap', selectedAppId, branch, runtimeVersion, updateId];
  const { data, isLoading, error } = useQuery({
    queryKey,
    enabled: !!selectedAppId,
    queryFn: () => api.getUpdateSourcemap(branch, runtimeVersion, updateId),
    // The job finishes within seconds; poll only while it is in flight.
    refetchInterval: query => (isLive(query.state.data) ? 3000 : false),
  });
  const reindex = useMutation({
    mutationFn: () => api.reindexUpdateSourcemap(branch, runtimeVersion, updateId),
    onSuccess: () => {
      toast({ title: 'Index scheduled', description: 'The source map is being indexed again.' });
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: err => {
      const message = describeApiError(err, 'Could not schedule the index');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    },
  });

  const reindexButton = (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={!canReindex || reindex.isPending}
      onClick={() => reindex.mutate()}>
      {reindex.isPending ? (
        <Loader2 className="h-4 w-4 animate-spin" />
      ) : (
        <RefreshCw className="h-4 w-4" />
      )}
      Reindex
    </Button>
  );

  return (
    <EnterpriseFeatureGate feature={sourcemapFeature}>
      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <h2 className="text-base font-semibold">Source map</h2>
          {canReindex ? (
            reindexButton
          ) : (
            <TooltipProvider delayDuration={150}>
              <Tooltip>
                {/* A disabled button emits no pointer events, so the wrapper carries the tooltip. */}
                <TooltipTrigger asChild>
                  <span tabIndex={0} className="inline-flex">
                    {reindexButton}
                  </span>
                </TooltipTrigger>
                <TooltipContent side="left" className="text-xs font-normal">
                  Only an admin can reindex a source map.
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )}
        </div>

        {error ? (
          <ApiError error={error} />
        ) : isLoading || !data ? (
          <Skeleton className="h-24 w-full rounded-xl" />
        ) : (
          <div className="flex items-start justify-between gap-4 rounded-xl border bg-card px-4 py-3 shadow-sm">
            <div className="min-w-0 space-y-0.5">
              <p className="text-xs text-muted-foreground">Map</p>
              <code className="break-all font-mono text-xs" title={sourcemapHash}>
                {sourcemapHash}
              </code>
            </div>
            <div className="w-64 shrink-0">
              <IndexOutcome record={data} />
            </div>
          </div>
        )}
      </section>
    </EnterpriseFeatureGate>
  );
};
