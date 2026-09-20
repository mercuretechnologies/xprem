import { createHash } from 'crypto';
import fs from 'fs-extra';
import http from 'http';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { startBuildRecord, uploadBuildArtifact } from '../artifacts';
import { BuildLog, createBuildLog } from '../log';
import { BuildInputs, selectProfile } from '../prepare';
import * as buildServer from '../server';

let directory: string;
let buildLog: BuildLog;
let server: http.Server;
let endpoint: string;
let headers: http.IncomingHttpHeaders;
let uploaded: Buffer;
let finalized: boolean;
let failFinalize: boolean;
let missingRegistry: boolean;
let registrations: string[];
let requests: string[];
const id = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const metadata = {
  profile: 'test',
  mode: 'release',
  channel: 'stable',
  cliVersion: '2.3.0',
  buildNumber: '7',
  fingerprint: 'a'.repeat(40),
  startedAt: '2026-09-09T00:00:00Z',
  finishedAt: '2026-09-09T00:01:00Z',
  durationMs: 60000,
};
let previousToken: string | undefined;
beforeEach(async () => {
  directory = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-upload-'));
  buildLog = await createBuildLog(directory, 'test');
  previousToken = process.env.EOO_TOKEN;
  process.env.EOO_TOKEN = 'test-registered-token';
  finalized = false;
  failFinalize = false;
  missingRegistry = false;
  registrations = [];
  requests = [];
  server = http.createServer(async (req, res) => {
    requests.push(req.url!);
    const chunks = [];
    for await (const chunk of req) {
      chunks.push(Buffer.from(chunk));
    }
    const body = Buffer.concat(chunks);
    res.setHeader('content-type', 'application/json');
    if (req.url === '/upload/sensitive-token') {
      headers = req.headers;
      uploaded = body;
      res.end('{}');
      return;
    }
    expect(req.headers.authorization).toBe('Bearer test-registered-token');
    if (missingRegistry) {
      res.statusCode = 404;
      res.end('404 page not found');
      return;
    }
    if (req.url?.endsWith('/complete')) {
      if (failFinalize) {
        res.statusCode = 503;
        res.end('{}');
        return;
      }
      finalized = true;
      res.end(JSON.stringify({ id, status: 'ready' }));
      return;
    }
    registrations.push(body.toString());
    res.end(
      JSON.stringify({
        build: { id, status: finalized ? 'ready' : 'uploading' },
        ...(finalized
          ? {}
          : {
              upload: {
                url: `${endpoint}/upload/sensitive-token`,
                method: 'PUT',
                headers: { 'x-ms-blob-type': 'BlockBlob' },
              },
            }),
      })
    );
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  endpoint = `http://127.0.0.1:${(server.address() as import('net').AddressInfo).port}`;
});
afterEach(async () => {
  vi.restoreAllMocks();
  await new Promise<void>(resolve =>
    server.close(() => {
      resolve();
    })
  );
  await buildLog.close();
  await fs.remove(directory);
  if (previousToken === undefined) {
    delete process.env.EOO_TOKEN;
  } else {
    process.env.EOO_TOKEN = previousToken;
  }
});
async function artifact(): Promise<string> {
  const file = path.join(directory, 'test.apk');
  await fs.writeFile(file, 'signed APK bytes');
  await fs.writeJson(`${file}.build.json`, {
    schemaVersion: 1,
    id,
    endpoint,
    artifactType: 'apk',
    metadata,
    size: 16,
    sha256: createHash('sha256').update('signed APK bytes').digest('hex'),
  });
  return file;
}
it('streams the artifact with only upload headers and finalizes the same build ID', async () => {
  const file = await artifact();
  await uploadBuildArtifact(file, endpoint, buildLog);
  expect(uploaded.toString()).toBe('signed APK bytes');
  expect(headers.authorization).toBeUndefined();
  expect(headers['x-ms-blob-type']).toBe('BlockBlob');
  expect(requests).toEqual([
    `/artifacts/${id}`,
    '/upload/sensitive-token',
    `/artifacts/${id}/complete`,
  ]);
  expect(await fs.readFile(file, 'utf8')).toBe('signed APK bytes');
  expect(JSON.parse(registrations[0]).metadata).toEqual(metadata);
});
it.each([
  { mode: 'release' as const, channel: 'stable', override: undefined, expectedChannel: 'stable' },
  { mode: 'debug' as const, channel: 'stable', override: 'preview', expectedChannel: 'preview' },
  { mode: 'release' as const, channel: undefined, override: undefined, expectedChannel: undefined },
])(
  'records $mode and the effective channel $expectedChannel before compiling',
  async ({ mode, channel, override, expectedChannel }) => {
    await fs.writeJson(path.join(directory, 'xprem.json'), {
      schemaVersion: 1,
      profiles: {
        test: { channel, android: { applicationId: 'com.example.app', mode, artifact: 'apk' } },
      },
    });
    const options = { profile: 'test', channel: override };
    const build: BuildInputs = {
      project: directory,
      options,
      profile: await selectProfile(directory, options),
      endpoint,
      output: path.join(directory, 'build.apk'),
      packageRunner: ['npx', []],
      variables: {},
      nodeEnv: mode === 'release' ? 'production' : 'development',
      toolEnv: {},
      toolVersions: {},
      env: {},
    };
    const record = await startBuildRecord(
      build,
      'apk',
      build.profile.android!.mode,
      metadata.startedAt
    );
    const persisted = await fs.readJson(`${build.output}.build.json`);
    expect(record.metadata).toMatchObject({ profile: 'test', mode, channel: expectedChannel });
    expect(persisted.metadata).toEqual(JSON.parse(registrations[0]).metadata);
    expect(requests).toEqual([`/artifacts/${record.id}/start`]);
  }
);
it('refuses to build against a server without the build registry', async () => {
  missingRegistry = true;
  await fs.writeJson(path.join(directory, 'xprem.json'), {
    schemaVersion: 1,
    profiles: {
      test: { android: { applicationId: 'com.example.app', mode: 'release', artifact: 'apk' } },
    },
  });
  const options = { profile: 'test' };
  const build: BuildInputs = {
    project: directory,
    options,
    profile: await selectProfile(directory, options),
    endpoint,
    output: path.join(directory, 'build.apk'),
    packageRunner: ['npx', []],
    variables: {},
    nodeEnv: 'production',
    toolEnv: {},
    toolVersions: {},
    env: {},
  };
  await expect(startBuildRecord(build, 'apk', 'release', metadata.startedAt)).rejects.toThrow(
    'Upgrade the xprem server'
  );
});
it('retries using persisted metadata and ID without allocating a number or rebuilding', async () => {
  const file = await artifact();
  failFinalize = true;
  await expect(uploadBuildArtifact(file, endpoint, buildLog)).rejects.toThrow(/HTTP 503/);
  failFinalize = false;
  await uploadBuildArtifact(file, endpoint, buildLog);
  expect(registrations[0]).toBe(registrations[1]);
  expect(requests.some(url => url.includes('build-number'))).toBe(false);
  requests = [];
  await uploadBuildArtifact(file, endpoint, buildLog);
  expect(requests).toEqual([`/artifacts/${id}`]);
});
it('refuses an artifact modified after compilation before any request', async () => {
  const file = await artifact();
  await fs.writeFile(file, 'different bytes!');
  await expect(uploadBuildArtifact(file, endpoint, buildLog)).rejects.toThrow(/changed/);
  expect(requests).toEqual([]);
});

it.each(['https://trusted.example.com', 'http://127.0.0.1:1', 'https://127.0.0.1'])(
  'rejects a metadata endpoint outside the selected server %s before any request',
  async selected => {
    const file = await artifact();
    const persisted = await fs.readFile(`${file}.build.json`, 'utf8');
    await expect(uploadBuildArtifact(file, selected, buildLog)).rejects.toThrow(/does not match/);
    expect(requests).toEqual([]);
    expect(await fs.readFile(`${file}.build.json`, 'utf8')).toBe(persisted);
  }
);

it.each(['/other/app/build/identifier', '/ota-evil/app/build/identifier', '/ota/../other'])(
  'rejects a metadata endpoint outside the configured path prefix: %s',
  async pathname => {
    const file = await artifact();
    const record = await fs.readJson(`${file}.build.json`);
    await fs.writeJson(`${file}.build.json`, { ...record, endpoint: `${endpoint}${pathname}` });
    await expect(uploadBuildArtifact(file, `${endpoint}/ota`, buildLog)).rejects.toThrow(
      /does not match/
    );
    expect(requests).toEqual([]);
  }
);

it('uploads an identifier-scoped endpoint under the selected HTTP server path', async () => {
  const file = await artifact();
  const record = await fs.readJson(`${file}.build.json`);
  const buildPath = `/ota/app/build/${id}`;
  await fs.writeJson(`${file}.build.json`, { ...record, endpoint: `${endpoint}${buildPath}` });
  await uploadBuildArtifact(file, `${endpoint}/ota/`, buildLog);
  expect(requests).toEqual([
    `${buildPath}/artifacts/${id}`,
    '/upload/sensitive-token',
    `${buildPath}/artifacts/${id}/complete`,
  ]);
});

it('allows a configured non-loopback HTTP server', async () => {
  const file = await artifact();
  const record = await fs.readJson(`${file}.build.json`);
  const selected = 'http://build.example.com/ota';
  const buildEndpoint = `${selected}/app/build/${id}`;
  await fs.writeJson(`${file}.build.json`, { ...record, endpoint: buildEndpoint });
  const request = vi.spyOn(buildServer, 'request').mockResolvedValue({
    build: { id, status: 'ready' },
  });
  await expect(uploadBuildArtifact(file, selected, buildLog)).resolves.toBe(id);
  expect(request).toHaveBeenCalledWith(
    `${buildEndpoint}/artifacts/${id}`,
    expect.objectContaining({ method: 'PUT', retry: false })
  );
});
