import { randomUUID } from 'crypto';
import fs from 'fs-extra';
import originalFetch from 'node-fetch';
import path from 'path';
import { validate as isUuid } from 'uuid';

import { BuildLog, LogWriter } from './log';
import { BuildMachine, describeMachine } from './machine';
import { BuildInputs } from './prepare';
import { BuildServerError, request } from './server';
import { BuildStep } from './steps';
import { assertSafeUploadUrl } from '../assets';
import { getAuthHeaders, retrieveCredentials } from '../auth';
import { digestFile } from '../crypto';
import GitClient from '../vcs/clients/git';

export interface BuildMetadata {
  profile: string;
  mode?: 'debug' | 'release';
  distribution?: 'app-store' | 'ad-hoc';
  environment?: string;
  channel?: string;
  cliVersion: string;
  gitCommit?: string;
  gitMessage?: string;
  gitDirty?: boolean;
  machine?: BuildMachine;
  startedAt: string;
  finishedAt?: string;
  durationMs?: number;
  version?: string;
  buildNumber?: string;
  fingerprint?: string;
  expoSdk?: string;
  runtimeVersion?: string;
}

export type ArtifactType = 'apk' | 'aab' | 'ipa';

export interface LocalBuildRecord {
  schemaVersion: 1;
  id: string;
  endpoint: string;
  artifactType: ArtifactType;
  metadata: BuildMetadata;
  size?: number;
  sha256?: string;
}

async function saveRecord(file: string, record: LocalBuildRecord): Promise<void> {
  await fs.ensureDir(path.dirname(file));
  const temporary = `${file}.${randomUUID()}.tmp`;
  try {
    await fs.writeJson(temporary, record, { mode: 0o600, spaces: 2 });
    await fs.rename(temporary, file);
  } finally {
    await fs.remove(temporary);
  }
}

export async function startBuildRecord(
  build: BuildInputs,
  artifactType: ArtifactType,
  mode: 'debug' | 'release',
  startedAt: string,
  distribution?: 'app-store' | 'ad-hoc'
): Promise<LocalBuildRecord> {
  const git = new GitClient(build.project);
  const gitCommit = await git.getCommitHashAsync();
  const cliPackage = await fs.readJson(path.resolve(__dirname, '../../../package.json'));
  const record: LocalBuildRecord = {
    schemaVersion: 1,
    id: randomUUID(),
    endpoint: build.endpoint,
    artifactType,
    metadata: {
      profile: build.options.profile,
      mode,
      distribution,
      environment: build.profile.environment,
      channel: build.profile.channel,
      cliVersion: cliPackage.version,
      gitCommit,
      gitMessage: gitCommit ? (await git.getLastCommitMessageAsync())?.slice(0, 250) : undefined,
      gitDirty: gitCommit ? await git.hasUncommittedChangesAsync() : undefined,
      machine: await describeMachine(build.toolVersions),
      startedAt,
    },
  };
  await saveRecord(`${build.output}.build.json`, record);
  try {
    await request(`${record.endpoint}/artifacts/${record.id}/start`, {
      method: 'PUT',
      body: { artifactType, metadata: record.metadata },
      retry: false,
    });
  } catch (error) {
    if (error instanceof BuildServerError && error.status === 404) {
      throw new Error(
        'This server does not record builds (HTTP 404 on the build registry). Upgrade the xprem server before building with this CLI.'
      );
    }
    throw error;
  }
  return record;
}

export async function failBuildRecord(record: LocalBuildRecord, file: string): Promise<void> {
  // A compiled build keeps its own finish time when only the upload failed.
  if (!record.metadata.finishedAt) {
    const finishedAt = new Date().toISOString();
    record.metadata.finishedAt = finishedAt;
    record.metadata.durationMs = Date.parse(finishedAt) - Date.parse(record.metadata.startedAt);
    await saveRecord(`${file}.build.json`, record);
  }
  await request(`${record.endpoint}/artifacts/${record.id}/failed`, {
    method: 'POST',
    body: { finishedAt: record.metadata.finishedAt },
    retry: false,
  });
}

export async function finishBuildRecord(record: LocalBuildRecord, file: string): Promise<void> {
  record.metadata.finishedAt = new Date().toISOString();
  record.metadata.durationMs =
    Date.parse(record.metadata.finishedAt) - Date.parse(record.metadata.startedAt);
  record.size = (await fs.stat(file)).size;
  record.sha256 = Buffer.from((await digestFile(file)).hash, 'base64url').toString('hex');
  await saveRecord(`${file}.build.json`, record);
}

async function uploadArtifact(file: string, serverUrl: string, log: LogWriter): Promise<string> {
  const record: LocalBuildRecord = await fs.readJson(`${file}.build.json`);
  if (
    record.schemaVersion !== 1 ||
    !isUuid(record.id) ||
    !record.metadata ||
    !record.size ||
    !record.sha256 ||
    !['apk', 'aab', 'ipa'].includes(record.artifactType)
  ) {
    throw new Error('No completed build metadata found beside this artifact.');
  }
  const endpoint = new URL(record.endpoint);
  if (
    !['https:', 'http:'].includes(endpoint.protocol) ||
    endpoint.username ||
    endpoint.password ||
    endpoint.search ||
    endpoint.hash
  ) {
    throw new Error('Invalid server address in build metadata.');
  }
  // The caller selects this URL from config/flags, never from persisted metadata.
  const server = new URL(serverUrl);
  const serverPath = server.pathname.replace(/\/+$/, '');
  if (
    endpoint.origin !== server.origin ||
    (endpoint.pathname !== serverPath && !endpoint.pathname.startsWith(`${serverPath}/`))
  ) {
    throw new Error(
      'The server address in build metadata does not match the selected server. Check your config or --serverUrl.'
    );
  }
  const size = (await fs.stat(file)).size;
  const sha256 = Buffer.from((await digestFile(file)).hash, 'base64url').toString('hex');
  if (size !== record.size || sha256 !== record.sha256) {
    throw new Error(
      'The artifact changed since compilation; refusing to upload it under the original build ID.'
    );
  }
  const url = `${endpoint.href.replace(/\/+$/, '')}/artifacts/${record.id}`;
  const registration = await request<{
    build: { id: string; status: string };
    upload?: { url: string; method: string; headers?: Record<string, string> };
  }>(url, {
    method: 'PUT',
    retry: false,
    body: { artifactType: record.artifactType, metadata: record.metadata, size, sha256 },
  });
  if (registration.build?.id !== record.id) {
    throw new Error('Unexpected build registration response.');
  }
  if (registration.build.status === 'ready') {
    return record.id;
  }
  const upload = registration.upload;
  if (!upload || upload.method !== 'PUT') {
    throw new Error('Invalid artifact upload response.');
  }
  assertSafeUploadUrl(upload.url);
  // Local storage uploads through the server's own route, which also checks the API token.
  // A bucket URL never receives it.
  const uploadAuth = upload.url === `${url}/upload` ? getAuthHeaders(retrieveCredentials()) : {};
  log.info(`Uploading ${path.basename(file)} (${(size / 1048576).toFixed(1)} MB)`);
  const stream = fs.createReadStream(file);
  let sent = 0;
  let lastProgress = 0;
  stream.on('data', chunk => {
    sent += chunk.length;
    if (Date.now() - lastProgress > 1000 || sent === size) {
      log.write(`Sending ${(sent / 1048576).toFixed(1)} / ${(size / 1048576).toFixed(1)} MB`);
      lastProgress = Date.now();
    }
  });
  try {
    const response = await originalFetch(upload.url, {
      method: 'PUT',
      body: stream,
      headers: { ...upload.headers, ...uploadAuth, 'Content-Length': String(size) },
      redirect: 'error',
      timeout: 30 * 60 * 1000,
    });
    if (!response.ok) {
      throw new Error(`Artifact upload returned HTTP ${response.status}.`);
    }
    response.body.resume();
  } catch (error) {
    const message =
      error instanceof Error && error.message.startsWith('Artifact upload returned HTTP')
        ? error.message
        : 'Artifact upload failed. The local artifact and build metadata are retained.';
    throw new Error(message);
  } finally {
    stream.destroy();
  }
  const completed = await request<{ id: string; status: string }>(`${url}/complete`, {
    method: 'POST',
    retry: false,
    timeout: 30 * 60 * 1000,
  });
  if (completed.id !== record.id || completed.status !== 'ready') {
    throw new Error('The server has not finalized the artifact.');
  }
  log.info(`Build ${record.id} uploaded and verified.`);
  return record.id;
}

export async function uploadBuildArtifact(
  file: string,
  serverUrl: string,
  log: BuildLog
): Promise<string> {
  return await log.runStep(BuildStep.UPLOAD_APPLICATION_ARCHIVE, stepLog =>
    uploadArtifact(file, serverUrl, stepLog)
  );
}
