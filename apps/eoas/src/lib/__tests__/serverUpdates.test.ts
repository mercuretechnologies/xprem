import { describe, expect, it, vi } from 'vitest';

import { fetchWithRetries } from '../fetch';
import {
  ServerUpdateItem,
  describePublishGroup,
  fetchPublishGroups,
  fetchRuntimeVersions,
  fetchUpdates,
  groupPublishedUpdates,
} from '../serverUpdates';

vi.mock('../fetch', () => ({
  fetchWithRetries: vi.fn(),
}));

const credentials = { token: 'test-token', sessionSecret: undefined };

function update(overrides: Partial<ServerUpdateItem>): ServerUpdateItem {
  return {
    updateUUID: 'uuid',
    createdAt: '2026-07-24T10:00:00Z',
    updateId: '1',
    platform: 'ios',
    commitHash: 'abc1234def',
    ...overrides,
  };
}

describe('groupPublishedUpdates', () => {
  it('splits grouped updates from ungrouped ones', () => {
    const { groups, ungrouped } = groupPublishedUpdates([
      update({ updateId: '1', platform: 'ios', publishGroup: 'group-a' }),
      update({ updateId: '2', platform: 'android', publishGroup: 'group-a' }),
      update({ updateId: '3', platform: 'ios' }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].publishGroup).toBe('group-a');
    expect(groups[0].platforms).toEqual(['ios', 'android']);
    expect(groups[0].updates).toHaveLength(2);
    expect(ungrouped).toHaveLength(1);
    expect(ungrouped[0].updateId).toBe('3');
  });

  it('sorts groups newest first and dates each group by its freshest member', () => {
    const { groups } = groupPublishedUpdates([
      update({ updateId: '1', publishGroup: 'old', createdAt: '2026-07-20T10:00:00Z' }),
      update({ updateId: '2', publishGroup: 'recent', createdAt: '2026-07-22T10:00:00Z' }),
      update({
        updateId: '3',
        platform: 'android',
        publishGroup: 'recent',
        createdAt: '2026-07-23T10:00:00Z',
      }),
    ]);
    expect(groups.map(group => group.publishGroup)).toEqual(['recent', 'old']);
    expect(groups[0].createdAt).toBe('2026-07-23T10:00:00Z');
  });

  it('does not duplicate a platform listed twice in one group', () => {
    const { groups } = groupPublishedUpdates([
      update({ updateId: '1', platform: 'ios', publishGroup: 'group-a' }),
      update({ updateId: '2', platform: 'ios', publishGroup: 'group-a' }),
    ]);
    expect(groups[0].platforms).toEqual(['ios']);
    expect(groups[0].updates).toHaveLength(2);
  });
});

describe('describePublishGroup', () => {
  it('titles the group with its message and platforms', () => {
    const { groups } = groupPublishedUpdates([
      update({ publishGroup: 'group-a', message: 'Fix crash on startup' }),
      update({ platform: 'android', publishGroup: 'group-a', message: 'Fix crash on startup' }),
    ]);
    const { title, description } = describePublishGroup(groups[0]);
    expect(title).toBe('Fix crash on startup (ios + android)');
    expect(description).toContain('abc1234');
  });

  it('truncates long messages so the platforms stay visible', () => {
    const longMessage =
      'Fix the crash on startup that happens when the bundle cache is corrupted after an OTA';
    const { groups } = groupPublishedUpdates([
      update({ publishGroup: 'group-a', message: longMessage }),
    ]);
    const { title } = describePublishGroup(groups[0]);
    expect(title).toBe('Fix the crash on startup that happens when the… (ios)');
    expect(title.length).toBeLessThan(longMessage.length);
  });

  it('describes each sub-update with its platform and release time', () => {
    const { groups } = groupPublishedUpdates([
      update({ publishGroup: 'group-a', createdAt: '2026-07-24T10:00:12Z' }),
      update({
        platform: 'android',
        publishGroup: 'group-a',
        createdAt: '2026-07-24T10:01:47Z',
      }),
    ]);
    const { description } = describePublishGroup(groups[0]);
    expect(description).toBe(
      'ios 2026-07-24 10:00 UTC, android 2026-07-24 10:01 UTC (commit abc1234)'
    );
  });

  it('falls back to the short commit hash when there is no message', () => {
    const { groups } = groupPublishedUpdates([update({ publishGroup: 'group-a' })]);
    expect(describePublishGroup(groups[0]).title).toBe('Commit abc1234 (ios)');
  });
});

describe('fetchUpdates and fetchRuntimeVersions', () => {
  it('calls the listing endpoints with CLI auth and returns the parsed body', async () => {
    const payload = {
      items: [update({ updateId: '10', publishGroup: 'group-a' })],
      nextCursor: '10',
    };
    vi.mocked(fetchWithRetries).mockResolvedValueOnce({
      ok: true,
      json: async () => payload,
    } as Response);

    const page = await fetchUpdates({
      baseUrl: 'https://ota.example.com',
      appId: 'app-1',
      branch: 'main',
      runtimeVersion: '1.0.0',
      credentials,
    });
    expect(page).toEqual(payload);
    const [url, options] = vi.mocked(fetchWithRetries).mock.calls[0];
    expect(url).toBe(
      'https://ota.example.com/api/apps/app-1/branch/main/runtimeVersion/1.0.0/updates?limit=20'
    );
    expect((options?.headers as Record<string, string>)['use-cli-auth']).toBe('true');
  });

  it('wraps the bare-array response of pre-pagination servers into a page', async () => {
    const legacyBody = [update({ updateId: '10' }), update({ updateId: '9', platform: 'android' })];
    vi.mocked(fetchWithRetries).mockResolvedValueOnce({
      ok: true,
      json: async () => legacyBody,
    } as Response);

    const page = await fetchUpdates({
      baseUrl: 'https://ota.example.com',
      appId: 'app-1',
      branch: 'main',
      runtimeVersion: '1.0.0',
      credentials,
    });
    expect(page).toEqual({ items: legacyBody, nextCursor: null });
  });

  it('fetches publish groups from their group-level cursor endpoint', async () => {
    const payload = {
      items: [],
      nextCursor: null,
    };
    vi.mocked(fetchWithRetries).mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => payload,
    } as Response);

    await expect(
      fetchPublishGroups({
        baseUrl: 'https://ota.example.com',
        appId: 'app-1',
        branch: 'main',
        runtimeVersion: '1.0.0',
        credentials,
        cursor: '200',
      })
    ).resolves.toEqual(payload);
    expect(vi.mocked(fetchWithRetries).mock.calls.at(-1)?.[0]).toBe(
      'https://ota.example.com/api/apps/app-1/branch/main/runtimeVersion/1.0.0/publish-groups?limit=20&cursor=200'
    );
  });

  it('treats a missing publish-group endpoint as unsupported', async () => {
    vi.mocked(fetchWithRetries).mockResolvedValueOnce({ ok: false, status: 404 } as Response);
    await expect(
      fetchPublishGroups({
        baseUrl: 'https://ota.example.com',
        appId: 'app-1',
        branch: 'main',
        runtimeVersion: '1.0.0',
        credentials,
      })
    ).resolves.toBeNull();
  });

  it('throws with the server text on a failed listing', async () => {
    vi.mocked(fetchWithRetries).mockResolvedValueOnce({
      ok: false,
      text: async () => 'boom',
    } as Response);
    await expect(
      fetchRuntimeVersions({
        baseUrl: 'https://ota.example.com',
        appId: 'app-1',
        branch: 'main',
        credentials,
      })
    ).rejects.toThrow('Failed to fetch runtime versions: boom');
  });
});
