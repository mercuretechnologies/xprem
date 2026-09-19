import { createHash, randomUUID } from 'crypto';
import fs from 'fs-extra';
import fetch from 'node-fetch';
import path from 'path';
import { Readable, Transform } from 'stream';
import { pipeline } from 'stream/promises';
import * as tar from 'tar';

import { BuildServerError, request } from './server';
import { assertSafeUploadUrl } from '../assets';
import { getAuthHeaders, retrieveCredentials } from '../auth';

const MAX_ARCHIVE_BYTES = 512 * 1024 * 1024;
const MAX_EXTRACTED_BYTES = 1024 * 1024 * 1024;
const TRANSFER_TIMEOUT = 120000;

function transferTimeout(size: number): number {
  return TRANSFER_TIMEOUT + (size / (1024 * 1024)) * 8000;
}

// These namespaces exchange directory snapshots as TAR archives.
export type CacheArchiveNamespace = 'gradle' | 'ccache';

export interface CacheArchive {
  endpoint: string;
  namespace: CacheArchiveNamespace;
  key: string;
  directory: string;
  temporary: string;
}

interface CacheObject {
  id: string;
  namespace: CacheArchiveNamespace;
  key: string;
  size: number;
  sha256: string;
}

export async function restoreCacheArchive(archive: CacheArchive): Promise<boolean> {
  const { endpoint, namespace, key, directory, temporary } = archive;
  let found: { object: CacheObject; url: string };
  try {
    found = await request(`${endpoint}/cache/${namespace}/${encodeURIComponent(key)}`, {
      retry: false,
      timeout: 15000,
    });
  } catch (error) {
    if (error instanceof BuildServerError && error.status === 404) {
      return false;
    }
    throw error;
  }
  const { object } = found;
  if (
    object.namespace !== namespace ||
    object.key !== key ||
    !Number.isSafeInteger(object.size) ||
    object.size < 1 ||
    object.size > MAX_ARCHIVE_BYTES ||
    !/^[a-f0-9]{64}$/.test(object.sha256)
  ) {
    throw new Error('Invalid cache archive metadata.');
  }
  const url = found.url || `${endpoint}/cache/uploads/${encodeURIComponent(object.id)}/download`;
  assertSafeUploadUrl(url);
  const file = path.join(temporary, `${namespace}-${randomUUID()}.tar`);
  const controller = new AbortController();
  const timeout = setTimeout(() => {
    controller.abort();
  }, transferTimeout(object.size));
  try {
    const response = await fetch(url, {
      headers: found.url ? {} : getAuthHeaders(retrieveCredentials()),
      redirect: 'error',
      signal: controller.signal,
    });
    if (!response.ok) {
      (response.body as Readable).destroy();
      throw new BuildServerError(response.status);
    }
    const content = await spool(response.body as Readable, file, object.size, controller.signal);
    if (content.size !== object.size || content.sha256 !== object.sha256) {
      throw new Error('Cache archive integrity check failed.');
    }
    // Validate the whole archive before writing anything into the build's cache directory.
    await validateArchive(file, namespace);
    await fs.ensureDir(directory);
    await tar.x({ file, cwd: directory, strict: true, noChmod: true });
    return true;
  } finally {
    clearTimeout(timeout);
    await fs.remove(file);
  }
}

export async function saveCacheArchive(archive: CacheArchive): Promise<number> {
  const { endpoint, namespace, key, directory, temporary } = archive;
  if ((await fs.readdir(directory)).length === 0) {
    return 0;
  }
  const file = path.join(temporary, `${namespace}-${randomUUID()}.tar`);
  try {
    // Compiler cache entries are already compressed; preserve their modification times for pruning.
    const content = await spool(
      Readable.from(tar.c({ cwd: directory, portable: true }, ['.'])),
      file,
      MAX_ARCHIVE_BYTES
    );
    const registration = await request<{
      object: CacheObject;
      cached?: boolean;
      upload?: { url: string; method: string; headers?: Record<string, string> };
    }>(`${endpoint}/cache/uploads`, {
      method: 'POST',
      retry: false,
      body: { namespace, key, ...content },
      timeout: 15000,
    });
    const { object, upload } = registration;
    if (
      object.namespace !== namespace ||
      object.key !== key ||
      object.size !== content.size ||
      object.sha256 !== content.sha256
    ) {
      throw new Error('Invalid cache archive registration.');
    }
    if (registration.cached) {
      return content.size;
    }
    const url = upload?.url ?? `${endpoint}/cache/uploads/${encodeURIComponent(object.id)}`;
    assertSafeUploadUrl(url);
    if (upload && upload.method !== 'PUT') {
      throw new Error('Invalid cache archive upload method.');
    }
    const controller = new AbortController();
    const timeout = setTimeout(() => {
      controller.abort();
    }, transferTimeout(content.size));
    const body = fs.createReadStream(file);
    try {
      const response = await fetch(url, {
        method: 'PUT',
        body,
        redirect: 'error',
        signal: controller.signal,
        headers: {
          ...(upload ? upload.headers : getAuthHeaders(retrieveCredentials())),
          'Content-Length': String(content.size),
        },
      });
      (response.body as Readable).destroy();
      if (!response.ok) {
        throw new BuildServerError(response.status);
      }
    } finally {
      clearTimeout(timeout);
      body.destroy();
    }
    await request(`${endpoint}/cache/uploads/${encodeURIComponent(object.id)}/complete`, {
      method: 'POST',
      retry: false,
      timeout: TRANSFER_TIMEOUT,
    });
    return content.size;
  } finally {
    await fs.remove(file);
  }
}

async function validateArchive(file: string, namespace: CacheArchiveNamespace): Promise<void> {
  let size = 0;
  let entries = 0;
  const input = fs.createReadStream(file);
  const parser = tar.t({
    strict: true,
    onentry(entry) {
      const name = entry.path;
      const parts = name.split('/').filter(part => part !== '' && part !== '.');
      // Gradle archives are installed into a persistent home; settings and scripts stay local.
      const gradlePath =
        parts.length === 0
          ? entry.type === 'Directory'
          : ['build-cache-1', 'journal-1'].includes(parts[0]) &&
            (parts.length > 1 || entry.type === 'Directory');
      const entrySize = entry.size ?? 0;
      size += entrySize;
      entries++;
      if (
        !['File', 'Directory'].includes(entry.type ?? '') ||
        path.posix.isAbsolute(name) ||
        name.includes('\\') ||
        /^[a-z]:/i.test(name) ||
        name.split('/').includes('..') ||
        (namespace === 'gradle' && !gradlePath) ||
        !Number.isSafeInteger(entrySize) ||
        entrySize < 0 ||
        size > MAX_EXTRACTED_BYTES ||
        entries > 100000
      ) {
        input.destroy(new Error('Unsafe cache archive.'));
      }
    },
  });
  await pipeline(input, parser);
}

async function spool(
  input: Readable,
  file: string,
  limit: number,
  signal?: AbortSignal
): Promise<{ size: number; sha256: string }> {
  const hash = createHash('sha256');
  let size = 0;
  const measure = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      size += chunk.length;
      if (size > limit) {
        callback(new Error('Cache archive exceeds its size limit.'));
        return;
      }
      hash.update(chunk);
      callback(null, chunk);
    },
  });
  await pipeline(input, measure, fs.createWriteStream(file, { flags: 'wx', mode: 0o600 }), {
    signal,
  });
  return { size, sha256: hash.digest('hex') };
}
