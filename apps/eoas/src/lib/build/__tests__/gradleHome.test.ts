import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

import { withGradleHome } from '../android/gradleHome';

let home: string;
const build = {
  endpoint: 'https://xprem.test/app/build/identifier',
  options: { profile: 'test' },
  env: {} as NodeJS.ProcessEnv,
};

beforeEach(async () => {
  home = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-gradle-home-'));
  vi.spyOn(os, 'homedir').mockReturnValue(home);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await fs.remove(home);
});

it('retains dependencies between builds and links the user configuration', async () => {
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
    expect(await fs.readlink(path.join(next, 'gradle.properties'))).toBe(settings);
    expect(await fs.pathExists(path.join(next, 'init.d'))).toBe(false);
    expect((await fs.stat(next)).mode & 0o777).toBe(0o700);
  });
  await fs.writeFile(settings, 'rotated credentials');
  await withGradleHome(build, async next => {
    expect(await fs.readFile(path.join(next, 'gradle.properties'), 'utf8')).toBe(
      'rotated credentials'
    );
  });
});

it('links the configuration of a custom GRADLE_USER_HOME', async () => {
  const userHome = path.join(home, 'custom-gradle-home');
  await fs.outputFile(path.join(userHome, 'init.d/repositories.gradle'), 'allprojects {}');
  await withGradleHome({ ...build, env: { GRADLE_USER_HOME: userHome } }, async directory => {
    expect(await fs.readFile(path.join(directory, 'init.d/repositories.gradle'), 'utf8')).toBe(
      'allprojects {}'
    );
    expect(await fs.pathExists(path.join(directory, 'gradle.properties'))).toBe(false);
  });
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
