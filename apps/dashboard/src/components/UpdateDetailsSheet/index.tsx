import { forwardRef, useImperativeHandle, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet.tsx';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api.ts';
import { Skeleton } from '@/components/ui/skeleton.tsx';
import { ApiError } from '@/components/APIError';
import { Badge } from '@/components/ui/badge.tsx';
import { Button } from '@/components/ui/button.tsx';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { formatTimestamp } from '@/lib/utils';
import { RolloutBar } from '@/components/rollout/RolloutBar';
import { Check, ChevronDown, ChevronUp, Copy, Package, Split, Undo2 } from 'lucide-react';
import { UpdateHealthHistory } from '@/ee/components/UpdateHealthHistory';

interface Update {
  updateUUID: string;
  createdAt: string;
  updateId: string;
  platform: string;
  commitHash: string;
  branch?: string;
  runtimeVersion?: string;
}

export type UpdateDetailsRef = {
  openSheet: (update: Update) => void;
  closeSheet: () => void;
};

const CopyButton = ({ value, label }: { value: string; label: string }) => {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="h-6 w-6 shrink-0 text-muted-foreground hover:text-foreground"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          setCopied(false);
        }
      }}>
      {copied ? (
        <Check className="h-3.5 w-3.5 text-emerald-700 dark:text-emerald-300" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
      <span className="sr-only">Copy {label}</span>
    </Button>
  );
};

const DetailSection = ({ title, children }: { title: string; children: ReactNode }) => (
  <section className="space-y-2">
    <h3 className="text-sm font-medium">{title}</h3>
    <div className="divide-y rounded-xl border bg-card shadow-sm">{children}</div>
  </section>
);

const DetailRow = ({ label, children }: { label: string; children: ReactNode }) => (
  <div className="flex items-center justify-between gap-4 px-4 py-2.5">
    <span className="shrink-0 text-sm text-muted-foreground">{label}</span>
    <div className="flex min-w-0 items-center gap-1 text-sm font-medium">{children}</div>
  </div>
);

const MonoValue = ({ value }: { value: string }) => (
  <code className="truncate font-mono text-xs" title={value}>
    {value}
  </code>
);

const platformLabel = (platform: string) =>
  platform === 'ios' ? 'iOS' : platform === 'android' ? 'Android' : platform;
const isUuid = (value: string) =>
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);

const UpdateDetailsBody = ({
  update,
  branch,
  runtimeVersion,
}: {
  update: Update;
  branch: string;
  runtimeVersion: string;
}) => {
  const { selectedAppId } = useSelectedApp();
  const [showRawConfig, setShowRawConfig] = useState(false);
  // Keyed on what the fetch actually uses. Never updateUUID: every rollback
  // row shares the literal "Rollback to embedded", so two rollbacks from
  // different branches would collide in the cache and show mixed data.
  const { data, isLoading, error } = useQuery({
    queryKey: ['update-details', selectedAppId, branch, runtimeVersion, update.updateId],
    enabled: !!update.updateId && !!selectedAppId && !!branch && !!runtimeVersion,
    queryFn: () => api.getUpdateDetails(branch, runtimeVersion, update.updateId),
  });

  const expoConfig = useMemo(() => {
    if (!data?.expoConfig) return null;
    try {
      const parsed = JSON.parse(data.expoConfig);
      return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : null;
    } catch {
      return null;
    }
  }, [data?.expoConfig]);

  if (isLoading || (!data && !error)) {
    return (
      <div className="space-y-4 py-4">
        <Skeleton className="h-24 w-full rounded-xl" />
        <Skeleton className="h-40 w-full rounded-xl" />
        <Skeleton className="h-40 w-full rounded-xl" />
      </div>
    );
  }
  if (error) {
    return (
      <div className="flex h-full flex-col items-center justify-center">
        <ApiError error={error} />
      </div>
    );
  }
  if (!data) return null;

  const isRollback = data.type !== 0;
  const rolloutActive = data.rolloutPercentage != null;
  const rolloutEnded = !rolloutActive && data.controlUpdateId != null;
  const publishedAt = formatTimestamp(data.createdAt, true);
  const configEntries = (
    [
      ['App name', expoConfig?.name],
      ['Slug', expoConfig?.slug],
      ['App version', expoConfig?.version],
      ['SDK version', expoConfig?.sdkVersion],
    ] as [string, unknown][]
  ).filter((entry): entry is [string, string] => typeof entry[1] === 'string' && entry[1] !== '');

  return (
    <>
      <SheetHeader>
        <div className="flex items-center gap-3">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full border bg-muted/50 text-muted-foreground">
            {isRollback ? <Undo2 className="h-5 w-5" /> : <Package className="h-5 w-5" />}
          </div>
          <div className="min-w-0">
            <SheetTitle className="truncate">Update {data.updateId}</SheetTitle>
            <SheetDescription>
              {publishedAt ? `Published ${publishedAt}` : 'Legacy record'}
            </SheetDescription>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-1.5 pt-1">
          <Badge variant="outline">{platformLabel(data.platform)}</Badge>
          {isRollback ? (
            <Badge
              variant="outline"
              className="border-amber-400/25 bg-amber-400/10 text-amber-700 dark:text-amber-300">
              Rollback
            </Badge>
          ) : (
            <Badge variant="outline">Normal update</Badge>
          )}
          {rolloutActive && (
            <Badge
              variant="outline"
              className="border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300">
              <Split className="mr-1 h-3 w-3" />
              Rollout in progress
            </Badge>
          )}
        </div>
      </SheetHeader>

      <div className="space-y-5 py-4">
        {rolloutActive && (
          <div className="space-y-2 rounded-lg border border-emerald-400/25 bg-emerald-400/[0.07] p-4">
            <div className="flex items-center justify-between gap-4">
              <span className="text-sm font-medium text-emerald-800 dark:text-emerald-200">
                Progressive rollout
              </span>
              <RolloutBar value={data.rolloutPercentage as number} />
            </div>
            {data.controlUpdateId && (
              <p className="text-xs text-muted-foreground">
                Devices outside the rollout bucket keep receiving update {data.controlUpdateId}.
              </p>
            )}
          </div>
        )}

        {isUuid(data.updateUUID) && (
          <UpdateHealthHistory
            from={data.createdAt}
            live={rolloutActive}
            series={[
              {
                key: 'update',
                label: platformLabel(data.platform),
                updateUUIDs: [data.updateUUID],
                color: '#2563eb',
              },
            ]}
          />
        )}

        <DetailSection title="Deployment">
          <DetailRow label="Branch">{branch}</DetailRow>
          <DetailRow label="Runtime version">{runtimeVersion}</DetailRow>
          <DetailRow label="Platform">{platformLabel(data.platform)}</DetailRow>
          <DetailRow label="Published">
            {publishedAt || <span className="italic text-muted-foreground">Legacy record</span>}
          </DetailRow>
          {rolloutEnded && (
            <DetailRow label="Rollout">
              <span className="text-muted-foreground">
                Ended, previously gated against update {data.controlUpdateId}
              </span>
            </DetailRow>
          )}
        </DetailSection>

        <DetailSection title="Source">
          <DetailRow label="Commit">
            <MonoValue value={data.commitHash} />
            <CopyButton value={data.commitHash} label="commit hash" />
          </DetailRow>
          {data.message && (
            <div className="space-y-1 px-4 py-2.5">
              <span className="text-sm text-muted-foreground">Message</span>
              <p className="text-sm font-medium">{data.message}</p>
            </div>
          )}
        </DetailSection>

        <DetailSection title="Identifiers">
          <DetailRow label="Update ID">
            <MonoValue value={data.updateId} />
            <CopyButton value={data.updateId} label="update ID" />
          </DetailRow>
          <DetailRow label="UUID">
            <MonoValue value={data.updateUUID} />
            <CopyButton value={data.updateUUID} label="update UUID" />
          </DetailRow>
        </DetailSection>

        {expoConfig && (
          <DetailSection title="App configuration">
            {configEntries.map(([label, value]) => (
              <DetailRow key={label} label={label}>
                <span className="truncate">{value}</span>
              </DetailRow>
            ))}
            <div className="px-4 py-2.5">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="-ml-2 h-7 gap-1 px-2 text-xs text-muted-foreground hover:text-foreground"
                onClick={() => setShowRawConfig(v => !v)}>
                {showRawConfig ? (
                  <ChevronUp className="h-3.5 w-3.5" />
                ) : (
                  <ChevronDown className="h-3.5 w-3.5" />
                )}
                {showRawConfig ? 'Hide raw configuration' : 'Show raw configuration'}
              </Button>
              {showRawConfig && (
                <pre className="mt-2 max-h-72 overflow-auto rounded-lg border bg-muted/50 p-3 font-mono text-xs">
                  {JSON.stringify(expoConfig, null, 2)}
                </pre>
              )}
            </div>
          </DetailSection>
        )}
      </div>
    </>
  );
};

type Props = {
  branch?: string;
  runtimeVersion?: string;
};

export const UpdateDetailsSheet = forwardRef<UpdateDetailsRef, Props>(
  ({ branch, runtimeVersion }: Props, ref) => {
    const [currentUpdate, setCurrentUpdate] = useState<Update | null>(null);
    useImperativeHandle(ref, () => ({
      openSheet: update => {
        setCurrentUpdate(update);
      },
      closeSheet: () => {
        setCurrentUpdate(null);
      },
    }));
    return (
      <Sheet
        open={!!currentUpdate}
        defaultOpen={false}
        onOpenChange={o => {
          if (!o) {
            setCurrentUpdate(null);
          }
        }}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
          {currentUpdate ? (
            <UpdateDetailsBody
              key={`${currentUpdate.branch ?? branch ?? ''}:${currentUpdate.runtimeVersion ?? runtimeVersion ?? ''}:${currentUpdate.updateId}`}
              update={currentUpdate}
              branch={currentUpdate.branch ?? branch ?? ''}
              runtimeVersion={currentUpdate.runtimeVersion ?? runtimeVersion ?? ''}
            />
          ) : (
            <SheetHeader>
              <SheetTitle>Update details</SheetTitle>
            </SheetHeader>
          )}
        </SheetContent>
      </Sheet>
    );
  }
);
