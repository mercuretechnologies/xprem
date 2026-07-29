// node-fetch's Response, not the DOM one: that is what fetchWithRetries resolves to.
import type { Response } from 'node-fetch';
import { describe, expect, it, vi } from 'vitest';

import { activeRolloutConflictMessage, requestUploadUrls } from '../assets';
import { fetchWithRetries } from '../fetch';

vi.mock('../fetch', () => ({
  fetchWithRetries: vi.fn(),
}));

const credentials = { token: 'test-token', sessionSecret: undefined };

function baseParams(): Parameters<typeof requestUploadUrls>[0] {
  return {
    body: { fileNames: ['bundle.js'] },
    requestUploadUrl: 'https://ota.example.com/app-1/requestUploadUrl/main',
    auth: credentials,
    runtimeVersion: '1.0.0',
    platform: 'ios',
    commitHash: 'abc1234',
    branch: 'main',
  };
}

function respondWith(payload: unknown): void {
  vi.mocked(fetchWithRetries).mockResolvedValueOnce({
    ok: true,
    status: 200,
    json: async () => payload,
  } as Response);
}

function requestedUrl(): URL {
  const calls = vi.mocked(fetchWithRetries).mock.calls;
  return new URL(String(calls[calls.length - 1][0]));
}

describe('requestUploadUrls publish group wire contract', () => {
  it('sends the publish group as a query parameter and returns the acknowledgment', async () => {
    respondWith({ updateId: '1', uploadRequests: [], publishGroup: 'group-a' });
    const result = await requestUploadUrls({ ...baseParams(), publishGroup: 'group-a' });
    expect(requestedUrl().searchParams.get('publishGroup')).toBe('group-a');
    expect(result.publishGroup).toBe('group-a');
  });

  it('omits the parameter entirely when no group is provided', async () => {
    respondWith({ updateId: '1', uploadRequests: [] });
    const result = await requestUploadUrls(baseParams());
    expect(requestedUrl().searchParams.has('publishGroup')).toBe(false);
    expect(result.publishGroup).toBeUndefined();
  });

  it('leaves the acknowledgment undefined when the server ignores the group', async () => {
    // Old server or stateless mode: no echo. The publish command reads this
    // to print the ungrouped note instead of a publish group line.
    respondWith({ updateId: '1', uploadRequests: [] });
    const result = await requestUploadUrls({ ...baseParams(), publishGroup: 'group-a' });
    expect(result.publishGroup).toBeUndefined();
  });
});

describe('requestUploadUrls response schema', () => {
  const validItem = {
    requestUploadUrl: 'https://storage.example.com/upload/bundle.js',
    fileName: 'bundle.js',
    filePath: 'bundle.js',
  };

  it('accepts a well-formed response and its optional headers', async () => {
    respondWith({
      updateId: '1',
      uploadRequests: [{ ...validItem, headers: { 'x-ms-blob-type': 'BlockBlob' } }],
    });
    const result = await requestUploadUrls(baseParams());
    expect(result.uploadRequests[0].headers).toEqual({ 'x-ms-blob-type': 'BlockBlob' });
  });

  it('accepts the numeric updateId the Go server actually marshals', async () => {
    // services.RequestUploadURLResponse.UpdateID is an int64, so the wire value
    // is a JSON number. Normalized to a string for the markUpdateAsUploaded query.
    respondWith({ updateId: 1753800000000, uploadRequests: [validItem] });
    const result = await requestUploadUrls(baseParams());
    expect(result.updateId).toBe('1753800000000');
  });

  it('still accepts a string updateId', async () => {
    respondWith({ updateId: '1753800000000', uploadRequests: [validItem] });
    const result = await requestUploadUrls(baseParams());
    expect(result.updateId).toBe('1753800000000');
  });

  it('accepts an empty header value', async () => {
    // Joi.string() rejects '' by default, which would abort a publish over a
    // header the CLI only ever forwards.
    respondWith({ updateId: '1', uploadRequests: [{ ...validItem, headers: { 'x-a': '' } }] });
    await expect(requestUploadUrls(baseParams())).resolves.toBeDefined();
  });

  it('tolerates unknown fields so a newer server does not break the CLI', async () => {
    // Both levels: a new top-level field and a new field inside an entry.
    respondWith({
      updateId: '1',
      uploadRequests: [{ ...validItem, expiresAt: '2026-01-01' }],
      somethingNew: true,
    });
    await expect(requestUploadUrls(baseParams())).resolves.toBeDefined();
  });

  it.each([
    ['a missing updateId', { uploadRequests: [] }],
    ['a missing uploadRequests', { updateId: '1' }],
    ['an updateId that is neither string nor number', { updateId: {}, uploadRequests: [] }],
    ['uploadRequests that is not an array', { updateId: '1', uploadRequests: {} }],
    ['an entry that is not an object', { updateId: '1', uploadRequests: ['bundle.js'] }],
    [
      'a non-string filePath',
      { updateId: '1', uploadRequests: [{ ...validItem, filePath: ['bundle.js'] }] },
    ],
    [
      'a missing requestUploadUrl',
      { updateId: '1', uploadRequests: [{ fileName: 'bundle.js', filePath: 'bundle.js' }] },
    ],
    [
      'a non-http upload URL',
      { updateId: '1', uploadRequests: [{ ...validItem, requestUploadUrl: 'file:///etc/passwd' }] },
    ],
    [
      'headers that are not strings',
      { updateId: '1', uploadRequests: [{ ...validItem, headers: { 'x-a': { nested: 1 } } }] },
    ],
    [
      'a header value smuggling a CRLF',
      {
        updateId: '1',
        uploadRequests: [{ ...validItem, headers: { 'x-a': 'v\r\nAuthorization: Bearer x' } }],
      },
    ],
    [
      'a header name that is not a token',
      { updateId: '1', uploadRequests: [{ ...validItem, headers: { 'x a': 'v' } }] },
    ],
  ])('rejects %s', async (_label, payload) => {
    respondWith(payload);
    await expect(requestUploadUrls(baseParams())).rejects.toThrow(/invalid upload response/);
  });
});

describe('requestUploadUrls rollout guardrails', () => {
  it('aborts when the server does not echo the rollout percentage', async () => {
    respondWith({ updateId: '1', uploadRequests: [] });
    await expect(requestUploadUrls({ ...baseParams(), rolloutPercentage: 10 })).rejects.toThrow(
      /ignored --rollout-percentage/
    );
  });

  it('continues when the rollout percentage is echoed', async () => {
    respondWith({ updateId: '1', uploadRequests: [], rolloutPercentage: 10 });
    const result = await requestUploadUrls({ ...baseParams(), rolloutPercentage: 10 });
    expect(result.rolloutPercentage).toBe(10);
  });

  it('surfaces an active rollout conflict as the dedicated message', async () => {
    vi.mocked(fetchWithRetries).mockResolvedValueOnce({
      ok: false,
      status: 409,
      text: async () => 'conflict',
    } as Response);
    await expect(requestUploadUrls(baseParams())).rejects.toThrow(
      activeRolloutConflictMessage('main')
    );
  });
});
