import originalFetch, { RequestInit, Response } from 'node-fetch';
import { validate as isUuid } from 'uuid';

import { detectServerImplementation, getAuthHeaders, retrieveCredentials } from '../auth';
import { fetchWithRetries } from '../fetch';

export type BuildPlatform = 'android' | 'ios';

export type EnvironmentSelection = { channel: string } | { environment: string };

// Returns the identifier-scoped endpoint the other build routes hang off.
export async function resolveIdentifier(
  root: string,
  platform: BuildPlatform,
  applicationId: string
): Promise<string> {
  const { identifierId } = await request<{ identifierId: string }>(
    `${root}/resolve/${platform}/${encodeURIComponent(applicationId)}`
  );
  if (!isUuid(identifierId)) {
    throw new Error('Invalid identifier resolution response.');
  }
  return `${root}/${identifierId}`;
}

export async function fetchEnvironment(
  endpoint: string,
  selection: EnvironmentSelection
): Promise<Record<string, string>> {
  const { variables } = await request<{ variables?: Record<string, unknown> }>(
    `${endpoint}/environment?${new URLSearchParams(selection)}`
  );
  if (!variables || Object.values(variables).some(value => typeof value !== 'string')) {
    throw new Error('Invalid server environment response.');
  }
  return variables as Record<string, string>;
}

// Every listed field must be a non-empty string; partial records are refused.
export async function fetchCredentials<T extends object>(
  endpoint: string,
  platform: BuildPlatform,
  fields: (keyof T & string)[],
  query: Record<string, string> = {}
): Promise<T> {
  const search = new URLSearchParams(query).toString();
  // A first iOS build has the server create a certificate and a profile at Apple before it answers.
  const credentials = await request<Record<string, unknown>>(
    `${endpoint}/credentials/${platform}${search ? `?${search}` : ''}`,
    { timeout: 180000 }
  );
  if (fields.some(field => typeof credentials[field] !== 'string' || !credentials[field])) {
    throw new Error(`Incomplete ${platform} signing credentials.`);
  }
  return credentials as T;
}

// One attempt only: an uncertain response may already have reserved the number,
// so this POST is never replayed automatically.
export async function allocateBuildNumber(endpoint: string): Promise<string> {
  const allocation = await request<{ buildNumber?: unknown }>(`${endpoint}/build-number`, {
    method: 'POST',
    retry: false,
  });
  const buildNumber = String(allocation.buildNumber);
  if (!/^[1-9][0-9]*$/.test(buildNumber)) {
    throw new Error('Invalid build number allocation response.');
  }
  return buildNumber;
}

export class BuildServerError extends Error {
  constructor(
    readonly status: number,
    detail?: string
  ) {
    super(
      detail
        ? `Build server returned HTTP ${status}: ${detail}`
        : `Build server returned HTTP ${status}. Check token permissions, identifier and environment selection.`
    );
  }
}

// The server's explanation: the detail of a problem+json answer, or a plain text body. Control
// characters are dropped and HTML error pages are ignored.
async function problemDetail(response: Response): Promise<string | undefined> {
  const body = await response.text().catch(() => '');
  let detail: unknown = body;
  try {
    detail = (JSON.parse(body) as { detail?: unknown }).detail;
  } catch {
    // Not JSON: a plain text error.
  }
  if (typeof detail !== 'string') {
    return undefined;
  }
  const sanitized = detail.replace(/[\u0000-\u001f\u007f-\u009f]/gu, ' ').trim();
  if (sanitized.startsWith('<')) {
    return undefined;
  }
  return sanitized.slice(0, 500) || undefined;
}

export async function request<T>(
  url: string,
  {
    method = 'GET',
    retry = true,
    body,
    timeout = 30000,
    signal,
  }: {
    method?: string;
    retry?: boolean;
    body?: unknown;
    timeout?: number;
    signal?: AbortSignal;
  } = {}
): Promise<T> {
  const credentials = retrieveCredentials();
  if (detectServerImplementation() !== 'eoo' || !credentials.token) {
    throw new Error('Build requires a registered EOO_TOKEN.');
  }
  const init: RequestInit = {
    method,
    headers: {
      ...getAuthHeaders(credentials),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: 'error',
    timeout,
    signal,
  };
  let response: Response;
  try {
    response = await (retry ? fetchWithRetries(url, init) : originalFetch(url, init));
  } catch (error) {
    throw new Error(
      `Build server request failed (${method})${retry ? '' : '; no automatic retry was made'}.`,
      { cause: error }
    );
  }
  if (!response.ok) {
    throw new BuildServerError(response.status, await problemDetail(response));
  }
  try {
    return (await response.json()) as T;
  } catch {
    throw new Error('Invalid build server response.');
  }
}
