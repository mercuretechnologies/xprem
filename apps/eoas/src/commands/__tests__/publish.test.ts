/* eslint-disable node/no-sync */
// End-to-end publish flow against a hostile update server. The oclif command
// runs for real on a temporary project, with the project/auth/network seams
// mocked, so these pin the property that matters: when the server forges an
// upload request, the CLI uploads NOTHING, not even the entries that were fine.
import spawnAsync from '@expo/spawn-async';
import fs from 'fs-extra';
// node-fetch's Response, not the DOM one: that is what fetchWithRetries resolves to.
import type { Response } from 'node-fetch';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchWithRetries } from '../../lib/fetch';
import Log from '../../lib/log';
import Publish from '../publish';

vi.mock('@expo/spawn-async', () => ({ default: vi.fn() }));
vi.mock('../../lib/fetch', () => {
  const fetchWithRetries = vi.fn();
  return {
    fetchWithRetries,
    // Forwards to the same spy with the built options, so putCalls() sees the
    // multipart uploads exactly like the plain ones.
    fetchWithRetriesRebuildingBody: vi.fn(async (url: string, makeOptions: () => unknown) =>
      fetchWithRetries(url, makeOptions() as any)
    ),
  };
});
vi.mock('../../lib/auth', () => ({
  retrieveCredentials: () => ({ token: 'test-token' }),
  validateCredentials: () => true,
  getAuthHeaders: () => ({ Authorization: 'Bearer test-token' }),
}));
vi.mock('../../lib/vcs', () => ({
  resolveVcsClient: () => ({
    getCommitHashAsync: async () => 'abc1234',
    canGetLastCommitMessage: () => false,
    ensureRepoExistsAsync: async () => {},
  }),
}));
vi.mock('../../lib/package', () => ({ isExpoInstalled: () => true }));
vi.mock('../../lib/runtimeVersion', () => ({
  resolveRuntimeVersionAsync: async () => ({ runtimeVersion: '1.0.0' }),
}));
vi.mock('../../lib/workflow', () => ({ resolveWorkflowAsync: async () => 'generic' }));
vi.mock('../../lib/expoConfig', async importOriginal => {
  const original = await importOriginal<typeof import('../../lib/expoConfig')>();
  return {
    ...original,
    getPrivateExpoConfigAsync: async () => ({}),
    getPublicExpoConfigAsync: async () => ({ name: 'test-app' }),
    requireExpoAppId: () => 'app-1',
    resolveServerUrl: async () => 'https://ota.example.com',
  };
});

const eoasRoot = path.resolve(__dirname, '../../..');
const SERVER = 'https://ota.example.com';

let projectDir: string;
let secretDir: string;
let previousCwd: string;

function distFile(...segments: string[]): string {
  return path.join(projectDir, 'dist', ...segments);
}

// The export the CLI believes it produced. spawnAsync is mocked, so this mock
// writes what `expo export` would have written.
function writeExport(): void {
  fs.ensureDirSync(distFile('bundles'));
  fs.writeJsonSync(distFile('metadata.json'), {
    version: 0,
    bundler: 'metro',
    fileMetadata: { ios: { bundle: 'bundles/ios-abc.hbc', assets: [] } },
  });
  fs.writeFileSync(distFile('bundles', 'ios-abc.hbc'), 'BUNDLE BYTES');
}

function uploadRequest(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  const filePath = (overrides.filePath as string | undefined) ?? 'metadata.json';
  return {
    requestUploadUrl: 'https://storage.example.com/upload/metadata.json',
    fileName: 'metadata.json',
    filePath,
    originalFileName: filePath,
    ...overrides,
  };
}

function respondWith(uploadRequests: Record<string, unknown>[]): void {
  vi.mocked(fetchWithRetries).mockImplementation(async (url: string) => {
    if (url.startsWith(`${SERVER}/app-1/requestUploadUrl`)) {
      // updateId is a JSON number on the wire: services.RequestUploadURLResponse
      // marshals an int64.
      return {
        ok: true,
        status: 200,
        json: async () => ({ updateId: 1753800000000, uploadRequests }),
      } as Response;
    }
    return { ok: true, status: 200, text: async () => '', json: async () => ({}) } as Response;
  });
}

function putCalls(): { url: string; options: any }[] {
  return vi
    .mocked(fetchWithRetries)
    .mock.calls.filter(([, options]) => (options as any)?.method === 'PUT')
    .map(([url, options]) => ({ url: String(url), options }));
}

// What the command printed through Log.error, so a test can tell a rejection by
// one of the guards apart from an incidental failure that exits the same way.
function loggedErrors(): string {
  return vi
    .mocked(Log.error)
    .mock.calls.flat()
    .map(arg => (arg instanceof Error ? arg.message : String(arg)))
    .join('\n');
}

function runPublish(): Promise<unknown> {
  return Publish.run(
    ['--branch', 'main', '--platform', 'ios', '--nonInteractive', '--disableRepositoryCheck'],
    eoasRoot
  );
}

beforeEach(() => {
  previousCwd = process.cwd();
  projectDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-project-'));
  secretDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-secrets-'));
  fs.writeFileSync(path.join(secretDir, 'id_rsa'), 'PRIVATE KEY');
  process.chdir(projectDir);
  vi.mocked(spawnAsync).mockImplementation((async () => {
    writeExport();
    return { stdout: 'exported', stderr: '' };
  }) as any);
  vi.spyOn(process, 'exit').mockImplementation(((code?: number) => {
    throw new Error(`process.exit(${code})`);
  }) as any);
  vi.spyOn(Log, 'error').mockImplementation(() => {});
});

afterEach(() => {
  process.chdir(previousCwd);
  fs.removeSync(projectDir);
  fs.removeSync(secretDir);
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe('publish against a hostile server response', () => {
  it('uploads nothing when one entry escapes the export directory', async () => {
    // Two levels up: the export root is projectDir/dist, so a single '..' would
    // land on a non-existent file inside projectDir and the test would pass on
    // an ENOENT rather than on the guard. This path really resolves onto the
    // secret, so removing the guard makes the file readable and the test fail.
    const traversal = path.join('..', '..', path.basename(secretDir), 'id_rsa');
    expect(fs.existsSync(path.resolve(projectDir, 'dist', traversal))).toBe(true);
    respondWith([
      uploadRequest(),
      uploadRequest({
        requestUploadUrl: 'https://attacker.tld/collect',
        fileName: 'id_rsa',
        filePath: traversal,
      }),
    ]);

    await expect(runPublish()).rejects.toThrow(/process\.exit\(1\)/);

    expect(loggedErrors()).toMatch(/requested a path outside the export directory/);
    expect(putCalls()).toHaveLength(0);
  });

  it('uploads nothing when the server names a local file that was not exported', async () => {
    // Written by the export step: the command wipes and recreates dist first.
    vi.mocked(spawnAsync).mockImplementation((async () => {
      writeExport();
      fs.writeFileSync(distFile('.env'), 'EXPO_TOKEN=secret');
      return { stdout: 'exported', stderr: '' };
    }) as any);
    respondWith([
      uploadRequest({
        requestUploadUrl: 'https://attacker.tld/collect',
        fileName: '.env',
        filePath: '.env',
      }),
    ]);

    await expect(runPublish()).rejects.toThrow(/process\.exit\(1\)/);
    expect(loggedErrors()).toMatch(/not part of this export/);
    expect(putCalls()).toHaveLength(0);
  });

  it('uploads nothing when an upload URL downgrades to plain HTTP', async () => {
    respondWith([uploadRequest({ requestUploadUrl: 'http://attacker.tld/collect' })]);

    await expect(runPublish()).rejects.toThrow(/process\.exit\(1\)/);
    expect(loggedErrors()).toMatch(/only be sent over HTTPS/);
    expect(putCalls()).toHaveLength(0);
  });

  it('never follows a redirect away from the validated upload URL', async () => {
    // The URL is only ever checked as a string, so the PUT itself must refuse to
    // be redirected to an origin nothing validated.
    respondWith([
      uploadRequest(),
      uploadRequest({
        requestUploadUrl: 'https://storage.example.com/upload/ios-abc.hbc',
        fileName: 'ios-abc.hbc',
        filePath: 'bundles/ios-abc.hbc',
      }),
    ]);

    await runPublish();

    expect(putCalls()).toHaveLength(2);
    for (const call of putCalls()) {
      expect(call.options.redirect).toBe('error');
    }
  });
});

describe('publish forwards the storage backend requirements', () => {
  it('carries the Azure Blob header through validation onto the PUT', async () => {
    // Azure Put Blob rejects a request without x-ms-blob-type, and the server
    // ships the requirement in the response (internal/bucket/bucket.go). The
    // schema must let it through and publish must forward it verbatim.
    respondWith([
      uploadRequest({
        requestUploadUrl: 'https://acct.blob.core.windows.net/updates/metadata.json?sig=abc',
        headers: { 'x-ms-blob-type': 'BlockBlob' },
      }),
    ]);

    await runPublish();

    const [call] = putCalls();
    expect(call.options.headers['x-ms-blob-type']).toBe('BlockBlob');
    expect(call.options.headers['Content-Type']).toBe('application/json');
  });
});

describe('publish credential handling', () => {
  it('attaches the token only to the configured server, never to storage', async () => {
    respondWith([
      uploadRequest({ requestUploadUrl: `${SERVER}/app-1/uploadLocalFile?file=metadata.json` }),
      uploadRequest({
        requestUploadUrl: 'https://storage.example.com/upload/ios-abc.hbc',
        fileName: 'ios-abc.hbc',
        filePath: 'bundles/ios-abc.hbc',
      }),
    ]);

    await runPublish();

    const byUrl = new Map(putCalls().map(call => [call.url, call.options]));
    const localBucket = byUrl.get(`${SERVER}/app-1/uploadLocalFile?file=metadata.json`);
    expect(localBucket.headers.Authorization).toBe('Bearer test-token');
    // The credential-bearing PUT must refuse redirects too, or the token follows
    // the Location header.
    expect(localBucket.redirect).toBe('error');
    expect(byUrl.get('https://storage.example.com/upload/ios-abc.hbc').headers.Authorization).toBe(
      undefined
    );
  });
});

describe('publish against an honest server response', () => {
  it('uploads exactly the files it exported and marks the update as uploaded', async () => {
    respondWith([
      uploadRequest(),
      uploadRequest({
        requestUploadUrl: 'https://storage.example.com/upload/expoConfig.json',
        fileName: 'expoConfig.json',
        filePath: 'expoConfig.json',
      }),
      uploadRequest({
        requestUploadUrl: 'https://storage.example.com/upload/ios-abc.hbc',
        fileName: 'ios-abc.hbc',
        filePath: 'bundles/ios-abc.hbc',
      }),
    ]);

    await runPublish();

    // Uploads run concurrently, so compare the set, not the order.
    expect(
      putCalls()
        .map(call => call.url)
        .sort()
    ).toEqual([
      'https://storage.example.com/upload/expoConfig.json',
      'https://storage.example.com/upload/ios-abc.hbc',
      'https://storage.example.com/upload/metadata.json',
    ]);
    const marked = vi
      .mocked(fetchWithRetries)
      .mock.calls.map(([url]) => String(url))
      .filter(url => url.includes('/markUpdateAsUploaded/'));
    expect(marked).toHaveLength(1);
    // The numeric updateId reached the query string as a string.
    expect(new URL(marked[0]).searchParams.get('updateId')).toBe('1753800000000');
  });
});
