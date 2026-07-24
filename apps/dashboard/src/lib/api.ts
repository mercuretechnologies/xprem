import { getRefreshToken, getToken, logout, setTokens } from '@/lib/auth.ts';

export type APIProblemPayload = {
  title: string;
  status: number;
  detail: string;
};

export class ApiProblemError extends Error {
  public title: string;
  public status: number;
  public detail: string;

  constructor(payload: APIProblemPayload) {
    super(payload.detail);
    this.name = 'ApiProblemError';
    this.title = payload.title;
    this.status = payload.status;
    this.detail = payload.detail;
  }
}

// Turns any thrown error into a toast-ready title/description pair: an
// ApiProblemError carries the server's actionable message, anything else
// falls back to the given title.
export const describeApiError = (
  error: unknown,
  fallbackTitle: string
): { title: string; description: string } => {
  if (error instanceof ApiProblemError) {
    return { title: error.title, description: error.detail };
  }
  return {
    title: fallbackTitle,
    description: error instanceof Error ? error.message : 'An unexpected error occurred.',
  };
};

export type KeysMode = 'local' | 'aws-secrets-manager' | 'environment' | 'database';

export type KeysConfig = {
  mode: KeysMode;
  publicPath?: string;
  privatePath?: string;
  publicSecretId?: string;
  privateSecretId?: string;
  publicB64?: string;
  privateB64?: string;
  sealedPublicKey?: string;
  sealedPrivateKey?: string;
};

export type AppDescriptor = {
  id: string;
  name?: string;
};

// One audit log entry, as served by GET /api/audit/events. Displays are
// denormalized snapshots taken at write time: they keep rendering after the
// actor or target they name is deleted.
export type AuditEventRecord = {
  id: number;
  occurredAt: string;
  actorType: 'user' | 'api_key' | 'system' | '';
  actorId?: string;
  actorDisplay: string;
  action: string;
  targetType: string;
  targetId: string;
  targetDisplay: string;
  appId?: string;
  outcome: 'success' | 'denied' | 'failure' | '';
  ip?: string;
  userAgent?: string;
  metadata?: Record<string, unknown>;
};

export type AuditEventsQuery = {
  actorId?: string;
  action?: string;
  appId?: string;
  outcome?: string;
  from?: string;
  to?: string;
  beforeId?: number;
  limit?: number;
};

export type AuditEventsPage = {
  events: AuditEventRecord[];
  // The cursor of the next page; null/absent on the last one. Sent back
  // verbatim as beforeId.
  nextCursor?: number | null;
  // The filtered total, cursor excluded.
  count: number;
};

export type AppDetails = AppDescriptor & {
  keys: KeysConfig;
  createdAt?: number;
};

export type CreateAppPayload = {
  name: string;
  keysConfig: KeysConfig;
};

export type BranchUpdateState = {
  runtimeVersion: string;
  commitHash: string;
  createdAt: string;
  rolloutPercentage?: number | null;
};

export type BranchRecord = {
  branchName: string;
  branchId: string;
  releaseChannel?: string | null;
  createdAt: string | null;
  // Enterprise branch protection; always false in stateless mode.
  protected: boolean;
  currentUpdate?: BranchUpdateState | null;
};

// An active channel rollout. Serves `rolloutBranchName` to `percentage`% of
// devices on the channel and `defaultBranchName` to the rest. `id` doubles as
// the bucketing salt on the server. Present only in control-plane mode.
export type ChannelRolloutRecord = {
  id: string;
  channelName: string;
  defaultBranchName: string;
  rolloutBranchName: string;
  percentage: number;
  createdAt: string;
  updatedAt: string;
};

export type ChannelRecord = {
  releaseChannelId: string;
  releaseChannelName: string;
  branchName?: string | null;
  branchId?: string | null;
  createdAt: string | null;
  // Set while a progressive rollout is active on the channel; null/absent
  // otherwise. The channels list carries it so the table needs no extra call.
  rollout?: ChannelRolloutRecord | null;
  branchCurrentUpdate?: BranchUpdateState | null;
  rolloutBranchCurrentUpdate?: BranchUpdateState | null;
};

export type RuntimeVersionRecord = {
  runtimeVersion: string;
  lastUpdatedAt: string;
  createdAt: string;
  numberOfUpdates: number;
  // True when a per-update rollout is active on any update of this runtime
  // version. Optional: only shipped by the control plane.
  activeRollout?: boolean;
  rolloutPercentage?: number | null;
};

// A published update. `rolloutPercentage` is set only while this exact update
// is being progressively rolled out; `controlUpdateId` points at the update
// out-of-bucket devices keep receiving during (and as a historical marker
// after) that rollout. Both are absent in stateless mode.
export type UpdateRecord = {
  updateUUID: string;
  createdAt: string;
  updateId: string;
  platform: string;
  commitHash: string;
  message?: string;
  rolloutPercentage?: number | null;
  controlUpdateId?: string | null;
  publishGroup?: string | null;
};

export type UpdateFeedRecord = UpdateRecord & {
  branch: string;
  runtimeVersion: string;
  // Current candidate, or the control still serving the out-of-bucket cohort
  // of an active progressive rollout. Historical updates are false.
  healthRelevant: boolean;
};

export type UpdateFeedQuery = {
  branch?: string;
  runtimeVersion?: string;
  platform?: string;
  uuid?: string;
  groupId?: string;
  commitHash?: string;
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
};

export type UpdateFeedPage = {
  items: UpdateFeedRecord[];
  nextCursor?: string;
};

// One active per-update rollout row (eoas publishes one per platform, so a
// single rollout on a runtime version can have up to two rows).
export type UpdateRolloutInfo = {
  updateId: string;
  platform: string;
  percentage: number;
  controlUpdateId?: string | null;
  createdAt: string;
};

// Instant-T health of one update, from the device registry (Postgres only,
// no ClickHouse needed). A manifest rollback is faulty but no longer current;
// a JS crash is faulty and still current. successfulDevices removes that
// overlap so healthPercent remains successes/(successes+faulty).
export type UpdateHealthRecord = {
  devicesOnUpdate: number;
  successfulDevices: number;
  faultyDevices: number;
  healthPercent: number | null;
};

export type UpdateHealthHistoryPoint = {
  timestamp: string;
  capturedAt: string;
  role: 'current' | 'candidate' | 'control';
  devicesOnUpdate: number;
  successfulDevices: number;
  faultyDevices: number;
  updateIssues: number;
  runtimeIssues: number;
  healthPercent: number | null;
};

export type UpdateHealthHistoryResponse = {
  available: boolean;
  updates: Record<string, UpdateHealthHistoryPoint[]>;
};

export type UpdateDetailsRecord = {
  updateUUID: string;
  createdAt: string;
  updateId: string;
  platform: string;
  commitHash: string;
  message?: string;
  type: number;
  expoConfig: string;
  rolloutPercentage?: number | null;
  controlUpdateId?: string | null;
};

export type ApiKeyRecord = {
  id: string;
  name: string;
  hint: string;
  createdAt: string;
  lastUsedAt?: string | null;
};

export type CreateApiKeyResponse = {
  apiKey: string;
};

// Enterprise per-token access restrictions (/apiKeys/restrictions,
// control-plane only). A token absent from the list is in the default state:
// no access to protected branches and no IP allowlist. Empty allowedIps means
// the token can be used from any source address.
export type ApiKeyRestrictionsRecord = {
  apiKeyId: string;
  canAccessProtectedBranches: boolean;
  allowedIps: string[];
};

// A dashboard user account. `id` is empty in stateless mode, where the only
// account comes from ADMIN_EMAIL and is not a database row. `lastConnectedAt`
// is absent until the account's first successful sign-in.
export type UserRecord = {
  id: string;
  email: string;
  isAdmin: boolean;
  // False for an account an admin revoked, or one awaiting approval under SSO
  // manual validation. Disabled accounts cannot sign in.
  enabled: boolean;
  createdAt?: string;
  lastConnectedAt?: string;
};

// A named permission bundle (enterprise user roles, ee/rbac). Roles are
// global; they apply to an app only through a user's grant.
export type RoleRecord = {
  id: string;
  name: string;
  permissions: string[];
  createdAt?: string;
  updatedAt?: string;
};

// One member's access to one app: an optional role plus direct extra
// permissions. effectivePermissions is the server-computed union the
// enforcement actually uses.
export type GrantRecord = {
  appId: string;
  roleId: string | null;
  roleName: string | null;
  extraPermissions: string[];
  effectivePermissions: string[];
};

// The deployment's Enterprise Edition license status (/api/license,
// control-plane only). `valid` is the single source of truth for "enterprise
// features are on": `hasKey` can be true with `valid` false when the stored
// key is expired or malformed, in which case `error` says why. `expiresAt` is
// absent for a perpetual license.
export type LicenseStatus = {
  hasKey: boolean;
  valid: boolean;
  error?: string;
  licenseId?: string;
  issuedAt?: string;
  expiresAt?: string;
  activatedAt?: string;
};

// Pre-auth SSO state (/auth/sso/config), read by the login page to decide
// whether to render the SSO button. `enabled` is false for every possible
// reason at once (not configured, toggled off, no valid license, stateless).
export type SsoPublicConfig = {
  enabled: boolean;
  providerName?: string;
};

// Admin view of the SSO configuration (/api/sso). The client secret never
// leaves the server: `hasClientSecret` only says whether one is stored, and
// `redirectUri` is derived from BASE_URL for copy-pasting into the IdP.
export type SsoSettings = {
  issuer: string;
  clientId: string;
  hasClientSecret: boolean;
  providerName: string;
  scopes: string;
  enabled: boolean;
  allowedEmailDomains: string[];
  allowedGroups: string[];
  groupsClaim: string;
  // Whether the server accepts an email the provider did not verify
  // (email_verified false or absent) for account lookup and authorization.
  trustUnverifiedEmail: boolean;
  // Whether accounts discovered on their first SSO sign-in are provisioned
  // disabled, waiting for an admin to approve them on the Users page.
  manualUserValidation: boolean;
  redirectUri: string;
};

// An empty `clientSecret` on an update means "keep the stored secret".
export type SaveSsoSettingsPayload = {
  issuer: string;
  clientId: string;
  clientSecret?: string;
  providerName: string;
  scopes: string;
  enabled: boolean;
  allowedEmailDomains: string[];
  allowedGroups: string[];
  groupsClaim: string;
  trustUnverifiedEmail: boolean;
  manualUserValidation: boolean;
};

// Mirror of the server's SettingsEnv payload (/api/settings). Field names are
// the raw env-var spellings on purpose; the server is the source of truth.
export type ServerSettings = {
  BASE_URL: string;
  CONTROL_PLANE_ENABLED: boolean;
  CACHE_MODE: string;
  REDIS_HOST: string;
  REDIS_PORT: string;
  REDIS_SENTINEL_ADDRS: string;
  REDIS_SENTINEL_MASTER_NAME: string;
  STORAGE_MODE: string;
  S3_BUCKET_NAME: string;
  CDN_BASE_URL: string;
  GCS_BUCKET_NAME: string;
  AZURE_BLOB_CONTAINER_NAME: string;
  AZURE_STORAGE_ACCOUNT_NAME: string;
  LOCAL_BUCKET_BASE_PATH: string;
  AWS_REGION: string;
  AWS_BASE_ENDPOINT: string;
  AWS_S3_FORCE_PATH_STYLE: string;
  AWS_ACCESS_KEY_ID: string;
  CLOUDFRONT_DOMAIN: string;
  CLOUDFRONT_KEY_PAIR_ID: string;
  PRIVATE_CLOUDFRONT_KEY_B64: string;
  AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID: string;
  PRIVATE_CLOUDFRONT_KEY_PATH: string;
  PROMETHEUS_ENABLED: string;
  CDN_TYPE: '' | 'cloudfront' | 'gcs-direct' | 'azure-direct' | 'generic';
  EXPO_ACCOUNT_USERNAME: string;
  SSO_ENABLED: boolean;
  APPS: { id: string; name?: string }[];
};

// All per-app routes (branches, channels, runtime versions, updates,
// updateChannelBranchMapping) are scoped under /api/apps/{appId} on the
// server. The dashboard keeps the currently-selected app id on the ApiClient
// instance so call sites don't all have to pass it explicitly; the
// SelectedAppContext is the single source of truth and calls setAppId()
// whenever the user switches apps.
export class ApiClient {
  private baseUrl: string;
  private appId: string | null = null;

  constructor() {
    // @ts-expect-error window.env is injected at runtime by /env.js
    this.baseUrl = window?.env?.VITE_OTA_API_URL || import.meta.env.VITE_OTA_API_URL;
    if (!this.baseUrl) {
      throw new Error('Missing VITE_OTA_API_URL environment variable');
    }
  }

  public setAppId(appId: string | null) {
    this.appId = appId;
  }

  public getAppId(): string | null {
    return this.appId;
  }

  private appScope(): string {
    if (!this.appId) {
      // Guarded separately from the server 400 so the failure mode is a
      // clear console error instead of a confusing "No app id provided"
      // coming back from the server.
      throw new Error(
        'No app selected. Set one via SelectedAppContext before making app-scoped calls.'
      );
    }
    return `/api/apps/${encodeURIComponent(this.appId)}`;
  }

  private populateHeaders(headers: Headers) {
    const token = getToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }
  }
  private async request<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`;
    const headers = new Headers(options.headers);
    this.populateHeaders(headers);

    const response = await fetch(url, { ...options, headers });
    const refreshToken = getRefreshToken();
    if (response.status === 401 && refreshToken) {
      await this.refreshTokens(refreshToken);
      return this.request<T>(endpoint, options);
    }

    if (!response.ok) {
      const contentType = response.headers.get('content-type');
      if (contentType && contentType.includes('application/problem+json')) {
        try {
          const problemPayload = (await response.json()) as APIProblemPayload;
          throw new ApiProblemError(problemPayload);
        } catch (parseError) {
          if (parseError instanceof ApiProblemError) throw parseError;
        }
      }
      throw new Error(`HTTP error! Status: ${response.status}`);
    }

    if (response.status === 204) {
      return {} as T;
    }

    return response.json() as Promise<T>;
  }

  private async refreshTokens(refreshToken: string) {
    try {
      const form = new URLSearchParams();
      form.append('refreshToken', refreshToken);
      const response = await fetch(`${this.baseUrl}/auth/refreshToken`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: form.toString(),
      });

      if (!response.ok) {
        throw new Error('Failed to refresh token');
      }

      const data = await response.json();
      setTokens(data.token, data.refreshToken);

      localStorage.setItem('accessToken', data.token);
      localStorage.setItem('refreshToken', data.refreshToken);
    } catch (error) {
      console.error('Failed to refresh token:', error);
      logout();
    }
  }

  public async login(email: string, password: string) {
    const form = new URLSearchParams();
    form.append('email', email);
    form.append('password', password);
    return this.request<{ token: string; refreshToken: string }>(`/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: form.toString(),
    });
  }

  public async getMe() {
    return this.request<UserRecord>(`/api/me`, {
      method: 'GET',
    });
  }

  // The current account's permission map (enterprise user roles, ee/rbac).
  // enabled=false means fine-grained roles are not enforced and the UI falls
  // back to the community rule: isAdmin decides everything. apps is null for
  // admins and when disabled.
  public async getMyPermissions() {
    return this.request<{
      enabled: boolean;
      isAdmin: boolean;
      apps: Record<string, string[]> | null;
    }>(`/api/me/permissions`, {
      method: 'GET',
    });
  }

  public async changeMyPassword(payload: { currentPassword: string; newPassword: string }) {
    return this.request<void>(`/api/me/password`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async getUsers() {
    return this.request<UserRecord[]>(`/api/users`, {
      method: 'GET',
    });
  }

  public async createUser(payload: { email: string; password: string; isAdmin: boolean }) {
    return this.request<UserRecord>(`/api/users`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async updateUserAdmin(userId: string, isAdmin: boolean) {
    return this.request<void>(`/api/users/${encodeURIComponent(userId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ isAdmin }),
    });
  }

  public async updateUserEnabled(userId: string, enabled: boolean) {
    return this.request<void>(`/api/users/${encodeURIComponent(userId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    });
  }

  public async deleteUser(userId: string) {
    return this.request<void>(`/api/users/${encodeURIComponent(userId)}`, {
      method: 'DELETE',
    });
  }

  // Enterprise audit log (admin only; reads work without a license so the
  // page can show its dormant state behind the enterprise gate).
  public async getAuditEvents(params: AuditEventsQuery = {}) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== null && value !== '') {
        query.set(key, String(value));
      }
    }
    const suffix = query.toString() ? `?${query.toString()}` : '';
    return this.request<AuditEventsPage>(`/api/audit/events${suffix}`, {
      method: 'GET',
    });
  }

  // Enterprise user roles (admin only; writes are license-gated server-side).
  public async getRoles() {
    return this.request<RoleRecord[]>(`/api/roles`, {
      method: 'GET',
    });
  }

  public async createRole(payload: { name: string; permissions: string[] }) {
    return this.request<RoleRecord>(`/api/roles`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async updateRole(roleId: string, payload: { name: string; permissions: string[] }) {
    return this.request<void>(`/api/roles/${encodeURIComponent(roleId)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async deleteRole(roleId: string) {
    return this.request<void>(`/api/roles/${encodeURIComponent(roleId)}`, {
      method: 'DELETE',
    });
  }

  public async getUserGrants(userId: string) {
    return this.request<GrantRecord[]>(`/api/users/${encodeURIComponent(userId)}/grants`, {
      method: 'GET',
    });
  }

  // Per-user grant counts ({userId: count}); users absent from the map hold
  // no grants. Backs the "no app access" warning on the Users page.
  public async getUserGrantsSummary() {
    return this.request<Record<string, number>>(`/api/users/grants/summary`, {
      method: 'GET',
    });
  }

  // Replaces the member's whole grant set in one transaction server-side.
  public async setUserGrants(
    userId: string,
    grants: { appId: string; roleId: string | null; extraPermissions: string[] }[]
  ) {
    return this.request<void>(`/api/users/${encodeURIComponent(userId)}/grants`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(grants),
    });
  }

  public async getLicense() {
    return this.request<LicenseStatus>(`/api/license`, {
      method: 'GET',
    });
  }

  // Pre-auth: answers whether the SSO button should show on the login page.
  public async getSsoPublicConfig() {
    return this.request<SsoPublicConfig>(`/auth/sso/config`, {
      method: 'GET',
    });
  }

  // Entry point of the SSO flow: a plain navigation, not an XHR — the server
  // answers with a redirect to the identity provider.
  public ssoLoginUrl(): string {
    return `${this.baseUrl}/auth/sso/login`;
  }

  // The callback the IdP must allow. Derived the same way the server derives
  // it from BASE_URL; the server's value (SsoSettings.redirectUri) stays the
  // source of truth once a configuration exists.
  public ssoRedirectUri(): string {
    return `${this.baseUrl}/auth/sso/callback`;
  }

  // Admin SSO configuration. `null` means "not configured yet": the card
  // shows the empty form instead of an error.
  public async getSsoSettings(): Promise<SsoSettings | null> {
    try {
      return await this.request<SsoSettings>(`/api/sso`, { method: 'GET' });
    } catch (error) {
      if (error instanceof ApiProblemError && error.status === 404) {
        return null;
      }
      throw error;
    }
  }

  public async saveSsoSettings(payload: SaveSsoSettingsPayload) {
    return this.request<SsoSettings>(`/api/sso`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async deleteSsoSettings() {
    return this.request<void>(`/api/sso`, {
      method: 'DELETE',
    });
  }

  public async activateLicense(key: string) {
    return this.request<LicenseStatus>(`/api/license`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key }),
    });
  }

  public async removeLicense() {
    return this.request<void>(`/api/license`, {
      method: 'DELETE',
    });
  }

  public async createApp(payload: CreateAppPayload) {
    return this.request<{ appId: string }>(`/api/apps`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async getApps() {
    return this.request<AppDescriptor[]>(`/api/apps`, {
      method: 'GET',
    });
  }

  public async getApp(appId: string) {
    return this.request<AppDetails>(`/api/apps/${encodeURIComponent(appId)}`, {
      method: 'GET',
    });
  }

  public async deleteApp() {
    return this.request<void>(`${this.appScope()}`, {
      method: 'DELETE',
    });
  }

  public async updateApp(payload: { name?: string }) {
    return this.request<void>(`${this.appScope()}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async getApiKeys() {
    return this.request<ApiKeyRecord[]>(`${this.appScope()}/apiKeys`, {
      method: 'GET',
    });
  }

  // Same list with an explicit app, for cross-app surfaces (the audit log's
  // actor filter) that are not bound to the selected app.
  public async getApiKeysForApp(appId: string) {
    return this.request<ApiKeyRecord[]>(`/api/apps/${encodeURIComponent(appId)}/apiKeys`, {
      method: 'GET',
    });
  }

  public async createApiKey(name: string) {
    return this.request<CreateApiKeyResponse>(`${this.appScope()}/apiKeys`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
  }

  public async revokeApiKey(apiKeyId: string) {
    return this.request<void>(`${this.appScope()}/apiKeys/${encodeURIComponent(apiKeyId)}/revoke`, {
      method: 'DELETE',
    });
  }

  public async getApiKeyRestrictions() {
    return this.request<ApiKeyRestrictionsRecord[]>(`${this.appScope()}/apiKeys/restrictions`, {
      method: 'GET',
    });
  }

  public async setApiKeyRestrictions(
    apiKeyId: string,
    restrictions: { canAccessProtectedBranches: boolean; allowedIps: string[] }
  ) {
    return this.request<void>(
      `${this.appScope()}/apiKeys/${encodeURIComponent(apiKeyId)}/restrictions`,
      {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(restrictions),
      }
    );
  }

  public async setBranchProtection(branchName: string, isProtected: boolean) {
    return this.request<void>(
      `${this.appScope()}/branches/${encodeURIComponent(branchName)}/protection`,
      {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ protected: isProtected }),
      }
    );
  }

  public async downloadAppCertificate(appId: string): Promise<string> {
    const url = `${this.baseUrl}/api/apps/${encodeURIComponent(appId)}/certificate`;
    const headers = new Headers();
    this.populateHeaders(headers);
    const response = await fetch(url, { method: 'GET', headers });
    const refreshToken = getRefreshToken();
    if (response.status === 401 && refreshToken) {
      await this.refreshTokens(refreshToken);
      return this.downloadAppCertificate(appId);
    }
    if (!response.ok) {
      throw new Error(`HTTP error! Status: ${response.status}`);
    }
    return response.text();
  }

  public async getChannels() {
    return this.request<ChannelRecord[]>(`${this.appScope()}/channels`, {
      method: 'GET',
    });
  }

  public async createChannel(payload: { branchName?: string; channelName: string }) {
    return this.request<{ channelId: string }>(`${this.appScope()}/channels`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  }

  public async deleteChannel(channelName: string) {
    return this.request<void>(`${this.appScope()}/channels/${encodeURIComponent(channelName)}`, {
      method: 'DELETE',
    });
  }

  public async getBranches() {
    return this.request<BranchRecord[]>(`${this.appScope()}/branches`, {
      method: 'GET',
    });
  }

  public async createBranch(branchName: string) {
    return this.request<{ branchId: string }>(`${this.appScope()}/branches`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ branchName }),
    });
  }

  public async deleteBranch(branchName: string) {
    return this.request<void>(`${this.appScope()}/branches/${encodeURIComponent(branchName)}`, {
      method: 'DELETE',
    });
  }

  // Remaps a release channel onto a branch. The channel id drives the remap;
  // its name is also sent because the server invalidates the channel-mapping
  // cache by name.
  public async updateChannelBranchMapping(
    branchId: string,
    payload: {
      releaseChannelId: string;
      releaseChannelName: string;
    }
  ) {
    return this.request(
      `${this.appScope()}/branch/${encodeURIComponent(branchId)}/updateChannelBranchMapping`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }
    );
  }

  public async getRuntimeVersions(branch: string) {
    return this.request<RuntimeVersionRecord[]>(
      `${this.appScope()}/branch/${encodeURIComponent(branch)}/runtimeVersions`,
      {
        method: 'GET',
      }
    );
  }
  public async getUpdates(branch: string, runtimeVersion: string) {
    return this.request<UpdateRecord[]>(
      `${this.appScope()}/branch/${encodeURIComponent(branch)}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/updates`,
      {
        method: 'GET',
      }
    );
  }
  public async getUpdateFeed(query: UpdateFeedQuery = {}) {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== '') search.set(key, String(value));
    }
    // Not URLSearchParams.size: it needs Safari 17+/Chrome 113+, above our
    // build target, and on older browsers `undefined > 0` silently drops
    // every param (filters AND the pagination cursor).
    const queryString = search.toString();
    const suffix = queryString ? `?${queryString}` : '';
    return this.request<UpdateFeedPage>(`${this.appScope()}/updates${suffix}`, {
      method: 'GET',
    });
  }
  public async getUpdateHealth(updateUUIDs: string[]) {
    return this.request<{ updates: Record<string, UpdateHealthRecord> }>(
      `${this.appScope()}/identity/update-health?ids=${encodeURIComponent(updateUUIDs.join(','))}`,
      {
        method: 'GET',
      }
    );
  }
  public async getUpdateHealthHistory(updateUUIDs: string[], from?: string, to?: string) {
    const search = new URLSearchParams({ ids: updateUUIDs.join(',') });
    if (from) search.set('from', from);
    if (to) search.set('to', to);
    return this.request<UpdateHealthHistoryResponse>(
      `${this.appScope()}/observe/update-health/history?${search.toString()}`,
      {
        method: 'GET',
      }
    );
  }
  public async getUpdateDetails(branch: string, runtimeVersion: string, updateId: string) {
    return this.request<UpdateDetailsRecord>(
      `${this.appScope()}/branch/${encodeURIComponent(branch)}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/updates/${encodeURIComponent(updateId)}`,
      {
        method: 'GET',
      }
    );
  }

  // Progressive rollout, control-plane only. Channel rollouts are keyed by
  // channel name (like the sibling channel routes); per-update rollouts by
  // branch + runtime version. Mutations are admin-only server-side.
  public async getChannelRollout(channelName: string) {
    return this.request<{ active: boolean; rollout?: ChannelRolloutRecord | null }>(
      `${this.appScope()}/channels/${encodeURIComponent(channelName)}/rollout`,
      {
        method: 'GET',
      }
    );
  }

  public async startChannelRollout(
    channelName: string,
    payload: { branchName: string; percentage: number }
  ) {
    return this.request<ChannelRolloutRecord>(
      `${this.appScope()}/channels/${encodeURIComponent(channelName)}/rollout`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }
    );
  }

  public async updateChannelRollout(channelName: string, payload: { percentage: number }) {
    return this.request<ChannelRolloutRecord>(
      `${this.appScope()}/channels/${encodeURIComponent(channelName)}/rollout`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }
    );
  }

  // outcome: 'promote' repoints the channel onto the rollout branch; 'revert'
  // discards the rollout and keeps the default branch.
  public async endChannelRollout(channelName: string, outcome: 'promote' | 'revert') {
    return this.request<void>(
      `${this.appScope()}/channels/${encodeURIComponent(channelName)}/rollout/end`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ outcome }),
      }
    );
  }

  public async getUpdateRollout(branch: string, runtimeVersion: string) {
    return this.request<{ active: boolean; updates: UpdateRolloutInfo[] }>(
      `${this.appScope()}/branch/${encodeURIComponent(branch)}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/rollout`,
      {
        method: 'GET',
      }
    );
  }

  // Sets the rollout percentage. Server accepts monotonic increases only;
  // percentage 100 finishes the rollout. `expectedUpdateId` guards against a
  // stale tab acting on a rollout that has since changed (409).
  public async setUpdateRolloutPercentage(
    branch: string,
    runtimeVersion: string,
    payload: { percentage: number; expectedUpdateId?: string }
  ) {
    return this.request<void>(
      `${this.appScope()}/branch/${encodeURIComponent(branch)}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/rollout`,
      {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }
    );
  }

  public async revertUpdateRollout(
    branch: string,
    runtimeVersion: string,
    payload: { expectedUpdateId?: string } = {}
  ) {
    return this.request<void>(
      `${this.appScope()}/branch/${encodeURIComponent(branch)}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/rollout/revert`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }
    );
  }

  public async getSettings() {
    return this.request<ServerSettings>(`/api/settings`, {
      method: 'GET',
    });
  }
}

export const api = new ApiClient();
