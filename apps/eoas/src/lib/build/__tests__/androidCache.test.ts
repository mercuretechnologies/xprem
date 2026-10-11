import spawnAsync from '@expo/spawn-async';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { restoreAndroidCache } from '../android/cache';
import { restoreCacheArchive, saveCacheArchive } from '../cacheArchive';
import { LogWriter } from '../log';
import { BuildInputs } from '../prepare';

vi.mock('@expo/spawn-async', () => ({ default: vi.fn() }));
vi.mock('../cacheArchive', () => ({
  restoreCacheArchive: vi.fn(),
  saveCacheArchive: vi.fn(),
}));

let temporary: string;
let build: Pick<BuildInputs, 'endpoint' | 'env' | 'options'>;
const log: LogWriter = { write: vi.fn(), info: vi.fn(), warn: vi.fn() };

beforeEach(async () => {
  vi.resetAllMocks();
  temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-android-cache-'));
  const bin = path.join(temporary, 'bin');
  await fs.outputFile(path.join(bin, 'ccache'), '', { mode: 0o755 });
  vi.mocked(spawnAsync).mockResolvedValue({
    stdout: 'ccache version 4.14',
    stderr: '',
    output: [],
    status: 0,
    signal: null,
  });
  vi.mocked(restoreCacheArchive).mockResolvedValue(false);
  vi.mocked(saveCacheArchive).mockResolvedValue(1024);
  build = {
    endpoint: 'https://xprem.test/app/build/identifier',
    env: { PATH: bin, GRADLE_USER_HOME: path.join(temporary, 'gradle-home') },
    options: { profile: 'test' },
  };
});

afterEach(async () => {
  await fs.remove(temporary);
});

it('restores native Gradle caches without touching local settings when ccache restoration fails', async () => {
  const home = build.env.GRADLE_USER_HOME!;
  await fs.outputFile(path.join(home, 'caches/build-cache-1/previous'), 'old task output');
  await fs.outputFile(path.join(home, 'caches/journal-1/previous'), 'old journal');
  await fs.outputFile(path.join(home, 'caches/modules-2/dependency'), 'local dependency');
  await fs.outputFile(path.join(home, 'gradle.properties'), 'local settings');
  const gradleEntry = 'a'.repeat(32);
  vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
    if (archive.namespace === 'gradle') {
      await fs.outputFile(
        path.join(archive.directory, 'build-cache-1', gradleEntry),
        'Gradle result'
      );
      await fs.outputFile(path.join(archive.directory, 'journal-1/file-access.bin'), 'journal');
      await fs.outputFile(
        path.join(archive.directory, 'journal-1/file-access.properties'),
        'inception'
      );
      return true;
    }
    await fs.outputFile(path.join(archive.directory, 'a', 'partial'), 'unfinished entry');
    throw new Error('Interrupted download');
  });
  const cache = await restoreAndroidCache(build, temporary, log);
  expect(await fs.readdir(cache!.env.CCACHE_DIR!)).toEqual([]);
  expect(await fs.readdir(path.join(home, 'caches/build-cache-1'))).toEqual([gradleEntry]);
  expect(await fs.readFile(path.join(home, 'caches/build-cache-1', gradleEntry), 'utf8')).toBe(
    'Gradle result'
  );
  expect(await fs.readFile(path.join(home, 'caches/journal-1/file-access.bin'), 'utf8')).toBe(
    'journal'
  );
  expect(await fs.pathExists(path.join(home, 'caches/journal-1/previous'))).toBe(false);
  expect(await fs.readFile(path.join(home, 'caches/modules-2/dependency'), 'utf8')).toBe(
    'local dependency'
  );
  expect(await fs.readFile(path.join(home, 'gradle.properties'), 'utf8')).toBe('local settings');
  expect(log.warn).toHaveBeenCalledWith(
    'Could not restore ccache cache; starting with an empty cache.'
  );
  expect(cache!.env.CCACHE_REMOTE_STORAGE).toBe('');
  expect(cache!.args).toContain('--build-cache');
});

it('discards machine-specific ccache metadata while retaining compiled entries', async () => {
  build.env.CCACHE_TEMPDIR = '/previous-machine/temporary';
  build.env.CCACHE_CONFIGPATH = '/current-machine/ccache.conf';
  vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
    if (archive.namespace !== 'ccache') {
      return false;
    }
    await fs.outputFile(path.join(archive.directory, 'a', 'result'), 'compiled result');
    await fs.outputFile(path.join(archive.directory, 'tmp', 'inode-cache-64.v3'), 'old inodes');
    await fs.writeFile(path.join(archive.directory, 'ccache.conf'), 'prefix_command=old-wrapper');
    return true;
  });

  const cache = await restoreAndroidCache(build, temporary, log);
  const directory = cache!.env.CCACHE_DIR!;
  expect(await fs.readFile(path.join(directory, 'a', 'result'), 'utf8')).toBe('compiled result');
  expect(await fs.pathExists(path.join(directory, 'tmp'))).toBe(false);
  expect(await fs.pathExists(path.join(directory, 'ccache.conf'))).toBe(false);
  expect(cache!.env.CCACHE_TEMPDIR).toBe(path.join(temporary, 'ccache-tmp'));
  expect({ ...build.env, ...cache!.env }.CCACHE_CONFIGPATH).toBe('/current-machine/ccache.conf');
});

it('restores and saves only Gradle when ccache is explicitly disabled', async () => {
  build.env.CCACHE_DISABLE = '1';
  build.env.CMAKE_CXX_COMPILER_LAUNCHER = '/project/compiler-launcher';
  vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
    await fs.outputFile(path.join(archive.directory, 'build-cache-1/result'), 'Gradle result');
    return true;
  });

  const cache = await restoreAndroidCache(build, temporary, log);
  expect(restoreCacheArchive).toHaveBeenCalledOnce();
  expect(restoreCacheArchive).toHaveBeenCalledWith(
    expect.objectContaining({ namespace: 'gradle' })
  );
  expect(cache!.args).toContain('--build-cache');
  expect(cache!.env).toEqual({});
  expect({ ...build.env, ...cache!.env }).toMatchObject({
    CCACHE_DISABLE: '1',
    CMAKE_CXX_COMPILER_LAUNCHER: '/project/compiler-launcher',
  });
  await cache!.report(log);
  await cache!.save(log);
  expect(saveCacheArchive).toHaveBeenCalledOnce();
  expect(saveCacheArchive).toHaveBeenCalledWith(expect.objectContaining({ namespace: 'gradle' }));
  expect(spawnAsync).not.toHaveBeenCalled();
  expect(log.warn).not.toHaveBeenCalled();
});

it.each([
  'ANDROID_CCACHE',
  'NDK_CCACHE',
  'CMAKE_C_COMPILER_LAUNCHER',
  'CMAKE_CXX_COMPILER_LAUNCHER',
])('preserves an explicitly configured %s', async name => {
  build.env[name] = '/project/compiler-cache';
  const cache = await restoreAndroidCache(build, temporary, log);
  expect({ ...build.env, ...cache!.env }[name]).toBe('/project/compiler-cache');
  expect(cache!.env.ANDROID_CCACHE).toBeUndefined();
  expect(cache!.env.CMAKE_C_COMPILER_LAUNCHER).toBeUndefined();
  expect(cache!.env.CMAKE_CXX_COMPILER_LAUNCHER).toBeUndefined();
});

it('retains the previous ccache archive when no C/C++ compilation ran', async () => {
  vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
    if (archive.namespace === 'ccache') {
      await fs.outputFile(path.join(archive.directory, 'a', 'result'), 'cached result');
      return true;
    }
    return false;
  });
  const cache = await restoreAndroidCache(build, temporary, log);
  await cache!.save(log);
  expect(saveCacheArchive).not.toHaveBeenCalled();
  expect(spawnAsync).toHaveBeenCalledTimes(1);
  expect(log.info).toHaveBeenCalledWith(
    'No C/C++ compilations; retaining the previous ccache archive.'
  );
});

it.each([
  {
    name: 'retains a restored archive when direct hits write no cache entries',
    restored: true,
    stats: {
      direct_cache_hit: 2,
      preprocessed_cache_hit: 0,
      cache_miss: 0,
      local_storage_write: 0,
      could_not_use_precompiled_header: 0,
    },
    publishedArchives: [],
  },
  {
    name: 'publishes a manifest created by a preprocessed hit',
    restored: true,
    stats: {
      direct_cache_hit: 0,
      preprocessed_cache_hit: 1,
      cache_miss: 0,
      local_storage_write: 1,
      could_not_use_precompiled_header: 0,
    },
    publishedArchives: ['ccache'],
  },
  {
    name: 'publishes new entries after a cold compilation',
    restored: false,
    stats: {
      direct_cache_hit: 0,
      preprocessed_cache_hit: 0,
      cache_miss: 1,
      local_storage_write: 2,
      could_not_use_precompiled_header: 0,
    },
    publishedArchives: ['ccache'],
  },
])('$name', async ({ restored, stats, publishedArchives }) => {
  build.env.CCACHE_NOSTATS = '1';
  build.env.CCACHE_STATS = '0';
  vi.mocked(restoreCacheArchive).mockResolvedValue(restored);
  const cache = await restoreAndroidCache(build, temporary, log);
  await fs.outputFile(path.join(cache!.env.CCACHE_DIR!, 'a', 'result'), 'compiled result');
  await fs.writeFile(cache!.env.CCACHE_STATSLOG!, 'per-build counters');
  vi.mocked(spawnAsync).mockResolvedValue({
    stdout: JSON.stringify(stats),
    stderr: '',
    output: [],
    status: 0,
    signal: null,
  });
  await cache!.report(log);
  await cache!.save(log);

  expect(spawnAsync).toHaveBeenCalledWith(
    expect.any(String),
    ['--print-log-stats', '--format=json'],
    expect.objectContaining({
      env: expect.objectContaining({ CCACHE_STATS: '1', CCACHE_NOSTATS: undefined }),
    })
  );
  const published = vi.mocked(saveCacheArchive).mock.calls.map(([archive]) => archive.namespace);
  expect(published).toEqual(publishedArchives);
});

it.each(['missing', 'failed'])(
  'preserves the local Gradle cache when its archive is %s',
  async state => {
    const entry = path.join(build.env.GRADLE_USER_HOME!, 'caches/build-cache-1/result');
    await fs.outputFile(entry, 'local task output');
    vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
      if (archive.namespace === 'gradle' && state === 'failed') {
        await fs.outputFile(path.join(archive.directory, 'build-cache-1/partial'), 'unfinished');
        throw new Error('Interrupted archive');
      }
      return false;
    });
    await restoreAndroidCache(build, temporary, log);
    expect(await fs.readFile(entry, 'utf8')).toBe('local task output');
    expect(await fs.pathExists(path.join(path.dirname(entry), 'partial'))).toBe(false);
  }
);

it('archives native Gradle entries and cleanup metadata without transient files or unrelated caches', async () => {
  const cache = await restoreAndroidCache(build, temporary, log);
  const home = build.env.GRADLE_USER_HOME!;
  for (const [file, content] of Object.entries({
    'build-cache-1/result': 'task output',
    'build-cache-1/build-cache-1.lock': 'lock',
    'build-cache-1/gc.properties': 'last cleanup',
    'build-cache-1/interrupted.part': 'unfinished write',
    'build-cache-1/corrupt.failed': 'failed read',
    'journal-1/file-access.bin': 'journal',
    'journal-1/file-access.properties': 'inception',
    'journal-1/journal-1.lock': 'lock',
    'modules-2/dependency': 'private dependency',
  })) {
    await fs.outputFile(path.join(home, 'caches', file), content);
  }
  await fs.symlink(
    path.join(home, 'caches/modules-2/dependency'),
    path.join(home, 'caches/build-cache-1/link')
  );
  const snapshot = path.join(temporary, 'saved-gradle-cache');
  vi.mocked(saveCacheArchive).mockImplementation(async archive => {
    await fs.copy(archive.directory, snapshot);
    return 1024;
  });

  await cache!.save(log);

  expect(saveCacheArchive).toHaveBeenCalledTimes(1);
  expect(saveCacheArchive).toHaveBeenCalledWith(expect.objectContaining({ namespace: 'gradle' }));
  expect((await fs.readdir(snapshot)).sort()).toEqual(['build-cache-1', 'journal-1']);
  expect((await fs.readdir(path.join(snapshot, 'build-cache-1'))).sort()).toEqual([
    'gc.properties',
    'result',
  ]);
  expect((await fs.readdir(path.join(snapshot, 'journal-1'))).sort()).toEqual([
    'file-access.bin',
    'file-access.properties',
  ]);
  expect(await fs.readFile(path.join(home, 'caches/build-cache-1/gc.properties'), 'utf8')).toBe(
    'last cleanup'
  );
});

it('continues saving the other archive if publication fails', async () => {
  const cache = await restoreAndroidCache(build, temporary, log);
  await fs.writeFile(cache!.env.CCACHE_STATSLOG!, 'cache_miss\n');
  await fs.outputFile(
    path.join(build.env.GRADLE_USER_HOME!, 'caches/build-cache-1/result'),
    'Gradle result'
  );
  vi.mocked(saveCacheArchive).mockImplementation(async archive => {
    if (archive.namespace === 'gradle') {
      throw new Error('request to https://bucket.test/cache?signature=secret failed: ECONNRESET');
    }
    return 1024;
  });
  await expect(cache!.save(log)).resolves.toBeUndefined();
  expect(saveCacheArchive).toHaveBeenCalledTimes(2);
  expect(log.warn).toHaveBeenCalledWith(
    'Could not save gradle cache. The build artifact is available. request to [URL] failed: ECONNRESET'
  );
  expect(log.info).toHaveBeenCalledWith(expect.stringContaining('ccache cache saved:'));
});

it('skips an oversized Gradle archive without deleting local entries or modifying its journal', async () => {
  const cache = await restoreAndroidCache(build, temporary, log);
  const home = build.env.GRADLE_USER_HOME!;
  const entry = path.join(home, 'caches/build-cache-1/result');
  const journal = path.join(home, 'caches/journal-1/file-access.bin');
  await fs.outputFile(entry, '');
  await fs.truncate(entry, 451 * 1024 * 1024);
  await fs.outputFile(journal, 'native journal');
  await cache!.save(log);
  expect(saveCacheArchive).not.toHaveBeenCalled();
  expect((await fs.stat(entry)).size).toBe(451 * 1024 * 1024);
  expect(await fs.readFile(journal, 'utf8')).toBe('native journal');
  expect(log.warn).toHaveBeenCalledWith(
    'Gradle cache exceeds 450 MiB; retaining the previous archive.'
  );
});
