import { Credentials } from './auth';
import { fetchWithRetries } from './fetch';

// Client for the server's Expo import surface, used by `eoas init` on
// control-plane servers: admin login, dry-run preview, the import itself and
// history job polling. There is no capability probe on purpose: a server
// that cannot import (stateless, or too old) says so when asked.

export interface ExpoImportPlanItem {
  name: string;
  mappedBranch?: string;
  skipReason?: string;
  warning?: string;
}

export interface ExpoImportPlan {
  appId: string;
  name: string;
  expoName: string;
  conflict?: string;
  branches: ExpoImportPlanItem[];
  channels: ExpoImportPlanItem[];
}

export interface ExpoImportResult {
  appId: string;
  name: string;
  branchCount: number;
  channelCount: number;
  skipped?: string[];
  warnings?: string[];
  historyJobId?: string;
}

export interface ExpoHistoryJobStatus {
  state: 'running' | 'done' | 'failed' | 'canceled';
  total: number;
  processed: number;
  imported: number;
  skipped?: string[];
  error?: string;
}

export type ImportKeysConfig =
  | { mode: 'database' }
  | { mode: 'aws-secrets-manager'; publicSecretId: string; privateSecretId: string };

// The server answers errors as problem+json ({title, status, detail}).
async function serverErrorMessage(response: {
  status: number;
  text(): Promise<string>;
}): Promise<string> {
  const text = await response.text();
  try {
    const parsed = JSON.parse(text) as { detail?: string; title?: string };
    return parsed.detail || parsed.title || text;
  } catch {
    return text || `HTTP ${response.status}`;
  }
}

// loginAsAdmin trades dashboard credentials for the session token the
// admin-only import routes require.
export async function loginAsAdmin(
  baseUrl: string,
  email: string,
  password: string
): Promise<string> {
  const response = await fetchWithRetries(`${baseUrl}/auth/login`, {
    method: 'POST',
    body: new URLSearchParams({ email, password }),
  });
  if (!response.ok) {
    throw new Error(await serverErrorMessage(response));
  }
  const body = (await response.json()) as { token?: string };
  if (!body.token) {
    throw new Error('The server did not return a session token.');
  }
  return body.token;
}

// The Expo credential rides in headers, never a URL or body: the server
// takes an access token or the local expo-cli session.
function importHeaders(adminToken: string, expoCredentials: Credentials): Record<string, string> {
  const headers: Record<string, string> = { Authorization: `Bearer ${adminToken}` };
  if (expoCredentials.token) {
    headers['X-Expo-Access-Token'] = expoCredentials.token;
  } else if (expoCredentials.sessionSecret) {
    headers['expo-session'] = expoCredentials.sessionSecret;
  }
  return headers;
}

export interface ImportRequest {
  baseUrl: string;
  adminToken: string;
  expoCredentials: Credentials;
  expoAppId: string;
}

export async function fetchImportPreview(request: ImportRequest): Promise<ExpoImportPlan> {
  const url = `${request.baseUrl}/api/expo-import/preview?expoAppId=${encodeURIComponent(
    request.expoAppId
  )}`;
  const response = await fetchWithRetries(url, {
    headers: importHeaders(request.adminToken, request.expoCredentials),
  });
  if (!response.ok) {
    throw new Error(await serverErrorMessage(response));
  }
  return (await response.json()) as ExpoImportPlan;
}

export async function importExpoApp(
  request: ImportRequest,
  keysConfig: ImportKeysConfig,
  historyLimit: number
): Promise<ExpoImportResult> {
  const response = await fetchWithRetries(`${request.baseUrl}/api/expo-import`, {
    method: 'POST',
    headers: {
      ...importHeaders(request.adminToken, request.expoCredentials),
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      expoAppId: request.expoAppId,
      keysConfig,
      ...(historyLimit > 0 && { historyLimit }),
    }),
  });
  if (!response.ok) {
    throw new Error(await serverErrorMessage(response));
  }
  return (await response.json()) as ExpoImportResult;
}

export async function fetchHistoryJobStatus(
  baseUrl: string,
  adminToken: string,
  jobId: string
): Promise<ExpoHistoryJobStatus> {
  const response = await fetchWithRetries(
    `${baseUrl}/api/expo-import/jobs/${encodeURIComponent(jobId)}`,
    { headers: { Authorization: `Bearer ${adminToken}` } }
  );
  if (!response.ok) {
    throw new Error(await serverErrorMessage(response));
  }
  return (await response.json()) as ExpoHistoryJobStatus;
}
