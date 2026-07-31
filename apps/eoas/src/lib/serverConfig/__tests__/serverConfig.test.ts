import { describe, expect, it } from 'vitest';

import type { ServerChoices } from '../choices';
import { inferChoicesFromEnv, parseEnvFile, renderEnvFile, validateEnvMap } from '../envCatalog';
import {
  extractSecretEnv,
  parseYamlFile,
  renderHelmSecretsValues,
  renderHelmValues,
  validateHelmPair,
} from '../helmValues';
import { missingPasswordRules } from '../passwordPolicy';

const baseChoices: ServerChoices = {
  baseUrl: 'https://ota.example.com',
  jwtSecret: 'jwt-secret',
  dbUrl: 'postgresql://user:password@host:5432/xprem',
  masterKeySource: 'environment',
  masterKey: 'bWFzdGVyLWtleQ==',
  awsAuth: 'iam-role',
  storage: 'aws-s3',
  s3BucketName: 'my-bucket',
  awsRegion: 'eu-west-1',
  delivery: 'presigned',
  replicas: 'single',
  cacheMode: 'local',
  adminEmail: 'admin@example.com',
  adminPassword: 'Str0ng!pass',
  deployment: 'docker',
  observe: false,
  geoip: false,
};

describe('renderEnvFile', () => {
  it('writes only the vars the choices need', () => {
    const content = renderEnvFile(baseChoices);
    expect(content).toContain('STORAGE_MODE=s3');
    expect(content).toContain('S3_BUCKET_NAME=my-bucket');
    expect(content).toContain('DB_KEYS_MASTER_KEY_B64=bWFzdGVyLWtleQ==');
    expect(content).not.toContain('GCS_BUCKET_NAME');
    expect(content).not.toContain('AZURE_');
    expect(content).not.toContain('CLOUDFRONT');
    expect(content).not.toContain('AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID');
    // IAM role auth keeps access keys out of the file.
    expect(content).not.toContain('AWS_ACCESS_KEY_ID');
    expect(content).not.toContain('CLICKHOUSE_URL');
    expect(content).not.toContain('GEOIP_MMDB_PATH');
  });

  it('renders optional vars commented out', () => {
    const content = renderEnvFile(baseChoices);
    expect(content).toContain('# PROMETHEUS_ENABLED=true');
    expect(content).toContain('# DISABLE_DEVICE_TELEMETRY=true');
  });

  it('switches the master key and CloudFront key to Secrets Manager together', () => {
    const content = renderEnvFile({
      ...baseChoices,
      masterKeySource: 'aws-secrets-manager',
      masterKey: undefined,
      masterKeySecretId: 'xprem/db-master-key',
      delivery: 'cloudfront',
    });
    expect(content).toContain('AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID=xprem/db-master-key');
    expect(content).toContain('AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID=');
    expect(content).not.toContain('DB_KEYS_MASTER_KEY_B64=');
    expect(content).not.toContain('PRIVATE_CLOUDFRONT_KEY_B64');
  });

  it('carries the S3-compatible endpoint, access keys and path style', () => {
    const content = renderEnvFile({
      ...baseChoices,
      storage: 's3-compatible',
      s3Provider: 'minio',
      awsBaseEndpoint: 'https://minio.internal:9000',
      forcePathStyle: true,
      delivery: 'through-server',
    });
    expect(content).toContain('AWS_BASE_ENDPOINT=https://minio.internal:9000');
    expect(content).toContain('AWS_S3_FORCE_PATH_STYLE=true');
    expect(content).toContain('AWS_ACCESS_KEY_ID=');
    expect(content).toContain('DISABLE_S3_DIRECT_CDN=true');
  });
});

describe('validateEnvMap', () => {
  it('accepts a fully filled generated file', () => {
    const env = parseEnvFile(renderEnvFile(baseChoices));
    expect(validateEnvMap(env)).toEqual([]);
  });

  it('reports missing required vars for the inferred choices', () => {
    const env = parseEnvFile(renderEnvFile(baseChoices));
    delete env.JWT_SECRET;
    env.CACHE_MODE = 'redis';
    const messages = validateEnvMap(env).map(issue => issue.message);
    expect(messages.some(m => m.startsWith('JWT_SECRET is required'))).toBe(true);
    expect(messages.some(m => m.startsWith('REDIS_HOST is required'))).toBe(true);
  });

  it('rejects placeholders left in required vars', () => {
    const env = parseEnvFile(renderEnvFile({ ...baseChoices, s3BucketName: undefined }));
    const issues = validateEnvMap(env);
    expect(issues.some(i => i.level === 'error' && i.message.includes('S3_BUCKET_NAME'))).toBe(
      true
    );
  });

  it('rejects two master key sources at once', () => {
    const env = parseEnvFile(renderEnvFile(baseChoices));
    env.AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID = 'xprem/db-master-key';
    const issues = validateEnvMap(env);
    expect(issues.some(i => i.level === 'error' && i.message.includes('exactly one'))).toBe(true);
  });

  it('enforces the dashboard password policy', () => {
    const env = parseEnvFile(renderEnvFile({ ...baseChoices, adminPassword: 'weakpass' }));
    const issues = validateEnvMap(env);
    expect(issues.some(i => i.level === 'error' && i.message.includes('ADMIN_PASSWORD'))).toBe(
      true
    );
  });

  it('treats a missing redis password as a warning, not an error', () => {
    const env = parseEnvFile(
      renderEnvFile({ ...baseChoices, cacheMode: 'redis', redisHost: 'redis', redisPort: '6379' })
    );
    const issues = validateEnvMap(env);
    expect(issues.filter(i => i.level === 'error')).toEqual([]);
    expect(issues.some(i => i.level === 'warning' && i.message.includes('REDIS_PASSWORD'))).toBe(
      true
    );
  });

  it('requires DB_URL', () => {
    const env = parseEnvFile(renderEnvFile(baseChoices));
    delete env.DB_URL;
    const issues = validateEnvMap(env);
    expect(issues.some(i => i.level === 'error' && i.message.includes('DB_URL'))).toBe(true);
  });

  it('rejects a DB_URL left as a placeholder', () => {
    const env = parseEnvFile(renderEnvFile({ ...baseChoices, dbUrl: undefined }));
    const issues = validateEnvMap(env);
    expect(issues.some(i => i.level === 'error' && i.message.includes('DB_URL'))).toBe(true);
  });
});

describe('inferChoicesFromEnv', () => {
  it('detects s3-compatible storage and delivery from the vars', () => {
    const choices = inferChoicesFromEnv({
      STORAGE_MODE: 's3',
      AWS_BASE_ENDPOINT: 'https://minio.internal:9000',
      CDN_BASE_URL: 'https://cdn.example.com',
      AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID: 'xprem/db-master-key',
    });
    expect(choices.storage).toBe('s3-compatible');
    expect(choices.delivery).toBe('generic-cdn');
    expect(choices.masterKeySource).toBe('aws-secrets-manager');
  });
});

describe('helm values', () => {
  const helmChoices: ServerChoices = {
    ...baseChoices,
    deployment: 'helm',
    replicas: 'multi',
    cacheMode: 'redis',
    redisHost: 'redis.internal',
    redisPort: '6379',
  };

  const renderPair = (
    choices: ServerChoices
  ): [Record<string, unknown>, Record<string, string> | undefined] => [
    parseYamlFile(renderHelmValues(choices)),
    extractSecretEnv(parseYamlFile(renderHelmSecretsValues(choices))),
  ];

  it('keeps every secret value out of the values file', () => {
    const content = renderHelmValues(helmChoices);
    expect(content).toContain('secretName: "xprem-secrets"');
    expect(content).not.toContain('jwt-secret');
    expect(content).not.toContain('Str0ng!pass');
    expect(content).not.toContain('bWFzdGVyLWtleQ==');
  });

  it('keeps the toggle-computed vars out of the secrets overlay', () => {
    const content = renderHelmSecretsValues(helmChoices);
    expect(content).toContain('secretEnv:');
    expect(content).not.toContain('STORAGE_MODE:');
    expect(content).not.toContain('CACHE_MODE:');
    expect(content).not.toContain('USE_DASHBOARD:');
    expect(content).toContain('JWT_SECRET: "jwt-secret"');
  });

  it('round-trips the pair through parse and validate without errors', () => {
    const [values, secretEnv] = renderPair(helmChoices);
    expect(values.replicaCount).toBe(3);
    expect(values.controlPlane).toBe('true');
    const issues = validateHelmPair(values, secretEnv);
    expect(issues.filter(i => i.level === 'error')).toEqual([]);
  });

  it('rejects a local cache with several replicas', () => {
    const [values, secretEnv] = renderPair(helmChoices);
    values.cacheMode = 'local';
    const issues = validateHelmPair(values, secretEnv);
    expect(issues.some(i => i.level === 'error' && i.message.includes('cacheMode'))).toBe(true);
  });

  it('only warns on stateless values files', () => {
    const issues = validateHelmPair({ controlPlane: 'false' }, undefined);
    expect(issues).toHaveLength(1);
    expect(issues[0].level).toBe('warning');
  });

  it('resolves computed entries through their toggle', () => {
    const [values, secretEnv] = renderPair(helmChoices);
    const issues = validateHelmPair({ ...values, cacheMode: 'redis-sentinel' }, secretEnv);
    expect(
      issues.some(i => i.level === 'error' && i.message.startsWith('REDIS_SENTINEL_ADDRS'))
    ).toBe(true);
  });

  it('reports a missing secrets overlay as an error', () => {
    const [values] = renderPair(helmChoices);
    const issues = validateHelmPair(values, undefined);
    expect(issues.some(i => i.level === 'error' && i.message.includes('secrets.yaml'))).toBe(true);
  });

  it('checks a secrets overlay alone at the value level', () => {
    const [, secretEnv] = renderPair({ ...helmChoices, adminPassword: 'weakpass' });
    const issues = validateHelmPair(undefined, secretEnv);
    expect(
      issues.some(i => i.level === 'warning' && i.message.includes('No chart values file'))
    ).toBe(true);
    expect(issues.some(i => i.level === 'error' && i.message.includes('ADMIN_PASSWORD'))).toBe(
      true
    );
    // REDIS_PASSWORD is lenient, so its placeholder stays a warning here too.
    expect(issues.some(i => i.level === 'warning' && i.message.includes('REDIS_PASSWORD'))).toBe(
      true
    );
  });
});

describe('missingPasswordRules', () => {
  it('mirrors the server rune counting and unicode classes', () => {
    expect(missingPasswordRules('Str0ng!pass')).toEqual([]);
    // 8 code points, accented letters count as letters, not specials.
    expect(missingPasswordRules('Ääää123!')).toEqual([]);
    expect(missingPasswordRules('weakpass')).toEqual([
      'an uppercase letter',
      'a digit',
      'a special character',
    ]);
  });
});
