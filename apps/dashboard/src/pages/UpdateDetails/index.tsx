import { useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useLocation, useParams } from 'react-router';
import {
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  Package,
  Split,
  Undo2,
} from 'lucide-react';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { formatTimestamp } from '@/lib/utils';
import { PageHeader } from '@/components/PageHeader';
import { ApiError } from '@/components/APIError';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { RolloutBar } from '@/components/rollout/RolloutBar';
import { UpdateHealthHistory } from '@/ee/components/UpdateHealthHistory';
import { BundlePatchesSection } from './BundlePatchesSection';

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

export const UpdateDetails = () => {
  const params = useParams();
  const location = useLocation();
  const branch = params.branchName ?? '';
  const runtimeVersion = params.runtimeVersion ?? '';
  const updateId = params.updateId ?? '';
  const { selectedAppId } = useSelectedApp();
  const { CONTROL_PLANE_ENABLED, BUNDLE_DIFFING } = useSettings();
  const [showRawConfig, setShowRawConfig] = useState(false);
  // Keyed on what the fetch actually uses. Never updateUUID: every rollback
  // row shares the literal "Rollback to embedded", so two rollbacks from
  // different branches would collide in the cache and show mixed data.
  const { data, isLoading, error } = useQuery({
    queryKey: ['update-details', selectedAppId, branch, runtimeVersion, updateId],
    enabled: !!updateId && !!selectedAppId && !!branch && !!runtimeVersion,
    queryFn: () => api.getUpdateDetails(branch, runtimeVersion, updateId),
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

  const fromFeed = location.pathname.startsWith('/updates/');
  const feedSearch = (location.state as { search?: string } | null)?.search;
  const backLink = (
    <Link
      to={
        fromFeed
          ? `/updates${feedSearch ? `?${feedSearch}` : ''}`
          : `/branches/${encodeURIComponent(branch)}/runtime-versions/${encodeURIComponent(runtimeVersion)}`
      }
      className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="h-4 w-4" />
      {fromFeed ? (
        'All updates'
      ) : (
        <>
          <span className="truncate">{branch}</span>
          <code className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs">
            {runtimeVersion}
          </code>
        </>
      )}
    </Link>
  );

  if (isLoading || (!data && !error)) {
    return (
      <div className="w-full">
        {backLink}
        <div className="space-y-4">
          <Skeleton className="h-16 w-full rounded-xl" />
          <Skeleton className="h-40 w-full rounded-xl" />
          <Skeleton className="h-40 w-full rounded-xl" />
        </div>
      </div>
    );
  }
  if (error) {
    return (
      <div className="w-full">
        {backLink}
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
    <div className="w-full">
      {backLink}
      <PageHeader
        title={
          <span className="flex min-w-0 items-center gap-3">
            <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full border bg-muted/50 text-muted-foreground">
              {isRollback ? <Undo2 className="h-5 w-5" /> : <Package className="h-5 w-5" />}
            </span>
            <span className="truncate">Update {data.updateId}</span>
          </span>
        }
        description={
          <div className="flex flex-wrap items-center gap-2">
            <span>{publishedAt ? `Published ${publishedAt}` : 'Legacy record'}</span>
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
        }
      />

      <div className="space-y-7">
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

        <div className="grid items-start gap-6 lg:grid-cols-2">
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

        {BUNDLE_DIFFING && CONTROL_PLANE_ENABLED && !isRollback && (
          <BundlePatchesSection
            branch={branch}
            runtimeVersion={runtimeVersion}
            updateId={data.updateId}
            platform={platformLabel(data.platform)}
            origin={fromFeed ? 'updates' : 'branch'}
          />
        )}
      </div>
    </div>
  );
};
