// Single source of truth for the server env vars server:init writes and
// server:validate checks. Every var declares when it applies to a set of
// choices, whether the server needs it to boot, and the value or
// <placeholder> to write. Variable names and rules mirror the server config
// (config/config.go, internal/cdn, helm/values.yaml).

import {
  type CacheMode,
  type Delivery,
  type MasterKeySource,
  type ServerChoices,
  type Storage,
  needsAwsAccessKeys,
} from './choices';
import { missingPasswordRules } from './passwordPolicy';

export type EnvVarSpec = {
  name: string;
  applies: (c: ServerChoices) => boolean;
  /** Required to boot the server; false renders the var commented out. */
  required: boolean;
  value: (c: ServerChoices) => string;
  comment?: string;
  /** Helm values key when the chart computes this var from a toggle. */
  helmKey?: string;
  /** Missing or placeholder value is a warning, not an error (the server boots without it). */
  lenient?: boolean;
};

export type EnvSection = {
  title: string;
  note?: (c: ServerChoices) => string | undefined;
  vars: EnvVarSpec[];
};

const isS3Family = (c: ServerChoices): boolean =>
  c.storage === 'aws-s3' || c.storage === 's3-compatible';

const or = (value: string | undefined, placeholder: string): string =>
  value && value.trim() !== '' ? value : placeholder;

export const ENV_SECTIONS: EnvSection[] = [
  {
    title: 'Server',
    vars: [
      {
        name: 'BASE_URL',
        applies: () => true,
        required: true,
        value: c => or(c.baseUrl, '<https://your-ota-domain>'),
        comment:
          'Public HTTPS URL of the server, including a path prefix if the gateway mounts it under one (e.g. https://api.example.com/ota).',
      },
      {
        name: 'SERVE_FROM_SUB_PATH',
        applies: c => c.serveFromSubPath === true,
        required: true,
        value: () => 'true',
        comment:
          "Serve every route under the BASE_URL path prefix. Remove when the gateway strips the prefix before forwarding (it only affects routing; links always use BASE_URL's path).",
      },
      {
        name: 'JWT_SECRET',
        applies: () => true,
        required: true,
        value: c => or(c.jwtSecret, '<openssl rand -base64 32>'),
        comment: 'Signs dashboard sessions and upload tokens.',
      },
    ],
  },
  {
    title: 'Database (control plane)',
    vars: [
      {
        name: 'DB_URL',
        applies: () => true,
        required: true,
        value: c => or(c.dbUrl, '<postgresql://user:password@host:5432/xprem>'),
        comment: 'The server runs its schema migrations itself, but never creates the database.',
      },
      {
        name: 'DB_KEYS_MASTER_KEY_B64',
        applies: c => c.masterKeySource === 'environment',
        required: true,
        value: c => or(c.masterKey, '<openssl rand -base64 32>'),
        comment: 'Seals the per-app signing keys in Postgres. Back it up, it is not recoverable.',
      },
      {
        name: 'AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID',
        applies: c => c.masterKeySource === 'aws-secrets-manager',
        required: true,
        value: c => or(c.masterKeySecretId, 'xprem/db-master-key'),
        comment: 'Secrets Manager secret holding the master key (openssl rand -base64 32).',
      },
    ],
  },
  {
    title: 'AWS credentials',
    note: c =>
      !needsAwsAccessKeys(c) &&
      (c.storage === 'aws-s3' ||
        c.masterKeySource === 'aws-secrets-manager' ||
        c.delivery === 'cloudfront')
        ? 'AWS auth comes from the IAM role attached to the runtime; no keys in the env.'
        : undefined,
    vars: [
      {
        name: 'AWS_ACCESS_KEY_ID',
        applies: needsAwsAccessKeys,
        required: true,
        value: () => '<your-access-key-id>',
      },
      {
        name: 'AWS_SECRET_ACCESS_KEY',
        applies: needsAwsAccessKeys,
        required: true,
        value: () => '<your-secret-access-key>',
      },
    ],
  },
  {
    title: 'Storage',
    vars: [
      {
        name: 'STORAGE_MODE',
        applies: () => true,
        required: true,
        value: c => (c.storage === 'gcs' ? 'gcs' : c.storage === 'azure' ? 'azure' : 's3'),
        helmKey: 'storageMode',
      },
      {
        name: 'S3_BUCKET_NAME',
        applies: isS3Family,
        required: true,
        value: c => or(c.s3BucketName, '<your-bucket-name>'),
      },
      {
        name: 'AWS_REGION',
        applies: isS3Family,
        required: true,
        value: c => or(c.awsRegion, 'eu-west-1'),
      },
      {
        name: 'AWS_BASE_ENDPOINT',
        applies: c => c.storage === 's3-compatible',
        required: true,
        value: c => or(c.awsBaseEndpoint, '<https://your-s3-endpoint>'),
        comment: "The provider's S3 API endpoint.",
      },
      {
        name: 'AWS_S3_FORCE_PATH_STYLE',
        applies: c => c.forcePathStyle === true,
        required: true,
        value: () => 'true',
      },
      {
        name: 'GCS_BUCKET_NAME',
        applies: c => c.storage === 'gcs',
        required: true,
        value: c => or(c.gcsBucketName, '<your-bucket-name>'),
      },
      {
        name: 'GOOGLE_APPLICATION_CREDENTIALS_B64',
        applies: c => c.storage === 'gcs',
        required: true,
        value: () => '<base64 service-account.json>',
        comment: 'Service account with Storage Admin on the bucket; also signs delivery URLs.',
      },
      {
        name: 'AZURE_BLOB_CONTAINER_NAME',
        applies: c => c.storage === 'azure',
        required: true,
        value: c => or(c.azureContainerName, '<your-container-name>'),
      },
      {
        name: 'AZURE_STORAGE_ACCOUNT_NAME',
        applies: c => c.storage === 'azure',
        required: true,
        value: c => or(c.azureAccountName, '<your-account-name>'),
      },
      {
        name: 'AZURE_STORAGE_ACCOUNT_KEY',
        applies: c => c.storage === 'azure',
        required: true,
        value: () => '<account access key>',
        comment: 'Authenticates the server and signs the SAS delivery URLs.',
      },
    ],
  },
  {
    title: 'CDN & Assets Delivery',
    note: c =>
      c.delivery === 'presigned'
        ? 'Nothing to configure: the server signs short-lived URLs straight into the private bucket.'
        : undefined,
    vars: [
      {
        name: 'CLOUDFRONT_DOMAIN',
        applies: c => c.delivery === 'cloudfront',
        required: true,
        value: () => '<your-cloudfront-domain>',
      },
      {
        name: 'CLOUDFRONT_KEY_PAIR_ID',
        applies: c => c.delivery === 'cloudfront',
        required: true,
        value: () => '<your-public-key-id>',
      },
      {
        name: 'PRIVATE_CLOUDFRONT_KEY_B64',
        applies: c => c.delivery === 'cloudfront' && c.masterKeySource === 'environment',
        required: true,
        value: () => '<base64 private_key.pem>',
      },
      {
        name: 'AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID',
        applies: c => c.delivery === 'cloudfront' && c.masterKeySource === 'aws-secrets-manager',
        required: true,
        value: () => 'xprem/cloudfront-private-key',
      },
      {
        name: 'DISABLE_S3_DIRECT_CDN',
        applies: c => c.delivery === 'through-server',
        required: true,
        value: () => 'true',
        comment: 'Assets stream through the server instead of redirecting to signed bucket URLs.',
      },
      {
        name: 'CDN_BASE_URL',
        applies: c => c.delivery === 'generic-cdn',
        required: true,
        value: c => or(c.cdnBaseUrl, '<https://cdn.example.com>'),
        comment: 'Base URL of the CDN fronting the bucket; the bucket must be publicly readable.',
      },
    ],
  },
  {
    title: 'Cache',
    vars: [
      {
        name: 'CACHE_MODE',
        applies: () => true,
        required: true,
        value: c => c.cacheMode,
        helmKey: 'cacheMode',
      },
      {
        name: 'REDIS_HOST',
        applies: c => c.cacheMode === 'redis',
        required: true,
        value: c => or(c.redisHost, '<your-redis-host>'),
      },
      {
        name: 'REDIS_PORT',
        applies: c => c.cacheMode === 'redis',
        required: true,
        value: c => or(c.redisPort, '6379'),
      },
      {
        name: 'REDIS_SENTINEL_ADDRS',
        applies: c => c.cacheMode === 'redis-sentinel',
        required: true,
        value: () => '<sentinel-0:26379,sentinel-1:26379>',
        comment: 'Comma-separated, at least one.',
      },
      {
        name: 'REDIS_SENTINEL_MASTER_NAME',
        applies: c => c.cacheMode === 'redis-sentinel',
        required: false,
        value: () => 'mymaster',
      },
      {
        name: 'REDIS_PASSWORD',
        applies: c => c.cacheMode !== 'local',
        required: true,
        lenient: true,
        value: () => '<your-redis-password>',
        comment: 'Leave empty only if your Redis accepts unauthenticated connections.',
      },
      {
        name: 'REDIS_USERNAME',
        applies: c => c.cacheMode !== 'local',
        required: false,
        value: () => '',
      },
      {
        name: 'REDIS_USE_TLS',
        applies: c => c.cacheMode !== 'local',
        required: false,
        value: () => 'true',
      },
      {
        name: 'CACHE_KEY_PREFIX',
        applies: c => c.cacheMode !== 'local',
        required: false,
        value: () => '',
        comment: 'Prefix on every cache key, for a Redis shared with other apps.',
      },
    ],
  },
  {
    title: 'Dashboard',
    vars: [
      {
        name: 'USE_DASHBOARD',
        applies: c => c.dashboard !== false,
        required: true,
        value: () => 'true',
        helmKey: 'useDashboard',
      },
      {
        name: 'ADMIN_EMAIL',
        applies: c => c.dashboard !== false,
        required: true,
        value: c => or(c.adminEmail, '<you@example.com>'),
        comment: 'Seeds the first admin at first boot; removable after.',
      },
      {
        name: 'ADMIN_PASSWORD',
        applies: c => c.dashboard !== false,
        required: true,
        value: c => or(c.adminPassword, '<strong-password>'),
        comment:
          'Policy: 8+ characters, an uppercase, a lowercase, a digit and a special character.',
      },
      {
        name: 'DASHBOARD_ROOT_REDIRECT',
        applies: c => c.dashboard !== false,
        required: false,
        value: () => 'true',
        comment:
          'Sends / to the dashboard login. Off by default: the root stays a 404 rather than pointing every visitor at the admin UI.',
      },
    ],
  },
  {
    title: 'Observe',
    vars: [
      {
        name: 'CLICKHOUSE_URL',
        applies: c => c.observe,
        required: true,
        value: c => or(c.clickhouseUrl, '<clickhouse://user:password@host:9000/xprem>'),
        comment: 'Enables telemetry ingestion and update-health history.',
      },
    ],
  },
  {
    title: 'Geolocation',
    note: c =>
      c.geoip && c.geoipStrategy === 'proxy-headers'
        ? 'GEOIP_HEADER_COUNTRY/CITY/LATITUDE/LONGITUDE replace the recognized header names when your proxy uses custom ones.'
        : undefined,
    vars: [
      {
        name: 'TRUST_GEOIP_HEADERS',
        applies: c => c.geoip && c.geoipStrategy === 'proxy-headers',
        required: true,
        value: () => 'true',
        comment:
          'Locate devices from the visitor-location headers of your proxy or CDN (Cloudflare, CloudFront, Vercel, X-Geo-*). Only safe when the server is reachable exclusively through it.',
      },
      {
        name: 'MAXMIND_ACCOUNT_ID',
        applies: c => c.geoip && c.geoipStrategy === 'maxmind',
        required: true,
        value: c => or(c.maxmindAccountId, '<maxmind-account-id>'),
        comment:
          'The server downloads the free GeoLite2 City database itself at startup; both MAXMIND_* variables must be set together.',
      },
      {
        name: 'MAXMIND_LICENSE_KEY',
        applies: c => c.geoip && c.geoipStrategy === 'maxmind',
        required: true,
        value: c => or(c.maxmindLicenseKey, '<maxmind-license-key>'),
        comment: 'Generate one in the MaxMind account portal (free GeoLite2 signup).',
      },
      {
        name: 'GEOIP_CACHE_DIR',
        applies: c => c.geoip && c.geoipStrategy === 'maxmind',
        required: false,
        value: () => '',
        comment:
          'Where the downloaded database is cached; point it at a writable volume when the filesystem is read-only.',
      },
    ],
  },
  {
    title: 'Optional extras',
    vars: [
      {
        name: 'PORT',
        applies: () => true,
        required: false,
        value: () => '3000',
      },
      {
        name: 'PROMETHEUS_ENABLED',
        applies: () => true,
        required: false,
        value: () => 'true',
        comment: 'Exposes /metrics; keep the route private at the proxy or ingress.',
      },
      {
        name: 'TRUST_PROXY_HEADERS',
        applies: () => true,
        required: false,
        value: () => 'true',
        comment: 'Read the client IP from X-Forwarded-For when behind a trusted proxy.',
      },
      {
        name: 'BUCKET_KEY_PREFIX',
        applies: () => true,
        required: false,
        value: () => '',
        comment: 'Key prefix inside the bucket, for a bucket shared with other data.',
      },
      {
        name: 'AZURE_BLOB_ENDPOINT',
        applies: c => c.storage === 'azure',
        required: false,
        value: () => '',
        comment: 'Blob service URL override (Azurite, private endpoints).',
      },
      {
        name: 'DISABLE_DEVICE_TELEMETRY',
        applies: c => !c.observe,
        required: false,
        value: () => 'true',
        comment: 'Record nothing about devices: no registry, no update health, no telemetry.',
      },
    ],
  },
];

export function applicableVars(choices: ServerChoices): EnvVarSpec[] {
  return ENV_SECTIONS.flatMap(section => section.vars.filter(spec => spec.applies(choices)));
}

const PLACEHOLDER_PATTERN = /<[^>]+>/;

export function isPlaceholder(value: string): boolean {
  return PLACEHOLDER_PATTERN.test(value);
}

/** Renders the .env.xprem content for the wizard's choices. */
export function renderEnvFile(choices: ServerChoices): string {
  const lines: string[] = [
    '# Generated by eoas server:init.',
    '# Fill every <placeholder>, then check the result with: eoas server:validate .env.xprem',
    '',
  ];
  for (const section of ENV_SECTIONS) {
    const vars = section.vars.filter(spec => spec.applies(choices));
    const note = section.note?.(choices);
    if (vars.length === 0 && !note) {
      continue;
    }
    lines.push(`# ----- ${section.title} -----`);
    if (note) {
      lines.push(`# ${note}`);
    }
    for (const spec of vars) {
      if (spec.comment) {
        lines.push(`# ${spec.comment}`);
      }
      const assignment = `${spec.name}=${spec.value(choices)}`;
      lines.push(spec.required ? assignment : `# ${assignment}`);
    }
    lines.push('');
  }
  return lines.join('\n');
}

/** Parses a dotenv-style file into a name/value map. Commented vars are skipped. */
export function parseEnvFile(content: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line === '' || line.startsWith('#')) {
      continue;
    }
    const match = line.match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/);
    if (!match) {
      continue;
    }
    let value = match[2].trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    env[match[1]] = value;
  }
  return env;
}

export type ValidationIssue = {
  level: 'error' | 'warning';
  message: string;
};

function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === 'http:' || url.protocol === 'https:';
  } catch {
    return false;
  }
}

/**
 * Rebuilds the wizard choices implied by an env map, so the catalog's
 * applicability rules can run against an existing file.
 */
export function inferChoicesFromEnv(env: Record<string, string>): ServerChoices {
  const storageMode = env.STORAGE_MODE ?? '';
  const storage: Storage =
    storageMode === 'gcs'
      ? 'gcs'
      : storageMode === 'azure'
        ? 'azure'
        : env.AWS_BASE_ENDPOINT
          ? 's3-compatible'
          : 'aws-s3';
  const delivery: Delivery =
    env.CLOUDFRONT_DOMAIN || env.CLOUDFRONT_KEY_PAIR_ID
      ? 'cloudfront'
      : env.CDN_BASE_URL
        ? 'generic-cdn'
        : env.DISABLE_S3_DIRECT_CDN === 'true'
          ? 'through-server'
          : 'presigned';
  const masterKeySource: MasterKeySource = env.AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID
    ? 'aws-secrets-manager'
    : 'environment';
  const cacheMode = (env.CACHE_MODE ?? 'local') as CacheMode;
  return {
    baseUrl: env.BASE_URL,
    serveFromSubPath: env.SERVE_FROM_SUB_PATH === 'true',
    jwtSecret: env.JWT_SECRET,
    dbUrl: env.DB_URL,
    masterKeySource,
    masterKey: env.DB_KEYS_MASTER_KEY_B64,
    masterKeySecretId: env.AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID,
    awsAuth: env.AWS_ACCESS_KEY_ID ? 'access-keys' : 'iam-role',
    storage,
    forcePathStyle: env.AWS_S3_FORCE_PATH_STYLE === 'true',
    delivery,
    replicas: 'single',
    cacheMode,
    dashboard: env.USE_DASHBOARD === 'true',
    adminEmail: env.ADMIN_EMAIL,
    adminPassword: env.ADMIN_PASSWORD,
    deployment: 'docker',
    observe: !!env.CLICKHOUSE_URL,
    geoip:
      env.TRUST_GEOIP_HEADERS === 'true' || !!env.MAXMIND_ACCOUNT_ID || !!env.MAXMIND_LICENSE_KEY,
    // Headers win over MaxMind server-side; the inference mirrors that.
    geoipStrategy:
      env.TRUST_GEOIP_HEADERS === 'true'
        ? 'proxy-headers'
        : env.MAXMIND_ACCOUNT_ID || env.MAXMIND_LICENSE_KEY
          ? 'maxmind'
          : undefined,
    maxmindAccountId: env.MAXMIND_ACCOUNT_ID,
    maxmindLicenseKey: env.MAXMIND_LICENSE_KEY,
  };
}

const VALID_STORAGE_MODES = ['s3', 'gcs', 'azure', 'local'];
const VALID_CACHE_MODES: CacheMode[] = ['redis', 'redis-sentinel', 'local'];

/**
 * Validates an env map against the catalog: every applicable required var is
 * present, no placeholder is left, and the server's own consistency rules
 * hold. Local storage mode is accepted (it has working defaults) even though
 * the wizard never generates it.
 */
export function validateEnvMap(
  env: Record<string, string>,
  overrides?: Partial<ServerChoices>
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const error = (message: string): void => {
    issues.push({ level: 'error', message });
  };
  const warning = (message: string): void => {
    issues.push({ level: 'warning', message });
  };

  if (!env.DB_URL) {
    error(
      'DB_URL is not set. server:validate checks control-plane configurations; the server needs a PostgreSQL connection string to run as a control plane.'
    );
  } else if (isPlaceholder(env.DB_URL)) {
    error(`DB_URL is still a placeholder: ${env.DB_URL}`);
  }

  const storageMode = env.STORAGE_MODE ?? '';
  if (!VALID_STORAGE_MODES.includes(storageMode)) {
    error(
      `STORAGE_MODE must be one of ${VALID_STORAGE_MODES.join(', ')} (got "${
        storageMode || 'nothing'
      }").`
    );
  }
  if (env.CACHE_MODE && !VALID_CACHE_MODES.includes(env.CACHE_MODE as CacheMode)) {
    error(`CACHE_MODE must be one of ${VALID_CACHE_MODES.join(', ')} (got "${env.CACHE_MODE}").`);
  }

  const choices = { ...inferChoicesFromEnv(env), ...overrides };
  if (choices.dashboard === false) {
    warning(
      'USE_DASHBOARD is not "true": a control plane without the dashboard cannot manage apps, keys or channels.'
    );
  }
  const skipStorageVars = storageMode === 'local';
  for (const spec of applicableVars(choices)) {
    if (spec.name === 'DB_URL' || spec.name === 'STORAGE_MODE') {
      continue; // Reported above with a fuller message.
    }
    const storageVar =
      spec.name.startsWith('S3_') ||
      spec.name.startsWith('AWS_') ||
      spec.name.startsWith('GCS_') ||
      spec.name.startsWith('AZURE_') ||
      spec.name === 'GOOGLE_APPLICATION_CREDENTIALS_B64';
    if (skipStorageVars && storageVar) {
      continue;
    }
    const value = env[spec.name];
    const report = spec.required && !spec.lenient ? error : warning;
    if (spec.required && (value === undefined || value === '')) {
      report(`${spec.name} is required${spec.comment ? ` (${spec.comment})` : ''}.`);
    } else if (value !== undefined && isPlaceholder(value)) {
      report(`${spec.name} is still a placeholder: ${value}`);
    }
  }

  if (env.DB_KEYS_MASTER_KEY_B64 && env.AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID) {
    error(
      'Both DB_KEYS_MASTER_KEY_B64 and AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID are set; the server requires exactly one master key source.'
    );
  }

  if (!!env.MAXMIND_ACCOUNT_ID !== !!env.MAXMIND_LICENSE_KEY) {
    error(
      'MAXMIND_ACCOUNT_ID and MAXMIND_LICENSE_KEY must be set together; the server refuses to start with only one of them.'
    );
  }

  if (env.BASE_URL && !isPlaceholder(env.BASE_URL) && !isHttpUrl(env.BASE_URL)) {
    error(`BASE_URL must be a valid http(s) URL (got "${env.BASE_URL}").`);
  }

  if (
    env.ADMIN_EMAIL &&
    !isPlaceholder(env.ADMIN_EMAIL) &&
    !/^\S+@\S+\.\S+$/.test(env.ADMIN_EMAIL)
  ) {
    error(`ADMIN_EMAIL does not look like an email address (got "${env.ADMIN_EMAIL}").`);
  }

  if (env.ADMIN_PASSWORD && !isPlaceholder(env.ADMIN_PASSWORD)) {
    const missing = missingPasswordRules(env.ADMIN_PASSWORD);
    if (missing.length > 0) {
      error(
        `ADMIN_PASSWORD does not meet the dashboard password policy, it needs ${missing.join(
          ', '
        )}. The first boot fails otherwise.`
      );
    }
  }

  return issues;
}
