import { execFile } from 'child_process';
import fs from 'fs-extra';
import os from 'os';
import path from 'path';
import { promisify } from 'util';
import { expect, it, vi } from 'vitest';

import { runBuildCommand, terminateBuildCommand } from '../run';

const exec = promisify(execFile);

it.each(['graceful', 'forced'])(
  'waits for a detached descendant during %s cancellation',
  async mode => {
    const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'eoas-cancel-test-'));
    const stopped = path.join(directory, 'stopped');
    const script = path.join(directory, 'client.cjs');
    await fs.writeFile(
      path.join(directory, 'worker.cjs'),
      `
      const fs = require('fs');
      process.on('SIGTERM', () => {
        if (process.argv[2] === 'forced') return;
        // Emulate daemon cleanup that outlives the Gradle client.
        setTimeout(() => {
          fs.writeFileSync('stopped', 'done');
          process.exit(0);
        }, 400);
      });
      process.send(process.pid);
      setTimeout(() => process.exit(0), 15000);
      `
    );
    await fs.writeFile(
      script,
      `
      const child = require('child_process').spawn(process.execPath, ['worker.cjs', process.argv[2]], {
        detached: true,
        stdio: ['ignore', 'ignore', 'ignore', 'ipc'],
      });
      child.once('message', pid => console.log('ready ' + pid));
      // The launcher exits without forwarding signals to its detached worker.
      process.on('SIGTERM', () => process.exit(0));
      `
    );
    const log = { write: vi.fn(), info: vi.fn(), warn: vi.fn() };
    let buildSettledBeforeDescendant = false;
    const build = runBuildCommand(
      {
        title: 'Cancellation fixture',
        command: process.execPath,
        args: [script, mode],
        cwd: directory,
        env: process.env,
      },
      log,
      []
    )
      .catch(() => {}) // Cancellation rejects the running command.
      .then(async () => {
        buildSettledBeforeDescendant = mode === 'graceful' && !(await fs.pathExists(stopped));
      });
    let pid: number | undefined;
    try {
      await expect
        .poll(() => log.write.mock.calls[0]?.[0], { timeout: 5000 })
        .toMatch(/^ready \d+$/);
      pid = Number(log.write.mock.calls[0][0].split(' ')[1]);
      await terminateBuildCommand();
      if (mode === 'graceful') {
        expect(await fs.pathExists(stopped)).toBe(true);
      }
      const { stdout } = await exec('ps', ['-p', String(pid), '-o', 'stat=']).catch(() => ({
        stdout: '',
      }));
      expect(stdout.trim() === '' || stdout.trim().startsWith('Z')).toBe(true);
      await build;
      expect(buildSettledBeforeDescendant).toBe(false);
    } finally {
      if (pid) {
        try {
          process.kill(pid, 'SIGKILL');
        } catch {
          /* Already exited. */
        }
      }
      await terminateBuildCommand();
      await build;
      await fs.remove(directory);
    }
  },
  15000
);
