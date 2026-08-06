import { Credentials, getAuthHeaders } from './auth';
import { fetchWithRetries } from './fetch';

// Read helpers for the server's update listing endpoints, shared by the
// republish and rollback commands (runtime version picker, update picker,
// publish group picker).

export interface RuntimeVersionInfo {
  runtimeVersion: string;
  lastUpdatedAt: string;
  createdAt: string;
  numberOfUpdates: number;
}

export interface ServerUpdateItem {
  updateUUID: string;
  createdAt: string;
  updateId: string;
  platform: string;
  commitHash: string;
  message?: string;
  publishGroup?: string;
}

export interface ServerUpdatesPage {
  items: ServerUpdateItem[];
  nextCursor: string | null;
}

export interface PublishGroupUpdateItem {
  updateId: string;
  createdAt: string;
  platform: string;
  commitHash: string;
}

export interface PublishGroupSummary {
  publishGroup: string;
  platforms: string[];
  commitHash: string;
  message?: string;
  createdAt: string;
  updates: PublishGroupUpdateItem[];
}

export interface ServerPublishGroupsPage {
  items: PublishGroupSummary[];
  nextCursor: string | null;
}

export async function fetchRuntimeVersions({
  baseUrl,
  appId,
  branch,
  credentials,
}: {
  baseUrl: string;
  appId: string;
  branch: string;
  credentials: Credentials;
}): Promise<RuntimeVersionInfo[]> {
  const response = await fetchWithRetries(
    `${baseUrl}/api/apps/${appId}/branch/${branch}/runtimeVersions`,
    {
      headers: {
        ...getAuthHeaders(credentials),
        'use-cli-auth': 'true',
      },
    }
  );
  if (!response.ok) {
    throw new Error(`Failed to fetch runtime versions: ${await response.text()}`);
  }
  return (await response.json()) as RuntimeVersionInfo[];
}

export async function fetchUpdates({
  baseUrl,
  appId,
  branch,
  runtimeVersion,
  credentials,
  cursor,
  limit = 20,
}: {
  baseUrl: string;
  appId: string;
  branch: string;
  runtimeVersion: string;
  credentials: Credentials;
  cursor?: string;
  limit?: number;
}): Promise<ServerUpdatesPage> {
  const url = new URL(
    `${baseUrl}/api/apps/${encodeURIComponent(appId)}/branch/${encodeURIComponent(
      branch
    )}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/updates`
  );
  url.searchParams.set('limit', String(limit));
  if (cursor) {
    url.searchParams.set('cursor', cursor);
  }
  const response = await fetchWithRetries(url.toString(), {
    headers: {
      ...getAuthHeaders(credentials),
      'use-cli-auth': 'true',
    },
  });
  if (!response.ok) {
    throw new Error(`Failed to fetch updates: ${await response.text()}`);
  }
  return (await response.json()) as ServerUpdatesPage;
}

// Publish groups only exist in control-plane mode. A 404 marks group mode as
// unavailable without requiring a separate capability negotiation request.
export async function fetchPublishGroups({
  baseUrl,
  appId,
  branch,
  runtimeVersion,
  credentials,
  cursor,
  limit = 20,
}: {
  baseUrl: string;
  appId: string;
  branch: string;
  runtimeVersion: string;
  credentials: Credentials;
  cursor?: string;
  limit?: number;
}): Promise<ServerPublishGroupsPage | null> {
  const url = new URL(
    `${baseUrl}/api/apps/${encodeURIComponent(appId)}/branch/${encodeURIComponent(
      branch
    )}/runtimeVersion/${encodeURIComponent(runtimeVersion)}/publish-groups`
  );
  url.searchParams.set('limit', String(limit));
  if (cursor) {
    url.searchParams.set('cursor', cursor);
  }
  const response = await fetchWithRetries(url.toString(), {
    headers: {
      ...getAuthHeaders(credentials),
      'use-cli-auth': 'true',
    },
  });
  if (response.status === 404) {
    return null;
  }
  if (!response.ok) {
    throw new Error(`Failed to fetch publish groups: ${await response.text()}`);
  }
  return (await response.json()) as ServerPublishGroupsPage;
}

// groupPublishedUpdates splits a listing into publish groups (newest first)
// and the leftover ungrouped updates (older CLIs, stateless servers). Filter
// out rollback markers before calling if they should not be offered.
export function groupPublishedUpdates(updates: ServerUpdateItem[]): {
  groups: PublishGroupSummary[];
  ungrouped: ServerUpdateItem[];
} {
  const groupsById = new Map<string, PublishGroupSummary>();
  const ungrouped: ServerUpdateItem[] = [];
  for (const update of updates) {
    if (!update.publishGroup) {
      ungrouped.push(update);
      continue;
    }
    const existing = groupsById.get(update.publishGroup);
    if (!existing) {
      groupsById.set(update.publishGroup, {
        publishGroup: update.publishGroup,
        platforms: [update.platform],
        commitHash: update.commitHash,
        message: update.message,
        createdAt: update.createdAt,
        updates: [update],
      });
      continue;
    }
    existing.updates.push(update);
    if (!existing.platforms.includes(update.platform)) {
      existing.platforms.push(update.platform);
    }
    // The freshest member dates the group in the picker.
    if (update.createdAt > existing.createdAt) {
      existing.createdAt = update.createdAt;
    }
  }
  const groups = [...groupsById.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { groups, ungrouped };
}

// A commit message can be a full paragraph; past this length the picker line
// wraps and drowns the platforms suffix.
const MAX_TITLE_MESSAGE_LENGTH = 48;

function truncateMessage(message: string): string {
  if (message.length <= MAX_TITLE_MESSAGE_LENGTH) {
    return message;
  }
  return `${message.slice(0, MAX_TITLE_MESSAGE_LENGTH - 1).trimEnd()}…`;
}

// Compact deterministic timestamp (no locale, no seconds): publishes made in
// the same run are seconds apart, minute precision is enough to tell runs
// apart without flooding the line.
function formatPublishedAt(createdAt: string): string {
  const parsed = new Date(createdAt);
  if (Number.isNaN(parsed.getTime())) {
    return createdAt;
  }
  return `${parsed.toISOString().slice(0, 16).replace('T', ' ')} UTC`;
}

// describePublishGroup renders one picker entry: a truncated message (or
// commit) plus the platforms as the title, and each sub-update with its
// platform and release time as the description.
export function describePublishGroup(group: PublishGroupSummary): {
  title: string;
  description: string;
} {
  // A publish made outside a git repository stores an empty commit hash; fall
  // back to the date so the picker never renders an empty label.
  const shortCommit = group.commitHash.slice(0, 7);
  const label = group.message?.trim()
    ? truncateMessage(group.message.trim())
    : shortCommit
      ? `Commit ${shortCommit}`
      : `Published ${formatPublishedAt(group.createdAt)}`;
  const members = group.updates
    .map(update => `${update.platform} ${formatPublishedAt(update.createdAt)}`)
    .join(', ');
  const commitSuffix = shortCommit ? ` (commit ${shortCommit})` : '';
  return {
    title: `${label} (${group.platforms.join(' + ')})`,
    description: `${members}${commitSuffix}`,
  };
}
