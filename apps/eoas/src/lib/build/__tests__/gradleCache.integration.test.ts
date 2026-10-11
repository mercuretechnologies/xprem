import { execFile } from 'child_process';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { promisify } from 'util';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { restoreAndroidCache } from '../android/cache';
import { restoreCacheArchive, saveCacheArchive } from '../cacheArchive';

vi.mock('../cacheArchive', () => ({ restoreCacheArchive: vi.fn(), saveCacheArchive: vi.fn() }));

const exec = promisify(execFile);
let root: string;
let archiveDirectory: string;

beforeEach(async () => {
  root = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-cache-gradle-'));
  archiveDirectory = path.join(root, 'archive');
  vi.mocked(restoreCacheArchive).mockImplementation(async archive => {
    if (!(await fs.pathExists(archiveDirectory))) {
      return false;
    }
    await fs.copy(archiveDirectory, archive.directory, { preserveTimestamps: true });
    return true;
  });
  vi.mocked(saveCacheArchive).mockImplementation(async archive => {
    await fs.copy(archive.directory, archiveDirectory, { preserveTimestamps: true });
    return 1;
  });
});

afterEach(async () => {
  await fs.remove(root);
  vi.resetAllMocks();
});

// TEST_GRADLE selects an installed version. The fixture needs no repositories or dependencies.
it.runIf(process.env.TEST_GRADLE)(
  'restores task outputs into a fresh Gradle home and invalidates changed inputs',
  async () => {
    expect(await buildInFreshGradleHome('cold', 'original')).not.toContain('FROM-CACHE');
    expect((await fs.readdir(archiveDirectory)).sort()).toEqual(['build-cache-1', 'journal-1']);

    expect(await buildInFreshGradleHome('warm', 'original')).toContain(':cached FROM-CACHE');
    expect(await buildInFreshGradleHome('changed', 'changed')).not.toContain('FROM-CACHE');
  },
  180000
);

async function buildInFreshGradleHome(name: string, content: string): Promise<string> {
  const project = path.join(root, name, 'project');
  const env = {
    ...process.env,
    GRADLE_USER_HOME: path.join(root, name, 'gradle-home'),
    CCACHE_DISABLE: '1',
  };
  await fs.outputFile(path.join(project, 'settings.gradle'), "rootProject.name = 'archive-test'\n");
  await fs.writeFile(path.join(project, 'build.gradle'), buildScript);

  const log = { info: vi.fn(), warn: vi.fn(), write: vi.fn() };
  const cache = await restoreAndroidCache(
    { endpoint: 'https://xprem.test/app/build/id', options: { profile: 'test' }, env },
    path.join(root, name, 'temporary'),
    log
  );
  const { stdout } = await exec(
    process.env.TEST_GRADLE!,
    [
      'cached',
      '--no-daemon',
      '--console=plain',
      '--offline',
      ...cache!.args,
      `-Pcontent=${content}`,
    ],
    { cwd: project, env, timeout: 120000 }
  );
  await cache!.save(log);

  expect(await fs.readFile(path.join(project, 'output.txt'), 'utf8')).toBe(content);
  expect(log.warn).not.toHaveBeenCalled();
  return stdout;
}

const buildScript = `
@CacheableTask
abstract class WriteOutput extends DefaultTask {
  @Input abstract Property<String> getContent()
  @OutputFile abstract RegularFileProperty getDestination()
  @TaskAction void writeOutput() { destination.get().asFile.text = content.get() }
}
tasks.register('cached', WriteOutput) {
  content = providers.gradleProperty('content')
  destination = layout.projectDirectory.file('output.txt')
}
`;
