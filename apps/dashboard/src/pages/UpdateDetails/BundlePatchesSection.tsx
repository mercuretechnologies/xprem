import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { ChevronRight, Loader2, RefreshCw } from 'lucide-react';
import { api, BundlePatchRecord, BundlePatchStatus, describeApiError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { useToast } from '@/hooks/use-toast';
import { formatTimestamp } from '@/lib/utils';
import { UpdateDetailsOrigin, updateDetailsPath, updateTitle } from '@/lib/update-format';
import { ApiError } from '@/components/APIError';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';

const STATUS: Record<BundlePatchStatus, { label: string; className: string }> = {
  pending: { label: 'Queued', className: 'border-border bg-muted/60 text-muted-foreground' },
  running: {
    label: 'Computing',
    className: 'border-sky-400/25 bg-sky-400/10 text-sky-700 dark:text-sky-300',
  },
  stored: {
    label: 'Stored',
    className: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300',
  },
  skipped: { label: 'Skipped', className: 'border-border bg-muted/60 text-muted-foreground' },
  failed: {
    label: 'Failed',
    className: 'border-red-400/25 bg-red-400/10 text-red-700 dark:text-red-300',
  },
  cancelled: {
    label: 'Cancelled',
    className: 'border-amber-400/25 bg-amber-400/10 text-amber-700 dark:text-amber-300',
  },
};

// The server's reason codes. A failed or cancelled row carries the code as a
// prefix of the underlying error, which stays available on hover.
const REASONS: Record<string, string> = {
  legacy_update: 'One of the bundles predates content-addressable storage',
  identical_bundles: 'Both updates ship the same bundle',
  patch_not_worth: 'Patch too close to the size of the full download',
  bundle_too_large: 'Bundle above the size limit for diffing',
  blob_missing: 'Bundle missing from the bucket',
  update_not_found: 'Source or target update no longer exists',
  different_branch: 'Source and target are on different branches',
  verification_failed: 'Patch did not rebuild the target bundle',
};

const describeReason = (reason: string) => {
  const code = reason.split(':')[0].trim();
  const label = REASONS[code];
  return label ? { label, detail: reason === code ? undefined : reason } : { label: reason };
};

const formatBytes = (bytes: number) => {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

const isLive = (patch: BundlePatchRecord) =>
  patch.status === 'pending' || patch.status === 'running';

// The right-hand side of a row: how much the patch saves, or why it is not
// served.
const PatchOutcome = ({ patch }: { patch: BundlePatchRecord }) => {
  const measured =
    patch.patchSize != null && patch.fullDownloadSize != null && patch.fullDownloadSize > 0;
  const share = measured ? patch.patchSize! / patch.fullDownloadSize! : 0;
  const reason = patch.reason ? describeReason(patch.reason) : null;

  if (patch.status === 'pending' || patch.status === 'running') {
    return (
      <p className="text-sm text-muted-foreground">
        {patch.status === 'pending' ? 'Waiting for a worker' : 'Diffing the two bundles'}
      </p>
    );
  }
  return (
    <div className="space-y-1.5">
      {measured && (
        <>
          <div className="flex items-baseline justify-end gap-2">
            <span
              className={`text-lg font-semibold tabular-nums ${
                patch.status === 'stored'
                  ? 'text-emerald-700 dark:text-emerald-300'
                  : 'text-muted-foreground'
              }`}>
              −{Math.round((1 - share) * 100)}%
            </span>
            <span className="text-xs text-muted-foreground">
              {formatBytes(patch.patchSize!)} instead of {formatBytes(patch.fullDownloadSize!)}
            </span>
          </div>
          <div className="h-1 overflow-hidden rounded-full bg-muted">
            <div
              className={`h-full rounded-full ${
                patch.status === 'stored' ? 'bg-emerald-500' : 'bg-amber-500'
              }`}
              style={{ width: `${Math.max(2, Math.round(share * 100))}%` }}
            />
          </div>
        </>
      )}
      {reason && (
        <p className="text-xs text-muted-foreground" title={reason.detail}>
          {reason.label}
        </p>
      )}
    </div>
  );
};

const PatchRow = ({
  patch,
  branch,
  runtimeVersion,
  origin,
}: {
  patch: BundlePatchRecord;
  branch: string;
  runtimeVersion: string;
  origin: UpdateDetailsOrigin;
}) => {
  const status = STATUS[patch.status];
  const title = updateTitle(patch.sourceMessage, patch.sourceCommitHash);
  const published = formatTimestamp(patch.sourceCreatedAt);
  const updated = formatTimestamp(patch.updatedAt, true);
  return (
    <Link
      to={updateDetailsPath({ branch, runtimeVersion, updateId: patch.sourceUpdateId }, origin)}
      className="group flex items-center gap-4 px-4 py-3 transition hover:bg-muted/40">
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{patch.sourceUpdateId}</span>
          <Badge variant="outline" className={status.className}>
            {patch.status === 'running' && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
            {status.label}
          </Badge>
        </div>
        <p className="truncate text-xs text-muted-foreground" title={title}>
          <code className="font-mono">{patch.sourceCommitHash.slice(0, 7)}</code>
          {title ? ` · ${title}` : ''}
        </p>
        <p className="text-xs text-muted-foreground">
          {published ? `Published ${published}` : 'Legacy record'} · {patch.attempts}{' '}
          {patch.attempts === 1 ? 'attempt' : 'attempts'}
          {updated ? ` · ${updated}` : ''}
        </p>
      </div>
      <div className="w-56 shrink-0 text-right">
        <PatchOutcome patch={patch} />
      </div>
      <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground transition group-hover:text-foreground" />
    </Link>
  );
};

export const BundlePatchesSection = ({
  branch,
  runtimeVersion,
  updateId,
  platform,
  origin,
}: {
  branch: string;
  runtimeVersion: string;
  updateId: string;
  platform: string;
  origin: UpdateDetailsOrigin;
}) => {
  const { selectedAppId } = useSelectedApp();
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const canRecompute = useAppPermission('update:publish', 'admin-only');
  const queryKey = ['update-patches', selectedAppId, branch, runtimeVersion, updateId];
  const { data, isLoading, error } = useQuery({
    queryKey,
    enabled: !!selectedAppId,
    queryFn: () => api.getUpdatePatches(branch, runtimeVersion, updateId),
    // Workers finish within minutes; poll only while something is in flight.
    refetchInterval: query => (query.state.data?.some(isLive) ? 5000 : false),
  });
  const recompute = useMutation({
    mutationFn: () => api.recomputeUpdatePatches(branch, runtimeVersion, updateId),
    onSuccess: ({ scheduled }) => {
      toast(
        scheduled === 0
          ? {
              title: 'Nothing to patch',
              description: `No earlier ${platform} update exists on this runtime version, so there is no bundle to diff from.`,
            }
          : {
              title: `${scheduled} ${scheduled === 1 ? 'patch' : 'patches'} scheduled`,
              description: 'Each earlier update of this platform is being diffed again.',
            }
      );
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: err => {
      const message = describeApiError(err, 'Could not schedule the patches');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    },
  });

  const recomputeButton = (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={!canRecompute || recompute.isPending}
      onClick={() => recompute.mutate()}>
      {recompute.isPending ? (
        <Loader2 className="h-4 w-4 animate-spin" />
      ) : (
        <RefreshCw className="h-4 w-4" />
      )}
      Recompute
    </Button>
  );

  return (
    <section className="space-y-3">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <h2 className="text-base font-semibold">Bundle diffing</h2>
          <p className="text-sm text-muted-foreground">
            Patches from the earlier updates of the same platform, served instead of the full bundle
            to devices that accept bsdiff.
          </p>
        </div>
        {canRecompute ? (
          recomputeButton
        ) : (
          <TooltipProvider delayDuration={150}>
            <Tooltip>
              {/* A disabled button emits no pointer events, so the wrapper carries the tooltip. */}
              <TooltipTrigger asChild>
                <span tabIndex={0} className="inline-flex">
                  {recomputeButton}
                </span>
              </TooltipTrigger>
              <TooltipContent side="left" className="text-xs font-normal">
                Only an admin can recompute patches.
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}
      </div>

      {error ? (
        <ApiError error={error} />
      ) : isLoading || !data ? (
        <Skeleton className="h-40 w-full rounded-xl" />
      ) : data.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-muted/30 px-4 py-6 text-center text-sm text-muted-foreground">
          <p>No patch for this update.</p>
          <p className="mt-1 text-xs">
            Either it is the first {platform} update on this runtime version, or it was published
            before bundle diffing was enabled. Recompute tells which.
          </p>
        </div>
      ) : (
        <div className="divide-y overflow-hidden rounded-xl border bg-card shadow-sm">
          {data.map(patch => (
            <PatchRow
              key={patch.sourceUpdateId}
              patch={patch}
              branch={branch}
              runtimeVersion={runtimeVersion}
              origin={origin}
            />
          ))}
        </div>
      )}
    </section>
  );
};
