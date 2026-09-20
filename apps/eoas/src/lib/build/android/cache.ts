import spawnAsync from '@expo/spawn-async';
import { createHash } from 'crypto';
import fg from 'fast-glob';
import fs from 'fs-extra';
import path from 'path';

import { CacheArchive, restoreCacheArchive, saveCacheArchive } from '../cacheArchive';
import { LogWriter } from '../log';
import { BuildInputs } from '../prepare';
import { copyTemplate } from '../workspace';

const gradleCacheDirectories = ['build-cache-1', 'journal-1'];

export interface AndroidCache {
  args: string[];
  env: NodeJS.ProcessEnv;
  report(log: LogWriter): Promise<void>;
  save(log: LogWriter): Promise<void>;
}

export async function restoreAndroidCache(
  build: Pick<BuildInputs, 'endpoint' | 'env' | 'options'>,
  temporary: string,
  log: LogWriter
): Promise<AndroidCache | undefined> {
  const script = path.join(temporary, 'cache.gradle');
  const gradleCache = path.join(build.env.GRADLE_USER_HOME!, 'caches');
  const statsLog = path.join(temporary, 'ccache-stats.log');
  const ccacheDisabled = build.env.CCACHE_DISABLE === '1';
  const ccache = ccacheDisabled ? undefined : await findCcache(build.env);
  const scope = ['android', build.options.profile, process.platform, process.arch];
  const archives: CacheArchive[] = (
    ccache ? (['gradle', 'ccache'] as const) : (['gradle'] as const)
  ).map(namespace => ({
    endpoint: build.endpoint,
    namespace,
    key: `archive-v1-${createHash('sha256')
      .update(JSON.stringify(namespace === 'gradle' ? [...scope, 'gradle-native-v2'] : scope))
      .digest('hex')}`,
    directory: path.join(temporary, 'cache', namespace),
    temporary,
  }));
  try {
    await copyTemplate('gradle/cache.gradle', script);
    for (const archive of archives) {
      await fs.ensureDir(archive.directory);
    }
  } catch {
    log.warn('Could not prepare cache directories; compiling without remote cache.');
    return undefined;
  }
  if (ccacheDisabled) {
    log.info('C/C++ cache disabled. Gradle cache remains enabled.');
  } else if (!ccache) {
    log.warn(
      'C/C++ cache unavailable: install ccache 4.11 or later. Gradle cache remains enabled.'
    );
  }
  let ccacheRestored = false;
  await Promise.all(
    archives.map(async archive => {
      const started = Date.now();
      try {
        const restored = await restoreCacheArchive(archive);
        if (archive.namespace === 'gradle' && restored) {
          for (const name of gradleCacheDirectories) {
            const destination = path.join(gradleCache, name);
            await fs.remove(destination);
            const source = path.join(archive.directory, name);
            if (await fs.pathExists(source)) {
              await fs.move(source, destination);
            }
          }
        }
        if (archive.namespace === 'ccache') {
          // Inode indexes and compiler configuration belong to the current machine.
          await fs.remove(path.join(archive.directory, 'tmp'));
          await fs.remove(path.join(archive.directory, 'ccache.conf'));
          ccacheRestored = restored;
        }
        log.info(
          restored
            ? `${archive.namespace} cache restored in ${((Date.now() - started) / 1000).toFixed(
                1
              )}s.`
            : `No ${archive.namespace} cache archive yet.`
        );
      } catch {
        await fs.emptyDir(archive.directory);
        log.warn(
          archive.namespace === 'gradle'
            ? 'Could not restore Gradle cache; continuing with the local cache.'
            : 'Could not restore ccache cache; starting with an empty cache.'
        );
      }
    })
  );
  const customLauncher = [
    'ANDROID_CCACHE',
    'NDK_CCACHE',
    'CMAKE_C_COMPILER_LAUNCHER',
    'CMAKE_CXX_COMPILER_LAUNCHER',
  ].some(name => build.env[name] !== undefined);
  const env = {
    ...(ccache
      ? {
          ...(!customLauncher
            ? {
                ANDROID_CCACHE: ccache,
                CMAKE_C_COMPILER_LAUNCHER: ccache,
                CMAKE_CXX_COMPILER_LAUNCHER: ccache,
              }
            : {}),
          CCACHE_DIR: path.join(temporary, 'cache/ccache'),
          CCACHE_TEMPDIR: path.join(temporary, 'ccache-tmp'),
          CCACHE_REMOTE_STORAGE: '',
          CCACHE_COMPILERCHECK: 'content',
          // Keep large PCH artifacts out of the bounded cache; cache their object-file consumers.
          CCACHE_SLOPPINESS: build.env.CCACHE_SLOPPINESS ?? 'time_macros',
          CCACHE_MAXSIZE: '450M',
          CCACHE_STATS: '1',
          CCACHE_NOSTATS: undefined,
          CCACHE_STATSLOG: statsLog,
        }
      : {}),
  };
  const started = Date.now();
  let ccacheStats: Record<string, number> | undefined;
  return {
    args: ['--build-cache', '--init-script', script],
    env,
    async report(stepLog) {
      if (ccache) {
        ccacheStats = await reportCcacheStats(ccache, statsLog, { ...build.env, ...env }, stepLog);
      }
    },
    async save(stepLog) {
      await Promise.all(
        archives.map(async archive => {
          const saving = Date.now();
          try {
            if (archive.namespace === 'ccache') {
              if (!(await fs.pathExists(statsLog))) {
                stepLog.info('No C/C++ compilations; retaining the previous ccache archive.');
                return;
              }
              if (ccacheRestored && ccacheStats?.local_storage_write === 0) {
                stepLog.info('C/C++ cache unchanged; retaining the previous archive.');
                return;
              }
              // Keep entries used by this build.
              const age = `${Math.ceil((Date.now() - started) / 1000)}s`;
              await spawnAsync(ccache!, ['--evict-older-than', age, '--cleanup'], {
                env: { ...build.env, ...env },
              });
            } else if (!(await snapshotGradleCache(gradleCache, archive.directory, stepLog))) {
              return;
            }
            const bytes = await saveCacheArchive(archive);
            stepLog.info(
              bytes
                ? `${archive.namespace} cache saved: ${(bytes / 1024 / 1024).toFixed(1)} MiB in ${(
                    (Date.now() - saving) /
                    1000
                  ).toFixed(1)}s.`
                : `${archive.namespace} cache is empty; nothing to save.`
            );
          } catch (error) {
            // Fetch errors include signed URLs; keep the failure reason without their credentials.
            const detail =
              error instanceof Error
                ? ` ${error.message.replace(/https?:\/\/\S+/gi, '[URL]')}`
                : '';
            stepLog.warn(
              `Could not save ${archive.namespace} cache. The build artifact is available.${detail}`
            );
          }
        })
      );
    },
  };
}

async function snapshotGradleCache(
  cache: string,
  directory: string,
  log: LogWriter
): Promise<boolean> {
  const paths = await fg(
    gradleCacheDirectories.map(name => `${name}/**/*`),
    {
      cwd: cache,
      dot: true,
      followSymbolicLinks: false,
      ignore: ['**/*.lock', '**/*.part', '**/*.failed'],
    }
  );
  const entries = await Promise.all(
    paths.map(async name => ({ name, stat: await fs.lstat(path.join(cache, name)) }))
  );
  const files = entries.filter(entry => entry.stat.isFile());
  if (
    !files.some(
      entry =>
        entry.name.startsWith('build-cache-1/') && path.basename(entry.name) !== 'gc.properties'
    )
  ) {
    log.info('No Gradle cache entries; retaining the previous archive.');
    return false;
  }
  if (files.reduce((size, entry) => size + entry.stat.size, 0) > 450 * 1024 * 1024) {
    log.warn('Gradle cache exceeds 450 MiB; retaining the previous archive.');
    return false;
  }
  await fs.emptyDir(directory);
  for (const entry of files) {
    await fs.copy(path.join(cache, entry.name), path.join(directory, entry.name), {
      preserveTimestamps: true,
    });
  }
  return true;
}

async function reportCcacheStats(
  executable: string,
  file: string,
  env: NodeJS.ProcessEnv,
  log: LogWriter
): Promise<Record<string, number> | undefined> {
  try {
    if (!(await fs.pathExists(file))) {
      return;
    }
    const { stdout } = await spawnAsync(executable, ['--print-log-stats', '--format=json'], {
      env: { ...env, CCACHE_STATSLOG: file },
    });
    const stats = JSON.parse(stdout) as Record<string, number>;
    const hits = stats.direct_cache_hit + stats.preprocessed_cache_hit;
    log.info(
      `C/C++ cache: ${hits} hits, ${stats.cache_miss} misses, ${stats.could_not_use_precompiled_header} cache bypasses for precompiled headers.`
    );
    return stats;
  } catch {
    log.write('Could not read C/C++ cache statistics.');
    return undefined;
  }
}

async function findCcache(env: NodeJS.ProcessEnv): Promise<string | undefined> {
  for (const directory of (env.PATH ?? process.env.PATH ?? '').split(path.delimiter)) {
    const executable = path.resolve(directory, 'ccache');
    try {
      await fs.access(executable, fs.constants.X_OK);
      const { stdout } = await spawnAsync(executable, ['--version'], { env });
      const version = /ccache version (\d+)\.(\d+)/.exec(stdout);
      if (version && Number(version[1]) === 4 && Number(version[2]) >= 11) {
        return executable;
      }
    } catch {
      /* Try the next PATH entry. */
    }
  }
  return undefined;
}
