// End-to-end flow of the republish command, prompts included: the oclif
// command runs for real, with the project/auth/network seams mocked. Pins the
// mode question (publish group vs single update), the group POST wire format,
// and the fallbacks that skip the question.
import path from 'path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchWithRetries } from '../../lib/fetch';
import { promptAsync } from '../../lib/prompts';
import { fetchPublishGroups, fetchRuntimeVersions, fetchUpdates } from '../../lib/serverUpdates';
import Republish from '../republish';

vi.mock('../../lib/fetch', () => ({
  fetchWithRetries: vi.fn(),
}));
vi.mock('../../lib/prompts', () => ({
  promptAsync: vi.fn(),
}));
vi.mock('../../lib/auth', () => ({
  retrieveCredentials: () => ({ token: 'test-token' }),
  validateCredentials: () => true,
  getAuthHeaders: () => ({ Authorization: 'Bearer test-token' }),
}));
vi.mock('../../lib/vcs', () => ({
  resolveVcsClient: () => ({ ensureRepoExistsAsync: async () => {} }),
}));
vi.mock('../../lib/package', () => ({
  isExpoInstalled: () => true,
}));
vi.mock('../../lib/expoConfig', () => ({
  getPrivateExpoConfigAsync: async () => ({}),
  resolveServerUrl: async (_config: unknown, customServerUrl?: string) =>
    new URL(customServerUrl ?? 'https://ota.example.com/manifest').origin,
  requireExpoAppId: () => 'app-1',
}));
vi.mock('../../lib/serverUpdates', async importOriginal => {
  const original = await importOriginal<typeof import('../../lib/serverUpdates')>();
  return {
    ...original,
    fetchPublishGroups: vi.fn(),
    fetchRuntimeVersions: vi.fn(),
    fetchUpdates: vi.fn(),
  };
});

const eoasRoot = path.resolve(__dirname, '../../..');

const runtimeVersionsPayload = [
  {
    runtimeVersion: '1.0.0',
    lastUpdatedAt: '2026-07-24T10:00:00Z',
    createdAt: '2026-07-01T10:00:00Z',
    numberOfUpdates: 3,
  },
];

function serverUpdate(overrides: Record<string, unknown>): any {
  return {
    updateUUID: 'a0000000-0000-0000-0000-000000000001',
    createdAt: '2026-07-24T10:00:00Z',
    updateId: '100',
    platform: 'ios',
    commitHash: 'abc1234def',
    message: 'Fix crash',
    ...overrides,
  };
}

function publishGroup(id: string, message = `Publish ${id}`): any {
  return {
    publishGroup: id,
    createdAt: '2026-07-24T10:00:00Z',
    commitHash: 'abc1234def',
    message,
    platforms: ['ios', 'android'],
    updates: [
      serverUpdate({ updateId: `${id}-ios`, platform: 'ios' }),
      serverUpdate({ updateId: `${id}-android`, platform: 'android' }),
    ],
  };
}

// Answers each prompt by name; a function receives the question (with its
// choices) and returns the value, mimicking a user selection.
type PromptAnswer = unknown | ((question: any) => unknown);
function answerPrompts(answers: Record<string, PromptAnswer>): void {
  vi.mocked(promptAsync).mockImplementation(async (questions: any) => {
    const question = Array.isArray(questions) ? questions[0] : questions;
    if (!(question.name in answers)) {
      throw new Error(`Unexpected prompt: ${question.name}`);
    }
    const answer = answers[question.name];
    const value = typeof answer === 'function' ? (answer as (q: any) => unknown)(question) : answer;
    return { [question.name]: value };
  });
}

function promptedNames(): string[] {
  return vi
    .mocked(promptAsync)
    .mock.calls.map(([questions]) => (Array.isArray(questions) ? questions[0] : questions).name);
}

function lastPostUrl(): URL {
  const calls = vi.mocked(fetchWithRetries).mock.calls;
  return new URL(String(calls[calls.length - 1][0]));
}

describe('republish command flow', () => {
  beforeEach(() => {
    vi.mocked(fetchRuntimeVersions).mockResolvedValue(runtimeVersionsPayload);
    vi.mocked(fetchPublishGroups).mockResolvedValue(null);
    vi.mocked(fetchWithRetries).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ publishGroup: 'new-group', updates: [] }),
    } as Response);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('offers the publish group mode and republishes the group in one call', async () => {
    vi.mocked(fetchPublishGroups).mockResolvedValue({
      items: [publishGroup('group-a')],
      nextCursor: null,
    });
    answerPrompts({
      runtimeVersion: '1.0.0',
      mode: 'group',
      group: (question: any) => question.choices[0].value,
    });

    await Republish.run(['--branch', 'main'], eoasRoot);

    expect(promptedNames()).toEqual(['runtimeVersion', 'mode', 'group']);
    const url = lastPostUrl();
    expect(url.pathname).toBe('/app-1/republish/main');
    expect(url.searchParams.get('publishGroup')).toBe('group-a');
    expect(url.searchParams.get('runtimeVersion')).toBe('1.0.0');
    expect(url.searchParams.has('updateId')).toBe(false);
    expect(url.searchParams.has('platform')).toBe(false);
    expect(fetchUpdates).not.toHaveBeenCalled();
  });

  it('republishes a single update through the historical wire format', async () => {
    vi.mocked(fetchPublishGroups).mockResolvedValue({
      items: [publishGroup('group-a')],
      nextCursor: null,
    });
    vi.mocked(fetchUpdates).mockResolvedValue({
      items: [
        serverUpdate({ updateId: '100', platform: 'ios', publishGroup: 'group-a' }),
        serverUpdate({ updateId: '200', platform: 'android', publishGroup: 'group-a' }),
      ],
      nextCursor: null,
    });
    answerPrompts({
      runtimeVersion: '1.0.0',
      mode: 'single',
      update: (question: any) => question.choices[0].value,
    });

    await Republish.run(['--branch', 'main'], eoasRoot);

    const url = lastPostUrl();
    expect(url.searchParams.get('updateId')).toBe('100');
    expect(url.searchParams.get('platform')).toBe('ios');
    expect(url.searchParams.get('commitHash')).toBe('abc1234def');
    expect(url.searchParams.has('publishGroup')).toBe(false);
  });

  it('pages complete publish groups independently from updates', async () => {
    vi.mocked(fetchPublishGroups)
      .mockResolvedValueOnce({
        items: [publishGroup('group-a'), publishGroup('group-b')],
        nextCursor: '200',
      })
      .mockResolvedValueOnce({
        items: [publishGroup('group-c')],
        nextCursor: null,
      });
    let groupPromptCount = 0;
    answerPrompts({
      runtimeVersion: '1.0.0',
      mode: 'group',
      group: (question: any) => {
        groupPromptCount += 1;
        if (groupPromptCount === 1) {
          expect(question.choices).toHaveLength(3);
          return question.choices.at(-1).value;
        }
        expect(question.choices).toHaveLength(3);
        expect(question.choices.every((choice: any) => choice.value.kind === 'group')).toBe(true);
        expect(question.initial).toBe(2);
        return question.choices[2].value;
      },
    });

    await Republish.run(['--branch', 'main'], eoasRoot);

    expect(fetchPublishGroups).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ cursor: '200', limit: 20 })
    );
    expect(fetchUpdates).not.toHaveBeenCalled();
    expect(lastPostUrl().searchParams.get('publishGroup')).toBe('group-c');
  });

  it('skips the mode question when --platform narrows the run', async () => {
    vi.mocked(fetchUpdates).mockResolvedValue({
      items: [
        serverUpdate({ updateId: '100', platform: 'ios', publishGroup: 'group-a' }),
        serverUpdate({ updateId: '200', platform: 'android', publishGroup: 'group-a' }),
      ],
      nextCursor: null,
    });
    answerPrompts({
      runtimeVersion: '1.0.0',
      update: (question: any) => question.choices[0].value,
    });

    await Republish.run(['--branch', 'main', '--platform', 'android'], eoasRoot);

    expect(promptedNames()).toEqual(['runtimeVersion', 'update']);
    // The platform filter reached the picker: only the android update remained.
    expect(lastPostUrl().searchParams.get('updateId')).toBe('200');
  });

  it('skips the mode question when the branch has no publish groups', async () => {
    vi.mocked(fetchUpdates).mockResolvedValue({
      items: [serverUpdate({ updateId: '100', platform: 'ios', publishGroup: undefined })],
      nextCursor: null,
    });
    answerPrompts({
      runtimeVersion: '1.0.0',
      update: (question: any) => question.choices[0].value,
    });

    await Republish.run(['--branch', 'main'], eoasRoot);

    expect(promptedNames()).toEqual(['runtimeVersion', 'update']);
    expect(lastPostUrl().searchParams.get('updateId')).toBe('100');
  });

  it('loads another page in the single-update picker and focuses the new choices', async () => {
    vi.mocked(fetchUpdates)
      .mockResolvedValueOnce({
        items: [serverUpdate({ updateId: '200', publishGroup: undefined })],
        nextCursor: '200',
      })
      .mockResolvedValueOnce({
        items: [serverUpdate({ updateId: '100', publishGroup: undefined })],
        nextCursor: null,
      });
    let updatePromptCount = 0;
    answerPrompts({
      runtimeVersion: '1.0.0',
      update: (question: any) => {
        updatePromptCount += 1;
        if (updatePromptCount === 1) {
          return question.choices.at(-1).value;
        }
        expect(question.initial).toBe(1);
        return question.choices[1].value;
      },
    });

    await Republish.run(['--branch', 'main'], eoasRoot);

    expect(fetchUpdates).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ cursor: '200', limit: 20 })
    );
    expect(lastPostUrl().searchParams.get('updateId')).toBe('100');
  });

  it('automatically skips a page containing only rollback markers', async () => {
    vi.mocked(fetchUpdates)
      .mockResolvedValueOnce({
        items: [serverUpdate({ updateUUID: 'Rollback to embedded', updateId: '200' })],
        nextCursor: '200',
      })
      .mockResolvedValueOnce({
        items: [serverUpdate({ updateId: '100', publishGroup: undefined })],
        nextCursor: null,
      });
    answerPrompts({
      runtimeVersion: '1.0.0',
      update: (question: any) => question.choices[0].value,
    });

    await Republish.run(['--branch', 'main'], eoasRoot);

    expect(fetchUpdates).toHaveBeenCalledTimes(2);
    expect(lastPostUrl().searchParams.get('updateId')).toBe('100');
  });

  it('targets the origin passed as --serverUrl instead of the config one', async () => {
    vi.mocked(fetchUpdates).mockResolvedValue({
      items: [serverUpdate({ updateId: '100', platform: 'ios', publishGroup: undefined })],
      nextCursor: null,
    });
    answerPrompts({
      runtimeVersion: '1.0.0',
      update: (question: any) => question.choices[0].value,
    });

    await Republish.run(
      ['--branch', 'main', '--serverUrl', 'https://publish.example.com/some/path'],
      eoasRoot
    );

    expect(lastPostUrl().origin).toBe('https://publish.example.com');
    expect(vi.mocked(fetchRuntimeVersions).mock.calls[0][0]).toMatchObject({
      baseUrl: 'https://publish.example.com',
    });
    expect(vi.mocked(fetchUpdates).mock.calls[0][0]).toMatchObject({
      baseUrl: 'https://publish.example.com',
    });
  });
});
