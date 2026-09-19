import { execFile } from 'child_process';
import fg from 'fast-glob';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { promisify } from 'util';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { restoreAndroidCache } from '../android/cache';
import { restoreCacheArchive, saveCacheArchive } from '../cacheArchive';

vi.mock('../cacheArchive', () => ({ restoreCacheArchive: vi.fn(), saveCacheArchive: vi.fn() }));

const exec = promisify(execFile);
const log = { info: vi.fn(), warn: vi.fn(), write: vi.fn() };
let root: string;
let env: NodeJS.ProcessEnv;

beforeEach(async () => {
  vi.resetAllMocks();
  root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-cache-android-')));
  // Test product defaults independently of the developer's compiler-cache configuration.
  env = {
    ...Object.fromEntries(
      Object.entries(process.env).filter(
        ([name]) =>
          !name.startsWith('CCACHE_') &&
          ![
            'ANDROID_CCACHE',
            'NDK_CCACHE',
            'CMAKE_C_COMPILER_LAUNCHER',
            'CMAKE_CXX_COMPILER_LAUNCHER',
          ].includes(name)
      )
    ),
    PATH: `${path.dirname(process.env.TEST_CCACHE ?? '')}:${process.env.PATH}`,
    GRADLE_USER_HOME: path.join(root, 'gradle-home'),
    CCACHE_CONFIGPATH: '/dev/null',
    SOURCE_DATE_EPOCH: undefined,
  };
});

afterEach(async () => {
  await fs.remove(root);
});

// Requires TEST_GRADLE, TEST_CCACHE, TEST_AGP_VERSION, GRADLE_RO_DEP_CACHE,
// and ANDROID_HOME with NDK 27.1.12297006, CMake 3.22.1 and platform 35 installed.
it.runIf(
  process.env.TEST_GRADLE &&
    process.env.TEST_CCACHE &&
    process.env.TEST_AGP_VERSION &&
    process.env.GRADLE_RO_DEP_CACHE &&
    process.env.ANDROID_HOME
)(
  'AGP reuses restored C/C++ objects and recompiles them when a PCH header changes',
  async () => {
    const project = path.join(root, 'project');
    await fs.outputFile(path.join(project, 'settings.gradle'), "rootProject.name = 'cache-test'\n");
    await fs.writeFile(
      path.join(project, 'build.gradle'),
      `
buildscript {
  repositories { google(); mavenCentral() }
  dependencies { classpath 'com.android.tools.build:gradle:${process.env.TEST_AGP_VERSION}' }
}
apply plugin: 'com.android.library'
android {
  namespace 'dev.xprem.cachetest'
  compileSdk 35
  ndkVersion '27.1.12297006'
  defaultConfig {
    minSdk 24
    ndk { abiFilters 'arm64-v8a' }
  }
  externalNativeBuild { cmake { path file('CMakeLists.txt'); version '3.22.1' } }
}
`
    );
    await fs.writeFile(
      path.join(project, 'CMakeLists.txt'),
      `
cmake_minimum_required(VERSION 3.22)
project(cachetest C CXX)
add_library(answer SHARED answer.c answer.cpp)
target_precompile_headers(answer PRIVATE answer.h)
target_compile_options(answer PRIVATE -Xclang -fno-pch-timestamp)
`
    );
    await fs.writeFile(
      path.join(project, 'answer.c'),
      'const char *answer(void) { return ANSWER; }\n'
    );
    await fs.writeFile(
      path.join(project, 'answer.cpp'),
      'const char *other_answer() { return ANSWER; }\n'
    );
    await fs.writeFile(path.join(project, 'answer.h'), '#define ANSWER "ANSWER:42"\n');
    vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
      const snapshot = path.join(root, 'archives', archive.namespace);
      if (!(await fs.pathExists(snapshot))) {
        return false;
      }
      await fs.copy(snapshot, archive.directory, { preserveTimestamps: true });
      return true;
    });
    vi.mocked(saveCacheArchive).mockImplementation(async archive => {
      await fs.copy(archive.directory, path.join(root, 'archives', archive.namespace), {
        preserveTimestamps: true,
      });
      return 1;
    });

    async function compile(
      stage: string
    ): Promise<{ stats: Record<string, number>; contents: Buffer[] }> {
      await fs.remove(path.join(project, '.cxx'));
      await fs.remove(path.join(project, 'build'));
      const cache = await restoreAndroidCache(
        { endpoint: 'https://xprem.test/app/build/id', options: { profile: 'test' }, env },
        path.join(root, stage),
        log
      );
      const buildEnv = { ...env, ...cache!.env };
      await exec(process.env.TEST_CCACHE!, ['--zero-stats'], { env: buildEnv });
      await exec(
        process.env.TEST_GRADLE!,
        [
          'externalNativeBuildRelease',
          '--no-daemon',
          '--offline',
          '--console=plain',
          ...cache!.args,
        ],
        { cwd: project, env: buildEnv, timeout: 120000 }
      );
      const { stdout } = await exec(process.env.TEST_CCACHE!, ['--print-stats', '--format=json'], {
        env: buildEnv,
      });
      await cache!.report(log);
      await cache!.save(log);
      const objects = await fg('.cxx/**/CMakeFiles/answer.dir/**/*.o', { cwd: project });
      expect(objects).toHaveLength(2);
      const contents = await Promise.all(
        objects.sort().map(file => fs.readFile(path.join(project, file)))
      );
      expect(log.warn).not.toHaveBeenCalled();
      return { stats: JSON.parse(stdout), contents };
    }

    const cold = await compile('cold');
    expect(cold.stats.cache_miss).toBeGreaterThanOrEqual(2);
    expect(cold.stats.could_not_use_precompiled_header).toBe(2);
    const warm = await compile('warm');
    expect(warm.stats.cache_miss).toBe(0);
    expect(warm.stats.direct_cache_hit + warm.stats.preprocessed_cache_hit).toBeGreaterThanOrEqual(
      2
    );
    expect(warm.contents).toEqual(cold.contents);

    await fs.writeFile(path.join(project, 'answer.h'), '#define ANSWER "ANSWER:43"\n');
    const changed = await compile('changed');
    expect(changed.stats.cache_miss).toBeGreaterThanOrEqual(2);
    for (const object of changed.contents) {
      expect(object.includes(Buffer.from('ANSWER:43\0'))).toBe(true);
      expect(object.includes(Buffer.from('ANSWER:42\0'))).toBe(false);
    }
  },
  180000
);

it.runIf(process.env.TEST_CCACHE && process.env.ANDROID_HOME)(
  'an explicit strict override recompiles time macros instead of returning stale objects',
  async () => {
    env.CCACHE_SLOPPINESS = '';
    const cache = await restoreAndroidCache(
      { endpoint: 'https://xprem.test/app/build/id', options: { profile: 'test' }, env },
      root,
      log
    );
    const buildEnv = { ...env, ...cache!.env };
    const host = process.platform === 'darwin' ? 'darwin-x86_64' : 'linux-x86_64';
    const compiler = path.join(
      process.env.ANDROID_HOME!,
      `ndk/27.1.12297006/toolchains/llvm/prebuilt/${host}/bin/clang++`
    );
    const source = path.join(root, 'timestamp.cpp');
    const object = path.join(root, 'timestamp.o');
    await fs.writeFile(source, 'extern "C" const char *timestamp() { return "TIME:" __TIME__; }\n');
    async function compileTimestamp(): Promise<string | undefined> {
      await exec(
        process.env.TEST_CCACHE!,
        [compiler, '--target=aarch64-none-linux-android24', '-c', source, '-o', object],
        { env: buildEnv }
      );
      const timestamp = /TIME:\d{2}:\d{2}:\d{2}/.exec(
        (await fs.readFile(object)).toString('latin1')
      )?.[0];
      expect(timestamp).toBeDefined();
      return timestamp;
    }
    const before = await compileTimestamp();
    await new Promise(resolve => setTimeout(resolve, 1100));
    expect(await compileTimestamp()).not.toBe(before);
  }
);
