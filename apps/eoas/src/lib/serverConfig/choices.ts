// The server:init decision model: every choice the wizard collects, and the
// option sets that depend on previous answers. The env catalog
// (envCatalog.ts) and the helm generator (helmValues.ts) consume it.

export type Storage = 'aws-s3' | 's3-compatible' | 'gcs' | 'azure';
export type S3Provider = 'cloudflare-r2' | 'minio' | 'digitalocean-spaces' | 'supabase';
export type Delivery = 'cloudfront' | 'presigned' | 'through-server' | 'generic-cdn';
export type MasterKeySource = 'environment' | 'aws-secrets-manager';
export type AwsAuth = 'iam-role' | 'access-keys';
export type CacheMode = 'redis' | 'redis-sentinel' | 'local';
export type Replicas = 'single' | 'multi';
export type Deployment = 'docker' | 'binary' | 'helm';
export type GeoipStrategy = 'proxy-headers' | 'maxmind';

export type ServerChoices = {
  baseUrl?: string;
  jwtSecret?: string;
  dbUrl?: string;
  masterKeySource: MasterKeySource;
  /** Generated value, only when masterKeySource is 'environment'. */
  masterKey?: string;
  masterKeySecretId?: string;
  /** How the server authenticates against AWS (S3 and/or Secrets Manager). */
  awsAuth?: AwsAuth;
  storage: Storage;
  s3Provider?: S3Provider;
  s3BucketName?: string;
  awsRegion?: string;
  awsBaseEndpoint?: string;
  forcePathStyle?: boolean;
  gcsBucketName?: string;
  azureContainerName?: string;
  azureAccountName?: string;
  delivery: Delivery;
  cdnBaseUrl?: string;
  replicas: Replicas;
  cacheMode: CacheMode;
  redisHost?: string;
  redisPort?: string;
  /** Always true in the wizard; only inference from an existing file can turn it off. */
  dashboard?: boolean;
  adminEmail?: string;
  adminPassword?: string;
  deployment: Deployment;
  observe: boolean;
  clickhouseUrl?: string;
  geoip: boolean;
  geoipStrategy?: GeoipStrategy;
  maxmindAccountId?: string;
  maxmindLicenseKey?: string;
};

/**
 * Delivery routes available per storage backend. GCS and Azure sign URLs with
 * the storage credential itself and have no server-side switch to stream
 * assets instead, so 'through-server' only exists for the s3 family
 * (DISABLE_S3_DIRECT_CDN). Generic CDN is always listed last.
 */
export function deliveryOptionsFor(storage: Storage): Delivery[] {
  switch (storage) {
    case 'aws-s3':
      return ['cloudfront', 'presigned', 'through-server', 'generic-cdn'];
    case 's3-compatible':
      return ['presigned', 'through-server', 'generic-cdn'];
    case 'gcs':
    case 'azure':
      return ['presigned', 'generic-cdn'];
  }
}

export function cacheOptionsFor(replicas: Replicas): CacheMode[] {
  return replicas === 'multi' ? ['redis', 'redis-sentinel'] : ['redis', 'redis-sentinel', 'local'];
}

export const S3_PROVIDER_DEFAULTS: Record<
  S3Provider,
  { label: string; endpoint: string; region: string; forcePathStyle: boolean }
> = {
  'cloudflare-r2': {
    label: 'Cloudflare R2',
    endpoint: 'https://<account-id>.r2.cloudflarestorage.com',
    region: 'auto',
    forcePathStyle: false,
  },
  minio: {
    label: 'MinIO',
    endpoint: 'https://<your-minio-host>',
    region: 'us-east-1',
    forcePathStyle: true,
  },
  'digitalocean-spaces': {
    label: 'DigitalOcean Spaces',
    endpoint: 'https://<region>.digitaloceanspaces.com',
    region: 'us-east-1',
    forcePathStyle: false,
  },
  supabase: {
    label: 'Supabase Storage',
    endpoint: 'https://<project-ref>.storage.supabase.co/storage/v1/s3',
    region: '<project-region>',
    forcePathStyle: true,
  },
};

/** True when the generated env must carry AWS access keys. */
export function needsAwsAccessKeys(choices: ServerChoices): boolean {
  if (choices.storage === 's3-compatible') {
    return true;
  }
  const usesAws =
    choices.storage === 'aws-s3' ||
    choices.masterKeySource === 'aws-secrets-manager' ||
    choices.delivery === 'cloudfront';
  return usesAws && choices.awsAuth === 'access-keys';
}
