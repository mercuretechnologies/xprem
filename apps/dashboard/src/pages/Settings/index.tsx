import {
  Activity,
  Database,
  Globe,
  HardDrive,
  LucideIcon,
  Server,
  UserRound,
  Zap,
} from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { PageHeader } from '@/components/PageHeader';
import { useSettings } from '@/lib/SettingsContext';
import clsx from 'clsx';

const Section = ({
  icon: Icon,
  title,
  children,
}: {
  icon: LucideIcon;
  title: string;
  children: React.ReactNode;
}) => (
  <section className="overflow-hidden rounded-xl border bg-card shadow-card">
    <div className="flex items-center gap-2 border-b bg-muted/40 px-5 py-3">
      <Icon className="h-4 w-4 text-muted-foreground" strokeWidth={1.75} />
      <h2 className="text-sm font-medium">{title}</h2>
    </div>
    <div className="divide-y">{children}</div>
  </section>
);

const Row = ({
  label,
  children,
  hint,
}: {
  label: string;
  children: React.ReactNode;
  hint?: string;
}) => (
  <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1 px-5 py-3.5">
    <div className="min-w-40">
      <p className="text-sm text-muted-foreground">{label}</p>
      {hint && <p className="mt-0.5 max-w-xs text-xs text-muted-foreground/70">{hint}</p>}
    </div>
    <div className="text-right text-sm">{children}</div>
  </div>
);

const Mono = ({ children }: { children: React.ReactNode }) => (
  <code className="select-all rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{children}</code>
);

const StatusDot = ({ on, children }: { on: boolean; children: React.ReactNode }) => (
  <span className="inline-flex items-center gap-2">
    <span
      className={clsx('h-2 w-2 rounded-full', on ? 'bg-emerald-500' : 'bg-muted-foreground/30')}
    />
    {children}
  </span>
);

export const Settings = () => {
  const settings = useSettings();

  const storageLabel =
    settings.STORAGE_MODE === 's3'
      ? 'Amazon S3'
      : settings.STORAGE_MODE === 'gcs'
        ? 'Google Cloud Storage'
        : settings.STORAGE_MODE === 'azure'
          ? 'Azure Blob Storage'
          : 'Local disk';

  const cacheLabel =
    settings.CACHE_MODE === 'redis'
      ? 'Redis'
      : settings.CACHE_MODE === 'redis-sentinel'
        ? 'Redis Sentinel'
        : 'In-memory';

  const prometheusEnabled = settings.PROMETHEUS_ENABLED === 'true';

  return (
    <div className="w-full">
      <PageHeader
        title="Settings"
        description="How this server is configured. Everything here applies to the whole server (not just the selected app) and comes from its environment, so it is read-only."
      />

      <div className="space-y-6">
        <Section icon={Server} title="Deployment">
          <Row
            label="Mode"
            hint={
              settings.CONTROL_PLANE_ENABLED
                ? 'Apps, branches, channels and tokens are managed from this dashboard and stored in a database.'
                : 'A single app configured through environment variables. Branches and channels live in your Expo account.'
            }>
            <Badge variant="secondary">
              {settings.CONTROL_PLANE_ENABLED ? 'Control plane' : 'Stateless'}
            </Badge>
          </Row>
          <Row label="Base URL">
            <Mono>{settings.BASE_URL || 'Not set'}</Mono>
          </Row>
        </Section>

        {!settings.CONTROL_PLANE_ENABLED && (
          <Section icon={UserRound} title="Expo account">
            <Row label="Account" hint="Resolved from the configured Expo access token.">
              {settings.EXPO_ACCOUNT_USERNAME ? (
                <span className="font-medium">@{settings.EXPO_ACCOUNT_USERNAME}</span>
              ) : (
                <span className="text-muted-foreground">
                  Could not be resolved. Check the Expo access token
                </span>
              )}
            </Row>
            {settings.APPS[0] && (
              <Row label="Expo project ID">
                <Mono>{settings.APPS[0].id}</Mono>
              </Row>
            )}
          </Section>
        )}

        <Section icon={HardDrive} title="Storage">
          <Row label="Provider" hint="Where published update bundles are stored.">
            <span className="font-medium">{storageLabel}</span>
          </Row>
          {settings.STORAGE_MODE === 's3' && (
            <>
              <Row label="Bucket">
                <Mono>{settings.S3_BUCKET_NAME || 'Not set'}</Mono>
              </Row>
              {settings.AWS_REGION && (
                <Row label="Region">
                  <Mono>{settings.AWS_REGION}</Mono>
                </Row>
              )}
              {settings.AWS_BASE_ENDPOINT && (
                <Row label="Custom endpoint" hint="S3-compatible storage (MinIO, R2…).">
                  <Mono>{settings.AWS_BASE_ENDPOINT}</Mono>
                </Row>
              )}
              {settings.AWS_ACCESS_KEY_ID && (
                <Row label="Access key">
                  <Mono>{settings.AWS_ACCESS_KEY_ID}</Mono>
                </Row>
              )}
            </>
          )}
          {settings.STORAGE_MODE === 'gcs' && (
            <Row label="Bucket">
              <Mono>{settings.GCS_BUCKET_NAME || 'Not set'}</Mono>
            </Row>
          )}
          {settings.STORAGE_MODE === 'azure' && (
            <>
              <Row label="Container">
                <Mono>{settings.AZURE_BLOB_CONTAINER_NAME || 'Not set'}</Mono>
              </Row>
              <Row label="Account">
                <Mono>{settings.AZURE_STORAGE_ACCOUNT_NAME || 'Not set'}</Mono>
              </Row>
            </>
          )}
          {settings.STORAGE_MODE !== 's3' &&
            settings.STORAGE_MODE !== 'gcs' &&
            settings.STORAGE_MODE !== 'azure' && (
              <Row label="Path" hint="Bundles live on the server's own disk.">
                <Mono>{settings.LOCAL_BUCKET_BASE_PATH || './updates'}</Mono>
              </Row>
            )}
        </Section>

        <Section icon={Zap} title="Cache">
          <Row
            label="Backend"
            hint={
              cacheLabel === 'In-memory'
                ? 'Fine for a single instance. Use Redis when running several replicas.'
                : undefined
            }>
            <span className="font-medium">{cacheLabel}</span>
          </Row>
          {settings.CACHE_MODE === 'redis' && (
            <Row label="Host">
              <Mono>
                {settings.REDIS_HOST || 'localhost'}
                {settings.REDIS_PORT ? `:${settings.REDIS_PORT}` : ''}
              </Mono>
            </Row>
          )}
          {settings.CACHE_MODE === 'redis-sentinel' && (
            <>
              <Row label="Sentinels">
                <Mono>{settings.REDIS_SENTINEL_ADDRS || 'Not set'}</Mono>
              </Row>
              <Row label="Master name">
                <Mono>{settings.REDIS_SENTINEL_MASTER_NAME || 'Not set'}</Mono>
              </Row>
            </>
          )}
        </Section>

        <Section icon={Globe} title="CDN">
          {settings.CDN_TYPE === 'cloudfront' && (
            <>
              <Row label="Provider" hint="Assets are served through signed CloudFront URLs.">
                <span className="font-medium">Amazon CloudFront</span>
              </Row>
              <Row label="Domain">
                <Mono>{settings.CLOUDFRONT_DOMAIN}</Mono>
              </Row>
              {settings.CLOUDFRONT_KEY_PAIR_ID && (
                <Row label="Key pair">
                  <Mono>{settings.CLOUDFRONT_KEY_PAIR_ID}</Mono>
                </Row>
              )}
            </>
          )}
          {settings.CDN_TYPE === 'gcs-direct' && (
            <>
              <Row
                label="Provider"
                hint="Assets are served through signed Google Cloud Storage URLs.">
                <span className="font-medium">Google Cloud Storage</span>
              </Row>
              <Row label="Bucket">
                <Mono>{settings.GCS_BUCKET_NAME}</Mono>
              </Row>
            </>
          )}
          {settings.CDN_TYPE === 's3-direct' && (
            <>
              <Row
                label="Provider"
                hint="Assets are served through presigned Amazon S3 URLs.">
                <span className="font-medium">Amazon S3</span>
              </Row>
              <Row label="Bucket">
                <Mono>{settings.S3_BUCKET_NAME}</Mono>
              </Row>
            </>
          )}
          {settings.CDN_TYPE === 'azure-direct' && (
            <>
              <Row
                label="Provider"
                hint="Assets are served through signed Azure Blob Storage URLs.">
                <span className="font-medium">Azure Blob Storage</span>
              </Row>
              <Row label="Container">
                <Mono>{settings.AZURE_BLOB_CONTAINER_NAME}</Mono>
              </Row>
            </>
          )}
          {settings.CDN_TYPE === 'generic' && (
            <>
              <Row
                label="Provider"
                hint="Assets are redirected to a CDN in front of the storage bucket.">
                <span className="font-medium">Custom CDN</span>
              </Row>
              <Row label="Base URL">
                <Mono>{settings.CDN_BASE_URL}</Mono>
              </Row>
            </>
          )}
          {!settings.CDN_TYPE && (
            <Row
              label="Provider"
              hint="Update assets are served directly by this server. Configure CloudFront or a CDN prefix to offload them.">
              <StatusDot on={false}>None</StatusDot>
            </Row>
          )}
        </Section>

        <Section icon={Activity} title="Monitoring">
          <Row
            label="Prometheus"
            hint={prometheusEnabled ? 'Metrics are exposed on /metrics.' : undefined}>
            <StatusDot on={prometheusEnabled}>
              {prometheusEnabled ? 'Enabled' : 'Disabled'}
            </StatusDot>
          </Row>
        </Section>

        {settings.CONTROL_PLANE_ENABLED && (
          <Section icon={Database} title="Apps on this server">
            {settings.APPS.length === 0 ? (
              <Row label="Apps">
                <span className="text-muted-foreground">No apps yet</span>
              </Row>
            ) : (
              settings.APPS.map(app => (
                <Row key={app.id} label={app.name || app.id}>
                  <Mono>{app.id}</Mono>
                </Row>
              ))
            )}
          </Section>
        )}
      </div>
    </div>
  );
};
