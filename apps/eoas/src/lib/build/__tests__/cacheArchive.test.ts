import { createHash } from 'crypto';
import fs from 'fs-extra';
import { IncomingHttpHeaders, IncomingMessage, ServerResponse, createServer } from 'http';
import os from 'os';
import path from 'path';
import { afterEach, expect, it, vi } from 'vitest';

import { CacheArchive, restoreCacheArchive, saveCacheArchive } from '../cacheArchive';
import { BuildServerError, request } from '../server';

vi.mock('../server', async importOriginal => ({
  ...(await importOriginal<typeof import('../server')>()),
  request: vi.fn(),
}));
vi.mock('../../auth', () => ({
  retrieveCredentials: () => ({ token: 'test-token' }),
  getAuthHeaders: () => ({ Authorization: 'Bearer test-token' }),
}));

const cleanups: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) {
    await cleanup();
  }
  vi.resetAllMocks();
});

it.each(['gradle', 'ccache'] as const)(
  'uploads and restores %s cache files with their timestamps',
  async namespace => {
    const archive = await fixture(namespace);
    const resultPath = namespace === 'gradle' ? 'build-cache-1/result' : 'a/result';
    const metadataPath = namespace === 'gradle' ? 'journal-1/file-access.bin' : 'b/manifest';
    await fs.outputFile(path.join(archive.directory, resultPath), 'compiled bytes');
    await fs.outputFile(path.join(archive.directory, metadataPath), 'manifest bytes');
    const modified = new Date('2026-01-02T03:04:05Z');
    await fs.utimes(path.join(archive.directory, resultPath), modified, modified);
    const storage = await uploadServer(archive, 'bucket');

    expect(await saveCacheArchive(archive)).toBeGreaterThan(0);
    const restored = { ...archive, directory: path.join(archive.temporary, 'restored') };
    expect(await restoreCacheArchive(restored)).toBe(true);

    expect(storage.bytesAtPublication).toBeGreaterThan(0);
    expect(request).toHaveBeenNthCalledWith(
      1,
      `${archive.endpoint}/cache/uploads`,
      expect.objectContaining({
        body: {
          namespace,
          key: archive.key,
          size: storage.bytes.length,
          sha256: createHash('sha256').update(storage.bytes).digest('hex'),
        },
      })
    );
    expect(await fs.readFile(path.join(restored.directory, resultPath), 'utf8')).toBe(
      'compiled bytes'
    );
    expect(await fs.readFile(path.join(restored.directory, metadataPath), 'utf8')).toBe(
      'manifest bytes'
    );
    expect((await fs.stat(path.join(restored.directory, resultPath))).mtime).toEqual(modified);
    expect((await fs.readdir(archive.temporary)).filter(name => name.endsWith('.tar'))).toEqual([]);
    expect(vi.mocked(request).mock.calls.map(([endpoint]) => endpoint)).toEqual([
      `${archive.endpoint}/cache/uploads`,
      `${archive.endpoint}/cache/uploads/upload-1/complete`,
      `${archive.endpoint}/cache/${namespace}/${archive.key}`,
    ]);
  }
);

it.each([
  {
    transport: 'bucket' as const,
    uploadPath: '/signed-upload',
    downloadPath: '/signed-download',
    headers: { 'x-upload-test': 'yes', authorization: undefined },
  },
  {
    transport: 'server' as const,
    uploadPath: '/cache/uploads/upload-1',
    downloadPath: '/cache/uploads/upload-1/download',
    headers: { 'x-upload-test': undefined, authorization: 'Bearer test-token' },
  },
])(
  'uses the $transport URL and credentials for transfers',
  async ({ transport, uploadPath, downloadPath, headers }) => {
    const archive = await fixture();
    await fs.outputFile(path.join(archive.directory, 'entry'), 'cached result');
    const storage = await uploadServer(archive, transport);

    await saveCacheArchive(archive);
    await restoreCacheArchive({ ...archive, directory: path.join(archive.temporary, 'restored') });

    expect(storage.transfers.map(({ method, url }) => [method, url])).toEqual([
      ['PUT', uploadPath],
      ['GET', downloadPath],
    ]);
    const [upload, download] = storage.transfers;
    expect(upload.headers['x-upload-test']).toBe(headers['x-upload-test']);
    expect(upload.headers.authorization).toBe(headers.authorization);
    expect(download.headers.authorization).toBe(headers.authorization);
    expect(Number(upload.headers['content-length'])).toBe(storage.bytes.length);
  }
);

it('treats only lookup HTTP 404 as a cache miss', async () => {
  const archive = await fixture();
  vi.mocked(request).mockRejectedValueOnce(new BuildServerError(404));
  expect(await restoreCacheArchive(archive)).toBe(false);
  vi.mocked(request).mockRejectedValueOnce(new BuildServerError(403));
  await expect(restoreCacheArchive(archive)).rejects.toMatchObject({ status: 403 });

  const url = await bucket((_req, res) => {
    res.writeHead(404).end();
  });
  vi.mocked(request).mockResolvedValue({ object: metadata(archive, Buffer.alloc(1024)), url });
  await expect(restoreCacheArchive(archive)).rejects.toMatchObject({ status: 404 });
});

it('rejects oversized archive metadata before downloading', async () => {
  const archive = await fixture();
  const downloaded = vi.fn();
  const url = await bucket((_req, res) => {
    downloaded();
    res.end();
  });
  vi.mocked(request).mockResolvedValue({
    object: { ...metadata(archive, Buffer.alloc(0)), size: 512 * 1024 * 1024 + 1 },
    url,
  });
  await expect(restoreCacheArchive(archive)).rejects.toThrow('Invalid cache archive metadata');
  expect(downloaded).not.toHaveBeenCalled();
});

it('does not publish an archive after an upload failure', async () => {
  const archive = await fixture();
  await fs.outputFile(path.join(archive.directory, 'entry'), 'result');
  const url = await bucket((_req, res) => {
    res.writeHead(500).end();
  });
  vi.mocked(request).mockImplementation(async (_url, options) => ({
    object: { id: 'upload-1', ...(options!.body as object) },
    upload: { url, method: 'PUT' },
  }));
  await expect(saveCacheArchive(archive)).rejects.toMatchObject({ status: 500 });
  expect(request).toHaveBeenCalledTimes(1);
  expect(await fs.readdir(archive.temporary)).toEqual(['cache']);
});

it('does not upload empty caches', async () => {
  const archive = await fixture();
  expect(await saveCacheArchive(archive)).toBe(0);
  expect(request).not.toHaveBeenCalled();
});

it.each([
  {
    name: 'mismatched SHA-256',
    change: (bytes: Buffer) => {
      const corrupt = Buffer.from(bytes);
      corrupt[512] ^= 1;
      return corrupt;
    },
  },
  { name: 'truncated download', change: (bytes: Buffer) => bytes.subarray(0, -1) },
  { name: 'extra byte', change: (bytes: Buffer) => Buffer.concat([bytes, Buffer.alloc(1)]) },
])('rejects a $name before extracting files', async ({ change }) => {
  const archive = await fixture();
  const original = tarArchive({ name: 'cached', content: 'result' });
  await serveArchive(archive, change(original), original);
  await expect(restoreCacheArchive(archive)).rejects.toThrow(/integrity|size limit/);
  expect(await fs.readdir(archive.directory)).toEqual([]);
});

it.each([
  { label: 'parent path', entry: { name: '../escaped', content: 'unsafe' } },
  { label: 'absolute path', entry: { name: '/escaped', content: 'unsafe' } },
  { label: 'symbolic link', entry: { name: 'link', type: '2', link: '../escaped' } },
  { label: 'hard link', entry: { name: 'link', type: '1', link: '../escaped' } },
  { label: 'device', entry: { name: 'device', type: '3' } },
])('rejects an archive containing a $label before extracting any entry', async ({ entry }) => {
  const archive = await fixture();
  const bytes = tarArchive({ name: 'safe', content: 'result' }, entry);
  await serveArchive(archive, bytes);
  await expect(restoreCacheArchive(archive)).rejects.toThrow('Unsafe cache archive');
  expect(await fs.readdir(archive.directory)).toEqual([]);
  expect(await fs.pathExists(path.join(archive.temporary, 'escaped'))).toBe(false);
});

it.each(['gradle.properties', 'init.d/injected.gradle', 'build-cache-1', 'journal-1'])(
  'rejects a Gradle archive that would install %s as a regular file',
  async name => {
    const archive = await fixture('gradle');
    await serveArchive(archive, tarArchive({ name, content: 'not a cache entry' }));
    await expect(restoreCacheArchive(archive)).rejects.toThrow('Unsafe cache archive');
    expect(await fs.readdir(archive.directory)).toEqual([]);
  }
);

async function fixture(namespace: 'gradle' | 'ccache' = 'ccache'): Promise<CacheArchive> {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-cache-test-'));
  cleanups.push(() => fs.remove(temporary));
  const directory = path.join(temporary, 'cache');
  await fs.ensureDir(directory);
  return {
    endpoint: 'https://xprem.test/app/build/identifier',
    namespace,
    key: `archive-v1-${'a'.repeat(64)}`,
    directory,
    temporary,
  };
}

function metadata(archive: CacheArchive, bytes: Buffer): Record<string, unknown> {
  return {
    id: 'cached',
    namespace: archive.namespace,
    key: archive.key,
    size: bytes.length,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  };
}

async function serveArchive(archive: CacheArchive, bytes: Buffer, expected = bytes): Promise<void> {
  const url = await bucket((_req, res) => {
    res.end(bytes);
  });
  vi.mocked(request).mockResolvedValue({ object: metadata(archive, expected), url });
}

async function uploadServer(
  archive: CacheArchive,
  transport: 'bucket' | 'server'
): Promise<{
  bytes: Buffer;
  bytesAtPublication: number;
  transfers: { method: string; url: string; headers: IncomingHttpHeaders }[];
}> {
  const storage = {
    bytes: Buffer.alloc(0),
    bytesAtPublication: 0,
    transfers: [] as { method: string; url: string; headers: IncomingHttpHeaders }[],
  };
  const url = await bucket(async (req, res) => {
    storage.transfers.push({ method: req.method!, url: req.url!, headers: req.headers });
    if (req.method === 'PUT') {
      const chunks: Buffer[] = [];
      for await (const chunk of req) {
        chunks.push(chunk);
      }
      storage.bytes = Buffer.concat(chunks);
    }
    res.end(req.method === 'GET' ? storage.bytes : undefined);
  });
  archive.endpoint = url;
  let object: Record<string, unknown>;
  vi.mocked(request)
    .mockImplementationOnce(async (_endpoint, options) => {
      object = { id: 'upload-1', ...(options!.body as object) };
      return {
        object,
        upload:
          transport === 'bucket'
            ? { url: `${url}/signed-upload`, method: 'PUT', headers: { 'x-upload-test': 'yes' } }
            : undefined,
      };
    })
    .mockImplementationOnce(async () => {
      storage.bytesAtPublication = storage.bytes.length;
      return object;
    })
    .mockImplementationOnce(async () => ({
      object,
      url: transport === 'bucket' ? `${url}/signed-download` : '',
    }));
  return storage;
}

async function bucket(
  handler: (req: IncomingMessage, res: ServerResponse) => void | Promise<void>
): Promise<string> {
  const server = createServer(handler);
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  cleanups.push(
    () =>
      new Promise<void>((resolve, reject) =>
        server.close(error => {
          if (error) {
            reject(error);
          } else {
            resolve();
          }
        })
      )
  );
  return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
}

// Build a USTAR header directly so malicious paths are not normalized by an archive writer.
function tarArchive(
  ...entries: { name: string; content?: string; type?: string; link?: string }[]
): Buffer {
  const chunks: Buffer[] = [];
  for (const { name, content = '', type = '0', link = '' } of entries) {
    const body = Buffer.from(content);
    const header = Buffer.alloc(512);
    header.write(name, 0, 100);
    header.write('0000600\0', 100);
    header.write('0000000\0', 108);
    header.write('0000000\0', 116);
    header.write(body.length.toString(8).padStart(11, '0') + '\0', 124);
    header.write('00000000000\0', 136);
    header.fill(' ', 148, 156);
    header.write(type, 156);
    header.write(link, 157, 100);
    header.write('ustar\0', 257);
    header.write('00', 263);
    const checksum = header.reduce((total, byte) => total + byte, 0);
    header.write(checksum.toString(8).padStart(6, '0') + '\0 ', 148);
    chunks.push(header, body, Buffer.alloc((512 - (body.length % 512)) % 512));
  }
  return Buffer.concat([...chunks, Buffer.alloc(1024)]);
}
