/* eslint-disable node/no-sync */
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { AssetToUpload, RequestUploadUrlItem, resolveUploadRequests } from '../assets';

// These cover what a hostile or compromised update server can put in its
// requestUploadUrl answer. Everything here must be rejected before the CLI opens
// a single file: the payoff for the attacker is reading arbitrary files off a
// developer or CI machine and having them PUT to a URL it controls.

let exportDir: string;
let outsideDir: string;

const manifest: AssetToUpload[] = [
  { path: 'metadata.json', name: 'metadata.json', ext: 'json', hash: 'unused' },
  { path: 'bundles/ios-abc.hbc', name: 'ios-abc.hbc', ext: 'hbc', hash: 'unused' },
];

function item(overrides: Partial<RequestUploadUrlItem> = {}): RequestUploadUrlItem {
  return {
    requestUploadUrl: 'https://storage.example.com/upload/metadata.json',
    fileName: 'metadata.json',
    filePath: 'metadata.json',
    ...overrides,
  };
}

function resolve(uploadRequests: RequestUploadUrlItem[]): Promise<unknown> {
  return resolveUploadRequests({ uploadRequests, exportDir, manifest });
}

beforeEach(() => {
  exportDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-export-'));
  outsideDir = fs.mkdtempSync(path.join(os.tmpdir(), 'eoas-secrets-'));
  fs.writeFileSync(path.join(exportDir, 'metadata.json'), '{}');
  fs.ensureDirSync(path.join(exportDir, 'bundles'));
  fs.writeFileSync(path.join(exportDir, 'bundles', 'ios-abc.hbc'), 'bundle');
  // Present in the export directory but never exported by the CLI.
  fs.writeFileSync(path.join(exportDir, '.env'), 'EXPO_TOKEN=secret');
  fs.writeFileSync(path.join(outsideDir, 'id_rsa'), 'private key');
});

afterEach(() => {
  fs.removeSync(exportDir);
  fs.removeSync(outsideDir);
});

describe('resolveUploadRequests on a well-behaved server response', () => {
  it('resolves each request to a canonical path inside the export directory', async () => {
    const resolved = await resolveUploadRequests({
      uploadRequests: [
        item(),
        item({
          filePath: 'bundles/ios-abc.hbc',
          fileName: 'ios-abc.hbc',
          requestUploadUrl: 'https://storage.example.com/upload/ios-abc.hbc',
        }),
      ],
      exportDir,
      manifest,
    });
    expect(resolved.map(r => r.absolutePath)).toEqual([
      path.join(fs.realpathSync(exportDir), 'metadata.json'),
      path.join(fs.realpathSync(exportDir), 'bundles', 'ios-abc.hbc'),
    ]);
    expect(resolved[1].manifestEntry.ext).toBe('hbc');
  });

  it('accepts an empty response', async () => {
    await expect(resolve([])).resolves.toEqual([]);
  });
});

describe('resolveUploadRequests against forged file paths', () => {
  it('rejects traversal out of the export directory', async () => {
    const filePath = path.join('..', path.basename(outsideDir), 'id_rsa');
    await expect(resolve([item({ filePath, fileName: 'id_rsa' })])).rejects.toThrow(
      /requested a path outside the export directory/
    );
  });

  it('rejects traversal that loops back into the export directory', async () => {
    // Lands on a real, readable file, so only the '..' rejection stops it.
    await expect(
      resolve([item({ filePath: 'bundles/../metadata.json', fileName: 'metadata.json' })])
    ).rejects.toThrow(/requested a path outside the export directory/);
  });

  it('rejects absolute paths', async () => {
    await expect(
      resolve([item({ filePath: path.join(outsideDir, 'id_rsa'), fileName: 'id_rsa' })])
    ).rejects.toThrow(/absolute path/);
  });

  it('rejects windows-style absolute paths', async () => {
    await expect(
      resolve([item({ filePath: 'C:\\Users\\ci\\.npmrc', fileName: '.npmrc' })])
    ).rejects.toThrow(/absolute path/);
  });

  it('rejects backslash traversal', async () => {
    await expect(
      resolve([item({ filePath: '..\\..\\.npmrc', fileName: '.npmrc' })])
    ).rejects.toThrow(/requested a path outside the export directory/);
  });

  it('matches the manifest by path, never by file name', async () => {
    // 'ios-abc.hbc' is the *name* of a manifest entry whose path is
    // 'bundles/ios-abc.hbc'. Matching on the name would resolve it at the
    // export root, which is not where the CLI exported it.
    await expect(
      resolve([item({ filePath: 'ios-abc.hbc', fileName: 'ios-abc.hbc' })])
    ).rejects.toThrow(/not part of this export/);
  });

  it('rejects a file that exists in the export directory but was never exported', async () => {
    await expect(resolve([item({ filePath: '.env', fileName: '.env' })])).rejects.toThrow(
      /not part of this export/
    );
  });

  it('rejects a path with a NUL byte', async () => {
    await expect(resolve([item({ filePath: 'metadata.json\0.png' })])).rejects.toThrow(
      /empty or malformed/
    );
  });

  it('rejects an empty path', async () => {
    await expect(resolve([item({ filePath: '' })])).rejects.toThrow(/empty or malformed/);
  });

  it('rejects a file name that does not match the file path', async () => {
    await expect(resolve([item({ fileName: '../../evil.json' })])).rejects.toThrow(
      /mismatched name/
    );
  });

  it('rejects the same file requested twice in one response', async () => {
    await expect(resolve([item(), item()])).rejects.toThrow(/more than once/);
  });

  it('allows the same file across two responses, as --platform all produces', async () => {
    // publish.ts asks for upload URLs once per runtime version, and each answer
    // names the very same local files. Deduplication is per response only.
    await expect(resolve([item()])).resolves.toHaveLength(1);
    await expect(
      resolve([item({ requestUploadUrl: 'https://storage.example.com/android/metadata.json' })])
    ).resolves.toHaveLength(1);
  });

  it('rejects a manifest entry that is missing on disk', async () => {
    fs.removeSync(path.join(exportDir, 'metadata.json'));
    await expect(resolve([item()])).rejects.toThrow(/not found in the export directory/);
  });
});

describe('resolveUploadRequests against symlinks', () => {
  it('rejects a symlinked file pointing outside the export directory', async () => {
    fs.removeSync(path.join(exportDir, 'metadata.json'));
    fs.symlinkSync(path.join(outsideDir, 'id_rsa'), path.join(exportDir, 'metadata.json'));
    await expect(resolve([item()])).rejects.toThrow(/symlink/);
  });

  it('rejects a file reached through a symlinked parent directory', async () => {
    fs.removeSync(path.join(exportDir, 'bundles'));
    fs.symlinkSync(outsideDir, path.join(exportDir, 'bundles'));
    fs.writeFileSync(path.join(outsideDir, 'ios-abc.hbc'), 'stolen');
    await expect(
      resolve([item({ filePath: 'bundles/ios-abc.hbc', fileName: 'ios-abc.hbc' })])
    ).rejects.toThrow(/symlink/);
  });

  it('rejects a symlink that stays inside the export directory', async () => {
    fs.removeSync(path.join(exportDir, 'metadata.json'));
    fs.symlinkSync(path.join(exportDir, '.env'), path.join(exportDir, 'metadata.json'));
    await expect(resolve([item()])).rejects.toThrow(/symlink/);
  });

  it('rejects a directory in place of a file', async () => {
    fs.removeSync(path.join(exportDir, 'metadata.json'));
    fs.ensureDirSync(path.join(exportDir, 'metadata.json'));
    await expect(resolve([item()])).rejects.toThrow(/not a regular file/);
  });
});

describe('resolveUploadRequests transport requirements', () => {
  it('rejects a plain HTTP upload URL on a remote host', async () => {
    await expect(
      resolve([item({ requestUploadUrl: 'http://attacker.tld/collect' })])
    ).rejects.toThrow(/only be sent over HTTPS/);
  });

  it('allows plain HTTP on loopback addresses', async () => {
    for (const requestUploadUrl of [
      'http://localhost:3000/app/uploadLocalFile',
      'http://127.0.0.1:3000/app/uploadLocalFile',
      'http://[::1]:3000/app/uploadLocalFile',
      // Node's URL normalizes these to 127.0.0.1 before the host is inspected.
      'http://127.1/app/uploadLocalFile',
      'http://2130706433/app/uploadLocalFile',
    ]) {
      await expect(resolve([item({ requestUploadUrl })])).resolves.toHaveLength(1);
    }
  });

  it('does not let a remote host masquerade as loopback', async () => {
    for (const requestUploadUrl of [
      // *.localhost is deliberately not exempt: a resolver may answer it from DNS.
      'http://evil.localhost/collect',
      'http://attacker.tld.localhost/collect',
      // The 127.x pattern is anchored at both ends.
      'http://127.0.0.1.evil.tld/collect',
      'http://evil127.0.0.1.tld/collect',
    ]) {
      await expect(resolve([item({ requestUploadUrl })])).rejects.toThrow(
        /only be sent over HTTPS/
      );
    }
  });

  it.each(['1', 'true', 'TRUE', 'True'])('honours the opt-in spelled %s', async spelling => {
    process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS = spelling;
    try {
      await expect(
        resolve([item({ requestUploadUrl: 'http://minio:9000/updates/metadata.json' })])
      ).resolves.toHaveLength(1);
    } finally {
      delete process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS;
    }
  });

  it.each(['0', 'false', 'yes', ''])('ignores the opt-in spelled %s', async spelling => {
    process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS = spelling;
    try {
      await expect(
        resolve([item({ requestUploadUrl: 'http://minio:9000/updates/metadata.json' })])
      ).rejects.toThrow(/only be sent over HTTPS/);
    } finally {
      delete process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS;
    }
  });

  it('allows plain HTTP when the operator opted in, and never lifts a path check', async () => {
    // MinIO or an S3-compatible endpoint on AWS_BASE_ENDPOINT, or a local bucket
    // on an internal BASE_URL, both legitimately serve http.
    process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS = '1';
    try {
      await expect(
        resolve([item({ requestUploadUrl: 'http://minio:9000/updates/metadata.json' })])
      ).resolves.toHaveLength(1);
      await expect(
        resolve([
          item({
            requestUploadUrl: 'http://minio:9000/updates/.env',
            filePath: '.env',
            fileName: '.env',
          }),
        ])
      ).rejects.toThrow(/not part of this export/);
    } finally {
      delete process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS;
    }
  });

  it('rejects non-http schemes even when the opt-in is set', async () => {
    process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS = '1';
    try {
      await expect(resolve([item({ requestUploadUrl: 'file:///etc/passwd' })])).rejects.toThrow(
        /only be sent over HTTPS/
      );
    } finally {
      delete process.env.EOAS_ALLOW_INSECURE_UPLOAD_URLS;
    }
  });

  it('rejects non-http schemes', async () => {
    await expect(resolve([item({ requestUploadUrl: 'file:///etc/passwd' })])).rejects.toThrow(
      /only be sent over HTTPS/
    );
  });

  it('rejects an unparseable upload URL', async () => {
    await expect(resolve([item({ requestUploadUrl: 'not a url' })])).rejects.toThrow(
      /unusable upload URL/
    );
  });

  it('rejects the whole response when a single entry is bad', async () => {
    // That no upload happens either is proven at the command level, in
    // src/commands/__tests__/publish.test.ts.
    await expect(resolve([item(), item({ filePath: '.env', fileName: '.env' })])).rejects.toThrow(
      /not part of this export/
    );
  });
});
