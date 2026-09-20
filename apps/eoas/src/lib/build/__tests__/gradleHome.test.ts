import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { withGradleHome } from '../android/gradleHome';

let home: string;
const build = { endpoint: 'https://xprem.test/app/build/identifier', options: { profile: 'test' } };

beforeEach(async () => {
  home = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-gradle-home-'));
  vi.spyOn(os, 'homedir').mockReturnValue(home);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await fs.remove(home);
});

it('retains dependencies between builds without using the shared Gradle home', async () => {
  const settings = path.join(home, '.gradle/gradle.properties');
  await fs.outputFile(settings, 'private repository credentials');
  const directory = await withGradleHome(build, async directory => {
    await fs.outputFile(path.join(directory, 'caches/modules-2/dependency'), 'downloaded jar');
    return directory;
  });
  await withGradleHome(build, async next => {
    expect(next).toBe(directory);
    expect(await fs.readFile(path.join(next, 'caches/modules-2/dependency'), 'utf8')).toBe(
      'downloaded jar'
    );
    expect(await fs.pathExists(path.join(next, 'gradle.properties'))).toBe(false);
    expect((await fs.stat(next)).mode & 0o777).toBe(0o700);
  });
  expect(await fs.readFile(settings, 'utf8')).toBe('private repository credentials');
});

it('isolates applications and profiles while rejecting concurrent use of the same home', async () => {
  await withGradleHome(build, async directory => {
    await expect(withGradleHome(build, async () => {})).rejects.toThrow('already running');
    for (const other of [
      { ...build, endpoint: 'https://xprem.test/app/build/another-identifier' },
      { ...build, options: { profile: 'production' } },
    ]) {
      await withGradleHome(other, async otherDirectory => {
        expect(otherDirectory).not.toBe(directory);
      });
    }
  });
});

it('releases the home after a failed build', async () => {
  await expect(
    withGradleHome(build, async () => {
      throw new Error('compilation failed');
    })
  ).rejects.toThrow('compilation failed');
  await expect(withGradleHome(build, async () => 'next build')).resolves.toBe('next build');
});
