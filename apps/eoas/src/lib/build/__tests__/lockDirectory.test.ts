import { spawnSync } from 'child_process';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { afterEach, expect, it } from 'vitest';

import { BuildLog } from '../log';
import { lockDirectory, withTemporaryDirectory } from '../workspace';

const directories: string[] = [];
const systemTemporary = process.env.TMPDIR;
afterEach(async () => {
  if (systemTemporary === undefined) {
    delete process.env.TMPDIR;
  } else {
    process.env.TMPDIR = systemTemporary;
  }
  await Promise.all(directories.splice(0).map(directory => fs.remove(directory)));
});

async function target(): Promise<string> {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-lock-'));
  directories.push(parent);
  return path.join(parent, 'build');
}

// The pid of a process that has already exited.
function deadPid(): number {
  return spawnSync(process.execPath, ['-e', '']).pid;
}

it('lets one build hold a directory until it releases it', async () => {
  const directory = await target();
  const release = await lockDirectory(directory);
  await expect(lockDirectory(directory)).rejects.toThrow('already running');
  await release();
  await (
    await lockDirectory(directory)
  )();
  expect(await fs.readdir(path.dirname(directory))).toEqual([]);
});

it.each([
  ['a free directory', false],
  ['the lock of a dead build', true],
])('gives %s to exactly one of many simultaneous builds', async (_name, stale) => {
  const directory = await target();
  if (stale) {
    await fs.writeFile(`${directory}.lock`, String(deadPid()));
  }
  const attempts = await Promise.allSettled(
    Array.from({ length: 20 }, () => lockDirectory(directory))
  );
  const winners = attempts.filter(attempt => attempt.status === 'fulfilled');
  expect(winners).toHaveLength(1);
  expect(await fs.readFile(`${directory}.lock`, 'utf8')).toMatch(new RegExp(`^${process.pid} `));
  expect(await fs.readdir(path.dirname(directory))).toEqual(['build.lock']);
});

it('takes over the lock of a dead build whose pid now belongs to another process', async () => {
  const directory = await target();
  await fs.writeFile(`${directory}.lock`, `${process.pid} Thu Jan  1 00:00:00 1970`);
  await (
    await lockDirectory(directory)
  )();
});

it('lets one build at a time use the stable directory of a project', async () => {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-lock-'));
  directories.push(temporary);
  process.env.TMPDIR = temporary;
  const buildLog = {} as BuildLog;
  await withTemporaryDirectory(
    buildLog,
    async directory => {
      expect((await fs.stat(directory)).mode & 0o777).toBe(0o700);
      await expect(
        withTemporaryDirectory(buildLog, async () => {}, '/projects/one')
      ).rejects.toThrow('already running');
      await withTemporaryDirectory(buildLog, async () => {}, '/projects/other');
    },
    '/projects/one'
  );
  await withTemporaryDirectory(buildLog, async () => {}, '/projects/one');
});
